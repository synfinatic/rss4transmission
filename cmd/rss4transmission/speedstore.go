package main

/*
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
	"encoding/json"
	"os"
	"sync"
	"time"
)

const SPEEDTEST_VERSION = 1

// SpeedFile persists throughput measurements and the rotations they triggered.
//
// It is modeled on HistoryFile rather than CacheFile: the SpeedMonitor
// goroutine appends to it while the web handlers read it, and unlike the seen
// cache there is no external mutex (watch.go's reloader.mu) serializing those
// two, so this type synchronizes itself.
type SpeedFile struct {
	Version   int             `json:"Version"`
	Results   []SpeedResult   `json:"Results"`
	Rotations []RotationEvent `json:"Rotations"`

	filename string
	mu       sync.RWMutex

	// staged is set between StageRotation and CompleteRotation, i.e. while a
	// requested rotation has an event recorded but no outcome yet. It is
	// deliberately not persisted: after a restart there is nothing left to
	// complete, and treating a leftover event as staged would let an unrelated
	// rotation write its exit IP onto it.
	staged bool
}

// What asked for a rotation. Recorded so the history can distinguish the
// daemon's own churn from a rotation the user clicked for, and so the daily cap
// can charge only the former. Events written before this field existed have an
// empty Source and count as automatic, which is the conservative reading.
const (
	RotationSourceSpeedtest  = "speedtest"   // a measurement came in below MinDownloadMbps
	RotationSourceManual     = "manual"      // the VPN page's Rotate button
	RotationSourceSchedule   = "schedule"    // Gluetun.RotateTime elapsed
	RotationSourceClosedPort = "closed-port" // Gluetun.ClosedPortChecks exceeded
)

// RotationEvent records a VPN rotation.
//
// ToExitIP is empty when Gluetun could not report the new exit; the next
// successful measurement backfills it. With a narrow Gluetun server filter
// (e.g. SERVER_CITIES=Los Angeles) a restart can reconnect to the same server,
// so ToExitIP == FromExitIP is the signal that rotating isn't actually buying
// anything.
//
// BeforeMbps is only meaningful for a speedtest-driven rotation; the other
// sources have no measurement attached.
type RotationEvent struct {
	At         time.Time `json:"At"`
	Source     string    `json:"Source,omitempty"`
	Reason     string    `json:"Reason"`
	BeforeMbps float64   `json:"BeforeMbps"`
	FromExitIP string    `json:"FromExitIP,omitempty"`
	ToExitIP   string    `json:"ToExitIP,omitempty"`
}

// OpenSpeedFile loads the results file, treating a missing file as a fresh start.
func OpenSpeedFile(path string) (*SpeedFile, error) {
	s := &SpeedFile{
		Version:   SPEEDTEST_VERSION,
		Results:   []SpeedResult{},
		Rotations: []RotationEvent{},
	}
	speedFile := GetPath(path)
	data, err := os.ReadFile(speedFile)
	if os.IsNotExist(err) {
		log.Infof("Creating new speedtest results file: %s", speedFile)
	} else if err != nil {
		return s, err
	} else if err = json.Unmarshal(data, s); err != nil {
		return s, err
	}
	s.filename = speedFile
	return s, nil
}

// AddResult appends a measurement.
func (s *SpeedFile) AddResult(r SpeedResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Results = append(s.Results, r)
}

// AddRotation appends a rotation event.
func (s *SpeedFile) AddRotation(e RotationEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Rotations = append(s.Rotations, e)
}

// StageRotation records a rotation that has been requested but not yet carried
// out, so the measurement that motivated it (BeforeMbps, FromExitIP) is kept
// alongside the outcome CompleteRotation fills in later.
func (s *SpeedFile) StageRotation(e RotationEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Rotations = append(s.Rotations, e)
	s.staged = true
}

// CompleteRotation records the outcome of a rotation that just finished. It
// completes the staged event when there is one and appends a new event
// otherwise, which is what puts RotateTime, closed-port and manual rotations
// into the history and the rotations metric rather than only speedtest-driven
// ones.
func (s *SpeedFile) CompleteRotation(source, reason, fromIP, toIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.staged && len(s.Rotations) > 0 {
		last := &s.Rotations[len(s.Rotations)-1]
		if last.FromExitIP == "" {
			last.FromExitIP = fromIP
		}
		last.ToExitIP = toIP
		s.staged = false
		return
	}

	s.Rotations = append(s.Rotations, RotationEvent{
		At:         time.Now(),
		Source:     source,
		Reason:     reason,
		FromExitIP: fromIP,
		ToExitIP:   toIP,
	})
}

// AutomaticRotationsSince counts the rotations the daemon decided on itself.
// Rotations the user asked for explicitly are excluded: MaxRotationsPerDay is a
// budget for automatic churn, and spending it on button clicks would silently
// disable the automatic rotation the setting exists to govern.
func (s *SpeedFile) AutomaticRotationsSince(t time.Time) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, e := range s.Rotations {
		if e.Source != RotationSourceManual && e.At.After(t) {
			n++
		}
	}
	return n
}

// Latest returns the most recent result of any kind, including failures.
func (s *SpeedFile) Latest() (SpeedResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.Results) == 0 {
		return SpeedResult{}, false
	}
	return s.Results[len(s.Results)-1], true
}

// LatestSuccessful returns the most recent result that actually measured
// something. Failed and skipped runs are recorded for visibility but carry a
// zero DownloadMbps, which must never be read as "the link is very slow".
func (s *SpeedFile) LatestSuccessful() (SpeedResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.Results) - 1; i >= 0; i-- {
		if s.Results[i].OK() {
			return s.Results[i], true
		}
	}
	return SpeedResult{}, false
}

// RotationsSince counts every rotation strictly after t, whatever asked for it.
// The daily cap uses AutomaticRotationsSince instead; this is the total.
func (s *SpeedFile) RotationsSince(t time.Time) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, e := range s.Rotations {
		if e.At.After(t) {
			n++
		}
	}
	return n
}

// LastRotation returns the most recent rotation event.
func (s *SpeedFile) LastRotation() (RotationEvent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.Rotations) == 0 {
		return RotationEvent{}, false
	}
	return s.Rotations[len(s.Rotations)-1], true
}

// RecordExitIP backfills the exit IP observed after the most recent rotation.
// Only the first observation counts -- later measurements describe the same
// tunnel, and overwriting would hide a rotation that changed nothing.
func (s *SpeedFile) RecordExitIP(ip string) {
	if ip == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Rotations) == 0 {
		return
	}
	last := &s.Rotations[len(s.Rotations)-1]
	if last.ToExitIP == "" {
		last.ToExitIP = ip
	}
}

// GetResults returns a copy of the results, safe for a handler to render while
// the monitor keeps appending.
func (s *SpeedFile) GetResults() []SpeedResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SpeedResult, len(s.Results))
	copy(out, s.Results)
	return out
}

// GetRotations returns a copy of the rotation events.
func (s *SpeedFile) GetRotations() []RotationEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RotationEvent, len(s.Rotations))
	copy(out, s.Rotations)
	return out
}

// Save prunes entries older than d and writes the file.
func (s *SpeedFile) Save(d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-d)

	results := make([]SpeedResult, 0, len(s.Results))
	for _, r := range s.Results {
		if r.At.After(cutoff) {
			results = append(results, r)
		}
	}
	s.Results = results

	rotations := make([]RotationEvent, 0, len(s.Rotations))
	for _, e := range s.Rotations {
		if e.At.After(cutoff) {
			rotations = append(rotations, e)
		}
	}
	s.Rotations = rotations

	type serialized struct {
		Version   int             `json:"Version"`
		Results   []SpeedResult   `json:"Results"`
		Rotations []RotationEvent `json:"Rotations"`
	}
	data, err := json.MarshalIndent(
		serialized{Version: s.Version, Results: s.Results, Rotations: s.Rotations}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filename, data, 0644) //nolint:gosec
}
