package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
		minUploadMbps  float64 // 0 leaves the upload check disabled
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
		{
			// The case this knob exists for: some exits carry download fine
			// but upload nothing at all, which is fatal on a ratio tracker
			// and completely invisible to MinDownloadMbps.
			name:          "fast download but dead upload rotates",
			result:        SpeedResult{At: now, DownloadMbps: 400, UploadMbps: 0},
			minUploadMbps: 5,
			wantRotate:    true,
			wantReasonHas: "upload",
		},
		{
			// speedtest-go reports a tiny negative rate for an upload leg
			// that moved nothing; it must read as slow, not as fast.
			name:          "negative upload rotates",
			result:        SpeedResult{At: now, DownloadMbps: 400, UploadMbps: -0.04},
			minUploadMbps: 5,
			wantRotate:    true,
			wantReasonHas: "upload",
		},
		{
			name:          "upload at the threshold does not rotate",
			result:        SpeedResult{At: now, DownloadMbps: 400, UploadMbps: 5},
			minUploadMbps: 5,
			wantRotate:    false,
		},
		{
			// Default config: no upload floor, so a zero upload -- which is
			// also what DownloadOnly records -- must never rotate.
			name:       "zero upload does not rotate while the check is disabled",
			result:     SpeedResult{At: now, DownloadMbps: 400, UploadMbps: 0},
			wantRotate: false,
		},
		{
			// Download is checked first: it is the more common failure and
			// makes the better reason line.
			name:          "both below threshold reports the download",
			result:        SpeedResult{At: now, DownloadMbps: 12.5, UploadMbps: 0},
			minUploadMbps: 5,
			wantRotate:    true,
			wantReasonHas: "download",
		},
		{
			name:          "errored result does not rotate on its zero upload",
			result:        SpeedResult{At: now, Error: "proxy refused"},
			minUploadMbps: 5,
			wantRotate:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfg
			cfg.MinUploadMbps = tc.minUploadMbps
			d := shouldRotate(cfg, tc.result, tc.lastRotation, tc.rotationsToday, now)
			if d.Rotate != tc.wantRotate {
				t.Errorf("shouldRotate = %v (%q), want %v", d.Rotate, d.Reason, tc.wantRotate)
			}
			if tc.wantRotate && d.Reason == "" {
				t.Error("shouldRotate returned true with an empty reason")
			}
			if tc.wantReasonHas != "" && !contains(d.Reason, tc.wantReasonHas) {
				t.Errorf("reason = %q, want it to mention %q", d.Reason, tc.wantReasonHas)
			}
		})
	}
}

// A zero lastRotation means "never rotated", which must not be read as
// "rotated at the epoch, so we're inside no cooldown" nor block the first rotation.
func TestShouldRotate_NeverRotatedIsNotInCooldown(t *testing.T) {
	cfg := testSpeedCfg()
	d := shouldRotate(cfg, SpeedResult{DownloadMbps: 5}, time.Time{}, 0, time.Now())
	if !d.Rotate {
		t.Error("shouldRotate = false when never rotated before, want true")
	}
}

