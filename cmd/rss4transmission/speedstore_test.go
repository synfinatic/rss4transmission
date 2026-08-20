package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func tempSpeedFile(t *testing.T) *SpeedFile {
	t.Helper()
	s, err := OpenSpeedFile(filepath.Join(t.TempDir(), "speedtest.json"))
	if err != nil {
		t.Fatalf("OpenSpeedFile returned error: %v", err)
	}
	return s
}

// A missing file is the normal first-run case, not an error.
func TestOpenSpeedFile_MissingFileIsNotAnError(t *testing.T) {
	s := tempSpeedFile(t)
	if s.Version != SPEEDTEST_VERSION {
		t.Errorf("Version = %d, want %d", s.Version, SPEEDTEST_VERSION)
	}
	if len(s.Results) != 0 {
		t.Errorf("Results = %d entries, want 0", len(s.Results))
	}
}

func TestSpeedFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "speedtest.json")

	s, err := OpenSpeedFile(path)
	if err != nil {
		t.Fatalf("OpenSpeedFile: %v", err)
	}
	s.AddResult(SpeedResult{
		At: time.Now(), DownloadMbps: 412.5, UploadMbps: 38.1,
		LatencyMs: 14, ExitIP: "185.1.2.3", ServerName: "Los Angeles",
	})
	s.AddRotation(RotationEvent{
		At: time.Now(), Reason: "slow", BeforeMbps: 12.5, FromExitIP: "185.1.2.3",
	})
	if err := s.Save(30 * 24 * time.Hour); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := OpenSpeedFile(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(reopened.Results))
	}
	if reopened.Results[0].DownloadMbps != 412.5 {
		t.Errorf("DownloadMbps = %v, want 412.5", reopened.Results[0].DownloadMbps)
	}
	if reopened.Results[0].ExitIP != "185.1.2.3" {
		t.Errorf("ExitIP = %q, want 185.1.2.3", reopened.Results[0].ExitIP)
	}
	if len(reopened.Rotations) != 1 {
		t.Fatalf("Rotations = %d, want 1", len(reopened.Rotations))
	}
	if reopened.Rotations[0].Reason != "slow" {
		t.Errorf("Reason = %q, want slow", reopened.Rotations[0].Reason)
	}
}

func TestSpeedFile_SavePrunesOldEntries(t *testing.T) {
	s := tempSpeedFile(t)
	now := time.Now()

	s.AddResult(SpeedResult{At: now.Add(-48 * time.Hour), DownloadMbps: 1})
	s.AddResult(SpeedResult{At: now.Add(-1 * time.Hour), DownloadMbps: 2})
	s.AddRotation(RotationEvent{At: now.Add(-48 * time.Hour), Reason: "old"})
	s.AddRotation(RotationEvent{At: now.Add(-1 * time.Hour), Reason: "recent"})

	if err := s.Save(24 * time.Hour); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if len(s.Results) != 1 || s.Results[0].DownloadMbps != 2 {
		t.Errorf("Results = %+v, want only the 1h-old entry", s.Results)
	}
	if len(s.Rotations) != 1 || s.Rotations[0].Reason != "recent" {
		t.Errorf("Rotations = %+v, want only the recent entry", s.Rotations)
	}
}

func TestSpeedFile_Latest(t *testing.T) {
	s := tempSpeedFile(t)

	if _, ok := s.Latest(); ok {
		t.Error("Latest() ok = true on an empty store, want false")
	}

	s.AddResult(SpeedResult{At: time.Now().Add(-time.Hour), DownloadMbps: 100})
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 200})

	got, ok := s.Latest()
	if !ok {
		t.Fatal("Latest() ok = false, want true")
	}
	if got.DownloadMbps != 200 {
		t.Errorf("Latest().DownloadMbps = %v, want 200", got.DownloadMbps)
	}
}

// A failed test still gets recorded, but must not be mistaken for a real
// measurement of zero -- that would trigger a rotation on every outage.
func TestSpeedFile_LatestSuccessfulSkipsErrors(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now().Add(-time.Hour), DownloadMbps: 300, ExitIP: "1.1.1.1"})
	s.AddResult(SpeedResult{At: time.Now(), Error: "proxy refused"})

	got, ok := s.LatestSuccessful()
	if !ok {
		t.Fatal("LatestSuccessful() ok = false, want true")
	}
	if got.DownloadMbps != 300 {
		t.Errorf("DownloadMbps = %v, want 300", got.DownloadMbps)
	}
}

