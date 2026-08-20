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
	rotateRequestFunc   func(reason string)
)

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

	if r.DownloadMbps >= cfg.MinDownloadMbps {
		return false, ""
	}

	reason := fmt.Sprintf("download %.1f Mbps below %.1f Mbps threshold",
		r.DownloadMbps, cfg.MinDownloadMbps)

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
	}
}

// Run blocks forever, measuring every Interval. Call it in its own goroutine.
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
			m.tick(ctx)
		}
	}
}

// tick performs one measure-and-decide cycle.
func (m *SpeedMonitor) tick(ctx context.Context) {
	result, canRotate := m.measure(ctx)

	m.store.AddResult(result)

	if result.OK() {
		// Backfills ToExitIP on the previous rotation, so a rotation that
		// landed back on the same server is visible rather than silent.
		m.store.RecordExitIP(result.ExitIP)
	}

	if canRotate && m.rotate != nil {
		m.decide(result)
	}

	if err := m.store.Save(m.cfg.RetentionDuration()); err != nil {
		log.WithError(err).Warn("Unable to save speedtest results")
	}
}

// measure runs the test unless the active-torrent gate blocks it. The second
// return value reports whether the result may be acted on: when we could not
// determine whether torrents are active we still measure, but refuse to
// rotate, since rotating blind could interrupt an active download.
func (m *SpeedMonitor) measure(ctx context.Context) (SpeedResult, bool) {
	canRotate := true

	if m.active != nil {
		n, err := m.active(ctx)
		switch {
		case err != nil:
			log.WithError(err).Warn("Unable to check for active torrents; will measure but not rotate")
			canRotate = false
		case n > 0 && m.cfg.SkipWhenActive:
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
	rotationsToday := m.store.RotationsSince(time.Now().Add(-24 * time.Hour))

	rotate, reason := shouldRotate(m.cfg, result, lastRotation, rotationsToday, time.Now())
	if !rotate {
		return
	}

	log.Warnf("Speedtest: requesting VPN rotation: %s", reason)
	m.store.AddRotation(RotationEvent{
		At:         time.Now(),
		Reason:     reason,
		BeforeMbps: result.DownloadMbps,
		FromExitIP: result.ExitIP,
	})
	m.rotate(reason)
	notifyVpnRotating(m.ntfy, &NtfyVpnContext{
		Reason:       reason,
		DownloadMbps: result.DownloadMbps,
		ExitIP:       result.ExitIP,
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
