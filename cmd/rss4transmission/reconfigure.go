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
	"reflect"
	"time"
)

// applyConfig pushes a newly loaded config into the components watch built at
// startup. prev is the config that was running, next is the one that just
// loaded and is already stored on rc.
//
// The reload goroutine calls it with the reload lock held, and WatchCmd.Run
// calls it once with an empty prev to build everything for the first time. One
// code path therefore covers both, so startup and reload cannot drift apart.
//
// It never touches a component from under that component's own lock. The port
// monitor holds its lock for the whole of a VPN rotation, which runs for tens
// of seconds, so it is given a queued update instead and adopts it on its next
// check.
func (rc *RunContext) applyConfig(prev, next Config) error {
	// The client and the cache come first: everything below is handed one or
	// the other, so a failure here must stop the rest from wiring itself to
	// something that is about to be replaced.
	if err := rc.applyTransmissionClient(next); err != nil {
		return err
	}
	if err := rc.applySeenFile(prev, next); err != nil {
		return err
	}

	rc.applyStoreTTLs(next)
	rc.applyGluetun(next)

	// The speed monitor is rebuilt rather than mutated. It captures the
	// rotation hook, the ntfy config and the results store, so replacing it is
	// both simpler and more complete than reaching into it, and a measurement
	// abandoned by the rebuild costs one hourly sample.
	if speedWiringChanged(prev, next) {
		rc.restartSpeedMonitor(next)
	}

	rc.pushPortMonitorConfig(next)

	return nil
}

// applyTransmissionClient rebuilds the RPC client when the Transmission origin
// moved. Credentials are not part of that decision: the RPC client does not
// carry them, and the web proxy reads them from the live config per request.
//
// An unchanged origin keeps the same client, which keeps the session ID it
// already negotiated.
func (rc *RunContext) applyTransmissionClient(cfg Config) error {
	txURL := cfg.Transmission.URL()
	if rc.Tx() != nil && rc.txURL == txURL {
		return nil
	}

	client, err := newTransmissionClient(cfg.Transmission)
	if err != nil {
		return fmt.Errorf("unable to build the Transmission client: %w", err)
	}
	log.Infof("Transmission moved to %s", txURL)
	rc.setTx(client, txURL)
	return nil
}

// applySeenFile follows a changed SeenFile, saving what is in the cache now
// before it opens the new path. The --seen-file flag wins: it pins the path
// for the life of the process.
func (rc *RunContext) applySeenFile(prev, next Config) error {
	if rc.Cache == nil {
		return nil
	}
	want := GetPath(rc.seenFileFor(next))
	if rc.Cache.filename == want {
		return nil
	}

	// Pruned with the retention the entries were written under, and only when
	// that is a real number: saving with a zero window would drop every record
	// on its way out.
	days := prev.SeenCacheDays
	if days <= 0 {
		days = next.SeenCacheDays
	}
	if days > 0 {
		if err := rc.Cache.SaveCache(time.Duration(days)*24*time.Hour, nil); err != nil {
			return fmt.Errorf("unable to save %s before following the new SeenFile: %w",
				rc.Cache.filename, err)
		}
	}

	cache, err := OpenCache(want)
	if err != nil {
		return fmt.Errorf("unable to open cache file %s: %w", want, err)
	}
	log.Infof("SeenFile moved to %s", want)
	rc.Cache = cache
	return nil
}

// applyStoreTTLs points both token stores at the configured lifetime. Tokens
// already handed out keep the expiry they were registered with.
func (rc *RunContext) applyStoreTTLs(cfg Config) {
	ttl := time.Duration(cfg.Notifications.TokenTTLH) * time.Hour
	if rc.CancelStore != nil {
		rc.CancelStore.SetTTL(ttl)
	}
	if rc.StartStore != nil {
		rc.StartStore.SetTTL(ttl)
	}
}

