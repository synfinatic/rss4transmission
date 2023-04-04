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

	"github.com/alecthomas/kong"
	"github.com/hekmon/transmissionrpc/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/mattn/go-colorable"

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
	Config       Config
	Cache        *CacheFile
	Transmission *transmissionrpc.Client
}

type CLI struct {
	LogLevel string `kong:"default='info',enum='error,warn,info,debug',help='Log Level [error|warn|info|debug]'"`
	Lines    bool   `kong:"help='Include line numbers in logs'"`
	LogFile  string `kong:"help='Output log file (default: stderr)',default='stderr'"`
	Config   string `kong:"help='Override path to config file'"`
	Cache    string `kong:"help='Override path to cache file'"`

	// comamnds
	Version VersionCmd `kong:"cmd,help='Print version and exit'"`
	Watch   WatchCmd   `kong:"cmd,help='Scrape RSS feeds in a loop'"`
	Once    OnceCmd    `kong:"cmd,help='Scrape RSS feeds once'"`
}

type WatchCmd struct {
	Feed  []string `kong:"help='Limit scraping to the given feed(s)'"`
	Sleep int      `kong:"short='s',default='60',help='Seconds to sleep between scraping'"`
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
		log.SetOutput(colorable.NewColorableStdout())
	case "warn":
		log.SetLevel(logrus.WarnLevel)
		log.SetOutput(colorable.NewColorableStdout())
	case "error":
		log.SetLevel(logrus.ErrorLevel)
		log.SetOutput(colorable.NewColorableStdout())
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

	var cache *CacheFile
	var err error
	if cache, err = OpenCache(cli.Cache); err != nil {
		log.WithError(err).Fatalf("Unable to open cache file")
	}

	rc := RunContext{
		Cli:    &cli,
		Ctx:    ctx,
		Konf:   koanf.New("."),
		Config: Config{},
		Cache:  cache,
	}

	if ctx.Command() != "version" {
		configFile := GetPath(cli.Config)
		if err := rc.Konf.Load(file.Provider(configFile), yaml.Parser()); err != nil {
			log.WithError(err).Fatalf("Unable to open config file: %s", configFile)
		}
	}

	ac := transmissionrpc.AdvancedConfig{
		HTTPS:       rc.Konf.Bool("Transmission.HTTPS"),
		Port:        uint16(rc.Konf.Int("Transmission.Port")),
		RPCURI:      rc.Konf.String("Transmission.Path"),
		HTTPTimeout: 30, // 30 sec
		UserAgent:   fmt.Sprintf("rss4transmission/%s", Version),
		Debug:       false,
	}
	if rc.Transmission, err = transmissionrpc.New(rc.Konf.String("Transmission.Host"),
		rc.Konf.String("Transmission.Username"), rc.Konf.String("Transmission.Password"), &ac); err != nil {
		log.WithError(err).Fatalf("Unable to setup Transmission client")
	}

	if err := rc.Konf.Unmarshal("", &rc.Config); err != nil {
		log.WithError(err).Fatalf("Unable to process config")
	}

	if err = ctx.Run(&rc); err != nil {
		log.Fatalf("Error running command: %s", err.Error())
	}
	rc.Cache.SaveCache()
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
