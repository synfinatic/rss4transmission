package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
	"github.com/sirupsen/logrus"
)

const defaultRetryInterval = 60 * time.Second

// defaultConfigReloadDebounce coalesces bursts of rapid-fire config file
// watch events (see configReloader.debounceInterval) into a single
// reload+notify. Bind-mount layers such as Docker Desktop's file sharing
// have been observed propagating one host-side save as 3+ distinct fsnotify
// events within a couple hundred milliseconds of each other.
const defaultConfigReloadDebounce = 1 * time.Second

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

// logNtfyStatus reports at startup whether ntfy push notifications are
// enabled and, if so, which topics are active: Topic gates torrent
// started/found/completed notifications, AlertTopic gates config-reload and
// port-state notifications, and either can be configured without the other.
func logNtfyStatus(cfg NtfyConfig) {
	if cfg.BaseURL == "" {
		log.Infof("ntfy notifications disabled (Ntfy.BaseURL not configured)")
		return
	}

	var active []string
	if cfg.Topic != "" {
		active = append(active, fmt.Sprintf("torrent (topic: %s)", cfg.Topic))
	}
	if cfg.AlertTopic != "" {
		active = append(active, fmt.Sprintf("alert (topic: %s)", cfg.AlertTopic))
	}

	if len(active) == 0 {
		log.Infof("ntfy notifications configured but no topics set; no notifications will be sent")
		return
	}
	log.Infof("ntfy notifications enabled: %s", strings.Join(active, ", "))
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

	// snapshot returns the config in effect. doReload and recover call it
	// under mu and hand the result to notifyReload, so the notification is
	// built from a config nothing is concurrently replacing.
	snapshot     func() Config
	notifyReload func(cfg Config, err error)

	// debounceInterval coalesces bursts of rapid-fire watch events into a
	// single reload+notify. Some bind-mount layers (e.g. Docker Desktop's
	// file sharing) propagate one host-side save as multiple distinct
	// fsnotify events spaced further apart than koanf's own 5ms
	// identical-event dedup window, so without this each one independently
	// reloads and notifies. Zero (the default) disables debouncing and
	// reloads synchronously, which is what every non-debounce test expects.
	//
	// Debouncing is implemented with a generation counter rather than
	// resetting a single *time.Timer via Stop()/AfterFunc: Stop() returning
	// false only means the timer's callback has already been scheduled to
	// run, not that it has finished — so a reset-based implementation can
	// still let a "cancelled" timer's callback fire, producing a duplicate
	// reload. Instead, every event bumps debounceGen and schedules its own
	// timer capturing that generation; a timer only calls doReload() if its
	// captured generation is still the latest when it fires, so at most one
	// timer per burst ever actually reloads, regardless of firing order.
	debounceInterval time.Duration
	debounceMu       sync.Mutex
	debounceGen      uint64
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
		// Invalidate any debounced reload still pending from before the
		// watcher died: recover() below performs its own reload/re-register
		// cycle, so a stale timer must not fire a redundant doReload()
		// racing with it.
		r.debounceMu.Lock()
		r.debounceGen++
		r.debounceMu.Unlock()

		log.Warnf("config file watcher stopped (%s); reloading and re-registering", err)
		go r.recover()
		return
	}

	if r.debounceInterval <= 0 {
		r.doReload()
		return
	}

	r.debounceMu.Lock()
	r.debounceGen++
	gen := r.debounceGen
	r.debounceMu.Unlock()

	time.AfterFunc(r.debounceInterval, func() {
		r.debounceMu.Lock()
		current := gen == r.debounceGen
		r.debounceMu.Unlock()
		if current {
			r.doReload()
		}
	})
}

// doReload performs the actual config reload and notification. It's called
// either synchronously from onWatchEvent (debounceInterval == 0) or once a
// burst of events has settled (debounceInterval > 0).
func (r *configReloader) doReload() {
	var cfg Config
	reloadErr := func() error {
		// don't change the config while we are processing the feed
		r.mu.Lock()
		defer r.mu.Unlock()

		log.Infof("config changed. reloading...")
		err := r.reload()
		cfg = r.currentConfig()
		return err
	}()

	if reloadErr != nil {
		log.WithError(reloadErr).Errorf("failed to reload config file")
	}
	// notify outside the lock: notifyConfigReload does a synchronous HTTP
	// POST with a 30s timeout, and holding r.mu that long would block the
	// ticker loop's once.Run and the web handlers that also take r.mu.
	if r.notifyReload != nil {
		r.notifyReload(cfg, reloadErr)
	}
}

// currentConfig is the config in effect, or a zero one when no source was
// wired. The caller must hold mu.
func (r *configReloader) currentConfig() Config {
	if r.snapshot == nil {
		return Config{}
	}
	return r.snapshot()
}

