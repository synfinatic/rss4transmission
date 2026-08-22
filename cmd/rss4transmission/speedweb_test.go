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
	registerSpeedRoutes(mux, staticSpeed(s), portOpen, nil, nil, staticActions(speedActions{}), navConfig{})
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
	registerSpeedRoutes(mux, staticSpeed(nil), nil, nil, nil, staticActions(speedActions{}), navConfig{})
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

	code, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for _, want := range []string{"412.5", "185.9.9.9", "Los Angeles"} {
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
func TestRotationsPage_FlagsUnchangedExit(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{
		At: time.Now(), Reason: "slow", FromExitIP: "1.1.1.1", ToExitIP: "1.1.1.1",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/rotations")
	if !strings.Contains(strings.ToLower(body), "same exit") {
		t.Errorf("page does not flag an unchanged exit IP\ngot:\n%s", body)
	}
}

func TestSpeedTestPage_NilStoreNotRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, staticSpeed(nil), nil, nil, nil, staticActions(speedActions{}), navConfig{})
	code, _ := getBody(t, mux, "/speedtest")
	if code != http.StatusNotFound {
		t.Errorf("status = %d with a nil store, want 404", code)
	}
}

// ---- history page nav link ----

func TestHistoryPage_LinksSpeedtestWhenEnabled(t *testing.T) {
	h := &HistoryFile{Records: []HistoryRecord{}, guidIndex: map[string]int{}}

	_, on := getBody(t, newWebMux(h, nil, nil, nil, nil, navConfig{Speedtest: navOn()}), "/")
	for _, want := range []string{`href="/speedtest"`, `href="/rotations"`} {
		if !strings.Contains(navLine(t, on), want) {
			t.Errorf("torrents page missing the %s link when speedtest is enabled", want)
		}
	}
	if !strings.Contains(navLine(t, on), `<span class="here">Torrents</span>`) {
		t.Errorf("torrents page nav does not mark the current page\ngot:\n%s", navLine(t, on))
	}

	// Both VPN pages are registered together, so neither is linked when the
	// speed store is absent and both routes would 404.
	_, off := getBody(t, newWebMux(h, nil, nil, nil, nil, navConfig{}), "/")
	for _, unwanted := range []string{`href="/speedtest"`, `href="/rotations"`} {
		if strings.Contains(off, unwanted) {
			t.Errorf("torrents page links %s when the route is not registered", unwanted)
		}
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
	registerSpeedRoutes(mux, staticSpeed(tempSpeedFile(t)), nil, nil, nil, staticActions(actions), navConfig{})
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
	registerSpeedRoutes(mux, staticSpeed(tempSpeedFile(t)), nil, nil, nil,
		staticActions(speedActions{Run: func() bool { return true }}), navConfig{})
	_, body := getBody(t, mux, "/speedtest")
	if !strings.Contains(body, `id="btn-run"`) {
		t.Error("page is missing the run button")
	}
	if strings.Contains(body, `id="btn-rotate"`) {
		t.Error("page renders the rotate button with no Rotate func")
	}

	mux = http.NewServeMux()
	registerSpeedRoutes(mux, staticSpeed(tempSpeedFile(t)), nil, nil, nil,
		staticActions(speedActions{Rotate: func() bool { return true }}), navConfig{})
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
func TestRotationsPage_ShowsRotationSource(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{
		At: time.Now(), Source: RotationSourceClosedPort,
		Reason: "peer port closed for 3 consecutive checks", FromExitIP: "1.1.1.1", ToExitIP: "2.2.2.2",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/rotations")
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

// Every page carries the same nav line under the record count, with the page
// you are on named but not linked -- so the bar reads the same everywhere and
// still says where you are.
func TestNav_SpeedPageLinksTheOtherTwo(t *testing.T) {
	_, body := getBody(t, speedMux(t, tempSpeedFile(t), nil), "/speedtest")

	nav := navLine(t, body)
	for _, want := range []string{`<a href="/">Torrents</a>`, `<a href="/rotations">Rotations</a>`} {
		if !strings.Contains(nav, want) {
			t.Errorf("VPN page nav missing %s\ngot:\n%s", want, nav)
		}
	}
	if strings.Contains(nav, `href="/speedtest"`) {
		t.Errorf("VPN page nav links the page you are already on\ngot:\n%s", nav)
	}
	if !strings.Contains(nav, `<span class="here">VPN Speed</span>`) {
		t.Errorf("VPN page nav does not mark the current page\ngot:\n%s", nav)
	}
}

func TestNav_RotationsPageLinksTheOtherTwo(t *testing.T) {
	_, body := getBody(t, speedMux(t, tempSpeedFile(t), nil), "/rotations")

	nav := navLine(t, body)
	for _, want := range []string{`<a href="/">Torrents</a>`, `<a href="/speedtest">VPN Speed</a>`} {
		if !strings.Contains(nav, want) {
			t.Errorf("rotations page nav missing %s\ngot:\n%s", want, nav)
		}
	}
	if !strings.Contains(nav, `<span class="here">Rotations</span>`) {
		t.Errorf("rotations page nav does not mark the current page\ngot:\n%s", nav)
	}
}

// navLine returns just the nav paragraph, so an assertion about a link cannot
// be satisfied by the page body.
func navLine(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<p id="nav">`)
	if start < 0 {
		t.Fatalf("page has no nav line:\n%s", body)
	}
	end := strings.Index(body[start:], "</p>")
	if end < 0 {
		t.Fatalf("page has an unterminated nav line:\n%s", body)
	}
	return body[start : start+end]
}

// The headline Exit IP is what Gluetun says about itself. It must not fall back
// to the measurement's ExitIP when both are available: some providers NAT per
// destination, so speedtest.net's view drifts over a tunnel that never moved.
func TestSpeedTestPage_ExitIPTileUsesGluetun(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{
		At: time.Now(), DownloadMbps: 412.5, ServerName: "Los Angeles", ExitIP: "45.12.3.9",
	})

	mux := http.NewServeMux()
	registerSpeedRoutes(mux, staticSpeed(s), nil, nil,
		staticExitIP(func() (string, bool) { return "185.9.9.9", true }), staticActions(speedActions{}), navConfig{})
	_, body := getBody(t, mux, "/speedtest")

	tile := summarySection(t, body)
	if !strings.Contains(tile, "185.9.9.9") {
		t.Errorf("summary does not show Gluetun's exit IP\ngot:\n%s", tile)
	}
	if strings.Contains(tile, "45.12.3.9") {
		t.Errorf("summary shows the measurement's exit IP\ngot:\n%s", tile)
	}
	// Unannotated: with Gluetun configured the tile is always Gluetun's answer,
	// so naming it on every page load is noise.
	if strings.Contains(tile, "Gluetun") {
		t.Errorf("summary annotates the source it always has\ngot:\n%s", tile)
	}
	// The per-measurement value is still on the page, under its own heading.
	if !strings.Contains(body, "45.12.3.9") {
		t.Errorf("measurements table lost the speedtest exit IP\ngot:\n%s", body)
	}
	if !strings.Contains(body, "speedtest.net") {
		t.Errorf("measurements column is not labeled as speedtest.net's view\ngot:\n%s", body)
	}
}

// Measure-only deployments have no Gluetun to ask, so the tile falls back to the
// measurement -- and says so, rather than passing it off as the tunnel's view.
func TestSpeedTestPage_ExitIPTileFallsBackToSpeedtest(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5, ExitIP: "45.12.3.9"})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")

	tile := summarySection(t, body)
	if !strings.Contains(tile, "45.12.3.9") {
		t.Errorf("summary does not fall back to the measurement's exit IP\ngot:\n%s", tile)
	}
	if !strings.Contains(tile, "speedtest.net") {
		t.Errorf("summary does not name speedtest.net as the source\ngot:\n%s", tile)
	}
}

// Gluetun configured but not yet asked (or answering) is not the same as no
// Gluetun: there is nothing to show, and the measurement's IP is not a stand-in.
func TestSpeedTestPage_ExitIPTileUnknown(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5, ExitIP: "45.12.3.9"})

	mux := http.NewServeMux()
	registerSpeedRoutes(mux, staticSpeed(s), nil, nil,
		staticExitIP(func() (string, bool) { return "", false }), staticActions(speedActions{}), navConfig{})
	_, body := getBody(t, mux, "/speedtest")

	tile := summarySection(t, body)
	if strings.Contains(tile, "45.12.3.9") {
		t.Errorf("summary substituted the measurement's exit IP\ngot:\n%s", tile)
	}
}

// summarySection returns just the tiles at the top of the page, so an assertion
// about the headline Exit IP can't be satisfied by the measurements table.
func summarySection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<div id="summary">`)
	if start < 0 {
		t.Fatalf("page has no summary block:\n%s", body)
	}
	end := strings.Index(body[start:], "<h2>")
	if end < 0 {
		t.Fatalf("page has no section after the summary block:\n%s", body)
	}
	return body[start : start+end]
}

// A row records both views of the exit: Gluetun's, which is what the tunnel
// actually is, and speedtest.net's, which on a provider that NATs per
// destination can be a different address over the very same tunnel.
func TestSpeedTestPage_ShowsBothExitIPsPerMeasurement(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{
		At: time.Now(), DownloadMbps: 412.5, ExitIP: "45.12.3.9", GluetunExitIP: "185.9.9.9",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")

	// The two headers are deliberately parallel: each names the value and then
	// who was asked for it, so neither column depends on the other to be read.
	table := measurementsSection(t, body)
	for _, want := range []string{
		"185.9.9.9", "45.12.3.9", "Exit IP (Gluetun)", "Exit IP (speedtest.net)",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("measurements table missing %q\ngot:\n%s", want, table)
		}
	}
}

// When the two agree there is nothing to compare, so the row says so instead of
// printing the same address twice and inviting a second look.
func TestSpeedTestPage_CollapsesMatchingExitIPs(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{
		At: time.Now(), DownloadMbps: 412.5, ExitIP: "185.9.9.9", GluetunExitIP: "185.9.9.9",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")

	table := measurementsSection(t, body)
	row := table[strings.Index(table, "<tr>\n            <td>"):]
	if n := strings.Count(row, "185.9.9.9"); n != 1 {
		t.Errorf("exit IP rendered %d times in the row, want 1\ngot:\n%s", n, row)
	}
	if !strings.Contains(row, "same") {
		t.Errorf("row does not say the two views agree\ngot:\n%s", row)
	}
}

// A measurement taken before Gluetun answered, or in measure-only mode, still
// renders -- with the Gluetun column blank rather than filled in from the other.
func TestSpeedTestPage_MeasurementWithoutGluetunExitIP(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5, ExitIP: "45.12.3.9"})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")

	table := measurementsSection(t, body)
	if !strings.Contains(table, "45.12.3.9") {
		t.Errorf("measurements table lost speedtest.net's exit IP\ngot:\n%s", table)
	}
	if strings.Contains(table, "same") {
		t.Errorf("table claims the two views agree when only one exists\ngot:\n%s", table)
	}
}

// measurementsSection returns just the measurements table, so an assertion about
// a row can't be satisfied by the summary tiles or the rotations table.
func measurementsSection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "<h2>Measurements</h2>")
	if start < 0 {
		t.Fatalf("page has no measurements section:\n%s", body)
	}
	end := strings.Index(body[start:], "</table>")
	if end < 0 {
		// The empty state has no table; everything after the heading is the
		// section either way.
		return body[start:]
	}
	return body[start : start+end]
}

