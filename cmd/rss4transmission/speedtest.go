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
	"net/url"
	"sync"
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
	ExitIP       string    `json:"ExitIP,omitempty"`
	Error        string    `json:"Error,omitempty"`
	Skipped      string    `json:"Skipped,omitempty"`
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
) (bool, string) {
	// A failed or skipped run measured nothing. Its zero DownloadMbps must
	// never be read as "the link is slow", or every proxy hiccup would
	// restart the VPN.
	if !r.OK() {
		return false, ""
	}

	reason := belowThreshold(cfg, r)
	if reason == "" {
		return false, ""
	}

	if !lastRotation.IsZero() {
		if cooldown := cfg.CooldownDuration(); now.Sub(lastRotation) < cooldown {
			log.Infof("Speedtest: %s, but last rotation was %s ago (cooldown %s); not rotating",
				reason, now.Sub(lastRotation).Round(time.Second), cooldown)
			return false, ""
		}
	}

	// 0 means unlimited, matching how Gluetun treats RotateTime and
	// ClosedPortChecks of 0 as "disabled".
	if cfg.MaxRotationsPerDay > 0 && rotationsToday >= cfg.MaxRotationsPerDay {
		log.Infof("Speedtest: %s, but already rotated %d times in the last 24h (max %d); not rotating",
			reason, rotationsToday, cfg.MaxRotationsPerDay)
		return false, ""
	}

	return true, reason
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
	result, canRotate := m.measure(ctx, m.cfg.SkipWhenActive)
	m.record(result)

	if canRotate && m.rotate != nil {
		m.decide(result)
	}

	m.save()
}

// measureNow is the VPN page's "Run speedtest now" button. It ignores
// SkipWhenActive -- an explicit click asked for a number, even a number dragged
// down by an active download -- and deliberately never calls decide(): the page
// has its own Rotate button, so a manual measurement must not cause a VPN
// restart the user did not ask for.
func (m *SpeedMonitor) measureNow(ctx context.Context) {
	result, _ := m.measure(ctx, false)
	m.record(result)
	m.save()
}

// record stores a result.
//
// It deliberately does not touch the rotation history: a measurement's ExitIP
// is speedtest.net's view, and only Gluetun can say which exit a rotation
// actually landed on. vpnRotatedHook fills that in.
func (m *SpeedMonitor) record(result SpeedResult) {
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
// The second return value reports whether the result may be acted on: when we
// could not determine whether torrents are active we still measure, but refuse
// to rotate, since rotating blind could interrupt an active download.
func (m *SpeedMonitor) measure(ctx context.Context, skipWhenActive bool) (SpeedResult, bool) {
	canRotate := true

	if m.active != nil {
		n, err := m.active(ctx)
		switch {
		case err != nil:
			log.WithError(err).Warn("Unable to check for active torrents; will measure but not rotate")
			canRotate = false
		case n > 0 && skipWhenActive:
			// Testing during a download both steals bandwidth from the
			// download and reads low because of it.
			reason := fmt.Sprintf("%d torrent(s) downloading", n)
			log.Infof("Speedtest: skipping, %s", reason)
			return SpeedResult{At: time.Now(), Skipped: reason}, false
		case n > 0:
			canRotate = false
		}
	}

	result, err := m.runTest(ctx)
	result.At = time.Now()
	if err != nil {
		log.WithError(err).Warn("Speedtest failed")
		result.Error = err.Error()
		return result, false
	}

	log.Infof("Speedtest: %.1f Mbps down, %.1f Mbps up, %.0f ms latency via %s (exit %s)",
		result.DownloadMbps, result.UploadMbps, result.LatencyMs, result.ServerName, result.ExitIP)

	return result, canRotate
}

// decide applies the rotation policy to a fresh result and, when it says so,
// records the event and asks Gluetun to rotate.
func (m *SpeedMonitor) decide(result SpeedResult) {
	var lastRotation time.Time
	if last, ok := m.store.LastRotation(); ok {
		lastRotation = last.At
	}
	// Manual rotations are excluded: MaxRotationsPerDay is a budget on the
	// daemon's own churn, and a person clicking the button has already decided
	// the rotation is worth it.
	rotationsToday := m.store.AutomaticRotationsSince(time.Now().Add(-24 * time.Hour))

	rotate, reason := shouldRotate(m.cfg, result, lastRotation, rotationsToday, time.Now())
	if !rotate {
		return
	}

	log.Warnf("Speedtest: requesting VPN rotation: %s", reason)
	if !m.rotate(RotationSourceSpeedtest, reason) {
		// A rotation is already pending or under way, so this measurement
		// changed nothing and must not be recorded or alerted as if it had.
		log.Info("Speedtest: a VPN rotation is already in progress; not requesting another")
		return
	}

	// Staged rather than recorded outright: all we know so far is that the
	// rotation was asked for. vpnRotatedHook fills in where it landed once
	// Gluetun reports the tunnel back up, and replaces the exit below with the
	// one rotate() reads immediately before dropping the tunnel.
	exitIP := m.currentExitIP()
	m.store.StageRotation(RotationEvent{
		At:         time.Now(),
		Source:     RotationSourceSpeedtest,
		Reason:     reason,
		BeforeMbps: result.DownloadMbps,
		FromExitIP: exitIP,
	})
	notifyVpnRotating(m.ntfy, &NtfyVpnContext{
		Reason:       reason,
		DownloadMbps: result.DownloadMbps,
		ExitIP:       exitIP,
	})
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

		st := speedtest.New(speedtest.WithUserConfig(&speedtest.UserConfig{
			Proxy:          cfg.Proxy,
			UserAgent:      fmt.Sprintf("rss4transmission/%s", Version),
			MaxConnections: threads,
			// HTTP ping is mandatory here: ICMP and raw-TCP probes cannot
			// traverse an HTTP CONNECT proxy, so any other mode would either
			// fail outright or measure the host's path instead of the VPN's.
			PingMode: speedtest.HTTP,
		}))

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
		result.DownloadMbps = server.DLSpeed.Mbps()

		if !cfg.DownloadOnly {
			if err := server.UploadTestContext(ctx); err != nil {
				return result, fmt.Errorf("upload test failed: %w", err)
			}
			result.UploadMbps = server.ULSpeed.Mbps()
		}

		return result, nil
	}, nil
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