// recover retries reload until it succeeds, then re-registers the watch. It
// blocks (via retryLoadConfig) until the config file is readable again, so
// callers run it in its own goroutine.
func (r *configReloader) recover() {
	notifiedFailure := false
	var cfg Config
	attempt := retryLoadConfig(func() error {
		reloadErr := func() error {
			r.mu.Lock()
			defer r.mu.Unlock()
			err := r.reload()
			cfg = r.currentConfig()
			return err
		}()

		// Report the first failure so a bad edit saved via atomic
		// rename/replace (which routes through this recovery path rather
		// than onWatchEvent's direct reload) is still visible, but don't
		// spam a notification per retry attempt.
		if reloadErr != nil && !notifiedFailure {
			notifiedFailure = true
			if r.notifyReload != nil {
				r.notifyReload(cfg, reloadErr)
			}
		}
		return reloadErr
	}, r.retryInterval)

	log.Infof("config reloaded after %d attempt(s), re-registering file watcher", attempt)
	if r.notifyReload != nil {
		r.notifyReload(cfg, nil)
	}
	if err := r.registerWatch(r.onWatchEvent); err != nil {
		log.WithError(err).Errorf("failed to re-register config file watcher")
	}
}

// liveConfig returns the config currently in effect. It takes the same lock
// the reload path takes, so a caller never observes a half-applied config.
//
// Returning a copy is safe: a reload replaces ctx.Config wholesale rather than
// mutating it, and loadConfig compiles every feed and extractor before it
// commits, so nothing inside the returned value is mutated later.
func (r *configReloader) liveConfig(ctx *RunContext) Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ctx.Config
}

// liveState is the set of accessors setupWebServers hands to the HTTP
// handlers. Every one of them reads the value in effect right now, so a config
// reload shows up on the next request instead of at the next restart.
type liveState struct {
	Config  func() Config
	Speed   func() *SpeedFile
	Actions func() speedActions
	// ExitIP returns the exit-IP source in effect, which is nil when Gluetun
	// is not configured. The VPN page reads that nil as "there is no authority
	// on the exit IP" and falls back to what the last measurement saw, so the
	// getter has to be able to answer nil rather than a func that always says
	// "unknown".
	ExitIP func() exitIPFunc
}

