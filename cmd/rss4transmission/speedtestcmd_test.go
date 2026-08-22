package main

import (
	"errors"
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

	got, err := oneShotSpeedConfig(cfg, "")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got.IntervalDuration() != time.Hour {
		t.Errorf("Interval not parsed: %s", got.IntervalDuration())
	}
}

func TestOneShotSpeedConfig_RejectsBadProxy(t *testing.T) {
	cfg := SpeedTestConfig{Interval: "1h", Cooldown: "2h", Proxy: "gluetun:8888", CaptureSeconds: 5}

	if _, err := oneShotSpeedConfig(cfg, ""); err == nil {
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

// speedtest measures the VPN link and nothing else. Opening the seen cache
// warns about creating a file the command never reads or writes, and building
// a Transmission client is equally pointless.
func TestCommandNeedsTransmission(t *testing.T) {
	tests := map[string]bool{
		"speedtest": false,
		"version":   false,
		"once":      true,
		"watch":     true,
		"simulate":  true,
	}
	for command, want := range tests {
		if got := commandNeedsTransmission(command); got != want {
			t.Errorf("commandNeedsTransmission(%q) = %v, want %v", command, got, want)
		}
	}
}

// A failed run leaves DownloadMbps at zero. Rendering that as "0.0 Mbps"
// reports a dead link when what actually happened is that no measurement was
// taken at all, so the error has to be stamped onto the result first.
func TestSpeedResultForOutput_StampsError(t *testing.T) {
	r := speedResultForOutput(SpeedResult{At: time.Now()}, errors.New("proxy refused"))

	if r.Error != "proxy refused" {
		t.Errorf("Error = %q, want %q", r.Error, "proxy refused")
	}
	if out := formatSpeedResult(r); strings.Contains(out, "Download") {
		t.Errorf("failed run still renders a download figure:\n%s", out)
	}
}

func TestSpeedResultForOutput_SuccessUnchanged(t *testing.T) {
	r := speedResultForOutput(SpeedResult{At: time.Now(), DownloadMbps: 412.5}, nil)

	if r.Error != "" {
		t.Errorf("Error = %q on a successful run, want empty", r.Error)
	}
	if out := formatSpeedResult(r); !strings.Contains(out, "412.5") {
		t.Errorf("successful run lost its measurement:\n%s", out)
	}
}

// Finding a working server means testing candidates one at a time through the
// same proxy the monitor uses, so the one-shot command has to be able to target
// a server without an edit to the config file.
func TestOneShotSpeedConfig_OverridesServerID(t *testing.T) {
	cfg := SpeedTestConfig{
		Interval: "1h", Cooldown: "2h", Proxy: "http://gluetun:8888",
		CaptureSeconds: 5, Threads: 2, ServerID: "1111",
	}

	got, err := oneShotSpeedConfig(cfg, "2222")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got.ServerID != "2222" {
		t.Errorf("ServerID = %q, want the override %q", got.ServerID, "2222")
	}
}

// An empty override must leave the configured server alone, so the flag is
// genuinely optional.
func TestOneShotSpeedConfig_KeepsConfiguredServerID(t *testing.T) {
	cfg := SpeedTestConfig{
		Interval: "1h", Cooldown: "2h", Proxy: "http://gluetun:8888",
		CaptureSeconds: 5, Threads: 2, ServerID: "1111",
	}

	got, err := oneShotSpeedConfig(cfg, "")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got.ServerID != "1111" {
		t.Errorf("ServerID = %q, want the configured %q", got.ServerID, "1111")
	}
}
