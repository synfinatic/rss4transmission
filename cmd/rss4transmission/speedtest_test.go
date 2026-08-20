package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testSpeedCfg() SpeedTestConfig {
	cfg := SpeedTestConfig{
		Enabled: true, Interval: "1h", Cooldown: "2h",
		Proxy: "http://gluetun:8888", MinDownloadMbps: 100,
		MaxRotationsPerDay: 6, CaptureSeconds: 5, Threads: 2,
		DownloadOnly: true, SkipWhenActive: true, RetentionDays: 30,
	}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func TestShouldRotate(t *testing.T) {
	now := time.Now()
	cfg := testSpeedCfg()

	tests := []struct {
		name           string
		result         SpeedResult
		lastRotation   time.Time
		rotationsToday int
		wantRotate     bool
		wantReasonHas  string
	}{
		{
			name:       "fast link does not rotate",
			result:     SpeedResult{At: now, DownloadMbps: 400},
			wantRotate: false,
		},
		{
			name:          "slow link rotates",
			result:        SpeedResult{At: now, DownloadMbps: 12.5},
			wantRotate:    true,
			wantReasonHas: "12.5",
		},
		{
			name:       "exactly at the threshold does not rotate",
			result:     SpeedResult{At: now, DownloadMbps: 100},
			wantRotate: false,
		},
		{
			// A failed run measures nothing; treating its zero as "slow" would
			// rotate the VPN every time the proxy hiccups.
			name:       "errored result does not rotate",
			result:     SpeedResult{At: now, Error: "proxy refused"},
			wantRotate: false,
		},
		{
			name:       "skipped result does not rotate",
			result:     SpeedResult{At: now, Skipped: "torrents active"},
			wantRotate: false,
		},
		{
			name:         "slow but inside cooldown does not rotate",
			result:       SpeedResult{At: now, DownloadMbps: 12.5},
			lastRotation: now.Add(-30 * time.Minute),
			wantRotate:   false,
		},
		{
			name:         "slow and past cooldown rotates",
			result:       SpeedResult{At: now, DownloadMbps: 12.5},
			lastRotation: now.Add(-3 * time.Hour),
			wantRotate:   true,
		},
		{
			name:           "slow but at the daily cap does not rotate",
			result:         SpeedResult{At: now, DownloadMbps: 12.5},
			rotationsToday: 6,
			wantRotate:     false,
		},
		{
			name:           "slow and under the daily cap rotates",
			result:         SpeedResult{At: now, DownloadMbps: 12.5},
			rotationsToday: 5,
			wantRotate:     true,
		},
		{
			name:           "over the daily cap does not rotate",
			result:         SpeedResult{At: now, DownloadMbps: 12.5},
			rotationsToday: 99,
			wantRotate:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := shouldRotate(cfg, tc.result, tc.lastRotation, tc.rotationsToday, now)
			if got != tc.wantRotate {
				t.Errorf("shouldRotate = %v (%q), want %v", got, reason, tc.wantRotate)
			}
			if tc.wantRotate && reason == "" {
				t.Error("shouldRotate returned true with an empty reason")
			}
			if tc.wantReasonHas != "" && !contains(reason, tc.wantReasonHas) {
				t.Errorf("reason = %q, want it to mention %q", reason, tc.wantReasonHas)
			}
		})
	}
}

// A zero lastRotation means "never rotated", which must not be read as
// "rotated at the epoch, so we're inside no cooldown" nor block the first rotation.
func TestShouldRotate_NeverRotatedIsNotInCooldown(t *testing.T) {
	cfg := testSpeedCfg()
	got, _ := shouldRotate(cfg, SpeedResult{DownloadMbps: 5}, time.Time{}, 0, time.Now())
	if !got {
		t.Error("shouldRotate = false when never rotated before, want true")
	}
}

// MaxRotationsPerDay: 0 means unlimited, matching how Gluetun treats
// ClosedPortChecks: 0 and RotateTime: 0 as "disabled".
func TestShouldRotate_ZeroDailyCapMeansUnlimited(t *testing.T) {
	cfg := testSpeedCfg()
	cfg.MaxRotationsPerDay = 0
	got, _ := shouldRotate(cfg, SpeedResult{DownloadMbps: 5}, time.Time{}, 100, time.Now())
	if !got {
		t.Error("shouldRotate = false with MaxRotationsPerDay 0, want true (unlimited)")
	}
}

// ---- SpeedMonitor tick ----

type fakeDeps struct {
	result          SpeedResult
	testErr         error
	activeDownloads int
	activeErr       error
	rotateReasons   []string
	testCalls       int
}

func (f *fakeDeps) runTest(context.Context) (SpeedResult, error) {
	f.testCalls++
	return f.result, f.testErr
}
func (f *fakeDeps) active(context.Context) (int, error) { return f.activeDownloads, f.activeErr }
func (f *fakeDeps) rotate(reason string)                { f.rotateReasons = append(f.rotateReasons, reason) }

func newTestMonitor(t *testing.T, cfg SpeedTestConfig, f *fakeDeps) (*SpeedMonitor, *SpeedFile) {
	t.Helper()
	store := tempSpeedFile(t)
	m := NewSpeedMonitor(cfg, NtfyConfig{}, store, f.runTest, f.active, f.rotate)
	return m, store
}

