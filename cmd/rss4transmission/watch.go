package main

import (
	"fmt"
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

	// Run once and then sleep between later runs...
	for ; true; <-ticker.C {
		mu.Lock()
		if err := once.Run(ctx); err != nil {
			return err
		}
		mu.Unlock()
		checkVpnTunnel(ctx)
	}
	return nil
}

var ForceRotate bool // flag to force rotation again due to failure

// checkVpnTunnel restarts / rotates the VPN tunnel as necessary
func checkVpnTunnel(rc *RunContext) {
	var err error

	// not configured, so skip it
	if rc.Config.Gluetun.Host == "" || rc.Config.Gluetun.Port == 0 {
		return
	}

	g := NewGluetun(rc.Config.Gluetun, rc.Transmission)

	if g.RotateNow() || ForceRotate {
		err = g.Rotate()
		if err != nil {
			log.WithError(err).Errorf("Rotate() failed")
			ForceRotate = true
			return
		}
	}
	ForceRotate = false

	var open bool
	err = fmt.Errorf("force execution")
	for i := 0; err != nil && i < 3; i++ {
		open, err = g.IsPortOpen()
		if err != nil {
			time.Sleep(3 * time.Second)
		}
	}
	if err != nil {
		log.WithError(err).Errorf("Unable to check IsPortOpen()")
		return
	}

	if !open {
		err = fmt.Errorf("force execution")
		for i := 0; err != nil && i < 3; i++ {
			err = g.UpdatePort()
			if err != nil {
				time.Sleep(3 * time.Second)
			}
		}
	}
	if err != nil {
		log.WithError(err).Errorf("Unable to UpdatePort()")
	}
}