// MaxRotationsPerDay: 0 means unlimited, matching how Gluetun treats
// ClosedPortChecks: 0 and RotateTime: 0 as "disabled".
func TestShouldRotate_ZeroDailyCapMeansUnlimited(t *testing.T) {
	cfg := testSpeedCfg()
	cfg.MaxRotationsPerDay = 0
	d := shouldRotate(cfg, SpeedResult{DownloadMbps: 5}, time.Time{}, 100, time.Now())
	if !d.Rotate {
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
	rotateSources   []string
	rotateRefuse    bool // simulate a rotation already pending or under way
	testCalls       int
}

func (f *fakeDeps) runTest(context.Context) (SpeedResult, error) {
	f.testCalls++
	return f.result, f.testErr
}
func (f *fakeDeps) active(context.Context) (int, error) { return f.activeDownloads, f.activeErr }
func (f *fakeDeps) rotate(source, reason string) bool {
	f.rotateSources = append(f.rotateSources, source)
	f.rotateReasons = append(f.rotateReasons, reason)
	return !f.rotateRefuse
}

func newTestMonitor(t *testing.T, cfg SpeedTestConfig, f *fakeDeps) (*SpeedMonitor, *SpeedFile) {
	t.Helper()
	store := tempSpeedFile(t)
	m := NewSpeedMonitor(cfg, NtfyConfig{}, store, f.runTest, f.active, f.rotate)
	return m, store
}

func TestSpeedMonitor_SlowResultRequestsRotation(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5, ExitIP: "45.12.3.9"}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)
	m.ExitIP = func() (string, bool) { return "1.1.1.1", true }

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
	// Gluetun's view of the exit, not the measurement's: speedtest.net sees a
	// different address on a provider that NATs per destination.
	if rot.FromExitIP != "1.1.1.1" {
		t.Errorf("FromExitIP = %q, want Gluetun's 1.1.1.1", rot.FromExitIP)
	}
}