func TestSpeedMonitor_SlowResultRequestsRotation(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5, ExitIP: "1.1.1.1"}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)

	m.tick(context.Background())

	if len(f.rotateReasons) != 1 {
		t.Fatalf("rotate called %d times, want 1", len(f.rotateReasons))
	}
	if len(store.GetResults()) != 1 {
		t.Errorf("stored %d results, want 1", len(store.GetResults()))
	}
	if len(store.GetRotations()) != 1 {
		t.Fatalf("stored %d rotations, want 1", len(store.GetRotations()))
	}
	rot := store.GetRotations()[0]
	if rot.BeforeMbps != 12.5 {
		t.Errorf("BeforeMbps = %v, want 12.5", rot.BeforeMbps)
	}
	if rot.FromExitIP != "1.1.1.1" {
		t.Errorf("FromExitIP = %q, want 1.1.1.1", rot.FromExitIP)
	}
}

func TestSpeedMonitor_FastResultDoesNotRotate(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)

	m.tick(context.Background())

	if len(f.rotateReasons) != 0 {
		t.Errorf("rotate called %v, want no calls", f.rotateReasons)
	}
	if len(store.GetResults()) != 1 {
		t.Errorf("stored %d results, want 1", len(store.GetResults()))
	}
}

// The active-torrent gate must skip the measurement itself, not just the
// rotation: a speedtest during a download steals bandwidth and reads low.
func TestSpeedMonitor_SkipsTestWhileTorrentsActive(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5}, activeDownloads: 2}
	m, store := newTestMonitor(t, testSpeedCfg(), f)

	m.tick(context.Background())

	if f.testCalls != 0 {
		t.Errorf("speed test ran %d times while torrents were active, want 0", f.testCalls)
	}
	if len(f.rotateReasons) != 0 {
		t.Errorf("rotate called %v while torrents were active, want no calls", f.rotateReasons)
	}
	results := store.GetResults()
	if len(results) != 1 {
		t.Fatalf("stored %d results, want 1 skip record", len(results))
	}
	if results[0].Skipped == "" {
		t.Error("stored result has empty Skipped, want a reason")
	}
}

func TestSpeedMonitor_SkipWhenActiveDisabledStillTests(t *testing.T) {
	cfg := testSpeedCfg()
	cfg.SkipWhenActive = false
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400}, activeDownloads: 3}
	m, _ := newTestMonitor(t, cfg, f)

	m.tick(context.Background())

	if f.testCalls != 1 {
		t.Errorf("speed test ran %d times, want 1", f.testCalls)
	}
}

// If we cannot tell whether torrents are active, run the test anyway but never
// rotate on the result -- rotating blind could interrupt an active download.
func TestSpeedMonitor_ActiveCheckErrorDoesNotRotate(t *testing.T) {
	f := &fakeDeps{
		result:    SpeedResult{DownloadMbps: 12.5},
		activeErr: errors.New("rpc down"),
	}
	m, _ := newTestMonitor(t, testSpeedCfg(), f)

	m.tick(context.Background())

	if len(f.rotateReasons) != 0 {
		t.Errorf("rotate called %v when active-download state was unknown, want no calls",
			f.rotateReasons)
	}
}

func TestSpeedMonitor_TestErrorIsRecordedNotRotated(t *testing.T) {
	f := &fakeDeps{testErr: errors.New("proxy refused")}
	m, store := newTestMonitor(t, testSpeedCfg(), f)

	m.tick(context.Background())

	if len(f.rotateReasons) != 0 {
		t.Errorf("rotate called %v on a failed test, want no calls", f.rotateReasons)
	}
	results := store.GetResults()
	if len(results) != 1 {
		t.Fatalf("stored %d results, want 1", len(results))
	}
	if results[0].Error == "" {
		t.Error("stored result has empty Error, want the failure recorded")
	}
}

// Cooldown is enforced against persisted history, so it survives a restart.
func TestSpeedMonitor_RespectsCooldownFromStore(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)
	store.AddRotation(RotationEvent{At: time.Now().Add(-10 * time.Minute), Reason: "earlier"})

	m.tick(context.Background())

	if len(f.rotateReasons) != 0 {
		t.Errorf("rotate called %v inside the cooldown window, want no calls", f.rotateReasons)
	}
}

func TestSpeedMonitor_RespectsDailyCapFromStore(t *testing.T) {
	cfg := testSpeedCfg()
	cfg.MaxRotationsPerDay = 2
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5}}
	m, store := newTestMonitor(t, cfg, f)
	// old enough to clear the 2h cooldown, recent enough to count toward the cap
	store.AddRotation(RotationEvent{At: time.Now().Add(-20 * time.Hour)})
	store.AddRotation(RotationEvent{At: time.Now().Add(-19 * time.Hour)})

	m.tick(context.Background())

	if len(f.rotateReasons) != 0 {
		t.Errorf("rotate called %v at the daily cap, want no calls", f.rotateReasons)
	}
}

