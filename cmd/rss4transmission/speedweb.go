package main

/*
 * RSS4Transmission
 * Copyright (c) 2023 Aaron Turner  <aturner at synfin dot net>
 *
 * This program is free software: you can redistribute it
 * and/or modify it under the terms of the GNU General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or with the authors permission any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

import (
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed web/speedtest.html
var speedTmpl string

// portOpenFunc reports the last observed state of the forwarded peer port.
// known is false when no check has completed yet, in which case the
// corresponding metric is omitted rather than published as a guess.
type portOpenFunc func() (open bool, known bool)

// exitIPFunc reports the VPN exit IP Gluetun claims for itself, as cached by
// the port monitor. known is false until Gluetun has answered once, and always
// when there is no Gluetun to ask.
//
// This is deliberately separate from SpeedResult.ExitIP, which is speedtest.net's
// view of the connection through Gluetun's proxy. The two disagree on providers
// that NAT per destination, where speedtest.net's address drifts over a tunnel
// that never rotated -- so only Gluetun's answers "which exit are we on".
type exitIPFunc func() (ip string, known bool)

// speedPageRow is one measurement as rendered on the /speedtest page.
type speedPageRow struct {
	SpeedResult
	Status string // "ok", "skipped" or "error"
}

// speedPageRotation is one rotation event plus whether it actually moved us.
type speedPageRotation struct {
	RotationEvent
	// SameExit is true when the VPN came back up on the exit IP we were
	// already using. With a narrow SERVER_CITIES filter the candidate pool
	// can be small enough that a restart lands on the same server, which
	// means the rotation cost us a reconnect and bought nothing.
	SameExit bool
}

type speedPageData struct {
	Rows      []speedPageRow
	Rotations []speedPageRotation
	Latest    *SpeedResult
	ExitIP    string
	// ExitIPSource names where ExitIP came from -- "Gluetun" or "speedtest.net".
	// It is rendered next to the value because the two are not interchangeable:
	// see exitIPFunc.
	ExitIPSource string
	// ShowUpload renders the upload tile. It is driven by whether any recent
	// measurement carries an upload leg rather than by the latest value: the
	// failure worth seeing is an exit whose upload has dropped to zero, and
	// keying off the latest value alone would hide exactly that.
	ShowUpload bool
	CanRun     bool // render the "Run speedtest now" button
	CanRotate  bool // render the "Rotate VPN now" button
}

// speedActions are the operations the /speedtest page's buttons invoke. A nil
// func means the button is not rendered and its route is not registered, which
// is how a measure-only deployment (no Gluetun) loses only the rotate button.
//
// Both funcs must return promptly: they are called from the HTTP goroutine and
// are expected to hand the work to the monitor that owns the state, not to do
// it inline.
type speedActions struct {
	Rotate func() bool         // re-pick an egress now; false => one is already under way
	Run    func() bool         // queue a measurement; false => one is already queued or running
	Active activeDownloadsFunc // only consulted to decide whether Rotate needs confirming
}

// registerSpeedRoutes adds GET /speedtest, GET /metrics and the page's two
// action routes to mux. All are intended for the private mux only: like GET /,
// they are unauthenticated and rely on --private-listen not being publicly
// reachable.
//
// /metrics is registered even with a nil store so a scrape configuration does
// not have to care whether SpeedTest is enabled; /speedtest is not, because a
// page with nothing to show is worse than a 404.
func registerSpeedRoutes(mux *http.ServeMux, speed *SpeedFile, portOpen portOpenFunc,
	exitIP exitIPFunc, actions speedActions,
) {
	if speed != nil {
		tmpl := template.Must(template.New("speedtest").Funcs(template.FuncMap{
			"mbps":    func(v float64) string { return fmt.Sprintf("%.1f", v) },
			"ms":      func(v float64) string { return fmt.Sprintf("%.1f", v) },
			"fmtTime": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
		}).Parse(speedTmpl))

		mux.HandleFunc("GET /speedtest", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data := buildSpeedPageData(speed, exitIP)
			data.CanRun = actions.Run != nil
			data.CanRotate = actions.Rotate != nil
			if err := tmpl.Execute(w, data); err != nil {
				log.WithError(err).Error("Failed to render speedtest template")
			}
		})

		if actions.Run != nil {
			mux.HandleFunc("POST /speedtest/run", makeSpeedRunHandler(actions.Run))
		}
		if actions.Rotate != nil {
			mux.HandleFunc("POST /speedtest/rotate", makeSpeedRotateHandler(actions))
		}
	}

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(renderMetrics(speed, portOpen)))
	})
}

// makeSpeedRunHandler queues an on-demand measurement. It answers immediately
// rather than waiting out the ~30s test: the page's periodic refresh is what
// eventually shows the row.
func makeSpeedRunHandler(run func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !run() {
			// Not an error: the monitor coalesces requests, so a second click
			// is simply redundant.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("A measurement is already queued."))
			return
		}
		log.Info("Speedtest: on-demand measurement requested from the VPN page")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("Measurement queued; the results table updates when it finishes."))
	}
}

// makeSpeedRotateHandler asks Gluetun to re-pick an egress. Rotating drops the
// tunnel, so when torrents are downloading it replies 409 with a message the
// page turns into a confirmation prompt; the client re-posts with confirm=1 to
// go ahead. Doing it as a 409-then-repost keeps the count fresh at click time
// without a second endpoint, and guards a bare curl of this route too.
func makeSpeedRotateHandler(actions speedActions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Unable to parse the request.", http.StatusBadRequest)
			return
		}

		if r.FormValue("confirm") != "1" && actions.Active != nil {
			// An unknown count is not the same as zero, so a failed check takes
			// the confirm path rather than rotating on an assumption.
			n, err := actions.Active(r.Context())
			switch {
			case err != nil:
				log.WithError(err).Warn("Unable to check for active torrents before rotating")
				http.Error(w, "Unable to check for active downloads. Rotate anyway?",
					http.StatusConflict)
				return
			case n > 0:
				http.Error(w, fmt.Sprintf(
					"%d torrent(s) are downloading and will be interrupted. Rotate anyway?", n),
					http.StatusConflict)
				return
			}
		}

		log.Warn("VPN rotation requested from the VPN page")
		if !actions.Rotate() {
			// Not an error: a rotation takes about a minute, and the button
			// comes back long before it finishes.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("A rotation is already in progress."))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("Rotation requested; the VPN will reconnect shortly."))
	}
}

// buildSpeedPageData assembles the /speedtest view, newest entries first.
func buildSpeedPageData(speed *SpeedFile, exitIP exitIPFunc) speedPageData {
	results := speed.GetResults()
	data := speedPageData{
		Rows:      make([]speedPageRow, 0, len(results)),
		Rotations: []speedPageRotation{},
	}

	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		status := "ok"
		switch {
		case r.Error != "":
			status = "error"
		case r.Skipped != "":
			status = "skipped"
		}
		data.Rows = append(data.Rows, speedPageRow{SpeedResult: r, Status: status})
	}

	if latest, ok := speed.LatestSuccessful(); ok {
		data.Latest = &latest
	}

	// Gluetun is the only source that answers "which exit are we on"; a
	// measurement's ExitIP is speedtest.net's view and can differ without the
	// tunnel having moved. Falling back to it keeps the tile useful in a
	// measure-only deployment, where there is no Gluetun to ask -- but only when
	// there is genuinely nothing to ask, and the tile says which it is showing.
	switch {
	case exitIP != nil:
		if ip, known := exitIP(); known {
			data.ExitIP, data.ExitIPSource = ip, "Gluetun"
		}
	case data.Latest != nil:
		data.ExitIP, data.ExitIPSource = data.Latest.ExitIP, "speedtest.net"
	}

	for _, r := range results {
		if r.OK() && r.UploadMbps > 0 {
			data.ShowUpload = true
			break
		}
	}

	rotations := speed.GetRotations()
	for i := len(rotations) - 1; i >= 0; i-- {
		e := rotations[i]
		data.Rotations = append(data.Rotations, speedPageRotation{
			RotationEvent: e,
			SameExit:      e.ToExitIP != "" && e.ToExitIP == e.FromExitIP,
		})
	}

	return data
}

// renderMetrics emits the Prometheus text exposition format by hand. The repo
// has no metrics dependency and this is a handful of values, so pulling in
// client_golang would cost more than it saves.
func renderMetrics(speed *SpeedFile, portOpen portOpenFunc) string {
	var b strings.Builder

	gauge := func(name, help string, value float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n",
			name, help, name, name, strconv.FormatFloat(value, 'g', -1, 64))
	}
	counter := func(name, help string, value float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %s\n",
			name, help, name, name, strconv.FormatFloat(value, 'g', -1, 64))
	}

	if speed != nil {
		// Throughput gauges come from the last *successful* run. A failed or
		// skipped run records 0 Mbps, and publishing that would look exactly
		// like a link that measured as dead.
		if r, ok := speed.LatestSuccessful(); ok {
			gauge("rss4transmission_speedtest_download_mbps",
				"Download throughput measured over the VPN, last successful run.", r.DownloadMbps)
			// Upload, latency and jitter are optional legs: DownloadOnly skips
			// the upload test entirely. Omit rather than report a zero.
			if r.UploadMbps > 0 {
				gauge("rss4transmission_speedtest_upload_mbps",
					"Upload throughput measured over the VPN, last successful run.", r.UploadMbps)
			}
			if r.LatencyMs > 0 {
				gauge("rss4transmission_speedtest_latency_ms",
					"Latency to the speedtest server, last successful run.", r.LatencyMs)
			}
			if r.JitterMs > 0 {
				gauge("rss4transmission_speedtest_jitter_ms",
					"Jitter to the speedtest server, last successful run.", r.JitterMs)
			}
		}

		// last_run covers every attempt, including failures: a scrape needs to
		// tell "measured badly" apart from "stopped measuring".
		if r, ok := speed.Latest(); ok {
			gauge("rss4transmission_speedtest_last_run_timestamp_seconds",
				"Unix timestamp of the last speedtest attempt.", float64(r.At.Unix()))
		}

		failures := 0
		for _, r := range speed.GetResults() {
			if r.Error != "" {
				failures++
			}
		}
		counter("rss4transmission_speedtest_failures_total",
			"Speedtest attempts that failed, within the retention window.", float64(failures))

		counter("rss4transmission_vpn_rotations_total",
			"VPN egress rotations recorded, within the retention window.",
			float64(len(speed.GetRotations())))
	}

	if portOpen != nil {
		if open, known := portOpen(); known {
			value := 0.0
			if open {
				value = 1
			}
			gauge("rss4transmission_peer_port_open",
				"1 if the forwarded peer port was reachable on the last check.", value)
		}
	}

	return b.String()
}