// Without Gluetun to ask, the rotation is staged with no From at all rather
// than with speedtest.net's address standing in for it.
func TestSpeedMonitor_StagesNoFromExitIPWhenGluetunIsSilent(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5, ExitIP: "45.12.3.9"}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)
	m.ExitIP = func() (string, bool) { return "", false }

	m.tick(context.Background())

	rot, ok := store.LastRotation()
	if !ok {
		t.Fatal("no rotation staged")
	}
	if rot.FromExitIP != "" {
		t.Errorf("FromExitIP = %q, want it left unknown", rot.FromExitIP)
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

// A measurement must not backfill a rotation's destination. Its ExitIP is
// speedtest.net's view, which drifts on a provider that NATs per destination --
// writing it here made a rotation that never completed render as a finished one,
// and a rotation that did complete render against an address it never used.
// Only vpnRotatedHook, fed by Gluetun, fills ToExitIP.
func TestSpeedMonitor_DoesNotBackfillExitIPAfterRotation(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400, ExitIP: "45.12.3.9"}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)
	m.ExitIP = func() (string, bool) { return "9.9.9.9", true }
	store.AddRotation(RotationEvent{At: time.Now().Add(-1 * time.Hour), FromExitIP: "1.1.1.1"})

	m.tick(context.Background())

	rot, _ := store.LastRotation()
	if rot.ToExitIP != "" {
		t.Errorf("ToExitIP = %q, want it left for Gluetun to fill", rot.ToExitIP)
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

// speedtest-go builds its own transport and only ever reads the Proxy string,
// so all we can do -- and all we need to do -- is refuse a value it would
// silently mishandle.
func TestValidateProxyURL_AcceptsFullURL(t *testing.T) {
	u, err := validateProxyURL("http://gluetun:8888")
	if err != nil {
		t.Fatalf("validateProxyURL: %v", err)
	}
	if want := (&url.URL{Scheme: "http", Host: "gluetun:8888"}).String(); u.String() != want {
		t.Errorf("proxy = %q, want %q", u.String(), want)
	}
}

func TestValidateProxyURL_RejectsBadURL(t *testing.T) {
	if _, err := validateProxyURL("://nope"); err == nil {
		t.Error("validateProxyURL = nil error on a malformed URL, want error")
	}
}

// url.Parse accepts "gluetun:8888" as an opaque URL with scheme "gluetun" and
// no host. speedtest-go would take that and produce a proxy the transport
// can't dial, so the scheme+host check has to be explicit.
func TestValidateProxyURL_RejectsSchemelessHostPort(t *testing.T) {
	if _, err := validateProxyURL("gluetun:8888"); err == nil {
		t.Error("validateProxyURL = nil error for a schemeless host:port, want error")
	}
}

func TestValidateProxyURL_RejectsEmpty(t *testing.T) {
	if _, err := validateProxyURL(""); err == nil {
		t.Error("validateProxyURL = nil error on an empty proxy, want error")
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

	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5, ExitIP: "45.12.3.9"}}
	m := NewSpeedMonitor(testSpeedCfg(), ntfy, tempSpeedFile(t), f.runTest, f.active, f.rotate)
	m.ExitIP = func() (string, bool) { return "1.1.1.1", true }

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
			t.Errorf("body = %q, want it to mention Gluetun's exit IP", c.body)
		}
		if contains(c.body, "45.12.3.9") {
			t.Errorf("body = %q, want it to not use speedtest.net's view of the exit", c.body)
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

// ---- on-demand measurement ----

func TestSpeedMonitor_Trigger_Coalesces(t *testing.T) {
	m, _ := newTestMonitor(t, testSpeedCfg(), &fakeDeps{})

	if !m.Trigger() {
		t.Error("first Trigger = false, want true")
	}
	if m.Trigger() {
		t.Error("second Trigger = true, want false (should coalesce)")
	}
}

func TestSpeedMonitor_TriggerRunsMeasurement(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	if !m.Trigger() {
		t.Fatal("Trigger = false, want true")
	}

	// The ticker is an hour out, so a result here can only have come from the
	// trigger.
	deadline := time.Now().Add(5 * time.Second)
	for len(store.GetResults()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("triggered measurement never recorded a result")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An explicit click means "measure now", so the manual path ignores
// SkipWhenActive -- unlike the scheduled one, which skips.
func TestSpeedMonitor_MeasureNowIgnoresSkipWhenActive(t *testing.T) {
	cfg := testSpeedCfg()
	cfg.SkipWhenActive = true
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400}, activeDownloads: 3}
	m, store := newTestMonitor(t, cfg, f)

	m.measureNow(context.Background())

	if f.testCalls != 1 {
		t.Errorf("speed test ran %d times, want 1", f.testCalls)
	}
	results := store.GetResults()
	if len(results) != 1 {
		t.Fatalf("stored %d results, want 1", len(results))
	}
	if results[0].Skipped != "" {
		t.Errorf("stored result Skipped = %q, want a real measurement", results[0].Skipped)
	}
}

// The page has its own Rotate button, so a manual measurement must never cause
// a VPN restart the user did not ask for -- even a very slow one.
func TestSpeedMonitor_MeasureNowNeverRotates(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 1.5}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)

	m.measureNow(context.Background())

	if len(f.rotateReasons) != 0 {
		t.Errorf("rotate called %v, want no calls", f.rotateReasons)
	}
	if len(store.GetRotations()) != 0 {
		t.Errorf("stored %d rotations, want 0", len(store.GetRotations()))
	}
	if len(store.GetResults()) != 1 {
		t.Errorf("stored %d results, want 1", len(store.GetResults()))
	}
}

