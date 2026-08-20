package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func speedMux(t *testing.T, s *SpeedFile, portOpen func() (bool, bool)) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, s, portOpen)
	return mux
}

func getBody(t *testing.T, mux *http.ServeMux, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestMetrics_ExposesLatestResult(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{
		At: time.Now(), DownloadMbps: 412.5, UploadMbps: 38.25,
		LatencyMs: 14.5, JitterMs: 2.25, ExitIP: "1.1.1.1",
	})

	code, body := getBody(t, speedMux(t, s, nil), "/metrics")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	for _, want := range []string{
		"rss4transmission_speedtest_download_mbps 412.5",
		"rss4transmission_speedtest_upload_mbps 38.25",
		"rss4transmission_speedtest_latency_ms 14.5",
		"rss4transmission_speedtest_jitter_ms 2.25",
		"rss4transmission_speedtest_last_run_timestamp_seconds ",
		"# TYPE rss4transmission_speedtest_download_mbps gauge",
		"# HELP rss4transmission_speedtest_download_mbps",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q\ngot:\n%s", want, body)
		}
	}
}

func TestMetrics_ContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	speedMux(t, tempSpeedFile(t), nil).ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

// A failed run must not publish a download_mbps of 0 -- a dashboard would
// read that as a real measurement and alert on a link that is merely untested.
func TestMetrics_OmitsGaugesWhenLastRunFailed(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), Error: "proxy refused"})

	_, body := getBody(t, speedMux(t, s, nil), "/metrics")

	if strings.Contains(body, "rss4transmission_speedtest_download_mbps ") {
		t.Errorf("download gauge published for a failed run\ngot:\n%s", body)
	}
	if !strings.Contains(body, "rss4transmission_speedtest_failures_total 1") {
		t.Errorf("failures_total missing or wrong\ngot:\n%s", body)
	}
}

// A failed run must not hide the last good measurement either.
func TestMetrics_UsesLastSuccessfulResult(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now().Add(-time.Hour), DownloadMbps: 300})
	s.AddResult(SpeedResult{At: time.Now(), Error: "proxy refused"})

	_, body := getBody(t, speedMux(t, s, nil), "/metrics")

	if !strings.Contains(body, "rss4transmission_speedtest_download_mbps 300") {
		t.Errorf("last successful measurement not exposed\ngot:\n%s", body)
	}
}

func TestMetrics_RotationsTotal(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{At: time.Now().Add(-time.Hour)})
	s.AddRotation(RotationEvent{At: time.Now()})

	_, body := getBody(t, speedMux(t, s, nil), "/metrics")

	if !strings.Contains(body, "rss4transmission_vpn_rotations_total 2") {
		t.Errorf("rotations_total missing or wrong\ngot:\n%s", body)
	}
}

func TestMetrics_PeerPortOpen(t *testing.T) {
	tests := []struct {
		name     string
		portOpen func() (bool, bool)
		want     string
		absent   bool
	}{
		{"open", func() (bool, bool) { return true, true }, "rss4transmission_peer_port_open 1", false},
		{"closed", func() (bool, bool) { return false, true }, "rss4transmission_peer_port_open 0", false},
		{"unknown", func() (bool, bool) { return false, false }, "rss4transmission_peer_port_open", true},
		{"nil", nil, "rss4transmission_peer_port_open", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, body := getBody(t, speedMux(t, tempSpeedFile(t), tc.portOpen), "/metrics")
			if tc.absent {
				if strings.Contains(body, tc.want) {
					t.Errorf("peer_port_open published when unknown\ngot:\n%s", body)
				}
				return
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("metrics missing %q\ngot:\n%s", tc.want, body)
			}
		})
	}
}

// An empty store is the normal state before the first measurement lands.
func TestMetrics_EmptyStore(t *testing.T) {
	code, body := getBody(t, speedMux(t, tempSpeedFile(t), nil), "/metrics")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "rss4transmission_vpn_rotations_total 0") {
		t.Errorf("rotations_total missing\ngot:\n%s", body)
	}
}

func TestMetrics_NilStore(t *testing.T) {
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, nil, nil)
	code, _ := getBody(t, mux, "/metrics")
	if code != http.StatusOK {
		t.Errorf("status = %d with a nil store, want 200", code)
	}
}

// ---- /speedtest page ----

func TestSpeedTestPage_RendersResults(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{
		At: time.Now(), DownloadMbps: 412.5, UploadMbps: 38.1,
		LatencyMs: 14, ServerName: "Los Angeles", Sponsor: "Acme", ExitIP: "185.9.9.9",
	})
	s.AddRotation(RotationEvent{
		At: time.Now(), Reason: "too slow", BeforeMbps: 12.5,
		FromExitIP: "1.1.1.1", ToExitIP: "2.2.2.2",
	})

	code, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for _, want := range []string{"412.5", "185.9.9.9", "Los Angeles", "too slow", "2.2.2.2"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestSpeedTestPage_ShowsSkipAndErrorRows(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), Skipped: "2 torrent(s) downloading"})
	s.AddResult(SpeedResult{At: time.Now(), Error: "proxy refused"})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	for _, want := range []string{"torrent(s) downloading", "proxy refused"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q\ngot:\n%s", want, body)
		}
	}
}

func TestSpeedTestPage_EmptyStore(t *testing.T) {
	code, _ := getBody(t, speedMux(t, tempSpeedFile(t), nil), "/speedtest")
	if code != http.StatusOK {
		t.Errorf("status = %d on an empty store, want 200", code)
	}
}

// A rotation that landed on the same exit is the signal that rotating isn't
// helping, so the page has to make it visible rather than just showing an IP.
func TestSpeedTestPage_FlagsUnchangedExit(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{
		At: time.Now(), Reason: "slow", FromExitIP: "1.1.1.1", ToExitIP: "1.1.1.1",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	if !strings.Contains(strings.ToLower(body), "same exit") {
		t.Errorf("page does not flag an unchanged exit IP\ngot:\n%s", body)
	}
}

func TestSpeedTestPage_NilStoreNotRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, nil, nil)
	code, _ := getBody(t, mux, "/speedtest")
	if code != http.StatusNotFound {
		t.Errorf("status = %d with a nil store, want 404", code)
	}
}

// ---- history page nav link ----

func TestHistoryPage_LinksSpeedtestWhenEnabled(t *testing.T) {
	h := &HistoryFile{Records: []HistoryRecord{}, guidIndex: map[string]int{}}

	_, on := getBody(t, newWebMux(h, nil, nil, nil, nil, true), "/")
	if !strings.Contains(on, `href="/speedtest"`) {
		t.Errorf("history page missing the /speedtest link when enabled")
	}

	_, off := getBody(t, newWebMux(h, nil, nil, nil, nil, false), "/")
	if strings.Contains(off, `href="/speedtest"`) {
		t.Errorf("history page links /speedtest when the route is not registered")
	}
}

// ---- PortMonitor.LastOpen ----

func TestPortMonitor_LastOpenUnknownBeforeFirstCheck(t *testing.T) {
	m := &PortMonitor{}
	if _, known := m.LastOpen(); known {
		t.Errorf("LastOpen reports a known state before any check has run")
	}
}

func TestPortMonitor_LastOpenReflectsLastCheck(t *testing.T) {
	for _, want := range []bool{true, false} {
		m := &PortMonitor{}
		v := want
		m.lastOpen = &v

		open, known := m.LastOpen()
		if !known {
			t.Fatalf("LastOpen reports unknown after a check")
		}
		if open != want {
			t.Errorf("LastOpen = %v, want %v", open, want)
		}
	}
}