// setupWebServers wires and starts the HTTP listener(s) for /cancel, /start,
// /notify-complete, /healthz, and (on the private listener) the history UI,
// based on which of --public-listen / --private-listen were configured. It
// sets ctx.CancelRoutesEnabled / ctx.StartRoutesEnabled to reflect what was
// actually registered. Factored out of WatchCmd.Run to keep its cyclomatic
// complexity down.
func setupWebServers(cmd *WatchCmd, ctx *RunContext, live liveState, removeT removeFunc,
	getProgress progressFunc, retryHistory retryFunc, feedConfigured func(string) bool,
	feedGroups func(string) []Group, forgetHistory forgetFunc, accessLog *logrus.Logger,
) {
	// Every gate below is a predicate rather than a bool: the routes are
	// registered once and decide per request, so a config reload can turn a
	// page on or off without a restart. The nav bar uses the same predicates,
	// so a link never leads to a 404.
	notif := func() NotificationsConfig { return live.Config().Notifications }
	ntfy := func() NtfyConfig { return live.Config().Ntfy }
	tx := func() Transmission { return live.Config().Transmission }
	nav := navConfig{
		Speedtest:    func() bool { return live.Speed() != nil },
		Transmission: func() bool { return transmissionProxyTarget(tx()) != nil },
	}

	if cmd.PublicListen != "" {
		// Split-listener mode: /cancel, /start, /notify-complete, and /healthz on the
		// public port, history on a separate private port. Cancel/start routes are NOT
		// registered on the private mux.
		ctx.CancelRoutesEnabled = true
		ctx.StartRoutesEnabled = ctx.History != nil
		addr, err := parseListenAddr(cmd.PublicListen)
		if err != nil {
			log.Fatalf("--public-listen: %s", err)
		}
		cancelMux := newCancelMux(ctx.CancelStore, notif, removeT, getProgress,
			ctx.StartStore, retryHistory, ctx.History, accessLog)
		registerNotifyCompleteRoute(cancelMux, ntfy, notif, accessLog)
		go startWebServer("public", cancelMux, addr)

		if cmd.PrivateListen != "" {
			histAddr, err := parseListenAddr(cmd.PrivateListen)
			if err != nil {
				log.Fatalf("--private-listen: %s", err)
			}
			if ctx.History == nil {
				log.Warnf("--private-listen is set but --history-file was not provided; history page will return 404")
			}
			privMux := newWebMux(ctx.History, retryHistory, feedConfigured, feedGroups, forgetHistory, nav)
			registerSpeedRoutes(privMux, live.Speed, ctx.PeerPortOpen, ctx.PeerPort, live.ExitIP, live.Actions, nav)
			registerTransmissionRoutes(privMux, tx, nav)
			go startWebServer("private", privMux, histAddr)
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
		mux := newWebMux(ctx.History, retryHistory, feedConfigured, feedGroups, forgetHistory, nav)
		registerSpeedRoutes(mux, live.Speed, ctx.PeerPortOpen, ctx.PeerPort, live.ExitIP, live.Actions, nav)
		registerTransmissionRoutes(mux, tx, nav)
		registerCancelRoutes(mux, ctx.CancelStore, notif, removeT, getProgress, accessLog)
		ctx.CancelRoutesEnabled = true
		if ctx.History != nil {
			registerStartRoutes(mux, ctx.StartStore, notif, retryHistory, ctx.History, accessLog)
			ctx.StartRoutesEnabled = true
		}
		registerNotifyCompleteRoute(mux, ntfy, notif, accessLog)
		go startWebServer("private", mux, addr)
	}
}

// newConfigReloader builds the reloader watch runs: it reads the config file
// again and pushes what it read into the running components. loadConfig
// commits nothing until every check passes, so a rejected edit leaves both the
// config and the components as they were.
func newConfigReloader(ctx *RunContext) *configReloader {
	return &configReloader{
		reload: func() error {
			prev := ctx.Config
			if err := ctx.loadConfig(ctx.configFile); err != nil {
				return err
			}
			return ctx.applyConfig(prev, ctx.Config)
		},
		snapshot:         func() Config { return ctx.Config },
		registerWatch:    ctx.Provider.Watch,
		retryInterval:    defaultRetryInterval,
		debounceInterval: defaultConfigReloadDebounce,
		notifyReload: func(cfg Config, err error) {
			notifyConfigReload(cfg.Ntfy, ctx.configFile, err)
		},
	}
}

func (cmd *WatchCmd) Run(ctx *RunContext) error {
	reloader := newConfigReloader(ctx)
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

	// The stores are created whether or not a HMAC secret is configured right
	// now: adding one to the config file must start handing out tokens without
	// a restart. The routes gate on the live secret per request, and once.go
	// checks it again before it puts a button in a notification.
	//
	// The reaper context is cancelled when Run returns, preventing a goroutine
	// leak.
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	defer reaperCancel()
	ttl := time.Duration(ctx.Config.Notifications.TokenTTLH) * time.Hour
	ctx.CancelStore = NewStore(ttl)
	ctx.CancelStore.StartReaper(reaperCtx)
	ctx.StartStore = NewStartStore(ttl)
	ctx.StartStore.StartReaper(reaperCtx)

	warnNotifyFeedsWithoutHistory(ctx.Config.Feeds, ctx.History)
	logNtfyStatus(ctx.Config.Ntfy)

	removeT := func(rCtx context.Context, ids []int64) error {
		return ctx.Tx().TorrentRemove(rCtx, transmissionrpc.TorrentRemovePayload{
			IDs:             ids,
			DeleteLocalData: false,
		})
	}
	getProgress := func(rCtx context.Context, torrentID int64) (int64, float64, error) {
		torrents, err := ctx.Tx().TorrentGet(rCtx,
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

	// The accessors the web handlers read through. Each takes the reload lock,
	// so a request always sees a fully applied config.
	live := liveState{
		Config: func() Config { return reloader.liveConfig(ctx) },
		Speed: func() *SpeedFile {
			reloader.mu.Lock()
			defer reloader.mu.Unlock()
			return ctx.Speed
		},
		Actions: func() speedActions {
			reloader.mu.Lock()
			defer reloader.mu.Unlock()
			return ctx.SpeedActions
		},
		ExitIP: func() exitIPFunc {
			reloader.mu.Lock()
			defer reloader.mu.Unlock()
			return ctx.ExitIP
		},
	}

	accessLog := openAccessLog(cmd.AccessLog)

	// The port monitor runs whether or not anything needs checking right now.
	// Its check() returns early while Gluetun and PortCheck are both off, so
	// turning either one on is a config edit rather than a restart.
	ctx.PortMonitor = NewPortMonitor(ctx.Tx(), nil, ctx.Config.Ntfy)
	ctx.PeerPortOpen = ctx.PortMonitor.LastOpen
	ctx.PeerPort = ctx.PortMonitor.LastPeerPort

	// Builds the Gluetun client, the speed monitor and the VPN page's actions
	// from the config as loaded. An empty previous config makes every block
	// count as changed, so startup and reload run the same code and cannot
	// drift apart. It runs before the web servers so /metrics can read peer
	// port state and ctx.Speed is populated before the first request.
	if err := ctx.applyConfig(Config{}, ctx.Config); err != nil {
		return err
	}

	setupWebServers(cmd, ctx, live, removeT, getProgress, retryHistory,
		feedConfigured, feedGroups, forgetHistory, accessLog)

	go ctx.PortMonitor.Run()

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