// Draining the trigger channel is not the end of the request: the measurement
// it queued still has ~30s to run, and a click during that window would buy a
// second full test (~250 MB) right behind the first.
func TestSpeedMonitor_TriggerRefusedWhileMeasuring(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	store := tempSpeedFile(t)
	m := NewSpeedMonitor(testSpeedCfg(), NtfyConfig{}, store,
		func(context.Context) (SpeedResult, error) {
			close(started)
			<-release
			return SpeedResult{DownloadMbps: 400}, nil
		}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	if !m.Trigger() {
		t.Fatal("first Trigger = false, want true")
	}
	<-started

	if m.Trigger() {
		t.Error("Trigger = true while a measurement was running, want false")
	}

	// Unblock, then wait for Run to return: the measurement saves to a
	// t.TempDir file, and t.Cleanup removes that directory as soon as the test
	// returns.
	close(release)
	cancel()
	<-done
}

// The scheduled measurement is just as expensive, so a click during one is
// refused too rather than queueing a second test behind it.
func TestSpeedMonitor_TriggerRefusedDuringScheduledMeasurement(t *testing.T) {
	var once sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	m := NewSpeedMonitor(testSpeedCfg(), NtfyConfig{}, tempSpeedFile(t),
		func(context.Context) (SpeedResult, error) {
			once.Do(func() { close(started) })
			<-release
			return SpeedResult{DownloadMbps: 400}, nil
		}, nil, nil)
	m.interval = 10 * time.Millisecond // Interval is parsed by Validate(), not settable here

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	<-started
	if m.Trigger() {
		t.Error("Trigger = true during a scheduled measurement, want false")
	}

	// Stop the loop before unblocking, so the fast ticker cannot spin up
	// further measurements once the fake stops blocking, then wait for Run to
	// return before t.Cleanup removes the results file out from under a save.
	cancel()
	close(release)
	<-done
}

// Every measurement is stamped with Gluetun's own view of the exit at the time
// it was taken, so a row can be read against the tunnel it went over rather
// than against whatever address speedtest.net saw through the proxy.
func TestSpeedMonitor_StampsGluetunExitIPOnResults(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400, ExitIP: "45.12.3.9"}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)
	m.ExitIP = func() (string, bool) { return "185.9.9.9", true }

	m.tick(context.Background())
	m.measureNow(context.Background())

	results := store.GetResults()
	if len(results) != 2 {
		t.Fatalf("stored %d results, want 2", len(results))
	}
	for i, r := range results {
		if r.GluetunExitIP != "185.9.9.9" {
			t.Errorf("results[%d].GluetunExitIP = %q, want Gluetun's 185.9.9.9", i, r.GluetunExitIP)
		}
		if r.ExitIP != "45.12.3.9" {
			t.Errorf("results[%d].ExitIP = %q, want speedtest.net's own view kept", i, r.ExitIP)
		}
	}
}

// No Gluetun to ask, or one that has not answered yet, leaves the field empty
// rather than borrowing speedtest.net's address for it.
func TestSpeedMonitor_LeavesGluetunExitIPEmptyWhenUnknown(t *testing.T) {
	tests := []struct {
		name   string
		exitIP exitIPFunc
	}{
		{name: "measure-only", exitIP: nil},
		{name: "gluetun silent", exitIP: func() (string, bool) { return "", false }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeDeps{result: SpeedResult{DownloadMbps: 400, ExitIP: "45.12.3.9"}}
			m, store := newTestMonitor(t, testSpeedCfg(), f)
			m.ExitIP = tt.exitIP

			m.tick(context.Background())

			results := store.GetResults()
			if len(results) != 1 {
				t.Fatalf("stored %d results, want 1", len(results))
			}
			if results[0].GluetunExitIP != "" {
				t.Errorf("GluetunExitIP = %q, want empty", results[0].GluetunExitIP)
			}
		})
	}
}

// ---- rotation notes ----