func TestSpeedFile_RotationsSince(t *testing.T) {
	s := tempSpeedFile(t)
	now := time.Now()

	s.AddRotation(RotationEvent{At: now.Add(-30 * time.Hour)})
	s.AddRotation(RotationEvent{At: now.Add(-10 * time.Hour)})
	s.AddRotation(RotationEvent{At: now.Add(-1 * time.Hour)})

	if got := s.RotationsSince(now.Add(-24 * time.Hour)); got != 2 {
		t.Errorf("RotationsSince(24h ago) = %d, want 2", got)
	}
	if got := s.RotationsSince(now.Add(-48 * time.Hour)); got != 3 {
		t.Errorf("RotationsSince(48h ago) = %d, want 3", got)
	}
	if got := s.RotationsSince(now); got != 0 {
		t.Errorf("RotationsSince(now) = %d, want 0", got)
	}
}

// Exactly-at-the-boundary is the case a daily cap hits in practice.
func TestSpeedFile_RotationsSinceBoundaryIsExclusive(t *testing.T) {
	s := tempSpeedFile(t)
	cutoff := time.Now().Add(-24 * time.Hour)
	s.AddRotation(RotationEvent{At: cutoff})

	if got := s.RotationsSince(cutoff); got != 0 {
		t.Errorf("RotationsSince(cutoff) with event exactly at cutoff = %d, want 0", got)
	}
}

func TestSpeedFile_LastRotation(t *testing.T) {
	s := tempSpeedFile(t)

	if _, ok := s.LastRotation(); ok {
		t.Error("LastRotation() ok = true on empty store, want false")
	}

	s.AddRotation(RotationEvent{At: time.Now().Add(-time.Hour), Reason: "old"})
	s.AddRotation(RotationEvent{At: time.Now(), Reason: "new"})

	got, ok := s.LastRotation()
	if !ok {
		t.Fatal("LastRotation() ok = false, want true")
	}
	if got.Reason != "new" {
		t.Errorf("Reason = %q, want new", got.Reason)
	}
}

// ToExitIP is backfilled by the next successful measurement, which is how we
// detect that a rotation landed back on the same server.
func TestSpeedFile_RecordExitIPBackfillsLastRotation(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{At: time.Now(), Reason: "slow", FromExitIP: "1.1.1.1"})

	s.RecordExitIP("2.2.2.2")

	got, _ := s.LastRotation()
	if got.ToExitIP != "2.2.2.2" {
		t.Errorf("ToExitIP = %q, want 2.2.2.2", got.ToExitIP)
	}
}

// Backfill happens once: a later measurement must not overwrite the exit IP
// that was observed immediately after the rotation.
func TestSpeedFile_RecordExitIPOnlyBackfillsOnce(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddRotation(RotationEvent{At: time.Now(), Reason: "slow", FromExitIP: "1.1.1.1"})

	s.RecordExitIP("2.2.2.2")
	s.RecordExitIP("3.3.3.3")

	got, _ := s.LastRotation()
	if got.ToExitIP != "2.2.2.2" {
		t.Errorf("ToExitIP = %q, want 2.2.2.2 (first observation wins)", got.ToExitIP)
	}
}

func TestSpeedFile_RecordExitIPNoRotationsIsSafe(t *testing.T) {
	s := tempSpeedFile(t)
	s.RecordExitIP("2.2.2.2") // must not panic
}

// The SpeedMonitor goroutine writes while web handlers read; unlike CacheFile
// there is no external lock serializing them, so SpeedFile must be self-synchronized.
func TestSpeedFile_ConcurrentAccess(t *testing.T) {
	s := tempSpeedFile(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 1})
				s.AddRotation(RotationEvent{At: time.Now()})
				_, _ = s.Latest()
				_, _ = s.LastRotation()
				_ = s.RotationsSince(time.Now().Add(-time.Hour))
				_ = s.GetResults()
				s.RecordExitIP("1.1.1.1")
			}
		}()
	}
	wg.Wait()
}

// GetResults must hand back a copy; callers are HTTP handlers rendering a page
// while the monitor keeps appending.
func TestSpeedFile_GetResultsReturnsCopy(t *testing.T) {
	s := tempSpeedFile(t)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 100})

	got := s.GetResults()
	got[0].DownloadMbps = 999

	again := s.GetResults()
	if again[0].DownloadMbps != 100 {
		t.Errorf("DownloadMbps = %v, want 100; GetResults leaked the backing array",
			again[0].DownloadMbps)
	}
}

func TestSpeedFile_SaveWritesReadableJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "speedtest.json")
	s, _ := OpenSpeedFile(path)
	s.AddResult(SpeedResult{At: time.Now(), DownloadMbps: 1})

	if err := s.Save(time.Hour); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() == 0 {
		t.Error("saved file is empty")
	}
}
