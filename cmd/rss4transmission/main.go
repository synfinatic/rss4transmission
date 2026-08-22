package main

/*
 * RSS4Transmission
 * Copyright (c) 2023 Aaron Turner  <aturner at synfin dot net>
 *
 * This program is free software: you can redistribute it
 * and/or modify it under the terms of the GNU General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or with the authors permission any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/alecthomas/kong"
	"github.com/hekmon/transmissionrpc/v3"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/sirupsen/logrus"
)

var Version = "unknown"
var Buildinfos = "unknown"
var Tag = "NO-TAG"
var CommitID = "unknown"
var Delta = ""
var log *logrus.Logger

const (
	Copyright = "2023"
)

var CONFIG_FILE = []string{
	"~/.rss4transmission/config.yaml",
	"~/.config/rss4transmission/config.yaml",
	"/etc/rss4transmission/config.yaml",
}

type RunContext struct {
	Ctx        *kong.Context
	Cli        *CLI
	configFile string
	// seenFileOverride is a non-empty --seen-file flag. It pins the cache
	// path, so a later config reload must not follow a changed SeenFile.
	seenFileOverride    string
	Config              Config
	Cache               *CacheFile
	History             *HistoryFile
	Speed               *SpeedFile
	PeerPortOpen        portOpenFunc
	PeerPort            peerPortFunc
	ExitIP              exitIPFunc
	SpeedActions        speedActions
	CancelStore         *Store
	CancelRoutesEnabled bool
	StartStore          *StartStore
	StartRoutesEnabled  bool
	Provider            *file.File

	// transmission is the RPC client, and txURL is the origin it points at
	// (the client does not expose it). A reload can replace both when the
	// Transmission block moves, while the monitors and the web handlers read
	// the client from their own goroutines, so every access goes through Tx().
	txMu         sync.RWMutex
	transmission *transmissionrpc.Client
	txURL        string

	// The long-lived components watch builds and a config reload updates in
	// place. They are written only from WatchCmd.Run and applyConfig, both of
	// which hold the reload lock.
	Gluetun      *Gluetun
	PortMonitor  *PortMonitor
	SpeedMonitor *SpeedMonitor
	// speedCancel stops the running speed monitor. Rebuilding the monitor
	// abandons a measurement in flight, which is acceptable at the hourly
	// cadence the monitor runs at.
	speedCancel context.CancelFunc
}

type CLI struct {
	LogLevel string `kong:"default='info',enum='error,warn,info,debug,trace',help='Log Level [error|warn|info|debug|trace]'"`
	Lines    bool   `kong:"help='Include line numbers in logs'"`
	LogFile  string `kong:"help='Output log file (default: stderr)',default='stderr'"`
	Config   string `kong:"help='Override path to config file'"`
	SeenFile string `kong:"help='Override path to SeenFile file'"`

	// comamnds
	Version   VersionCmd   `kong:"cmd,help='Print version and exit'"`
	Watch     WatchCmd     `kong:"cmd,help='Scrape RSS feeds in a loop'"`
	Once      OnceCmd      `kong:"cmd,help='Scrape RSS feeds once'"`
	Simulate  SimulateCmd  `kong:"cmd,help='Replay a local RSS feed file for testing'"`
	SpeedTest SpeedTestCmd `kong:"cmd,name='speedtest',help='Run a single speedtest over the VPN proxy'"`
}

func main() {
	log = logrus.New()

	cli := CLI{}
	ctx := kong.Parse(
		&cli,
		kong.Description("RSS4Transmission: A RSS Feed download tool for TransmissionBT"),
		kong.Vars{},
	)

	if level, err := logrus.ParseLevel(cli.LogLevel); err == nil {
		log.SetLevel(level)
	}
	if cli.Lines {
		log.SetReportCaller(true)
	}

	log.SetFormatter(&logrus.TextFormatter{
		DisableLevelTruncation: true,
		PadLevelText:           true,
		DisableTimestamp:       true,
	})

	if cli.LogFile == "stderr" {
		log.SetOutput(os.Stderr)
	} else {
		file, err := os.OpenFile(cli.LogFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			log.WithError(err).Fatalf("Unable to open log file: %s", cli.LogFile)
		}
		log.SetOutput(file)
	}

	rc := &RunContext{
		Cli:    &cli,
		Ctx:    ctx,
		Config: Config{},
	}

	if ctx.Command() == "version" {
		_ = ctx.Run(rc)
		return
	}

	if cli.Config != "" {
		rc.configFile = GetPath(cli.Config)
	} else {
		for _, fName := range CONFIG_FILE {
			if _, err := os.Stat(GetPath(fName)); err == nil {
				rc.configFile = fName
				break
			}
		}
	}
	if rc.configFile == "" {
		log.Fatalf("Unable to locate config file")
	}

	if err := rc.loadConfig(rc.configFile); err != nil {
		log.WithError(err).Fatalf("Unable to load %s", rc.configFile)
	}

	if !commandNeedsTransmission(ctx.Command()) {
		if err := ctx.Run(rc); err != nil {
			log.WithError(err).Fatalf("Error running command")
		}
		return
	}

	// use our SeenFile
	rc.seenFileOverride = cli.SeenFile
	seenFileName := rc.seenFile()

	var err error
	if rc.Cache, err = OpenCache(seenFileName); err != nil {
		log.WithError(err).Fatalf("Unable to open cache file: %s", seenFileName)
	}

	client, err := newTransmissionClient(rc.Config.Transmission)
	if err != nil {
		log.WithError(err).Fatalf("Unable to setup Transmission client")
	}
	rc.setTx(client, rc.Config.Transmission.URL())
	if err = ctx.Run(rc); err != nil {
		log.WithError(err).Fatalf("Error running command")
	}
}

// Tx is the Transmission RPC client in effect. Read it per call rather than
// holding on to it: a config reload can point the daemon at a different
// Transmission server.
func (rc *RunContext) Tx() *transmissionrpc.Client {
	rc.txMu.RLock()
	defer rc.txMu.RUnlock()
	return rc.transmission
}

// setTx installs a client and records the origin it talks to.
func (rc *RunContext) setTx(client *transmissionrpc.Client, txURL string) {
	rc.txMu.Lock()
	defer rc.txMu.Unlock()
	rc.transmission = client
	rc.txURL = txURL
}

// seenFile is the cache path in effect: the --seen-file flag when given,
// otherwise the config file's SeenFile.
func (rc *RunContext) seenFile() string {
	return rc.seenFileFor(rc.Config)
}

// seenFileFor is the cache path cfg asks for, with the --seen-file flag still
// winning. applyConfig calls it with a config it is in the middle of applying,
// which is why the config is a parameter.
func (rc *RunContext) seenFileFor(cfg Config) string {
	if rc.seenFileOverride != "" {
		return rc.seenFileOverride
	}
	return cfg.SeenFile
}

// newTransmissionClient builds the RPC client for a Transmission block. cfg
// must already have passed Transmission.Validate(), which loadConfig runs.
//
// Username and Password are deliberately not applied here: the RPC client
// takes credentials through the URL userinfo, and the configured pair is used
// only by the web reverse proxy in transmissionweb.go.
func newTransmissionClient(cfg Transmission) (*transmissionrpc.Client, error) {
	log.Debugf("Transmission URL: %s", cfg.URL())
	connectUrl, err := url.Parse(cfg.URL())
	if err != nil {
		return nil, fmt.Errorf("unable to parse Transmission URL %q: %w", cfg.URL(), err)
	}
	return transmissionrpc.New(connectUrl, &transmissionrpc.Config{
		UserAgent: fmt.Sprintf("rss4transmission/%s", Version),
	})
}

// commandNeedsTransmission reports whether a subcommand needs the seen cache
// and a Transmission client. speedtest measures the VPN link and nothing else:
// opening the cache would warn about creating a file it never reads or writes,
// and the RPC client would go unused.
func commandNeedsTransmission(command string) bool {
	switch command {
	case "speedtest", "version":
		return false
	}
	return true
}

type VersionCmd struct{}

func (cmd *VersionCmd) Run(ctx *RunContext) error {
	delta := ""
	if len(Delta) > 0 {
		delta = fmt.Sprintf(" [%s delta]", Delta)
		Tag = "Unknown"
	}
	fmt.Printf("RSS4Transmission v%s -- Copyright %s Aaron Turner\n", Version, Copyright)
	fmt.Printf("%s (%s)%s built at %s\n", CommitID, Tag, delta, Buildinfos)
	return nil
}

// Returns the config file path.
func GetPath(path string) string {
	return strings.Replace(path, "~", os.Getenv("HOME"), 1)
}

func (rc *RunContext) loadConfig(configFile string) error {
	konf := koanf.New(".")

	// load our defaults
	if err := konf.Load(confmap.Provider(ConfigDefaults, "."), nil); err != nil {
		log.WithError(err).Fatalf("Unable to load defaults")
	}

	// On the initial load Provider is nil; store it so watch.go can call
	// Provider.Watch() on it. On reloads we create a fresh reader but keep
	// the original Provider so its Watch goroutine continues to run.
	provider := file.Provider(configFile)
	if rc.Provider == nil {
		rc.Provider = provider
	}

	if err := konf.Load(provider, yaml.Parser()); err != nil {
		return err
	}

	var cfg Config
	if err := konf.Unmarshal("", &cfg); err != nil {
		return err
	}

	if err := cfg.Ntfy.Validate(); err != nil {
		return fmt.Errorf("invalid ntfy template: %w", err)
	}

	if err := cfg.SpeedTest.Validate(); err != nil {
		return fmt.Errorf("invalid SpeedTest configuration: %w", err)
	}

	if err := cfg.Transmission.Validate(); err != nil {
		return fmt.Errorf("invalid Transmission configuration: %w", err)
	}

	if err := cfg.Gluetun.Validate(); err != nil {
		return fmt.Errorf("invalid Gluetun configuration: %w", err)
	}

	// Compiling the extractors here does double duty: it rejects a bad Regexp
	// or Normalize pattern up front instead of at first use, and it means the
	// map is fully built before anything shares it, so handing a Config copy
	// to another goroutine cannot race on the lazy compile.
	for name, es := range cfg.Extractors {
		if err := es.Compile(); err != nil {
			return fmt.Errorf("invalid extractor %q: %w", name, err)
		}
	}

	if err := validateFeedNames(cfg.Feeds); err != nil {
		return fmt.Errorf("invalid feed configuration: %w", err)
	}

	for i := range cfg.Feeds {
		feedCfg := &cfg.Feeds[i]
		if err := feedCfg.Validate(feedCfg.Name, cfg.Extractors); err != nil {
			return fmt.Errorf("invalid feed %q config: %w", feedCfg.Name, err)
		}
		if err := feedCfg.Compile(); err != nil {
			return fmt.Errorf("invalid feed %q config: %w", feedCfg.Name, err)
		}
	}

	// Only commit the newly parsed config once every check above has passed,
	// so a bad reload (e.g. watch.go's live config-reload) leaves the
	// previously running config fully intact instead of partially applied.
	rc.Config = cfg

	return nil
}
