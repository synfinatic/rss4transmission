package main

import (
	"context"
	"sync"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
	"github.com/sirupsen/logrus"
)

const defaultRetryInterval = 60 * time.Second

type WatchCmd struct {
	Feed            []string `kong:"help='Limit scraping to the given feed(s)'"`
	Download        bool     `kong:"short='d',help='Download torrent file instead of torrenting',xor='action'"`
	DownloadPath    string   `kong:"short='p',help='Path to download torrent files to ($PWD)'"`
	Sleep           int      `kong:"short='s',default='300',help='Seconds to sleep between scraping'"`
	HistoryFile     string   `kong:"help='Path to history JSON file'"`
	PrivateListen   string   `kong:"help='Address to serve torrent history on (internal only), as host:port or bare port (disabled if empty)'"`
	PublicListen    string   `kong:"help='Address to serve /cancel, /start, /notify-complete, and /healthz on (host:port or bare port); splits listeners so history stays on the private listener'"`
	TorrentCacheDir string   `kong:"help='Directory to cache fetched .torrent files across runs'"`
	AccessLog       string   `kong:"help='Path to append-mode HTTP access log for fail2ban integration (disabled if empty)'"`
}

// warnNotifyFeedsWithoutHistory logs a startup warning for each feed configured
// with Action: notify when no history file is available: the /start token to
// history-record mapping requires ctx.History to resolve, so a notify feed
// with no history file can never be manually started.
func warnNotifyFeedsWithoutHistory(feeds []Feed, history *HistoryFile) {
	if history != nil {
		return
	}
	for _, f := range feeds {
		if f.Action == "notify" {
			log.Warnf("feed %q has Action: notify but no --history-file was provided; "+
				"it will never be downloadable via /start", f.Name)
		}
	}
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

// configReloader owns the live config-reload watch loop: reloading on file
// change, and recovering when the underlying fsnotify watch dies. It's
// factored out of WatchCmd.Run so the recovery behavior can be exercised
// without spinning up tickers, web servers, or a Transmission client.
type configReloader struct {
	mu            sync.Mutex
	reload        func() error
	registerWatch func(cb func(event any, err error)) error
	retryInterval time.Duration
	notifyReload  func(err error)
}

// onWatchEvent is the callback registered with the file watcher.
//
// koanf's file provider watch goroutine exits after calling back with ANY
// error, not just when the file is removed (which is merely the common
// case editors trigger on save) — see providers/file/file.go: every branch
// that invokes cb(nil, err) is immediately followed by `break loop`. So
// every error, not only a "was removed" one, must trigger recovery, or the
// watcher dies permanently and silently and no further reload ever happens.
func (r *configReloader) onWatchEvent(event any, err error) {
	if err != nil {
		log.Warnf("config file watcher stopped (%s); reloading and re-registering", err)
		go r.recover()
		return
	}

	reloadErr := func() error {
		// don't change the config while we are processing the feed
		r.mu.Lock()
		defer r.mu.Unlock()

		log.Infof("config changed. reloading...")
		return r.reload()
	}()

	if reloadErr != nil {
		log.WithError(reloadErr).Errorf("failed to reload config file")
	}
	// notify outside the lock: notifyConfigReload does a synchronous HTTP
	// POST with a 30s timeout, and holding r.mu that long would block the
	// ticker loop's once.Run and the web handlers that also take r.mu.
	if r.notifyReload != nil {
		r.notifyReload(reloadErr)
	}
}

// recover retries reload until it succeeds, then re-registers the watch. It
// blocks (via retryLoadConfig) until the config file is readable again, so
// callers run it in its own goroutine.
func (r *configReloader) recover() {
	notifiedFailure := false
	attempt := retryLoadConfig(func() error {
		reloadErr := func() error {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.reload()
		}()

		// Report the first failure so a bad edit saved via atomic
		// rename/replace (which routes through this recovery path rather
		// than onWatchEvent's direct reload) is still visible, but don't
		// spam a notification per retry attempt.
		if reloadErr != nil && !notifiedFailure {
			notifiedFailure = true
			if r.notifyReload != nil {
				r.notifyReload(reloadErr)
			}
		}
		return reloadErr
	}, r.retryInterval)

	log.Infof("config reloaded after %d attempt(s), re-registering file watcher", attempt)
	if r.notifyReload != nil {
		r.notifyReload(nil)
	}
	if err := r.registerWatch(r.onWatchEvent); err != nil {
		log.WithError(err).Errorf("failed to re-register config file watcher")
	}
}

// setupWebServers wires and starts the HTTP listener(s) for /cancel, /start,
// /notify-complete, /healthz, and (on the private listener) the history UI,
// based on which of --public-listen / --private-listen were configured. It
// sets ctx.CancelRoutesEnabled / ctx.StartRoutesEnabled to reflect what was
// actually registered. Factored out of WatchCmd.Run to keep its cyclomatic
// complexity down.
func setupWebServers(cmd *WatchCmd, ctx *RunContext, removeT removeFunc, getProgress progressFunc,
	retryHistory retryFunc, feedConfigured func(string) bool, feedGroups func(string) []Group,
	forgetHistory forgetFunc, accessLog *logrus.Logger,
) {
	if cmd.PublicListen != "" {
		// Split-listener mode: /cancel, /start, /notify-complete, and /healthz on the
		// public port, history on a separate private port. Cancel/start routes are NOT
		// registered on the private mux.
		if ctx.CancelStore != nil {
			ctx.CancelRoutesEnabled = true
		}
		if ctx.StartStore != nil && ctx.History != nil {
			ctx.StartRoutesEnabled = true
		}
		addr, err := parseListenAddr(cmd.PublicListen)
		if err != nil {
			log.Fatalf("--public-listen: %s", err)
		}
		cancelMux := newCancelMux(ctx.CancelStore, ctx.Config.Notifications, removeT, getProgress,
			ctx.StartStore, retryHistory, ctx.History, accessLog)
		registerNotifyCompleteRoute(cancelMux, ctx.Config.Ntfy, ctx.Config.Notifications, accessLog)
		go startWebServer("public", cancelMux, addr)

		if cmd.PrivateListen != "" {
			histAddr, err := parseListenAddr(cmd.PrivateListen)
			if err != nil {
				log.Fatalf("--private-listen: %s", err)
			}
			if ctx.History == nil {
				log.Warnf("--private-listen is set but --history-file was not provided; history page will return 404")
			}
			go startWebServer("private", newWebMux(ctx.History, retryHistory, feedConfigured, feedGroups, forgetHistory), histAddr)
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
		mux := newWebMux(ctx.History, retryHistory, feedConfigured, feedGroups, forgetHistory)
		if ctx.CancelStore != nil {
			registerCancelRoutes(mux, ctx.CancelStore, ctx.Config.Notifications, removeT, getProgress, accessLog)
			ctx.CancelRoutesEnabled = true
		}
		if ctx.StartStore != nil && ctx.History != nil {
			registerStartRoutes(mux, ctx.StartStore, ctx.Config.Notifications, retryHistory, ctx.History, accessLog)
			ctx.StartRoutesEnabled = true
		}
		registerNotifyCompleteRoute(mux, ctx.Config.Ntfy, ctx.Config.Notifications, accessLog)
		go startWebServer("private", mux, addr)
	}
}

func (cmd *WatchCmd) Run(ctx *RunContext) error {
	reloader := &configReloader{
		reload: func() error {
			konf, err := ctx.loadConfig(ctx.configFile)
			if err != nil {
				return err
			}
			ctx.Konf = konf
			return nil
		},
		registerWatch: ctx.Provider.Watch,
		retryInterval: defaultRetryInterval,
		notifyReload: func(err error) {
			notifyConfigReload(ctx.Config.Ntfy, ctx.configFile, err)
		},
	}
	_ = reloader.registerWatch(reloader.onWatchEvent)

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

	// Initialize the cancel and start stores if the HMAC secret is configured.
	// The reaper context is cancelled when Run returns, preventing a goroutine leak.
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	defer reaperCancel()
	if ctx.Config.Notifications.HMACSecret != "" {
		ttl := time.Duration(ctx.Config.Notifications.TokenTTLH) * time.Hour
		ctx.CancelStore = NewStore(ttl)
		ctx.CancelStore.StartReaper(reaperCtx)
		ctx.StartStore = NewStartStore(ttl)
		ctx.StartStore.StartReaper(reaperCtx)
	}

	warnNotifyFeedsWithoutHistory(ctx.Config.Feeds, ctx.History)

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

	retryHistory := func(rec HistoryRecord) (int64, error) {
		reloader.mu.Lock()
		defer reloader.mu.Unlock()
		return retryHistoryItem(ctx, rec)
	}

	// forgetHistory removes a (feed, guid) pair from both the seen cache and
	// history, so it can be freshly re-evaluated (e.g. after a config change).
	// Neither file is saved immediately; the next scheduled once.Run() tick's
	// SaveCache/SaveHistory calls persist the removal.
	forgetHistory := func(feed, guid string) (bool, error) {
		reloader.mu.Lock()
		defer reloader.mu.Unlock()
		ctx.Cache.RemoveEntry(feed, guid)
		return ctx.History.RemoveRecord(feed, guid), nil
	}

	// feedConfigured checks against the live (possibly reloaded) config, so
	// the history page's Torrent button reflects the config as it stands at
	// render time, not as it was when the server started.
	feedConfigured := func(name string) bool {
		reloader.mu.Lock()
		defer reloader.mu.Unlock()
		_, ok := findFeedByName(ctx.Config.Feeds, name)
		return ok
	}

	// feedGroups exposes a feed's own configured Groups against the live
	// config, so the history page can break primary-row ties using the same
	// group evaluation the feed itself uses during processing.
	feedGroups := func(name string) []Group {
		reloader.mu.Lock()
		defer reloader.mu.Unlock()
		f, ok := findFeedByName(ctx.Config.Feeds, name)
		if !ok {
			return nil
		}
		return f.Groups
	}

	accessLog := openAccessLog(cmd.AccessLog)

	setupWebServers(cmd, ctx, removeT, getProgress, retryHistory, feedConfigured, feedGroups, forgetHistory, accessLog)

	var g *Gluetun
	if ctx.Config.Gluetun.Host != "" && ctx.Config.Gluetun.Port != 0 {
		g = NewGluetun(ctx.Config.Gluetun, ctx.Transmission)
	}
	if g != nil || ctx.Config.PortCheck.Enabled {
		go NewPortMonitor(ctx.Transmission, g, ctx.Config.Ntfy).Run()
	}

	// Run once and then sleep between later runs...
	for ; true; <-ticker.C {
		reloader.mu.Lock()
		if err := once.Run(ctx); err != nil {
			return err
		}
		reloader.mu.Unlock()
	}
	return nil
}
