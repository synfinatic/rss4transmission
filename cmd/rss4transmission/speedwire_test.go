package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

func statusPtr(s transmissionrpc.TorrentStatus) *transmissionrpc.TorrentStatus { return &s }

func TestCountDownloading(t *testing.T) {
	tests := []struct {
		name     string
		torrents []transmissionrpc.Torrent
		want     int
	}{
		{"none", nil, 0},
		{
			"only downloading counts",
			[]transmissionrpc.Torrent{
				{Status: statusPtr(transmissionrpc.TorrentStatusDownload)},
				{Status: statusPtr(transmissionrpc.TorrentStatusSeed)},
				{Status: statusPtr(transmissionrpc.TorrentStatusStopped)},
				{Status: statusPtr(transmissionrpc.TorrentStatusDownload)},
			},
			2,
		},
		{
			// Queued-to-download torrents move no bytes, so they must not
			// suppress a measurement the way an active download does.
			"queued does not count",
			[]transmissionrpc.Torrent{
				{Status: statusPtr(transmissionrpc.TorrentStatusDownloadWait)},
			},
			0,
		},
		{
			"nil status is ignored",
			[]transmissionrpc.Torrent{{Status: nil}},
			0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countDownloading(tc.torrents); got != tc.want {
				t.Errorf("countDownloading = %d, want %d", got, tc.want)
			}
		})
	}
}

func enabledSpeedCtx(t *testing.T) *RunContext {
	t.Helper()
	cfg := SpeedTestConfig{
		Enabled: true, Interval: "1h", Cooldown: "2h", Proxy: "http://gluetun:8888",
		MinDownloadMbps: 100, CaptureSeconds: 5, Threads: 2, RetentionDays: 30,
		ResultsFile: t.TempDir() + "/speed.json",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("fixture config is invalid: %s", err)
	}
	return &RunContext{Config: Config{SpeedTest: cfg}}
}

