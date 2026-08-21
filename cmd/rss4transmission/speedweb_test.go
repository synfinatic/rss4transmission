package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func speedMux(t *testing.T, s *SpeedFile, portOpen func() (bool, bool)) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, s, portOpen, speedActions{})
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
	registerSpeedRoutes(mux, nil, nil, speedActions{})
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
	registerSpeedRoutes(mux, nil, nil, speedActions{})
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

// ---- button actions ----

func actionMux(t *testing.T, actions speedActions) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, tempSpeedFile(t), nil, actions)
	return mux
}

func postForm(t *testing.T, mux *http.ServeMux, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestSpeedRun_QueuesMeasurement(t *testing.T) {
	calls := 0
	mux := actionMux(t, speedActions{Run: func() bool { calls++; return true }})

	code, body := postForm(t, mux, "/speedtest/run", "")
	if code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", code)
	}
	if calls != 1 {
		t.Errorf("Run called %d times, want 1", calls)
	}
	if body == "" {
		t.Error("empty body, want a message for the page to display")
	}
}

// A second click while a measurement is already queued is not an error: the
// monitor coalesces, and the page should say so rather than claim a new run.
func TestSpeedRun_AlreadyQueued(t *testing.T) {
	mux := actionMux(t, speedActions{Run: func() bool { return false }})

	code, body := postForm(t, mux, "/speedtest/run", "")
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if !strings.Contains(strings.ToLower(body), "already") {
		t.Errorf("body = %q, want it to say a run is already queued", body)
	}
}

func TestSpeedRun_NotRegisteredWithoutFunc(t *testing.T) {
	code, _ := postForm(t, actionMux(t, speedActions{}), "/speedtest/run", "")
	if code != http.StatusNotFound {
		t.Errorf("status = %d with no Run func, want 404", code)
	}
}

func TestSpeedRotate_NoActiveDownloads(t *testing.T) {
	calls := 0
	mux := actionMux(t, speedActions{
		Rotate: func() bool { calls++; return true },
		Active: func(context.Context) (int, error) { return 0, nil },
	})

	code, _ := postForm(t, mux, "/speedtest/rotate", "")
	if code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", code)
	}
	if calls != 1 {
		t.Errorf("Rotate called %d times, want 1", calls)
	}
}

// Rotating drops the tunnel, so an unconfirmed request while torrents are
// downloading has to come back for confirmation instead of acting.
func TestSpeedRotate_ActiveDownloadsNeedConfirmation(t *testing.T) {
	calls := 0
	mux := actionMux(t, speedActions{
		Rotate: func() bool { calls++; return true },
		Active: func(context.Context) (int, error) { return 3, nil },
	})

	code, body := postForm(t, mux, "/speedtest/rotate", "")
	if code != http.StatusConflict {
		t.Errorf("status = %d, want 409", code)
	}
	if !strings.Contains(body, "3") {
		t.Errorf("body = %q, want it to name the number of active downloads", body)
	}
	if calls != 0 {
		t.Errorf("Rotate called %d times without confirmation, want 0", calls)
	}
}

func TestSpeedRotate_ConfirmedWithActiveDownloads(t *testing.T) {
	calls := 0
	mux := actionMux(t, speedActions{
		Rotate: func() bool { calls++; return true },
		Active: func(context.Context) (int, error) { return 3, nil },
	})

	code, _ := postForm(t, mux, "/speedtest/rotate", "confirm=1")
	if code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", code)
	}
	if calls != 1 {
		t.Errorf("Rotate called %d times, want 1", calls)
	}
}

// "Couldn't ask Transmission" is not "nothing is downloading", so an error
// takes the confirm path rather than silently rotating.
func TestSpeedRotate_ActiveCheckErrorNeedsConfirmation(t *testing.T) {
	calls := 0
	mux := actionMux(t, speedActions{
		Rotate: func() bool { calls++; return true },
		Active: func(context.Context) (int, error) { return 0, fmt.Errorf("rpc down") },
	})

	code, _ := postForm(t, mux, "/speedtest/rotate", "")
	if code != http.StatusConflict {
		t.Errorf("status = %d, want 409", code)
	}
	if calls != 0 {
		t.Errorf("Rotate called %d times, want 0", calls)
	}
}

