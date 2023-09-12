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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/hekmon/transmissionrpc/v2"
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
	Ctx          *kong.Context
	Cli          *CLI
	Konf         *koanf.Koanf
	configFile   string
	Config       Config
	Cache        *CacheFile
	Transmission *transmissionrpc.Client
	Provider     *file.File
}

type CLI struct {
	LogLevel string `kong:"default='info',enum='error,warn,info,debug',help='Log Level [error|warn|info|debug]'"`
	Lines    bool   `kong:"help='Include line numbers in logs'"`
	LogFile  string `kong:"help='Output log file (default: stderr)',default='stderr'"`
	Config   string `kong:"help='Override path to config file'"`
	SeenFile string `kong:"help='Override path to SeenFile file'"`

	// comamnds
	Version VersionCmd `kong:"cmd,help='Print version and exit'"`
	Watch   WatchCmd   `kong:"cmd,help='Scrape RSS feeds in a loop'"`
	Once    OnceCmd    `kong:"cmd,help='Scrape RSS feeds once'"`
}

func main() {
	log = logrus.New()

	cli := CLI{}
	ctx := kong.Parse(
		&cli,
		kong.Description("RSS4Transmission: A RSS Feed download tool for TransmissionBT"),
		kong.Vars{},
	)

	switch cli.LogLevel {
	case "debug":
		log.SetLevel(logrus.DebugLevel)
	case "info":
		log.SetLevel(logrus.InfoLevel)
	case "warn":
		log.SetLevel(logrus.WarnLevel)
	case "error":
		log.SetLevel(logrus.ErrorLevel)
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

	var err error
	if rc.Konf, err = rc.loadConfig(rc.configFile); err != nil {
		log.WithError(err).Fatalf("Unable to load %s", rc.configFile)
	}

	// use our SeenFile
	seenFileName := rc.Konf.String("SeenFile")
	if cli.SeenFile != "" {
		seenFileName = cli.SeenFile
	}

	if rc.Cache, err = OpenCache(seenFileName); err != nil {
		log.WithError(err).Fatalf("Unable to open cache file: %s", seenFileName)
	}

	ac := transmissionrpc.AdvancedConfig{
		HTTPS:       rc.Konf.Bool("Transmission.HTTPS"),
		Port:        uint16(rc.Konf.Int("Transmission.Port")),
		RPCURI:      rc.Konf.String("Transmission.Path"),
		HTTPTimeout: time.Duration(30 * time.Second),
		UserAgent:   fmt.Sprintf("rss4transmission/%s", Version),
		Debug:       false,
	}
	if rc.Transmission, err = transmissionrpc.New(rc.Konf.String("Transmission.Host"),
		rc.Konf.String("Transmission.Username"), rc.Konf.String("Transmission.Password"), &ac); err != nil {
		log.WithError(err).Fatalf("Unable to setup Transmission client")
	}

	if err = ctx.Run(rc); err != nil {
		log.WithError(err).Fatalf("Error running command")
	}
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

func (rc *RunContext) loadConfig(configFile string) (*koanf.Koanf, error) {
	konf := koanf.New(".")

	// load our defaults
	if err := konf.Load(confmap.Provider(ConfigDefaults, "."), nil); err != nil {
		log.WithError(err).Fatalf("Unable to load defaults")
	}

	rc.Provider = file.Provider(configFile)
	if err := konf.Load(rc.Provider, yaml.Parser()); err != nil {
		return konf, err
	}

	if err := konf.Unmarshal("", &rc.Config); err != nil {
		return konf, err
	}

	return konf, nil
}