// A measurement that missed a throughput floor carries the policy's verdict, so
// a slow row on the page says whether it cost a rotation and, if not, what
// stopped it. Rows that met the floors have no story and stay blank.
func TestSpeedMonitor_RotationNotes(t *testing.T) {
	twoHoursAgo := time.Now().Add(-2 * time.Hour)

	tests := []struct {
		name           string
		download       float64
		skipWhenActive bool
		active         int
		activeErr      error
		rotateRefuse   bool
		rotations      []RotationEvent
		wantNoteHas    string // "" means the row must carry no note at all
		wantRotations  int    // rotations staged by this tick
	}{
		{
			name:          "rotation requested",
			download:      12.5,
			wantNoteHas:   "rotation requested",
			wantRotations: 1,
		},
		{
			name:        "fast enough says nothing",
			download:    400,
			wantNoteHas: "",
		},
		{
			name:     "daily cap reached",
			download: 12.5,
			// Older than the 2h cooldown, so the cap is what blocks it.
			rotations: []RotationEvent{
				{At: twoHoursAgo.Add(-time.Hour), Source: RotationSourceSpeedtest},
				{At: twoHoursAgo.Add(-time.Hour), Source: RotationSourceSpeedtest},
				{At: twoHoursAgo.Add(-time.Hour), Source: RotationSourceSpeedtest},
				{At: twoHoursAgo.Add(-time.Hour), Source: RotationSourceSpeedtest},
				{At: twoHoursAgo.Add(-time.Hour), Source: RotationSourceSpeedtest},
				{At: twoHoursAgo.Add(-time.Hour), Source: RotationSourceSpeedtest},
			},
			wantNoteHas: "max 6",
		},
		{
			name:        "cooldown",
			download:    12.5,
			rotations:   []RotationEvent{{At: time.Now().Add(-10 * time.Minute)}},
			wantNoteHas: "cooldown",
		},
		{
			name:        "active downloads block the rotation",
			download:    12.5,
			active:      2,
			wantNoteHas: "2 torrent(s) downloading",
		},
		{
			name:        "unknown torrent count blocks the rotation",
			download:    12.5,
			activeErr:   errors.New("rpc down"),
			wantNoteHas: "could not check",
		},
		{
			name:         "another rotation already under way",
			download:     12.5,
			rotateRefuse: true,
			wantNoteHas:  "already in progress",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testSpeedCfg()
			cfg.SkipWhenActive = tt.skipWhenActive
			f := &fakeDeps{
				result:          SpeedResult{DownloadMbps: tt.download},
				activeDownloads: tt.active,
				activeErr:       tt.activeErr,
				rotateRefuse:    tt.rotateRefuse,
			}
			m, store := newTestMonitor(t, cfg, f)
			for _, e := range tt.rotations {
				store.AddRotation(e)
			}
			before := len(store.GetRotations())

			m.tick(context.Background())

			results := store.GetResults()
			if len(results) != 1 {
				t.Fatalf("stored %d results, want 1", len(results))
			}
			note := results[0].RotationNote
			switch {
			case tt.wantNoteHas == "" && note != "":
				t.Errorf("RotationNote = %q, want none", note)
			case tt.wantNoteHas != "" && !strings.Contains(note, tt.wantNoteHas):
				t.Errorf("RotationNote = %q, want it to mention %q", note, tt.wantNoteHas)
			}
			if got := len(store.GetRotations()) - before; got != tt.wantRotations {
				t.Errorf("staged %d rotations, want %d", got, tt.wantRotations)
			}
		})
	}
}

// Measure-only deployments have no rotation policy, so a slow row there must not
// claim one was considered.
func TestSpeedMonitor_NoRotationNoteWithoutRotateFunc(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5}}
	store := tempSpeedFile(t)
	m := NewSpeedMonitor(testSpeedCfg(), NtfyConfig{}, store, f.runTest, f.active, nil)

	m.tick(context.Background())

	if note := store.GetResults()[0].RotationNote; note != "" {
		t.Errorf("RotationNote = %q in measure-only mode, want none", note)
	}
}

// The page's "Run speedtest now" button never rotates by design, so a slow row
// from one says that rather than leaving the reader to guess.
func TestSpeedMonitor_MeasureNowSaysItNeverRotates(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 12.5}}
	m, store := newTestMonitor(t, testSpeedCfg(), f)

	m.measureNow(context.Background())

	note := store.GetResults()[0].RotationNote
	if !strings.Contains(note, "on-demand") {
		t.Errorf("RotationNote = %q, want it to say the run was on-demand", note)
	}
	if len(store.GetRotations()) != 0 {
		t.Error("measureNow staged a rotation")
	}
}

// ---- upload sentinel ----

