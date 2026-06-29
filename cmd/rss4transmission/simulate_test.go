package main

import (
	"os"
	"path/filepath"
	"testing"
)

// simTestCtx returns a minimal RunContext suitable for dispatchBatch tests
// (no Transmission client needed — only ctx.Cache is accessed).
func simTestCtx() *RunContext {
	return &RunContext{
		Cache: emptyCache(),
	}
}

func simTestCmd(t *testing.T) (*SimulateCmd, string) {
	t.Helper()
	dir := t.TempDir()
	return &SimulateCmd{Dir: dir}, dir
}

// --- sanitizeFilename ---

func TestSanitizeFilename_Clean(t *testing.T) {
	if got := sanitizeFilename("MotoGP.2024.RD01.Race.1080p"); got != "MotoGP.2024.RD01.Race.1080p" {
		t.Errorf("unexpected sanitization: %q", got)
	}
}

func TestSanitizeFilename_SpecialChars(t *testing.T) {
	cases := map[string]string{
		"a/b":  "a_b",
		`a\b`:  "a_b",
		"a:b":  "a_b",
		"a*b":  "a_b",
		"a?b":  "a_b",
		`a"b`:  "a_b",
		"a<b":  "a_b",
		"a>b":  "a_b",
		"a|b":  "a_b",
		"a//b": "a__b",
	}
	for input, want := range cases {
		if got := sanitizeFilename(input); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

// --- splitBatches ---

func TestSplitBatches_Even(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6}
	batches := splitBatches(items, 2)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	for i, b := range batches {
		if len(b) != 2 {
			t.Errorf("batch %d: expected 2 items, got %d", i, len(b))
		}
	}
}

func TestSplitBatches_Remainder(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	batches := splitBatches(items, 2)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	if len(batches[2]) != 1 {
		t.Errorf("last batch: expected 1 item, got %d", len(batches[2]))
	}
}

func TestSplitBatches_LargerThanInput(t *testing.T) {
	items := []int{1, 2, 3}
	batches := splitBatches(items, 10)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 3 {
		t.Errorf("batch: expected 3 items, got %d", len(batches[0]))
	}
}

func TestSplitBatches_Empty(t *testing.T) {
	batches := splitBatches([]int{}, 5)
	if len(batches) != 0 {
		t.Errorf("expected 0 batches for empty input, got %d", len(batches))
	}
}

func TestSplitBatches_ZeroSize(t *testing.T) {
	items := []int{1, 2, 3}
	batches := splitBatches(items, 0)
	// size <= 0 is clamped to 1 → one item per batch
	if len(batches) != 3 {
		t.Errorf("expected 3 batches for size=0, got %d", len(batches))
	}
}

// --- dispatchBatch ---

func testFeedForSim() Feed {
	return makeFeed(
		[]string{"series", "round", "session"},
		[]PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}},
		[]Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	)
}

func TestDispatchBatch_WritesTorrentFile(t *testing.T) {
	cmd, dir := simTestCmd(t)
	ctx := simTestCtx()
	feedCfg := testFeedForSim()

	torrentData := buildSingleFileTorrent("MotoGP.2024.RD01.Race.1080p.mkv")
	c := makeCandidate("guid1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	c.torrentBytes = torrentData

	winners := cmd.dispatchBatch(ctx, feedCfg, []*candidate{c})
	if winners != 1 {
		t.Errorf("expected 1 winner, got %d", winners)
	}

	// torrent file should exist on disk
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in dir, got %d", len(entries))
	}
	if filepath.Ext(entries[0].Name()) != ".torrent" {
		t.Errorf("expected .torrent extension, got %s", entries[0].Name())
	}
}

func TestDispatchBatch_NonWinnerStillWritesFile(t *testing.T) {
	// The non-winner (wrong series) should still have its torrent file written.
	cmd, dir := simTestCmd(t)
	ctx := simTestCtx()
	feedCfg := testFeedForSim()

	winner := makeCandidate("motogp",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	winner.torrentBytes = buildSingleFileTorrent("winner.mkv")

	nonMatch := makeCandidate("worldsbk",
		map[string]string{"series": "WorldSBK", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	nonMatch.torrentBytes = buildSingleFileTorrent("nonmatch.mkv")

	count := cmd.dispatchBatch(ctx, feedCfg, []*candidate{winner, nonMatch})
	if count != 1 {
		t.Errorf("expected 1 winner, got %d", count)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// Both candidates had torrent bytes → both files written
	if len(entries) != 2 {
		t.Errorf("expected 2 torrent files (winner + non-winner), got %d", len(entries))
	}
}

func TestDispatchBatch_NoTorrentBytesSkipsWrite(t *testing.T) {
	cmd, dir := simTestCmd(t)
	ctx := simTestCtx()
	feedCfg := testFeedForSim()

	c := makeCandidate("guid1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	// torrentBytes is nil

	cmd.dispatchBatch(ctx, feedCfg, []*candidate{c})

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected 0 files when no torrent bytes, got %d", len(entries))
	}
}

func TestDispatchBatch_MultiBatch_CacheDedup(t *testing.T) {
	// Batch 1 wins MotoGP RD01 Race at 1080p.
	// Batch 2 offers the same at 720p → should be skipped.
	cmd, _ := simTestCmd(t)
	ctx := simTestCtx()
	feedCfg := testFeedForSim()

	c1 := makeCandidate("batch1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	cmd.dispatchBatch(ctx, feedCfg, []*candidate{c1})

	c2 := makeCandidate("batch2",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "720p"},
		nil,
	)
	won := cmd.dispatchBatch(ctx, feedCfg, []*candidate{c2})
	if won != 0 {
		t.Errorf("expected 0 winners in batch 2 (720p < cached 1080p), got %d", won)
	}
}

func TestDispatchBatch_MultiBatch_BetterPreference(t *testing.T) {
	// Batch 1 wins at 720p. Batch 2 offers 1080p → should win (beats cache).
	cmd, _ := simTestCmd(t)
	ctx := simTestCtx()
	feedCfg := testFeedForSim()

	c1 := makeCandidate("batch1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "720p"},
		nil,
	)
	cmd.dispatchBatch(ctx, feedCfg, []*candidate{c1})

	c2 := makeCandidate("batch2",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	won := cmd.dispatchBatch(ctx, feedCfg, []*candidate{c2})
	if won != 1 {
		t.Errorf("expected 1 winner in batch 2 (1080p > cached 720p), got %d", won)
	}
}

func TestDispatchBatch_FileNotOverwritten(t *testing.T) {
	// If a .torrent file already exists on disk, it should not be overwritten.
	cmd, dir := simTestCmd(t)
	ctx := simTestCtx()
	feedCfg := testFeedForSim()

	filename := sanitizeFilename("guid1") + ".torrent"
	dest := filepath.Join(dir, filename)
	original := []byte("original")
	if err := os.WriteFile(dest, original, 0644); err != nil { //nolint:gosec
		t.Fatalf("setup: %v", err)
	}

	c := makeCandidate("guid1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	c.torrentBytes = buildSingleFileTorrent("different.mkv")

	cmd.dispatchBatch(ctx, feedCfg, []*candidate{c})

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "original" {
		t.Errorf("file was overwritten; got %q, want %q", string(got), "original")
	}
}