func TestSpeedRotate_NotRegisteredWithoutFunc(t *testing.T) {
	code, _ := postForm(t, actionMux(t, speedActions{}), "/speedtest/rotate", "")
	if code != http.StatusNotFound {
		t.Errorf("status = %d with no Rotate func, want 404", code)
	}
}

func TestSpeedTestPage_RendersOnlyAvailableButtons(t *testing.T) {
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, tempSpeedFile(t), nil, speedActions{Run: func() bool { return true }})
	_, body := getBody(t, mux, "/speedtest")
	if !strings.Contains(body, `id="btn-run"`) {
		t.Error("page is missing the run button")
	}
	if strings.Contains(body, `id="btn-rotate"`) {
		t.Error("page renders the rotate button with no Rotate func")
	}

	mux = http.NewServeMux()
	registerSpeedRoutes(mux, tempSpeedFile(t), nil, speedActions{Rotate: func() bool { return true }})
	_, body = getBody(t, mux, "/speedtest")
	if !strings.Contains(body, `id="btn-rotate"`) {
		t.Error("page is missing the rotate button")
	}
	if strings.Contains(body, `id="btn-run"`) {
		t.Error("page renders the run button with no Run func")
	}
}

// Rotating takes about a minute, and the button comes back enabled as soon as
// the request is accepted, so a second click has to be told "already going"
// rather than queueing a second tunnel restart.
func TestSpeedRotate_AlreadyInProgress(t *testing.T) {
	mux := actionMux(t, speedActions{
		Rotate: func() bool { return false },
		Active: func(context.Context) (int, error) { return 0, nil },
	})

	code, body := postForm(t, mux, "/speedtest/rotate", "")
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if !strings.Contains(strings.ToLower(body), "already") {
		t.Errorf("body = %q, want it to say a rotation is already under way", body)
	}
}

// The rotations table used to show only speedtest-driven rotations, so the
// reason alone identified them. Now that schedule, closed-port and manual
// rotations share the table, the page names the source outright.
func TestSpeedPage_ShowsRotationSource(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{
		At: time.Now(), Source: RotationSourceClosedPort,
		Reason: "peer port closed for 3 consecutive checks", FromExitIP: "1.1.1.1", ToExitIP: "2.2.2.2",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	if !strings.Contains(body, RotationSourceClosedPort) {
		t.Errorf("page does not name the rotation source %q", RotationSourceClosedPort)
	}
}

// A dead upload leg is the failure MinUploadMbps exists to catch, so the
// summary has to surface it next to the download rather than burying it in a
// table column.
func TestSpeedPage_ShowsLastUploadWhenMeasured(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now().Add(-time.Hour), DownloadMbps: 400, UploadMbps: 42})
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 400, UploadMbps: -0.04})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	if !strings.Contains(strings.ToLower(body), "last upload") {
		t.Error("summary does not report the last upload")
	}
}

// With DownloadOnly there is no upload leg at all, and a permanent "0.0 Mbps"
// tile would read as a dead link rather than a disabled test.
func TestSpeedPage_HidesLastUploadWhenNeverMeasured(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 400})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	if strings.Contains(strings.ToLower(body), "last upload") {
		t.Error("summary reports an upload that was never measured")
	}
}

// The two pages cross-link to each other, and the link belongs in the same
// place on both: its own line under the record count, not trailing the count
// sentence.
func TestSpeedPage_LinksHistoryOnItsOwnLine(t *testing.T) {
	_, body := getBody(t, speedMux(t, tempSpeedFile(t), nil), "/speedtest")

	if !strings.Contains(body, `<p id="nav"><a href="/">History</a></p>`) {
		t.Error("VPN page does not carry the History link on its own nav line")
	}
}
