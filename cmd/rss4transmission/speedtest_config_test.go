package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSpeedTestConfig_Validate_ParsesDurations(t *testing.T) {
	cfg := SpeedTestConfig{
		Enabled:  true,
		Interval: "90m",
		Cooldown: "2h",
		Proxy:    "http://gluetun:8888",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() returned error: %v", err)
	}
	if got := cfg.IntervalDuration(); got != 90*time.Minute {
		t.Errorf("IntervalDuration() = %v, want 90m", got)
	}
	if got := cfg.CooldownDuration(); got != 2*time.Hour {
		t.Errorf("CooldownDuration() = %v, want 2h", got)
	}
}

// Validate must return an error rather than log.Fatalf like NewGluetun does --
// a bad live config reload has to leave the running config intact.
func TestSpeedTestConfig_Validate_BadDurationReturnsError(t *testing.T) {
	tests := []struct {
		name string
		cfg  SpeedTestConfig
	}{
		{"bad interval", SpeedTestConfig{Enabled: true, Interval: "banana", Cooldown: "2h", Proxy: "http://gluetun:8888"}},
		{"bad cooldown", SpeedTestConfig{Enabled: true, Interval: "1h", Cooldown: "banana", Proxy: "http://gluetun:8888"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Error("Validate() = nil, want error")
			}
		})
	}
}

// A disabled block is never used, so its contents must not block startup.
func TestSpeedTestConfig_Validate_SkippedWhenDisabled(t *testing.T) {
	cfg := SpeedTestConfig{Enabled: false, Interval: "banana"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() on disabled config returned error: %v", err)
	}
}

func TestSpeedTestConfig_Validate_RequiresProxyWhenEnabled(t *testing.T) {
	cfg := SpeedTestConfig{Enabled: true, Interval: "1h", Cooldown: "2h", Proxy: ""}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil with empty Proxy, want error")
	}
}

func TestSpeedTestConfig_Validate_RejectsBadProxyURL(t *testing.T) {
	cfg := SpeedTestConfig{Enabled: true, Interval: "1h", Cooldown: "2h", Proxy: "://nope"}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil with malformed Proxy, want error")
	}
}

// An interval shorter than the capture window would leave tests permanently
// running, saturating the very link we are trying to measure.
func TestSpeedTestConfig_Validate_RejectsIntervalShorterThanCapture(t *testing.T) {
	cfg := SpeedTestConfig{
		Enabled: true, Interval: "3s", Cooldown: "2h",
		Proxy: "http://gluetun:8888", CaptureSeconds: 10,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil with Interval < CaptureSeconds, want error")
	}
}

func TestSpeedTestConfig_Defaults(t *testing.T) {
	want := map[string]interface{}{
		"SpeedTest.Enabled":            false,
		"SpeedTest.Interval":           "1h",
		"SpeedTest.Proxy":              "http://gluetun:8888",
		"SpeedTest.MinDownloadMbps":    100.0,
		"SpeedTest.Cooldown":           "2h",
		"SpeedTest.MaxRotationsPerDay": 6,
		"SpeedTest.CaptureSeconds":     5,
		"SpeedTest.Threads":            2,
		"SpeedTest.DownloadOnly":       true,
		"SpeedTest.SkipWhenActive":     true,
		"SpeedTest.RetentionDays":      30,
	}
	for k, v := range want {
		got, ok := ConfigDefaults[k]
		if !ok {
			t.Errorf("ConfigDefaults missing %q", k)
			continue
		}
		if got != v {
			t.Errorf("ConfigDefaults[%q] = %v (%T), want %v (%T)", k, got, got, v, v)
		}
	}
}

func TestSpeedTestConfig_RetentionDuration(t *testing.T) {
	cfg := SpeedTestConfig{RetentionDays: 7}
	if got := cfg.RetentionDuration(); got != 7*24*time.Hour {
		t.Errorf("RetentionDuration() = %v, want 168h", got)
	}
}

// The parsed durations must survive loadConfig's unmarshal->validate->commit
// sequence, since Validate() is what fills them in.
func TestLoadConfig_SpeedTestParsedAndCommitted(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	yamlContent := `
SpeedTest:
  Enabled: true
  Interval: 30m
  Cooldown: 90m
  Proxy: http://gluetun:8888
  MinDownloadMbps: 250
`
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0600); err != nil {
		t.Fatal(err)
	}

	rc := &RunContext{}
	if _, err := rc.loadConfig(cfgFile); err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}

	st := rc.Config.SpeedTest
	if !st.Enabled {
		t.Error("SpeedTest.Enabled = false, want true")
	}
	if got := st.IntervalDuration(); got != 30*time.Minute {
		t.Errorf("IntervalDuration() = %v, want 30m", got)
	}
	if got := st.CooldownDuration(); got != 90*time.Minute {
		t.Errorf("CooldownDuration() = %v, want 90m", got)
	}
	if st.MinDownloadMbps != 250 {
		t.Errorf("MinDownloadMbps = %v, want 250", st.MinDownloadMbps)
	}
	// unset keys must fall back to ConfigDefaults
	if st.CaptureSeconds != 5 {
		t.Errorf("CaptureSeconds = %d, want default 5", st.CaptureSeconds)
	}
	if !st.DownloadOnly {
		t.Error("DownloadOnly = false, want default true")
	}
}

// A bad SpeedTest block must fail the whole load, leaving rc.Config untouched.
func TestLoadConfig_BadSpeedTestRejected(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	yamlContent := `
SpeedTest:
  Enabled: true
  Interval: banana
`
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0600); err != nil {
		t.Fatal(err)
	}

	rc := &RunContext{}
	if _, err := rc.loadConfig(cfgFile); err == nil {
		t.Fatal("loadConfig returned nil error, want failure")
	}
	if rc.Config.SpeedTest.Enabled {
		t.Error("rc.Config was committed despite a validation failure")
	}
}
