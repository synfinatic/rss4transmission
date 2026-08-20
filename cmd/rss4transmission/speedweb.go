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
}

// registerSpeedRoutes adds GET /speedtest and GET /metrics to mux. Both are
// intended for the private mux only: like GET /, they are unauthenticated and
// rely on --private-listen not being publicly reachable.
//
// /metrics is registered even with a nil store so a scrape configuration does
// not have to care whether SpeedTest is enabled; /speedtest is not, because a
// page with nothing to show is worse than a 404.
func registerSpeedRoutes(mux *http.ServeMux, speed *SpeedFile, portOpen portOpenFunc) {
	if speed != nil {
		tmpl := template.Must(template.New("speedtest").Funcs(template.FuncMap{
			"mbps":    func(v float64) string { return fmt.Sprintf("%.1f", v) },
			"ms":      func(v float64) string { return fmt.Sprintf("%.1f", v) },
			"fmtTime": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
		}).Parse(speedTmpl))

		mux.HandleFunc("GET /speedtest", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := tmpl.Execute(w, buildSpeedPageData(speed)); err != nil {
				log.WithError(err).Error("Failed to render speedtest template")
			}
		})
	}

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(renderMetrics(speed, portOpen)))
	})
}

// buildSpeedPageData assembles the /speedtest view, newest entries first.
func buildSpeedPageData(speed *SpeedFile) speedPageData {
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
		data.ExitIP = latest.ExitIP
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