func TestNewSpeedMonitorFor_DisabledReturnsNil(t *testing.T) {
	ctx := &RunContext{Config: Config{SpeedTest: SpeedTestConfig{Enabled: false}}}

	m, err := newSpeedMonitorFor(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if m != nil {
		t.Errorf("built a monitor while SpeedTest is disabled")
	}
	if ctx.Speed != nil {
		t.Errorf("opened a results file while SpeedTest is disabled")
	}
}

func TestNewSpeedMonitorFor_OpensStore(t *testing.T) {
	ctx := enabledSpeedCtx(t)

	m, err := newSpeedMonitorFor(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if m == nil {
		t.Fatal("no monitor built while SpeedTest is enabled")
	}
	if ctx.Speed == nil {
		t.Error("ctx.Speed not set, so the web UI would see no results")
	}
}

// Without Gluetun there is nothing to rotate, but measurements are still
// worth recording -- the monitor runs in measure-only mode.
func TestNewSpeedMonitorFor_NoGluetunMeansNoRotate(t *testing.T) {
	ctx := enabledSpeedCtx(t)

	m, err := newSpeedMonitorFor(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if m.rotate != nil {
		t.Error("rotate wired up without a Gluetun client")
	}
}

func TestNewSpeedMonitorFor_GluetunWiresRotate(t *testing.T) {
	ctx := enabledSpeedCtx(t)
	g := &Gluetun{}

	m, err := newSpeedMonitorFor(ctx, g)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if m.rotate == nil {
		t.Fatal("rotate not wired up with a Gluetun client")
	}

	m.rotate("too slow")
	if got := g.PendingRotate(); got != "too slow" {
		t.Errorf("rotate did not reach Gluetun.RequestRotate; pending = %q", got)
	}
}

func TestNewSpeedMonitorFor_RequiresResultsFile(t *testing.T) {
	ctx := enabledSpeedCtx(t)
	ctx.Config.SpeedTest.ResultsFile = ""

	if _, err := newSpeedMonitorFor(ctx, nil); err == nil {
		t.Fatal("expected an error when ResultsFile is unset")
	} else if !strings.Contains(err.Error(), "ResultsFile") {
		t.Errorf("error does not name the missing setting: %s", err)
	}
}

func TestNewSpeedMonitorFor_BadProxyIsAnError(t *testing.T) {
	ctx := enabledSpeedCtx(t)
	ctx.Config.SpeedTest.Proxy = "://nope"

	if _, err := newSpeedMonitorFor(ctx, nil); err == nil {
		t.Error("expected an error for an unusable proxy")
	}
}

// --- post-rotation hook: vpnRotatedHook ---

func TestVpnRotatedHook_NotifiesWithNewExitIP(t *testing.T) {
	var body []byte
	var title string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		title = r.Header.Get("Title")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ntfy := NtfyConfig{BaseURL: srv.URL, AlertTopic: "alerts"}
	if err := ntfy.Validate(); err != nil {
		t.Fatalf("ntfy Validate: %v", err)
	}

	vpnRotatedHook(ntfy, nil, time.Hour)("1.1.1.1", "2.2.2.2")

	if title != "VPN Rotated" {
		t.Errorf("Title = %q, want %q", title, "VPN Rotated")
	}
	if !strings.Contains(string(body), "2.2.2.2") {
		t.Errorf("body = %q, want it to name the new exit IP", body)
	}
}

// The new exit IP is also what backfills ToExitIP on the rotation the speed
// monitor recorded, so the /speedtest page stops showing a blank destination
// until the next hourly measurement.
func TestVpnRotatedHook_RecordsExitIPInStore(t *testing.T) {
	store := tempSpeedFile(t)
	store.AddRotation(RotationEvent{At: time.Now(), Reason: "slow", FromExitIP: "1.1.1.1"})

	vpnRotatedHook(NtfyConfig{}, store, 30*24*time.Hour)("1.1.1.1", "2.2.2.2")

	last, ok := store.LastRotation()
	if !ok {
		t.Fatal("LastRotation() returned no rotation")
	}
	if last.ToExitIP != "2.2.2.2" {
		t.Errorf("ToExitIP = %q, want %q", last.ToExitIP, "2.2.2.2")
	}
}

// A rotation that landed on the same exit must still be recorded -- that is
// precisely the case worth seeing.
func TestVpnRotatedHook_RecordsSameExitIP(t *testing.T) {
	store := tempSpeedFile(t)
	store.AddRotation(RotationEvent{At: time.Now(), Reason: "slow", FromExitIP: "1.1.1.1"})

	vpnRotatedHook(NtfyConfig{}, store, 30*24*time.Hour)("1.1.1.1", "1.1.1.1")

	last, _ := store.LastRotation()
	if last.ToExitIP != "1.1.1.1" {
		t.Errorf("ToExitIP = %q, want %q", last.ToExitIP, "1.1.1.1")
	}
}

// Rotations also fire from RotateTime and ClosedPortChecks, where there is no
// speed store at all; the hook must still notify rather than panic.
func TestVpnRotatedHook_NilStore(t *testing.T) {
	vpnRotatedHook(NtfyConfig{}, nil, time.Hour)("1.1.1.1", "")
}

// ---- VPN page actions ----

func TestNewSpeedActions_NoMonitorNoGluetun(t *testing.T) {
	actions := newSpeedActions(enabledSpeedCtx(t), nil, nil, nil)

	if actions.Run != nil {
		t.Error("Run wired up without a speed monitor")
	}
	if actions.Rotate != nil {
		t.Error("Rotate wired up without Gluetun")
	}
}

func TestNewSpeedActions_MonitorWiresRun(t *testing.T) {
	ctx := enabledSpeedCtx(t)
	m, err := newSpeedMonitorFor(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	actions := newSpeedActions(ctx, m, nil, nil)
	if actions.Run == nil {
		t.Fatal("Run not wired up with a speed monitor")
	}
	if !actions.Run() {
		t.Error("first Run() = false, want true")
	}
	if actions.Run() {
		t.Error("second Run() = true, want false (should coalesce)")
	}
	if actions.Active == nil {
		t.Error("Active not wired up, so Rotate could not warn about downloads")
	}
}

// The button must not touch Gluetun from the HTTP goroutine: it records the
// request and wakes the port monitor, which owns Gluetun's state.
func TestNewSpeedActions_RotateGoesThroughPortMonitor(t *testing.T) {
	ctx := enabledSpeedCtx(t)
	g := &Gluetun{}
	pm := NewPortMonitor(nil, g, NtfyConfig{})

	actions := newSpeedActions(ctx, nil, g, pm)
	if actions.Rotate == nil {
		t.Fatal("Rotate not wired up with a Gluetun client")
	}

	actions.Rotate()

	if got := g.PendingRotate(); got == "" {
		t.Error("Rotate did not reach Gluetun.RequestRotate")
	}
	if pm.Trigger() {
		t.Error("Rotate did not wake the port monitor: its trigger is still empty")
	}
}
