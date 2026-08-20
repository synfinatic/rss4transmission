package main

import (
	"strings"
	"testing"

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