// The Detail column carries the rotation verdict for a slow measurement, so a
// row below the floor is never ambiguous about whether it cost a rotation.
func TestSpeedTestPage_ShowsRotationNoteInDetail(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{
		At: time.Now(), DownloadMbps: 12.5,
		RotationNote: "no rotation: 6 automatic rotations in the last 24h (max 6)",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")

	table := measurementsSection(t, body)
	if !strings.Contains(table, "max 6") {
		t.Errorf("Detail column does not carry the rotation note\ngot:\n%s", table)
	}
}

// An error still wins the Detail cell: a run that never measured has no rotation
// verdict worth reporting over the reason it failed.
func TestSpeedTestPage_ErrorOutranksRotationNoteInDetail(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), Error: "proxy refused", RotationNote: "unreachable"})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")

	table := measurementsSection(t, body)
	if !strings.Contains(table, "proxy refused") {
		t.Errorf("Detail column lost the error\ngot:\n%s", table)
	}
	if strings.Contains(table, "unreachable") {
		t.Errorf("Detail column showed a rotation note over the error\ngot:\n%s", table)
	}
}

// ---- rotations page ----

// Measurements land every few minutes while rotations are rare, so keeping both
// tables on one page buried the rotation log under hours of scrolling. It has
// its own page now, and /speedtest carries measurements only.
func TestSpeedTestPage_HasNoRotationsTable(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5})
	s.AddRotation(RotationEvent{
		At: time.Now(), Source: RotationSourceSpeedtest, Reason: "too slow",
		BeforeMbps: 12.5, FromExitIP: "1.1.1.1", ToExitIP: "2.2.2.2",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	for _, unwanted := range []string{"<h2>Rotations</h2>", "too slow", "2.2.2.2"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("VPN page still renders the rotation log (%q)\ngot:\n%s", unwanted, body)
		}
	}
}