// speedtest-go reports a dead upload leg by setting ULSpeed to -1 and returning
// no error (speedtest/request.go). Divided by 125000 that becomes -0.000008
// Mbps, which reads as a real measurement far below MinUploadMbps and drives a
// rotation that cannot help: the broken leg belongs to the speedtest server,
// not to the exit.
func TestUploadMbps_RejectsNotAvailableSentinel(t *testing.T) {
	got, err := uploadMbps(-1)
	if err == nil {
		t.Fatalf("uploadMbps(-1) = %v, nil error, want an error for the N/A sentinel", got)
	}
	if !strings.Contains(err.Error(), "upload") {
		t.Errorf("error = %q, want it to name the upload leg", err)
	}
}

func TestUploadMbps_ConvertsRealRate(t *testing.T) {
	got, err := uploadMbps(125000 * 100)
	if err != nil {
		t.Fatalf("uploadMbps: %v", err)
	}
	if got != 100 {
		t.Errorf("uploadMbps = %v Mbps, want 100", got)
	}
}

// A zero rate with a low request error rate is a real measurement of a dead
// link, not the sentinel. MinUploadMbps must still see it and rotate.
func TestUploadMbps_KeepsGenuineZero(t *testing.T) {
	got, err := uploadMbps(0)
	if err != nil {
		t.Fatalf("uploadMbps: %v", err)
	}
	if got != 0 {
		t.Errorf("uploadMbps = %v Mbps, want 0", got)
	}
}

// speedtest-go reports a dead download leg by setting DLSpeed to -1 and
// returning no error (speedtest/request.go). Divided by 125000 that becomes
// -0.000008 Mbps, which reads as a real measurement far below MinDownloadMbps
// and drives a rotation that cannot help.
func TestDownloadMbps_RejectsNotAvailableSentinel(t *testing.T) {
	got, err := downloadMbps(-1)
	if err == nil {
		t.Fatalf("downloadMbps(-1) = %v, nil error, want an error for the N/A sentinel", got)
	}
	if !strings.Contains(err.Error(), "download") {
		t.Errorf("error = %q, want it to name the download leg", err)
	}
}

func TestDownloadMbps_ConvertsRealRate(t *testing.T) {
	got, err := downloadMbps(125000 * 100)
	if err != nil {
		t.Fatalf("downloadMbps: %v", err)
	}
	if got != 100 {
		t.Errorf("downloadMbps = %v Mbps, want 100", got)
	}
}

// A zero rate with a low request error rate is a real measurement of a dead
// link, not the sentinel. MinDownloadMbps must still see it and rotate.
func TestDownloadMbps_KeepsGenuineZero(t *testing.T) {
	got, err := downloadMbps(0)
	if err != nil {
		t.Fatalf("downloadMbps: %v", err)
	}
	if got != 0 {
		t.Errorf("downloadMbps = %v Mbps, want 0", got)
	}
}

// ---- cache buster ----

// captureRT records the requests it is handed and returns a canned response.
type captureRT struct {
	got []*http.Request
}

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.got = append(c.got, req)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
	}, nil
}

func mustRoundTrip(t *testing.T, rt http.RoundTripper, rawURL string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
}

// Cloudflare fronts speedtest-config.php with a cache that ignores who is
// asking, so a HIT hands back the address and coordinates of whoever populated
// the edge. A unique query parameter forces the origin, because Cloudflare
// includes the query string in its cache key.
func TestCacheBuster_AddsUniqueParam(t *testing.T) {
	next := &captureRT{}
	cb := &cacheBuster{next: next}

	mustRoundTrip(t, cb, "https://www.speedtest.net/speedtest-config.php")
	mustRoundTrip(t, cb, "https://www.speedtest.net/speedtest-config.php")

	if len(next.got) != 2 {
		t.Fatalf("next saw %d requests, want 2", len(next.got))
	}
	first := next.got[0].URL.Query().Get(cacheBusterParam)
	second := next.got[1].URL.Query().Get(cacheBusterParam)
	if first == "" || second == "" {
		t.Fatalf("cache buster param missing: %q, %q", first, second)
	}
	if first == second {
		t.Errorf("both requests used %q, want a unique value per request", first)
	}
	if next.got[0].URL.Path != "/speedtest-config.php" {
		t.Errorf("path = %q, want it left alone", next.got[0].URL.Path)
	}
}

