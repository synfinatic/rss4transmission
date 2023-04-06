package main

import (
	"time"
)

type WatchCmd struct {
	Feed         []string `kong:"help='Limit scraping to the given feed(s)'"`
	Download     bool     `kong:"short='d',help='Download torrent file instead of torrenting',xor='action'"`
	DownloadPath string   `kong:"short='p',help='Path to download torrent files to ($PWD)'"`
	Sleep        int      `kong:"short='s',default='300',help='Seconds to sleep between scraping'"`
}

func (cmd *WatchCmd) Run(ctx *RunContext) error {

	once := OnceCmd{
		Feed:         ctx.Cli.Watch.Feed,
		Download:     ctx.Cli.Watch.Download,
		DownloadPath: ctx.Cli.Watch.DownloadPath,
	}

	ticker := time.NewTicker(time.Duration(ctx.Cli.Watch.Sleep) * time.Second)
	// Run once and then sleep between later runs...
	for ; true; <-ticker.C {
		if err := once.Run(ctx); err != nil {
			return err
		}
	}
	return nil
}