// The exit IP seen on a successful run backfills the previous rotation, which
// is how a rotation that landed on the same server becomes visible.
func TestSpeedMonitor_BackfillsExitIPAfterRotation(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400, ExitIP: "2.2.2.2"}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)
	store.AddRotation(RotationEvent{At: time.Now().Add(-1 * time.Hour), FromExitIP: "1.1.1.1"})

	m.tick(context.Background())

	rot, _ := store.LastRotation()
	if rot.ToExitIP != "2.2.2.2" {
		t.Errorf("ToExitIP = %q, want 2.2.2.2", rot.ToExitIP)
	}
}

// Measure-only mode: no Gluetun configured means no rotate function at all.
func TestSpeedMonitor_NilRotateIsSafe(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5}}
	store := tempSpeedFile(t)
	m := NewSpeedMonitor(testSpeedCfg(), NtfyConfig{}, store, f.runTest, f.active, nil)

	m.tick(context.Background()) // must not panic

	if len(store.GetResults()) != 1 {
		t.Errorf("stored %d results, want 1", len(store.GetResults()))
	}
	if len(store.GetRotations()) != 0 {
		t.Errorf("stored %d rotations in measure-only mode, want 0", len(store.GetRotations()))
	}
}

// ---- runner ----

// The whole design depends on speedtest traffic going through Gluetun; if the
// transport isn't proxied, every measurement silently describes the wrong path.
func TestNewSpeedtestTransport_UsesProxy(t *testing.T) {
	tr, err := newSpeedtestTransport("http://gluetun:8888")
	if err != nil {
		t.Fatalf("newSpeedtestTransport: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://speedtest.example.com/download", nil)
	proxyURL, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy(): %v", err)
	}
	if proxyURL == nil {
		t.Fatal("Proxy() = nil, want the configured proxy URL")
	}
	if want := (&url.URL{Scheme: "http", Host: "gluetun:8888"}).String(); proxyURL.String() != want {
		t.Errorf("proxy = %q, want %q", proxyURL.String(), want)
	}
}

func TestNewSpeedtestTransport_RejectsBadURL(t *testing.T) {
	if _, err := newSpeedtestTransport("://nope"); err == nil {
		t.Error("newSpeedtestTransport = nil error on a malformed URL, want error")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The runner must refuse to start with a proxy speedtest-go would silently
// ignore, since that would measure the host's connection instead of the VPN's.
func TestNewSpeedtestRunner_RejectsBadProxy(t *testing.T) {
	cfg := testSpeedCfg()
	cfg.Proxy = "gluetun:8888" // no scheme; url.Parse accepts it as opaque
	if _, err := newSpeedtestRunner(cfg); err == nil {
		t.Error("newSpeedtestRunner = nil error for a schemeless proxy, want error")
	}
}

func TestNewSpeedtestRunner_AcceptsValidProxy(t *testing.T) {
	if _, err := newSpeedtestRunner(testSpeedCfg()); err != nil {
		t.Errorf("newSpeedtestRunner returned error: %v", err)
	}
}

// ---- ntfy on rotation ----

func TestSpeedMonitor_SendsNtfyOnRotation(t *testing.T) {
	type captured struct {
		topic, title, body string
	}
	got := make(chan captured, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- captured{
			topic: strings.TrimPrefix(r.URL.Path, "/"),
			title: r.Header.Get("Title"),
			body:  string(b),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ntfy := NtfyConfig{BaseURL: ts.URL, AlertTopic: "alerts"}
	if err := ntfy.Validate(); err != nil {
		t.Fatalf("ntfy Validate: %v", err)
	}

	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5, ExitIP: "1.1.1.1"}}
	m := NewSpeedMonitor(testSpeedCfg(), ntfy, tempSpeedFile(t), f.runTest, f.active, f.rotate)

	m.tick(context.Background())

	select {
	case c := <-got:
		if c.topic != "alerts" {
			t.Errorf("topic = %q, want alerts", c.topic)
		}
		if c.title == "" {
			t.Error("Title header is empty")
		}
		if !contains(c.body, "1.1.1.1") {
			t.Errorf("body = %q, want it to mention the exit IP", c.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no ntfy notification sent on rotation")
	}
}

// No alert topic configured means no notification, and definitely no failure.
func TestSpeedMonitor_NoNtfyWhenUnconfigured(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5}}
	m, _ := newTestMonitor(t, testSpeedCfg(), f)

	m.tick(context.Background()) // NtfyConfig{} -- must not panic or block

	if len(f.rotateReasons) != 1 {
		t.Errorf("rotate called %d times, want 1", len(f.rotateReasons))
	}
}

func TestSpeedMonitor_NoNtfyWhenNotRotating(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("ntfy called on a healthy result")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ntfy := NtfyConfig{BaseURL: ts.URL, AlertTopic: "alerts"}
	if err := ntfy.Validate(); err != nil {
		t.Fatalf("ntfy Validate: %v", err)
	}

	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400}}
	m := NewSpeedMonitor(testSpeedCfg(), ntfy, tempSpeedFile(t), f.runTest, f.active, f.rotate)
	m.tick(context.Background())
}
