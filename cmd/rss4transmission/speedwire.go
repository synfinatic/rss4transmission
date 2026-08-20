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

// startSpeedMonitor launches the speed monitor when configured. A setup
// failure is logged and swallowed: a bad speedtest config must not stop feed
// processing, which is what the daemon is actually here to do.
func startSpeedMonitor(ctx *RunContext, g *Gluetun) {
	monitor, err := newSpeedMonitorFor(ctx, g)
	if err != nil {
		log.WithError(err).Error("SpeedTest is enabled but could not be started")
		return
	}
	if monitor == nil {
		return
	}

	if g == nil {
		log.Info("SpeedTest is enabled without Gluetun: recording measurements only, VPN rotation is disabled")
	} else {
		log.Infof("SpeedTest enabled: measuring every %s via %s, rotating below %.1f Mbps",
			ctx.Config.SpeedTest.IntervalDuration(), ctx.Config.SpeedTest.Proxy,
			ctx.Config.SpeedTest.MinDownloadMbps)
	}

	go monitor.Run(context.Background())
}