// applyGluetun decides which Gluetun client the rest of the process sees.
//
// It builds a client when a block appears and drops the pointer when the block
// goes away. It deliberately does not write to an existing client: the port
// monitor owns those fields, and it applies the edit itself under its own lock
// when it consumes the queued update.
func (rc *RunContext) applyGluetun(cfg Config) {
	switch {
	case !cfg.Gluetun.Enabled():
		rc.Gluetun = nil
	case rc.Gluetun == nil:
		rc.Gluetun = NewGluetun(cfg.Gluetun, rc.Tx())
	}
}

// pushPortMonitorConfig queues the current config on the port monitor. The
// monitor adopts it at the top of its next check.
func (rc *RunContext) pushPortMonitorConfig(cfg Config) {
	if rc.PortMonitor == nil {
		return
	}
	rc.PortMonitor.ApplyConfig(portMonitorUpdate{
		Gluetun:      cfg.Gluetun,
		GluetunOn:    cfg.Gluetun.Enabled(),
		Client:       rc.Gluetun,
		Ntfy:         cfg.Ntfy,
		PortCheckOn:  cfg.PortCheck.Enabled,
		Transmission: rc.Tx(),
		OnRotated:    vpnRotatedHook(cfg.Ntfy, rc.Speed, cfg.SpeedTest.RetentionDuration()),
	})
}

// restartSpeedMonitor stops the running speed monitor and builds a new one
// from the current config. A monitor that fails to build is logged and left
// absent: a bad speedtest block must not stop feed processing, which is what
// the daemon is actually here to do.
func (rc *RunContext) restartSpeedMonitor(cfg Config) {
	if rc.speedCancel != nil {
		rc.speedCancel()
		rc.speedCancel = nil
	}
	rc.SpeedMonitor = nil
	// Cleared so the VPN page stops serving a store the monitor no longer
	// writes to. newSpeedMonitorFor sets it again when SpeedTest is on.
	rc.Speed = nil

	// Set before the monitor is built: the monitor reads it for the exit IP
	// its rotation alerts name.
	rc.ExitIP = exitIPSource(rc.Gluetun, rc.PortMonitor)

	monitor, err := newSpeedMonitorFor(rc, cfg, rc.Gluetun)
	if err != nil {
		log.WithError(err).Error("SpeedTest is enabled but could not be started")
	}

	rc.SpeedMonitor = monitor
	rc.SpeedActions = newSpeedActions(rc, monitor, rc.Gluetun, rc.PortMonitor)

	if monitor == nil {
		return
	}

	if rc.Gluetun == nil {
		log.Info("SpeedTest is enabled without Gluetun: recording measurements only, VPN rotation is disabled")
	} else {
		log.Infof("SpeedTest enabled: measuring every %s via %s, rotating below %.1f Mbps",
			cfg.SpeedTest.IntervalDuration(), cfg.SpeedTest.Proxy, cfg.SpeedTest.MinDownloadMbps)
	}

	speedCtx, cancel := context.WithCancel(context.Background())
	rc.speedCancel = cancel
	go monitor.Run(speedCtx)
}

// speedWiringChanged reports whether anything the speed monitor captured at
// build time was edited. Gluetun counts: the monitor holds the rotate callback
// for the client that existed when it was built.
func speedWiringChanged(prev, next Config) bool {
	return !exportedEqual(prev.SpeedTest, next.SpeedTest) ||
		!exportedEqual(prev.Ntfy, next.Ntfy) ||
		!exportedEqual(prev.Gluetun, next.Gluetun)
}

// exportedEqual reports whether two config blocks are the same in everything
// the user can write in the config file.
//
// It skips unexported fields, which hold values derived at load time -- the
// compiled ntfy templates and the parsed SpeedTest durations. Those are built
// fresh on every load, so a plain reflect.DeepEqual would report every block as
// edited and restart the speed monitor on every reload.
func exportedEqual(a, b any) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() || va.Kind() != reflect.Struct {
		return reflect.DeepEqual(a, b)
	}

	for i := 0; i < va.NumField(); i++ {
		if va.Type().Field(i).PkgPath != "" {
			continue
		}
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			return false
		}
	}
	return true
}
