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
	"context"
	"fmt"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

// countDownloading counts torrents actively pulling data. Queued, seeding,
// checking and stopped torrents are excluded: they move no download bytes, so
// they neither compete with a speedtest nor make its result unrepresentative.
func countDownloading(torrents []transmissionrpc.Torrent) int {
	n := 0
	for _, t := range torrents {
		if t.Status != nil && *t.Status == transmissionrpc.TorrentStatusDownload {
			n++
		}
	}
	return n
}

// activeDownloads builds the activeDownloadsFunc the speed monitor uses to
// decide whether it is safe to measure.
func activeDownloads(ctx *RunContext) activeDownloadsFunc {
	return func(rCtx context.Context) (int, error) {
		torrents, err := ctx.Transmission.TorrentGet(rCtx, []string{"status", "rateDownload"}, nil)
		if err != nil {
			return 0, err
		}
		return countDownloading(torrents), nil
	}
}

// newSpeedMonitorFor assembles the SpeedMonitor from the live config, opening
// the results store and setting ctx.Speed as a side effect so the web UI can
// serve the same data. Returns (nil, nil) when SpeedTest is disabled.
//
// When g is nil the monitor runs in measure-only mode: results are still
// recorded and served, but nothing can rotate the VPN egress.
func newSpeedMonitorFor(ctx *RunContext, g *Gluetun) (*SpeedMonitor, error) {
	cfg := ctx.Config.SpeedTest
	if !cfg.Enabled {
		return nil, nil
	}

	// Cooldown and the daily rotation cap are both derived from the persisted
	// history, so there is no useful measure-only fallback without a store.
	if cfg.ResultsFile == "" {
		return nil, fmt.Errorf("SpeedTest.ResultsFile is required when SpeedTest is enabled")
	}

	runTest, err := newSpeedtestRunner(cfg)
	if err != nil {
		return nil, err
	}

	speed, err := OpenSpeedFile(cfg.ResultsFile)
	if err != nil {
		return nil, fmt.Errorf("unable to open %s: %w", cfg.ResultsFile, err)
	}
	ctx.Speed = speed

	var rotate rotateRequestFunc
	if g != nil {
		rotate = g.RequestRotate
	}

	return NewSpeedMonitor(cfg, ctx.Config.Ntfy, speed, runTest, activeDownloads(ctx), rotate), nil
}

// startSpeedMonitor launches the speed monitor when configured and returns it
// so the VPN page's buttons can reach Trigger. A setup failure is logged and
// swallowed -- a bad speedtest config must not stop feed processing, which is
// what the daemon is actually here to do -- so nil covers both "disabled" and
// "misconfigured".
func startSpeedMonitor(ctx *RunContext, g *Gluetun) *SpeedMonitor {
	monitor, err := newSpeedMonitorFor(ctx, g)
	if err != nil {
		log.WithError(err).Error("SpeedTest is enabled but could not be started")
		return nil
	}
	if monitor == nil {
		return nil
	}

	if g == nil {
		log.Info("SpeedTest is enabled without Gluetun: recording measurements only, VPN rotation is disabled")
	} else {
		log.Infof("SpeedTest enabled: measuring every %s via %s, rotating below %.1f Mbps",
			ctx.Config.SpeedTest.IntervalDuration(), ctx.Config.SpeedTest.Proxy,
			ctx.Config.SpeedTest.MinDownloadMbps)
	}

	go monitor.Run(context.Background())

	return monitor
}

// newSpeedActions builds the operations behind the VPN page's buttons. Each is
// left nil when the thing it drives is absent, which is how a measure-only
// deployment renders the page without a rotate button.
//
// Rotate deliberately does not call into Gluetun beyond RequestRotate: Gluetun
// has no internal locking and its state is serialized by PortMonitor.mu, so the
// button records the request and wakes the port monitor, which performs the
// rotation on its own goroutine along with the peer-port resync. portMonitor is
// non-nil whenever g is, since a configured Gluetun always builds one.
func newSpeedActions(ctx *RunContext, monitor *SpeedMonitor, g *Gluetun, portMonitor *PortMonitor) speedActions {
	var actions speedActions

	if monitor != nil {
		actions.Run = monitor.Trigger
		actions.Active = activeDownloads(ctx)
	}

	if g != nil && portMonitor != nil {
		actions.Rotate = func() bool {
			// RequestRotate refuses while a rotation is pending or running, so
			// an impatient second click is reported back to the page rather
			// than dropping the tunnel twice.
			if !g.RequestRotate(RotationSourceManual, "requested from the VPN page") {
				return false
			}
			portMonitor.Trigger()
			return true
		}
	}

	return actions
}

// vpnRotatedHook builds the callback Gluetun invokes once a rotation has
// completed and the tunnel is back up. It answers the question the request-time
// alert cannot: which exit are we on now.
//
// It is wired for every rotation trigger, not just the speedtest one, since a
// RotateTime or closed-port rotation changes the exit just as thoroughly -- and
// since this is the one place every rotation passes through, it is also where
// the history gets written. store is nil when SpeedTest is disabled, in which
// case there is nothing to record and only the notification is sent. retention
// is passed straight to Save, which prunes on write -- it must be the configured
// retention window, never a zero value, or the save would drop every record.
func vpnRotatedHook(ntfy NtfyConfig, store *SpeedFile, retention time.Duration) func(RotationOutcome) {
	return func(o RotationOutcome) {
		if store != nil {
			// Completes the event decide() staged, or records one from scratch
			// for a rotation nothing staged: schedule, closed-port and manual
			// rotations all arrive here with no prior event, and without this
			// they would reach neither the /speedtest page nor the metric.
			store.CompleteRotation(o.Source, o.Reason, o.PreviousIP, o.NewIP)
			if err := store.Save(retention); err != nil {
				log.WithError(err).Warn("Unable to save speedtest results after rotation")
			}
		}

		notifyVpnRotated(ntfy, &NtfyVpnRotatedContext{
			ExitIP:     o.NewIP,
			PreviousIP: o.PreviousIP,
			SameExit:   o.NewIP != "" && o.NewIP == o.PreviousIP,
		})
	}
}
