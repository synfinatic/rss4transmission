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
	"strings"
	"time"
)

type SpeedTestCmd struct {
	Save   bool   `kong:"help='Record the result in SpeedTest.ResultsFile'"`
	Server string `kong:"help='Test this speedtest.net server ID instead of SpeedTest.ServerID'"`
}

// oneShotSpeedConfig prepares the SpeedTest config for a single manual run.
// Enabled is forced on: the whole point of this command is to prove the proxy
// is wired up correctly *before* the user turns the background monitor on.
//
// serverID overrides SpeedTest.ServerID when it is not empty, so candidates can
// be compared over the same proxy the monitor uses without an edit to the
// config file between runs. That matters because a server can carry download at
// full rate and fail upload entirely, which only a real run reveals.
func oneShotSpeedConfig(cfg SpeedTestConfig, serverID string) (SpeedTestConfig, error) {
	cfg.Enabled = true
	if serverID != "" {
		cfg.ServerID = serverID
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// formatSpeedResult renders a single measurement for the terminal. Legs that
// were not measured are omitted rather than printed as zero, so a skipped
// upload test can't be mistaken for a dead uplink.
func formatSpeedResult(r SpeedResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Time:     %s\n", r.At.Local().Format(time.RFC3339))
	if r.Error != "" {
		fmt.Fprintf(&b, "Error:    %s\n", r.Error)
		return b.String()
	}

	if r.ServerName != "" || r.Sponsor != "" || r.ServerID != "" {
		server := r.ServerName
		if r.Sponsor != "" {
			server = strings.TrimSpace(server + " (" + r.Sponsor + ")")
		}
		if r.ServerID != "" {
			server = strings.TrimSpace(server + " [id " + r.ServerID + "]")
		}
		fmt.Fprintf(&b, "Server:   %s\n", server)
	}
	if r.ExitIP != "" {
		fmt.Fprintf(&b, "Exit IP:  %s\n", r.ExitIP)
	}
	if r.LatencyMs > 0 {
		fmt.Fprintf(&b, "Latency:  %.2f ms\n", r.LatencyMs)
	}
	if r.JitterMs > 0 {
		fmt.Fprintf(&b, "Jitter:   %.2f ms\n", r.JitterMs)
	}
	fmt.Fprintf(&b, "Download: %.1f Mbps\n", r.DownloadMbps)
	if r.UploadMbps > 0 {
		fmt.Fprintf(&b, "Upload:   %.1f Mbps\n", r.UploadMbps)
	}

	return b.String()
}

// speedResultForOutput stamps a run's error onto the result before rendering,
// mirroring what SpeedMonitor.measure does. Without it a failed run renders as
// "Download: 0.0 Mbps" -- a dead link rather than a test that never ran.
func speedResultForOutput(r SpeedResult, err error) SpeedResult {
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func (cmd *SpeedTestCmd) Run(ctx *RunContext) error {
	cfg, err := oneShotSpeedConfig(ctx.Config.SpeedTest, cmd.Server)
	if err != nil {
		return fmt.Errorf("invalid SpeedTest configuration: %w", err)
	}

	runTest, err := newSpeedtestRunner(cfg)
	if err != nil {
		return err
	}

	log.Infof("Running speedtest via %s (%d second capture)...", cfg.Proxy, cfg.CaptureSeconds)
	result, testErr := runTest(context.Background())

	fmt.Print(formatSpeedResult(speedResultForOutput(result, testErr)))

	// The exit IP is the only cheap proof that the proxy is really carrying
	// traffic over the tunnel; without it every later measurement could be
	// silently timing the host's own connection.
	if testErr == nil && result.ExitIP == "" {
		log.Warn("No exit IP reported; unable to confirm traffic went over the VPN")
	}

	if cmd.Save && testErr == nil {
		if cfg.ResultsFile == "" {
			return fmt.Errorf("--save requires SpeedTest.ResultsFile to be set")
		}
		speed, err := OpenSpeedFile(cfg.ResultsFile)
		if err != nil {
			return fmt.Errorf("unable to open %s: %w", cfg.ResultsFile, err)
		}
		speed.AddResult(result)
		if err := speed.Save(cfg.RetentionDuration()); err != nil {
			return fmt.Errorf("unable to save %s: %w", cfg.ResultsFile, err)
		}
	}

	return testErr
}
