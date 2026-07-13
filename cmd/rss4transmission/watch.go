package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

const defaultRetryInterval = 60 * time.Second

type WatchCmd struct {
	Feed            []string `kong:"help='Limit scraping to the given feed(s)'"`
	Download        bool     `kong:"short='d',help='Download torrent file instead of torrenting',xor='action'"`
	DownloadPath    string   `kong:"short='p',help='Path to download torrent files to ($PWD)'"`
	Sleep           int      `kong:"short='s',default='300',help='Seconds to sleep between scraping'"`
	HistoryFile     string   `kong:"help='Path to history JSON file'"`
	PrivateListen   string   `kong:"help='Address to serve torrent history on (internal only), as host:port or bare port (disabled if empty)'"`
	PublicListen    string   `kong:"help='Address to serve /cancel, /notify-complete, and /healthz on (host:port or bare port); splits listeners so history stays on the private listener'"`
	TorrentCacheDir string   `kong:"help='Directory to cache fetched .torrent files across runs'"`
	AccessLog       string   `kong:"help='Path to append-mode HTTP access log for fail2ban integration (disabled if empty)'"`
}

// retryLoadConfig calls tryLoad repeatedly, sleeping interval between attempts.
// It retries forever until tryLoad succeeds and returns the 1-based attempt number.
func retryLoadConfig(tryLoad func() error, interval time.Duration) int {
	for i := 1; ; i++ {
		if err := tryLoad(); err == nil {
			return i
		} else {
			log.Errorf("config reload attempt %d failed: %s; retrying in %s", i, err, interval)
		}
		time.Sleep(interval)
	}
}

func (cmd *WatchCmd) Run(ctx *RunContext) error {
	mu := sync.Mutex{}

	// watchCallback and retryWatch reference each other via closures.
	var watchCallback func(event interface{}, err error)
	var retryWatch func()

	watchCallback = func(event interface{}, err error) {
		if err != nil {
			// Editors often save by deleting then recreating the file, which
			// causes fsnotify to fire a remove event and stop watching.
			// Retry loading and re-register the watcher once the file is back.
			if strings.Contains(err.Error(), "was removed") {
				log.Warnf("config file temporarily removed (editor save?), retrying reload...")
				go retryWatch()
				return
			}
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
	}

	retryWatch = func() {
		attempt := retryLoadConfig(func() error {
			mu.Lock()
			defer mu.Unlock()
			konf, err := ctx.loadConfig(ctx.configFile)
			if err != nil {
				return err
			}
			ctx.Konf = konf
			return nil
		}, defaultRetryInterval)

		log.Infof("config reloaded after %d attempt(s), re-registering file watcher", attempt)
		if err := ctx.Provider.Watch(watchCallback); err != nil {
			log.WithError(err).Errorf("failed to re-register config file watcher")
		}
	}

	_ = ctx.Provider.Watch(watchCallback)

	ticker := time.NewTicker(time.Duration(ctx.Cli.Watch.Sleep) * time.Second)

	// watch just calls `once` in a loop
	once := OnceCmd{
		Feed:            ctx.Cli.Watch.Feed,
		Download:        ctx.Cli.Watch.Download,
		DownloadPath:    ctx.Cli.Watch.DownloadPath,
		TorrentCacheDir: ctx.Cli.Watch.TorrentCacheDir,
	}

	if cmd.HistoryFile != "" {
		var err error
		if ctx.History, err = OpenHistory(cmd.HistoryFile); err != nil {
			log.WithError(err).Warnf("Unable to open history file: %s", cmd.HistoryFile)
			ctx.History = nil
		}
	}

	// Initialize the cancel store if the HMAC secret is configured.
	// The reaper context is cancelled when Run returns, preventing a goroutine leak.
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	defer reaperCancel()
	if ctx.Config.Cancel.HMACSecret != "" {
		ttl := time.Duration(ctx.Config.Cancel.TokenTTLH) * time.Hour
		ctx.CancelStore = NewStore(ttl)
		ctx.CancelStore.StartReaper(reaperCtx)
	}

	var removeT removeFunc
	var getProgress progressFunc
	if ctx.CancelStore != nil {
		removeT = func(rCtx context.Context, ids []int64) error {
			return ctx.Transmission.TorrentRemove(rCtx, transmissionrpc.TorrentRemovePayload{
				IDs:             ids,
				DeleteLocalData: false,
			})
		}
		getProgress = func(rCtx context.Context, torrentID int64) (int64, float64, error) {
			torrents, err := ctx.Transmission.TorrentGet(rCtx,
				[]string{"downloadedEver", "percentDone"}, []int64{torrentID})
			if err != nil {
				return 0, 0, err
			}
			if len(torrents) == 0 {
				return 0, 0, nil
			}
			t := torrents[0]
			var dlBytes int64
			if t.DownloadedEver != nil {
				dlBytes = *t.DownloadedEver
			}
			var pct float64
			if t.PercentDone != nil {
				pct = *t.PercentDone
			}
			return dlBytes, pct, nil
		}
	}

	accessLog := openAccessLog(cmd.AccessLog)

	if cmd.PublicListen != "" {
		// Split-listener mode: /cancel, /notify-complete, and /healthz on the public
		// port, history on a separate private port. Cancel routes are NOT registered on
		// the private mux.
		if ctx.CancelStore != nil {
			ctx.CancelRoutesEnabled = true
		}
		addr, err := parseListenAddr(cmd.PublicListen)
		if err != nil {
			log.Fatalf("--public-listen: %s", err)
		}
		cancelMux := newCancelMux(ctx.CancelStore, ctx.Config.Cancel, removeT, getProgress, accessLog)
		registerNotifyCompleteRoute(cancelMux, ctx.Config.Ntfy, ctx.Config.Cancel, accessLog)
		go startWebServer("public", cancelMux, addr)

		if cmd.PrivateListen != "" {
			histAddr, err := parseListenAddr(cmd.PrivateListen)
			if err != nil {
				log.Fatalf("--private-listen: %s", err)
			}
			if ctx.History == nil {
				log.Warnf("--private-listen is set but --history-file was not provided; history page will return 404")
			}
			go startWebServer("private", newWebMux(ctx.History), histAddr)
		}
	} else if cmd.PrivateListen != "" {
		// Single-listener mode: history + cancel on the same port.
		addr, err := parseListenAddr(cmd.PrivateListen)
		if err != nil {
			log.Fatalf("--private-listen: %s", err)
		}
		if ctx.History == nil {
			log.Warnf("--private-listen is set but --history-file was not provided; history page will return 404")
		}
		mux := newWebMux(ctx.History)
		if ctx.CancelStore != nil {
			registerCancelRoutes(mux, ctx.CancelStore, ctx.Config.Cancel, removeT, getProgress, accessLog)
			ctx.CancelRoutesEnabled = true
		}
		registerNotifyCompleteRoute(mux, ctx.Config.Ntfy, ctx.Config.Cancel, accessLog)
		go startWebServer("private", mux, addr)
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
