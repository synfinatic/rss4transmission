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