// The server list endpoint takes a search keyword and coordinates. Busting the
// cache must not drop them.
func TestCacheBuster_PreservesExistingQuery(t *testing.T) {
	next := &captureRT{}
	cb := &cacheBuster{next: next}

	mustRoundTrip(t, cb, "https://www.speedtest.net/api/js/servers?search=austin&lat=37.751")

	q := next.got[0].URL.Query()
	if q.Get("search") != "austin" {
		t.Errorf("search = %q, want %q", q.Get("search"), "austin")
	}
	if q.Get("lat") != "37.751" {
		t.Errorf("lat = %q, want %q", q.Get("lat"), "37.751")
	}
	if q.Get(cacheBusterParam) == "" {
		t.Error("cache buster param missing")
	}
}

// The throughput legs run against per-server hosts, whose URLs carry parameters
// the server itself parses. Only the Cloudflare-fronted host may be rewritten.
func TestCacheBuster_IgnoresOtherHosts(t *testing.T) {
	next := &captureRT{}
	cb := &cacheBuster{next: next}

	const raw = "https://speedtest.example.net/upload.php?nocache=1"
	mustRoundTrip(t, cb, raw)

	if got := next.got[0].URL.String(); got != raw {
		t.Errorf("URL = %q, want it passed through as %q", got, raw)
	}
}

// RoundTrip must not modify the request it is given.
func TestCacheBuster_DoesNotMutateRequest(t *testing.T) {
	next := &captureRT{}
	cb := &cacheBuster{next: next}

	req, err := http.NewRequest(http.MethodGet, "https://www.speedtest.net/speedtest-config.php", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := cb.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if req.URL.RawQuery != "" {
		t.Errorf("caller's request was mutated: RawQuery = %q, want empty", req.URL.RawQuery)
	}
}

// speedtest-go's New() assigns its doer to http.DefaultClient and NewUserConfig
// points that client's Transport at the Speedtest -- before any option runs, so
// WithDoer does not undo it. Left alone, every http.DefaultClient user in this
// process (Gluetun.control, fetchTorrentBytes) moves onto the VPN proxy after
// the first measurement.
func TestNewSpeedtestClient_LeavesDefaultClientAlone(t *testing.T) {
	before := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = before })

	cfg := testSpeedCfg()
	cfg.Proxy = "http://gluetun:8888"
	if st := newSpeedtestClient(cfg, 2); st == nil {
		t.Fatal("newSpeedtestClient = nil")
	}

	if http.DefaultClient.Transport != before {
		t.Error("http.DefaultClient.Transport was replaced, want it left untouched")
	}
}

// A config reload rebuilds the monitor by cancelling the old one's context, so
// a monitor that ignored the cancel would leave a goroutine measuring against
// a config that is no longer in effect.
func TestSpeedMonitor_Run_ReturnsWhenContextIsCancelled(t *testing.T) {
	f := &fakeDeps{result: SpeedResult{DownloadMbps: 400}}
	m, _ := newTestMonitor(t, testSpeedCfg(), f)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// The measurement itself must stop too: a speedtest runs for tens of seconds
// and moves hundreds of megabytes, so a rebuild has to be able to abandon one
// in flight rather than wait it out.
func TestSpeedMonitor_Measure_PassesTheContextToTheRunner(t *testing.T) {
	var got context.Context
	m, _ := newTestMonitor(t, testSpeedCfg(), &fakeDeps{})
	m.runTest = func(ctx context.Context) (SpeedResult, error) {
		got = ctx
		return SpeedResult{DownloadMbps: 400}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.measure(ctx, false)

	if got == nil {
		t.Fatal("the runner was called without a context")
	}
	if got.Err() == nil {
		t.Error("the runner got a context that does not carry the cancellation")
	}
}
