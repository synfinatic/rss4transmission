package main

/*
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
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/showwin/speedtest-go/speedtest"
)

// SpeedResult is one throughput measurement taken over the VPN.
//
// Error and Skipped are mutually exclusive with a real measurement: a run that
// failed or was deliberately skipped is still recorded (so the web UI and
// /metrics show the gap) but carries a zero DownloadMbps that must never be
// interpreted as "the link is slow". Use OK() to tell them apart.
type SpeedResult struct {
	At           time.Time `json:"At"`
	DownloadMbps float64   `json:"DownloadMbps"`
	UploadMbps   float64   `json:"UploadMbps,omitempty"`
	LatencyMs    float64   `json:"LatencyMs,omitempty"`
	JitterMs     float64   `json:"JitterMs,omitempty"`
	ServerID     string    `json:"ServerID,omitempty"`
	ServerName   string    `json:"ServerName,omitempty"`
	Sponsor      string    `json:"Sponsor,omitempty"`
	// ExitIP is speedtest.net's view of the connection through Gluetun's proxy,
	// and GluetunExitIP is what Gluetun said about itself when the measurement
	// was recorded (empty in measure-only mode, or before Gluetun first
	// answered). They are kept apart rather than reconciled: providers that NAT
	// per destination hand speedtest.net a different address over the very same
	// tunnel, so a change in ExitIP alone is not a rotation.
	ExitIP        string `json:"ExitIP,omitempty"`
	GluetunExitIP string `json:"GluetunExitIP,omitempty"`
	Error         string `json:"Error,omitempty"`
	Skipped       string `json:"Skipped,omitempty"`
	// RotationNote is what the rotation policy did about a measurement that
	// missed a throughput floor: "rotation requested", or "no rotation: ..."
	// naming what stopped it. It is empty for a run that met the floors, one
	// that measured nothing, and in measure-only mode -- in all of which there
	// is no verdict to report.
	RotationNote string `json:"RotationNote,omitempty"`
}

// OK reports whether this result carries a usable measurement.
func (r SpeedResult) OK() bool {
	return r.Error == "" && r.Skipped == ""
}

// speedTestFunc runs a single throughput measurement over the VPN.
// activeDownloadsFunc reports how many torrents are currently downloading.
// rotateRequestFunc asks for a VPN rotation; nil in measure-only mode.
//
// These mirror the function-type injection style already used for removeFunc
// and progressFunc in web.go: the policy and bookkeeping below stay testable
// without a network, a VPN, or a Transmission instance.
type (
	speedTestFunc       func(ctx context.Context) (SpeedResult, error)
	activeDownloadsFunc func(ctx context.Context) (int, error)
	rotateRequestFunc   func(source, reason string) bool
)

// rotationDecision is what the rotation policy concluded about one measurement.
// Note is filled in only when the policy declined: it is the human-readable
// verdict the measurements table shows, and it exists so a slow row can say
// what stopped the rotation rather than only that none happened.
type rotationDecision struct {
	Rotate bool
	Reason string // why it should rotate; empty unless Rotate
	Note   string // what happened, for SpeedResult.RotationNote
}

// belowThreshold reports which throughput floor this result missed, or "" when
// it met them all. Download is tested first: it is the more common failure and
// makes the more useful reason line when both legs are bad.
//
// Upload matters on its own because the two fail independently -- an exit can
// carry download at full rate and upload nothing at all, which is invisible to
// MinDownloadMbps and fatal on a tracker that measures ratio.
func belowThreshold(cfg SpeedTestConfig, r SpeedResult) string {
	if r.DownloadMbps < cfg.MinDownloadMbps {
		return fmt.Sprintf("download %.1f Mbps below %.1f Mbps threshold",
			r.DownloadMbps, cfg.MinDownloadMbps)
	}

	// A zero floor disables the check, which is the default: without
	// DownloadOnly: false there is no upload measurement to compare against.
	if cfg.MinUploadMbps > 0 && r.UploadMbps < cfg.MinUploadMbps {
		return fmt.Sprintf("upload %.1f Mbps below %.1f Mbps threshold",
			r.UploadMbps, cfg.MinUploadMbps)
	}

	return ""
}

// shouldRotate is the whole rotation policy, kept pure so every branch is
// table-testable. now is passed in rather than read from the clock.
//
// lastRotation is the zero time when we have never rotated.
func shouldRotate(cfg SpeedTestConfig, r SpeedResult, lastRotation time.Time,
	rotationsToday int, now time.Time,
) rotationDecision {
	// A failed or skipped run measured nothing. Its zero DownloadMbps must
	// never be read as "the link is slow", or every proxy hiccup would
	// restart the VPN.
	if !r.OK() {
		return rotationDecision{}
	}

	reason := belowThreshold(cfg, r)
	if reason == "" {
		return rotationDecision{}
	}

	if !lastRotation.IsZero() {
		if cooldown := cfg.CooldownDuration(); now.Sub(lastRotation) < cooldown {
			ago := now.Sub(lastRotation).Round(time.Second)
			log.Infof("Speedtest: %s, but last rotation was %s ago (cooldown %s); not rotating",
				reason, ago, cooldown)
			return rotationDecision{Note: fmt.Sprintf(
				"no rotation: in cooldown, last was %s ago (cooldown %s)", ago, cooldown)}
		}
	}

	// 0 means unlimited, matching how Gluetun treats RotateTime and
	// ClosedPortChecks of 0 as "disabled".
	if cfg.MaxRotationsPerDay > 0 && rotationsToday >= cfg.MaxRotationsPerDay {
		log.Infof("Speedtest: %s, but already rotated %d times in the last 24h (max %d); not rotating",
			reason, rotationsToday, cfg.MaxRotationsPerDay)
		return rotationDecision{Note: fmt.Sprintf(
			"no rotation: %d automatic rotations in the last 24h (max %d)",
			rotationsToday, cfg.MaxRotationsPerDay)}
	}

	return rotationDecision{Rotate: true, Reason: reason}
}

// SpeedMonitor periodically measures throughput over the VPN and asks Gluetun
// to re-pick an egress when it looks bad.
//
// It does not rotate the VPN itself: Gluetun has no internal locking and its
// state is serialized by PortMonitor.mu, so rotation goes through
// Gluetun.RequestRotate and lands on the PortMonitor goroutine's next tick.
type SpeedMonitor struct {
	cfg      SpeedTestConfig
	ntfy     NtfyConfig
	store    *SpeedFile
	runTest  speedTestFunc
	active   activeDownloadsFunc
	rotate   rotateRequestFunc // nil => measure-only, no Gluetun configured
	interval time.Duration

	// ExitIP reports which exit Gluetun says we are on, for the rotation alert
	// that names the exit we are leaving. It is set after construction (like
	// Gluetun.OnRotated) because the port monitor that caches it is built
	// separately; nil, or unknown, simply leaves the exit out of the alert.
	//
	// A measurement's own ExitIP is not a substitute: that is speedtest.net's
	// view through the proxy, which on some providers drifts per destination
	// over a tunnel that never moved.
	ExitIP exitIPFunc

	trigger chan struct{} // buffered(1): an out-of-band measurement request

	// stateMu guards measuring, which is written by the Run goroutine and read
	// by Trigger from whichever goroutine asked for a measurement.
	stateMu   sync.Mutex
	measuring bool
}

func NewSpeedMonitor(cfg SpeedTestConfig, ntfy NtfyConfig, store *SpeedFile,
	runTest speedTestFunc, active activeDownloadsFunc, rotate rotateRequestFunc,
) *SpeedMonitor {
	return &SpeedMonitor{
		cfg:      cfg,
		ntfy:     ntfy,
		store:    store,
		runTest:  runTest,
		active:   active,
		rotate:   rotate,
		interval: cfg.IntervalDuration(),
		trigger:  make(chan struct{}, 1),
	}
}

// Trigger asks for an on-demand measurement without waiting for the next tick.
// It returns false when one is already queued or still running, so a burst of
// requests costs one measurement rather than one per request.
//
// Covering the running case matters as much as the queued one: draining the
// trigger channel is not the end of the request, since the measurement it
// queued still has tens of seconds to go, and a speedtest is hundreds of
// megabytes. The lock is what makes the check exact -- a plain flag read could
// slip through in the instant Run() picks the request up.
//
// It is the only SpeedMonitor method safe to call from another goroutine while
// Run() is going.
func (m *SpeedMonitor) Trigger() bool {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.measuring {
		return false
	}

	select {
	case m.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

// measuring is held across the scheduled measurement too, not just the manual
// one: both spend the same bandwidth, so neither should have a second test
// queued behind it.
func (m *SpeedMonitor) whileMeasuring(f func()) {
	m.stateMu.Lock()
	m.measuring = true
	m.stateMu.Unlock()

	defer func() {
		m.stateMu.Lock()
		m.measuring = false
		m.stateMu.Unlock()
	}()

	f()
}

// Run blocks forever, measuring every Interval and whenever Trigger() is
// called. Call it in its own goroutine.
//
// The first measurement is deliberately deferred by one full interval: at
// startup Gluetun may still be establishing the tunnel, and a test against a
// half-open VPN would report a spuriously bad number.
func (m *SpeedMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.whileMeasuring(func() { m.tick(ctx) })
		case <-m.trigger:
			m.whileMeasuring(func() { m.measureNow(ctx) })
		}
	}
}

// tick performs one scheduled measure-and-decide cycle: gated by
// SkipWhenActive, and its result feeds the rotation policy.
func (m *SpeedMonitor) tick(ctx context.Context) {
	result, blocked := m.measure(ctx, m.cfg.SkipWhenActive)

	// decide() runs before record() so its verdict is stored on the row it
	// describes; in measure-only mode there is no policy to report on.
	if m.rotate != nil {
		result.RotationNote = m.decide(result, blocked)
	}

	m.record(result)
	m.save()
}

// measureNow is the VPN page's "Run speedtest now" button. It ignores
// SkipWhenActive -- an explicit click asked for a number, even a number dragged
// down by an active download -- and deliberately never calls decide(): the page
// has its own Rotate button, so a manual measurement must not cause a VPN
// restart the user did not ask for.
func (m *SpeedMonitor) measureNow(ctx context.Context) {
	result, _ := m.measure(ctx, false)
	// Never rotates, so a slow row from here says as much: otherwise it looks
	// identical to a scheduled run whose rotation was silently declined.
	if m.rotate != nil && result.OK() && belowThreshold(m.cfg, result) != "" {
		result.RotationNote = "no rotation: on-demand measurement"
	}
	m.record(result)
	m.save()
}

// record stores a result.
//
// It deliberately does not touch the rotation history: a measurement's ExitIP
// is speedtest.net's view, and only Gluetun can say which exit a rotation
// actually landed on. vpnRotatedHook fills that in.
func (m *SpeedMonitor) record(result SpeedResult) {
	// Stamped here rather than in the runner: the runner talks to speedtest.net
	// through the proxy and has no way to ask Gluetun anything. The value is the
	// port monitor's cache, so it can be up to one check (5 minutes) old --
	// close enough to attribute a measurement to a tunnel, since a rotation
	// refreshes it.
	result.GluetunExitIP = m.currentExitIP()
	m.store.AddResult(result)
}

// currentExitIP returns Gluetun's view of the exit we are on, or "" when there
// is no Gluetun to ask or it has not answered yet.
func (m *SpeedMonitor) currentExitIP() string {
	if m.ExitIP == nil {
		return ""
	}
	if ip, known := m.ExitIP(); known {
		return ip
	}
	return ""
}

func (m *SpeedMonitor) save() {
	if err := m.store.Save(m.cfg.RetentionDuration()); err != nil {
		log.WithError(err).Warn("Unable to save speedtest results")
	}
}

// measure runs the test unless the active-torrent gate blocks it. skipWhenActive
// is passed in rather than read from the config so both callers are explicit:
// the scheduled path honors the setting, the manual one never does.
//
// The second return value is why this run may not rotate, or "" when it may:
// when we could not determine whether torrents are active we still measure but
// refuse to rotate, since rotating blind could interrupt an active download. It
// is phrased for display -- decide() puts it on the measurement so the page can
// say what stopped the rotation.
func (m *SpeedMonitor) measure(ctx context.Context, skipWhenActive bool) (SpeedResult, string) {
	blocked := ""

	if m.active != nil {
		n, err := m.active(ctx)
		switch {
		case err != nil:
			log.WithError(err).Warn("Unable to check for active torrents; will measure but not rotate")
			blocked = "could not check for active downloads"
		case n > 0 && skipWhenActive:
			// Testing during a download both steals bandwidth from the
			// download and reads low because of it.
			reason := fmt.Sprintf("%d torrent(s) downloading", n)
			log.Infof("Speedtest: skipping, %s", reason)
			return SpeedResult{At: time.Now(), Skipped: reason}, reason
		case n > 0:
			blocked = fmt.Sprintf("%d torrent(s) downloading", n)
		}
	}

	result, err := m.runTest(ctx)
	result.At = time.Now()
	if err != nil {
		log.WithError(err).Warn("Speedtest failed")
		result.Error = err.Error()
		return result, "the measurement failed"
	}

	log.Infof("Speedtest: %.1f Mbps down, %.1f Mbps up, %.0f ms latency via %s (exit %s)",
		result.DownloadMbps, result.UploadMbps, result.LatencyMs, result.ServerName, result.ExitIP)

	return result, blocked
}

// decide applies the rotation policy to a fresh result and, when it says so,
// records the event and asks Gluetun to rotate.
//
// blocked is why this particular run may not rotate whatever the policy says
// ("" when it may). The returned string is the verdict to store on the
// measurement; see SpeedResult.RotationNote.
func (m *SpeedMonitor) decide(result SpeedResult, blocked string) string {
	// Only a run that actually missed a floor has a rotation story: everything
	// else would be reporting on a decision that was never in play.
	if !result.OK() || belowThreshold(m.cfg, result) == "" {
		return ""
	}
	if blocked != "" {
		return "no rotation: " + blocked
	}

	var lastRotation time.Time
	if last, ok := m.store.LastRotation(); ok {
		lastRotation = last.At
	}
	// Manual rotations are excluded: MaxRotationsPerDay is a budget on the
	// daemon's own churn, and a person clicking the button has already decided
	// the rotation is worth it.
	rotationsToday := m.store.AutomaticRotationsSince(time.Now().Add(-24 * time.Hour))

	decision := shouldRotate(m.cfg, result, lastRotation, rotationsToday, time.Now())
	if !decision.Rotate {
		return decision.Note
	}

	log.Warnf("Speedtest: requesting VPN rotation: %s", decision.Reason)
	if !m.rotate(RotationSourceSpeedtest, decision.Reason) {
		// A rotation is already pending or under way, so this measurement
		// changed nothing and must not be recorded or alerted as if it had.
		log.Info("Speedtest: a VPN rotation is already in progress; not requesting another")
		return "no rotation: one is already in progress"
	}

	// Staged rather than recorded outright: all we know so far is that the
	// rotation was asked for. vpnRotatedHook fills in where it landed once
	// Gluetun reports the tunnel back up, and replaces the exit below with the
	// one rotate() reads immediately before dropping the tunnel.
	exitIP := m.currentExitIP()
	m.store.StageRotation(RotationEvent{
		At:         time.Now(),
		Source:     RotationSourceSpeedtest,
		Reason:     decision.Reason,
		BeforeMbps: result.DownloadMbps,
		FromExitIP: exitIP,
	})
	notifyVpnRotating(m.ntfy, &NtfyVpnContext{
		Reason:       decision.Reason,
		DownloadMbps: result.DownloadMbps,
		ExitIP:       exitIP,
	})

	return "rotation requested"
}

// validateProxyURL rejects a proxy value speedtest-go would mishandle.
//
// We can only hand speedtest-go a Proxy string; it builds its own transport
// (with its own DialContext, which we must not replace) and reads the string
// from there. That makes up-front validation the only defense, and it matters
// in two ways:
//
//   - On a url.Parse error speedtest-go logs a warning and falls back to
//     http.ProxyFromEnvironment, sending the test over the host's own
//     connection and reporting a number with nothing to do with the VPN.
//   - url.Parse accepts "gluetun:8888" without error, as an opaque URL with
//     scheme "gluetun" and no host, which yields a proxy that cannot be
//     dialed. Hence the explicit scheme and host check.
func validateProxyURL(proxy string) (*url.URL, error) {
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, fmt.Errorf("unable to parse Proxy %q: %w", proxy, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy %q must be a full URL, e.g. http://gluetun:8888", proxy)
	}
	return u, nil
}

// newSpeedtestRunner returns a speedTestFunc that performs a real speedtest.net
// run routed over the VPN via Gluetun's HTTP proxy.
//
// This is the only code that touches speedtest-go; everything above it works
// against the speedTestFunc type so the policy stays testable without a network.
func newSpeedtestRunner(cfg SpeedTestConfig) (speedTestFunc, error) {
	// Validate the proxy here rather than letting speedtest-go swallow a bad
	// value and silently measure the non-VPN path.
	if _, err := validateProxyURL(cfg.Proxy); err != nil {
		return nil, err
	}

	threads := cfg.Threads
	if threads < 1 {
		threads = 1
	}

	return func(ctx context.Context) (SpeedResult, error) {
		result := SpeedResult{At: time.Now()}

		st := newSpeedtestClient(cfg, threads)

		// The IP speedtest.net sees is the VPN exit IP. If it ever matches the
		// host's own public IP, the proxy isn't being used and every number
		// below describes the wrong path.
		user, err := st.FetchUserInfoContext(ctx)
		if err != nil {
			return result, fmt.Errorf("unable to reach speedtest.net via proxy %s: %w", cfg.Proxy, err)
		}
		result.ExitIP = user.IP

		server, err := pickServer(ctx, st, cfg.ServerID)
		if err != nil {
			return result, err
		}
		result.ServerID = server.ID
		result.ServerName = server.Name
		result.Sponsor = server.Sponsor

		if err := server.PingTestContext(ctx, nil); err != nil {
			return result, fmt.Errorf("ping test failed: %w", err)
		}
		result.LatencyMs = float64(server.Latency) / float64(time.Millisecond)
		result.JitterMs = float64(server.Jitter) / float64(time.Millisecond)

		// Bounds how much bandwidth a single run burns; see the cost table in
		// docs/deployment.md.
		st.SetCaptureTime(time.Duration(cfg.CaptureSeconds) * time.Second)
		st.SetNThread(threads)

		if err := server.DownloadTestContext(ctx); err != nil {
			return result, fmt.Errorf("download test failed: %w", err)
		}
		dlMbps, err := downloadMbps(server.DLSpeed)
		if err != nil {
			return result, err
		}
		result.DownloadMbps = dlMbps

		if !cfg.DownloadOnly {
			if err := server.UploadTestContext(ctx); err != nil {
				return result, fmt.Errorf("upload test failed: %w", err)
			}
			mbps, err := uploadMbps(server.ULSpeed)
			if err != nil {
				return result, err
			}
			result.UploadMbps = mbps
		}

		return result, nil
	}, nil
}

// rateNotAvailable is the sentinel speedtest-go uses for "N/A" on both the
// download and upload legs (speedtest/request.go).
const rateNotAvailable speedtest.ByteRate = -1

// downloadMbps converts speedtest-go's download rate to Mbps, or reports the
// dead download leg it declined to report itself.
//
// When every download request fails, speedtest-go returns no error and instead
// sets DLSpeed to the -1 sentinel it prints as "N/A" (speedtest/request.go).
// Divided by 125000 that is -0.000008 Mbps, which is indistinguishable from a
// real measurement of a very slow link: it renders as "-0.0" in the web UI and
// it sits below any MinDownloadMbps floor, so it requests a rotation on every
// run. Rotation cannot fix it. Report it as the failure it is instead, which
// leaves OK() false and stops shouldRotate before it looks at the floors.
//
// A genuine zero is left alone: speedtest-go sets the sentinel only when the
// request error rate also exceeds 10%, so a zero rate here is a measurement of
// a dead link and the download floor must still act on it.
func downloadMbps(rate speedtest.ByteRate) (float64, error) {
	if rate == rateNotAvailable {
		return 0, fmt.Errorf("download test reported no throughput: every download request " +
			"to the speedtest server failed")
	}
	return rate.Mbps(), nil
}

// uploadMbps converts speedtest-go's upload rate to Mbps, or reports the dead
// upload leg it declined to report itself.
//
// When every upload request fails, speedtest-go returns no error and instead
// sets ULSpeed to the -1 sentinel it prints as "N/A" (speedtest/request.go).
// Divided by 125000 that is -0.000008 Mbps, which is indistinguishable from a
// real measurement of a very slow link: it renders as "-0.0" in the web UI and
// it sits below any MinUploadMbps floor, so it requests a rotation on every
// run. Rotation cannot fix it. The server is chosen from speedtest.net's list
// and not from the exit, so a server with a broken upload endpoint survives
// every rotation. Report it as the failure it is instead, which leaves OK()
// false and stops shouldRotate before it looks at the floors.
//
// A genuine zero is left alone: speedtest-go sets the sentinel only when the
// request error rate also exceeds 10%, so a zero rate here is a measurement of
// a dead link and the upload floor must still act on it.
func uploadMbps(rate speedtest.ByteRate) (float64, error) {
	if rate == rateNotAvailable {
		return 0, fmt.Errorf("upload test reported no throughput: every upload request " +
			"to the speedtest server failed")
	}
	return rate.Mbps(), nil
}

// speedtestConfigHost serves the config and server-list endpoints, and is the
// only host behind the shared Cloudflare cache. The throughput legs run against
// per-server hosts, whose URLs carry parameters the server itself parses.
const speedtestConfigHost = "www.speedtest.net"

// cacheBusterParam is namespaced so it cannot collide with a parameter Ookla
// reads.
const cacheBusterParam = "r4tnocache"

// cacheBuster forces www.speedtest.net requests past the Cloudflare cache.
//
// speedtest-config.php reports the caller's IP and coordinates, and Cloudflare
// caches it without regard to who is asking. A HIT therefore returns the
// response built for whoever populated that edge: observed values included a
// Cox Business address in Tempe, a Comcast address, and a Zscaler address, none
// of them this host's exit. speedtest-go reads that as its own identity and
// picks a server near the stranger, which is how a fixed tunnel produced a
// server list that moved between Los Angeles, Texas, and Arizona.
//
// Cloudflare's default cache key includes the query string, so a value that is
// unique per request reaches the origin.
type cacheBuster struct {
	next http.RoundTripper
	seq  atomic.Uint64
}

func (c *cacheBuster) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != speedtestConfigHost {
		return c.next.RoundTrip(req)
	}
	// RoundTrip must not modify the request it is given.
	clone := req.Clone(req.Context())
	q := clone.URL.Query()
	q.Set(cacheBusterParam, c.token())
	clone.URL.RawQuery = q.Encode()
	return c.next.RoundTrip(clone)
}

// token pairs the clock with a counter: the counter alone repeats across a
// restart, and the clock alone can repeat when two requests share a nanosecond.
func (c *cacheBuster) token() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" +
		strconv.FormatUint(c.seq.Add(1), 36)
}

// newSpeedtestClient builds the speedtest-go client with its HTTP path wrapped
// in a cacheBuster, so identity and server selection come from the origin.
//
// It also repairs a global that speedtest-go mutates. New() assigns its doer to
// http.DefaultClient and calls NewUserConfig, which sets that client's Transport
// to the Speedtest itself -- and it does both before it applies any option, so
// WithDoer redirects later requests without undoing the assignment. Left alone,
// every http.DefaultClient caller in this process (Gluetun.control,
// fetchTorrentBytes) moves onto the VPN proxy after the first measurement.
// Saving and restoring the field is racy in principle, but the window spans one
// constructor with no I/O in it.
func newSpeedtestClient(cfg SpeedTestConfig, threads int) *speedtest.Speedtest {
	saved := http.DefaultClient.Transport
	client := &http.Client{}

	// WithDoer must precede WithUserConfig: NewUserConfig is what assigns
	// client.Transport, and it reads whichever doer is current when it runs.
	st := speedtest.New(
		speedtest.WithDoer(client),
		speedtest.WithUserConfig(&speedtest.UserConfig{
			Proxy:          cfg.Proxy,
			UserAgent:      fmt.Sprintf("rss4transmission/%s", Version),
			MaxConnections: threads,
			// HTTP ping is mandatory here: ICMP and raw-TCP probes cannot
			// traverse an HTTP CONNECT proxy, so any other mode would either
			// fail outright or measure the host's path instead of the VPN's.
			PingMode: speedtest.HTTP,
		}),
	)

	http.DefaultClient.Transport = saved
	client.Transport = &cacheBuster{next: client.Transport}
	return st
}

// pickServer returns the configured speedtest.net server, or the closest one
// when no ServerID is pinned.
func pickServer(ctx context.Context, st *speedtest.Speedtest, serverID string) (*speedtest.Server, error) {
	if serverID != "" {
		server, err := st.FetchServerByIDContext(ctx, serverID)
		if err != nil {
			return nil, fmt.Errorf("unable to fetch speedtest server %s: %w", serverID, err)
		}
		return server, nil
	}

	servers, err := st.FetchServerListContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to fetch speedtest server list: %w", err)
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("speedtest.net returned no servers")
	}
	return servers[0], nil
}
