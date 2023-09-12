package main

import (
	"sync"
	"time"
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
		konf, err := ctx.loadConfig(ctx.configFile)
		if err != nil {
			log.WithError(err).Errorf("failed to reload config file")
			return
		}
		ctx.Konf = konf
	})

	ticker := time.NewTicker(time.Duration(ctx.Cli.Watch.Sleep) * time.Second)

	// watch just calls `once` in a loop
	once := OnceCmd{
		Feed:         ctx.Cli.Watch.Feed,
		Download:     ctx.Cli.Watch.Download,
		DownloadPath: ctx.Cli.Watch.DownloadPath,
	}

	var g *Gluetun
	if ctx.Config.Gluetun.Host != "" && ctx.Config.Gluetun.Port != 0 {
		g = NewGluetun(ctx.Config.Gluetun, ctx.Transmission)
	}

	// Run once and then sleep between later runs...
	for ; true; <-ticker.C {
		mu.Lock()
		if err := once.Run(ctx); err != nil {
			return err
		}
		mu.Unlock()
		if g != nil {
			g.CheckVpnTunnel()
		}
	}
	return nil
}