func TestRotationsPage_RendersRotations(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{
		At: time.Now(), Source: RotationSourceSpeedtest, Reason: "too slow",
		BeforeMbps: 12.5, FromExitIP: "1.1.1.1", ToExitIP: "2.2.2.2",
	})

	code, body := getBody(t, speedMux(t, s, nil), "/rotations")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for _, want := range []string{RotationSourceSpeedtest, "too slow", "12.5", "1.1.1.1", "2.2.2.2"} {
		if !strings.Contains(body, want) {
			t.Errorf("rotations page missing %q\ngot:\n%s", want, body)
		}
	}
}

// Newest first, same as the measurements table: the last rotation is the one
// you came to look at.
func TestRotationsPage_NewestFirst(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{At: time.Now().Add(-time.Hour), Reason: "older"})
	s.AddRotation(RotationEvent{At: time.Now(), Reason: "newer"})

	_, body := getBody(t, speedMux(t, s, nil), "/rotations")
	if strings.Index(body, "newer") > strings.Index(body, "older") {
		t.Errorf("rotations are not newest first\ngot:\n%s", body)
	}
}

// A rotation whose outcome has not been recorded yet still renders: the To
// column says so rather than showing a blank cell.
func TestRotationsPage_PendingOutcome(t *testing.T) {
	s := tempSpeedFile(t)
	s.StageRotation(RotationEvent{
		At: time.Now(), Source: RotationSourceManual, Reason: "asked", FromExitIP: "1.1.1.1",
	})

	_, body := getBody(t, speedMux(t, s, nil), "/rotations")
	if !strings.Contains(body, "pending") {
		t.Errorf("rotations page does not mark an unfinished rotation\ngot:\n%s", body)
	}
}

