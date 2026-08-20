package main

import (
	"strings"
	"testing"
	"time"
)

// The one-shot command exists to validate proxy wiring before turning the
// monitor on, so it must work while SpeedTest.Enabled is still false.
func TestOneShotSpeedConfig_WorksWhileDisabled(t *testing.T) {
	cfg := SpeedTestConfig{
		Enabled: false, Interval: "1h", Cooldown: "2h",
		Proxy: "http://gluetun:8888", CaptureSeconds: 5, Threads: 2,
	}

	got, err := oneShotSpeedConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got.IntervalDuration() != time.Hour {
		t.Errorf("Interval not parsed: %s", got.IntervalDuration())
	}
}

func TestOneShotSpeedConfig_RejectsBadProxy(t *testing.T) {
	cfg := SpeedTestConfig{Interval: "1h", Cooldown: "2h", Proxy: "gluetun:8888", CaptureSeconds: 5}

	if _, err := oneShotSpeedConfig(cfg); err == nil {
		t.Error("expected an error for a proxy with no scheme")
	}
}

func TestFormatSpeedResult_Success(t *testing.T) {
	out := formatSpeedResult(SpeedResult{
		At: time.Now(), DownloadMbps: 412.5, UploadMbps: 38.1, LatencyMs: 14.25,
		JitterMs: 2.5, ServerID: "1234", ServerName: "Los Angeles", Sponsor: "Acme",
		ExitIP: "185.9.9.9",
	})

	for _, want := range []string{"412.5", "38.1", "14.2", "Los Angeles", "Acme", "1234", "185.9.9.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot:\n%s", want, out)
		}
	}
}

// DownloadOnly leaves upload at zero; printing "0.0 Mbps" would read as a
// broken uplink rather than a leg that was deliberately skipped.
func TestFormatSpeedResult_OmitsSkippedUpload(t *testing.T) {
	out := formatSpeedResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5})

	if strings.Contains(out, "Upload") {
		t.Errorf("output reports an upload that was never measured\ngot:\n%s", out)
	}
}

func TestFormatSpeedResult_Error(t *testing.T) {
	out := formatSpeedResult(SpeedResult{At: time.Now(), Error: "proxy refused"})

	if !strings.Contains(out, "proxy refused") {
		t.Errorf("output missing the error text\ngot:\n%s", out)
	}
}
