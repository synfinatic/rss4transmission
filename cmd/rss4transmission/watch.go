package main

import (
	"sync"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/v2"
)

type WatchCmd struct {
	Feed         []string `kong:"help='Limit scraping to the given feed(s)'"`
	Download     bool     `kong:"short='d',help='Download torrent file instead of torrenting',xor='action'"`
	DownloadPath string   `kong:"short='p',help='Path to download torrent files to ($PWD)'"`
	Sleep        int      `kong:"short='s',default='300',help='Seconds to sleep between scraping'"`
}

func (cmd *WatchCmd) Run(ctx *RunContext) error {
	mu := sync.Mutex{}

	// watch for config file changes
	_ = ctx.Provider.Watch(func(event interface{}, err error) {
		if err != nil {
			log.Errorf("watch error: %s", err)
			return
		}

		// don't change the config while we are processing the feed
		mu.Lock()
		defer mu.Unlock()

		log.Infof("config changed. reloading...")
		konf := koanf.New(".")
		if err := konf.Load(ctx.Provider, yaml.Parser()); err != nil {
			log.WithError(err).Errorf("unable to load config")
			return
		}
		if err := konf.Unmarshal("", &ctx.Config); err != nil {
			log.WithError(err).Errorf("unable to process config")
			return
		}
		ctx.Konf = konf
	})

	ticker := time.NewTicker(time.Duration(ctx.Cli.Watch.Sleep) * time.Second)
	// Run once and then sleep between later runs...
	for ; true; <-ticker.C {
		if err := runOnce(ctx, &mu); err != nil {
			return err
		}
	}
	return nil
}

func runOnce(ctx *RunContext, mu *sync.Mutex) error {
	once := OnceCmd{
		Feed:         ctx.Cli.Watch.Feed,
		Download:     ctx.Cli.Watch.Download,
		DownloadPath: ctx.Cli.Watch.DownloadPath,
	}

	mu.Lock()
	defer mu.Unlock()
	if err := once.Run(ctx); err != nil {
		return err
	}
	return nil
}