func TestRotationsPage_EmptyStore(t *testing.T) {
	code, body := getBody(t, speedMux(t, tempSpeedFile(t), nil), "/rotations")
	if code != http.StatusOK {
		t.Fatalf("status = %d on an empty store, want 200", code)
	}
	if !strings.Contains(body, "No rotations recorded yet.") {
		t.Errorf("rotations page has no empty state\ngot:\n%s", body)
	}
}

// Without a speed store there is nothing to show, and a page of nothing is
// worse than a 404 -- the same call the /speedtest route makes.
func TestRotationsPage_NilStoreNotRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, staticSpeed(nil), nil, nil, nil, staticActions(speedActions{}), navConfig{})

	if code, _ := getBody(t, mux, "/rotations"); code != http.StatusNotFound {
		t.Errorf("status = %d without a speed store, want 404", code)
	}
}

// ---- last rotation tile ----

// With the rotation log a page away, the summary has to say when the egress
// last moved; otherwise the opening view says nothing about it at all.
func TestSpeedPage_SummaryShowsLastRotation(t *testing.T) {
	s := tempSpeedFile(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.Local)
	s.AddRotation(RotationEvent{At: at.Add(-time.Hour), Source: RotationSourceSchedule})
	s.AddRotation(RotationEvent{At: at, Source: RotationSourceClosedPort, Reason: "port closed"})

	_, body := getBody(t, speedMux(t, s, nil), "/speedtest")
	summary := summarySection(t, body)

	if !strings.Contains(strings.ToLower(summary), "last rotation") {
		t.Errorf("summary does not report the last rotation\ngot:\n%s", summary)
	}
	if !strings.Contains(summary, "2026-03-04 05:06:07") {
		t.Errorf("summary does not carry the last rotation's date and time\ngot:\n%s", summary)
	}
	if !strings.Contains(summary, RotationSourceClosedPort) {
		t.Errorf("summary does not name what asked for the last rotation\ngot:\n%s", summary)
	}
}

// A deployment that has never rotated says so, rather than printing an
// epoch-zero timestamp that reads as a rotation in 1970.
func TestSpeedPage_SummaryWithoutRotations(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5})

	summary := summarySection(t, mustBody(t, speedMux(t, s, nil), "/speedtest"))
	if !strings.Contains(strings.ToLower(summary), "last rotation") {
		t.Errorf("summary drops the tile when nothing has rotated\ngot:\n%s", summary)
	}
	if strings.Contains(summary, "1970-01-01") {
		t.Errorf("summary shows a zero-time rotation\ngot:\n%s", summary)
	}
}

// The count tile is the way to the rotation log now that it is off this page.
func TestSpeedPage_RotationCountLinksToRotations(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{At: time.Now(), Source: RotationSourceManual})

	summary := summarySection(t, mustBody(t, speedMux(t, s, nil), "/speedtest"))
	if !strings.Contains(summary, `href="/rotations"`) {
		t.Errorf("rotations tile does not link the rotation log\ngot:\n%s", summary)
	}
}

func mustBody(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	code, body := getBody(t, mux, path)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, code)
	}
	return body
}

// ---- forwarded port / port open tiles ----

// The two peer-port tiles answer the question the exit IP tile cannot: which
// port Gluetun forwards, and whether Transmission can actually reach it.
func TestSpeedTestPage_PeerPortTilesOpen(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5})

	mux := http.NewServeMux()
	registerSpeedRoutes(mux, staticSpeed(s),
		func() (bool, bool) { return true, true },
		func() (int64, bool) { return 51413, true },
		nil, staticActions(speedActions{}), navConfig{})
	_, body := getBody(t, mux, "/speedtest")

	tile := summarySection(t, body)
	if !strings.Contains(tile, "Forwarded port") {
		t.Errorf("summary has no forwarded port tile\ngot:\n%s", tile)
	}
	if !strings.Contains(tile, "51413") {
		t.Errorf("summary does not show the forwarded port\ngot:\n%s", tile)
	}
	if !strings.Contains(tile, "Port open") {
		t.Errorf("summary has no port open tile\ngot:\n%s", tile)
	}
	if !strings.Contains(tile, `<span class="value ok">yes</span>`) {
		t.Errorf("summary does not report the port as open\ngot:\n%s", tile)
	}
}

func TestSpeedTestPage_PeerPortTileClosed(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5})

	mux := http.NewServeMux()
	registerSpeedRoutes(mux, staticSpeed(s),
		func() (bool, bool) { return false, true },
		func() (int64, bool) { return 51413, true },
		nil, staticActions(speedActions{}), navConfig{})
	_, body := getBody(t, mux, "/speedtest")

	tile := summarySection(t, body)
	if !strings.Contains(tile, `<span class="value error">no</span>`) {
		t.Errorf("summary does not report the port as closed\ngot:\n%s", tile)
	}
}

// Nothing checked yet, or no Gluetun to ask, is not the same as "closed" or
// "no port forwarded", so both tiles stay empty rather than report a guess.
func TestSpeedTestPage_PeerPortTilesUnknown(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 412.5})

	mux := http.NewServeMux()
	registerSpeedRoutes(mux, staticSpeed(s),
		func() (bool, bool) { return false, false },
		func() (int64, bool) { return 0, false },
		nil, staticActions(speedActions{}), navConfig{})
	_, body := getBody(t, mux, "/speedtest")

	tile := summarySection(t, body)
	if strings.Contains(tile, `<span class="value ok">yes</span>`) ||
		strings.Contains(tile, `<span class="value error">no</span>`) {
		t.Errorf("summary reports a port state it has not checked\ngot:\n%s", tile)
	}
	if strings.Contains(tile, `<span class="value">0</span>`) {
		t.Errorf("summary shows a zero forwarded port\ngot:\n%s", tile)
	}
}

// A measure-only deployment wires neither func. The page must still render.
func TestBuildSpeedPageData_NilPortFuncs(t *testing.T) {
	data := buildSpeedPageData(tempSpeedFile(t), nil, nil, nil)

	if data.PortOpenKnown {
		t.Error("PortOpenKnown is set without a port-open func")
	}
	if data.PeerPortKnown {
		t.Error("PeerPortKnown is set without a peer-port func")
	}
}

func TestBuildSpeedPageData_PortFuncs(t *testing.T) {
	data := buildSpeedPageData(tempSpeedFile(t),
		func() (bool, bool) { return true, true },
		func() (int64, bool) { return 51413, true },
		nil)

	if !data.PortOpenKnown || !data.PortOpen {
		t.Errorf("PortOpen = %v, known = %v, want true/true", data.PortOpen, data.PortOpenKnown)
	}
	if !data.PeerPortKnown || data.PeerPort != 51413 {
		t.Errorf("PeerPort = %d, known = %v, want 51413/true", data.PeerPort, data.PeerPortKnown)
	}
}
