package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
)

func makeGofeedItem(title, guid string) *gofeed.Item {
	return &gofeed.Item{Title: title, GUID: guid}
}

func emptyHistory() *HistoryFile {
	return &HistoryFile{
		Version:   HISTORY_VERSION,
		Records:   []HistoryRecord{},
		guidIndex: map[string]int{},
	}
}

// --- outcomeRank ---

func TestOutcomeRank(t *testing.T) {
	cases := []struct {
		outcome string
		want    int
	}{
		{"dispatched", 0},
		{"downloaded", 0},
		{"skipped", 1},
		{"excluded", 2},
		{"error", 3},
		{"unknown", 3},
		{"", 3},
	}
	for _, tc := range cases {
		if got := outcomeRank(tc.outcome); got != tc.want {
			t.Errorf("outcomeRank(%q) = %d, want %d", tc.outcome, got, tc.want)
		}
	}
}

// --- historyKey ---

func TestHistoryKey_Distinct(t *testing.T) {
	if historyKey("feed1", "guid") == historyKey("feed2", "guid") {
		t.Error("different feeds should produce different keys for the same GUID")
	}
	if historyKey("feed", "guid1") == historyKey("feed", "guid2") {
		t.Error("different GUIDs should produce different keys for the same feed")
	}
}

func TestHistoryKey_NotSymmetric(t *testing.T) {
	if historyKey("a", "b") == historyKey("b", "a") {
		t.Error("feed and GUID positions should not be interchangeable")
	}
}

// --- NewHistoryRecord ---

func TestNewHistoryRecord_Fields(t *testing.T) {
	item := makeGofeedItem("My Show S01E01", "https://example.com/1")
	labels := map[string]string{"resolution": "1080p"}
	rec := NewHistoryRecord("myfeed", item, "dispatched", "reason", labels)

	if rec.Feed != "myfeed" {
		t.Errorf("Feed = %q, want myfeed", rec.Feed)
	}
	if rec.Title != "My Show S01E01" {
		t.Errorf("Title = %q, want My Show S01E01", rec.Title)
	}
	if rec.GUID != "https://example.com/1" {
		t.Errorf("GUID = %q, want https://example.com/1", rec.GUID)
	}
	if rec.Outcome != "dispatched" {
		t.Errorf("Outcome = %q, want dispatched", rec.Outcome)
	}
	if rec.Reason != "reason" {
		t.Errorf("Reason = %q, want reason", rec.Reason)
	}
	if rec.Labels["resolution"] != "1080p" {
		t.Errorf("Labels[resolution] = %q, want 1080p", rec.Labels["resolution"])
	}
}

func TestNewHistoryRecord_NilPublished(t *testing.T) {
	item := makeGofeedItem("title", "guid")
	rec := NewHistoryRecord("feed", item, "excluded", "", nil)
	if !rec.Published.IsZero() {
		t.Error("Published should be zero when item has no PublishedParsed")
	}
}

func TestNewHistoryRecord_WithPublished(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	item := makeGofeedItem("title", "guid")
	item.PublishedParsed = &now
	rec := NewHistoryRecord("feed", item, "excluded", "", nil)
	if !rec.Published.Equal(now) {
		t.Errorf("Published = %v, want %v", rec.Published, now)
	}
}

// --- AddOrUpdateRecord ---

func TestAddOrUpdateRecord_NewGUID(t *testing.T) {
	h := emptyHistory()
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "dispatched", "", nil))

	if len(h.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(h.Records))
	}
	if h.Records[0].Outcome != "dispatched" {
		t.Errorf("Outcome = %q, want dispatched", h.Records[0].Outcome)
	}
	if h.Records[0].ProcessedAt.IsZero() {
		t.Error("ProcessedAt should be set by AddOrUpdateRecord")
	}
}

func TestAddOrUpdateRecord_SameOutcome_NoUpdate(t *testing.T) {
	h := emptyHistory()
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "excluded", "reason1", nil))
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "excluded", "reason2", nil))

	if len(h.Records) != 1 {
		t.Fatalf("expected 1 record (no duplicate on equal outcome), got %d", len(h.Records))
	}
	if h.Records[0].Reason != "reason1" {
		t.Errorf("Reason = %q, want reason1 (first write wins for equal outcome)", h.Records[0].Reason)
	}
}

func TestAddOrUpdateRecord_BetterOutcome_Updates(t *testing.T) {
	h := emptyHistory()
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "excluded", "matched filter", nil))
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "dispatched", "", nil))

	if len(h.Records) != 1 {
		t.Fatalf("expected 1 record after upgrade, got %d", len(h.Records))
	}
	if h.Records[0].Outcome != "dispatched" {
		t.Errorf("Outcome = %q, want dispatched (should upgrade from excluded)", h.Records[0].Outcome)
	}
}

func TestAddOrUpdateRecord_WorseOutcome_NoDowngrade(t *testing.T) {
	h := emptyHistory()
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "dispatched", "", nil))
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "excluded", "matched", nil))

	if len(h.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(h.Records))
	}
	if h.Records[0].Outcome != "dispatched" {
		t.Errorf("Outcome = %q, want dispatched (should not downgrade)", h.Records[0].Outcome)
	}
}

func TestAddOrUpdateRecord_SkippedToDispatched(t *testing.T) {
	h := emptyHistory()
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "skipped", "outranked", nil))
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "dispatched", "", nil))

	if h.Records[0].Outcome != "dispatched" {
		t.Errorf("Outcome = %q, want dispatched (skipped should upgrade to dispatched)", h.Records[0].Outcome)
	}
}

func TestAddOrUpdateRecord_DifferentFeed_SameGUID(t *testing.T) {
	h := emptyHistory()
	item := makeGofeedItem("Show", "guid1")
	h.AddOrUpdateRecord(NewHistoryRecord("feed1", item, "excluded", "", nil))
	h.AddOrUpdateRecord(NewHistoryRecord("feed2", item, "excluded", "", nil))

	if len(h.Records) != 2 {
		t.Fatalf("expected 2 records (different feeds with same GUID), got %d", len(h.Records))
	}
}

func TestAddOrUpdateRecord_MultipleGUIDs(t *testing.T) {
	h := emptyHistory()
	for i := 0; i < 5; i++ {
		item := makeGofeedItem("Show", fmt.Sprintf("guid%d", i))
		h.AddOrUpdateRecord(NewHistoryRecord("feed", item, "excluded", "", nil))
	}
	if len(h.Records) != 5 {
		t.Errorf("expected 5 records, got %d", len(h.Records))
	}
	if len(h.guidIndex) != 5 {
		t.Errorf("expected 5 GUID index entries, got %d", len(h.guidIndex))
	}
}

// --- GetRecords ---

func TestGetRecords_ReturnsCopy(t *testing.T) {
	h := emptyHistory()
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("title", "guid1"), "dispatched", "", nil))

	records := h.GetRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	records[0].Outcome = "mutated"
	if h.Records[0].Outcome != "dispatched" {
		t.Error("GetRecords should return a copy — mutating it affected the original")
	}
}

func TestGetRecords_Empty(t *testing.T) {
	h := emptyHistory()
	records := h.GetRecords()
	if records == nil {
		t.Error("GetRecords should return a non-nil slice for empty history")
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

// --- SaveHistory / OpenHistory ---

func TestSaveHistory_PrunesOldRecords(t *testing.T) {
	dir := t.TempDir()
	h := &HistoryFile{
		Version:   HISTORY_VERSION,
		filename:  filepath.Join(dir, "history.json"),
		guidIndex: map[string]int{},
	}

	now := time.Now()
	old := now.Add(-48 * time.Hour)

	oldItem := makeGofeedItem("Old Show", "old-guid")
	h.AddOrUpdateRecord(NewHistoryRecord("feed", oldItem, "dispatched", "", nil))
	h.Records[0].ProcessedAt = old // backdate to simulate an old record

	newItem := makeGofeedItem("New Show", "new-guid")
	newItem.PublishedParsed = &now
	h.AddOrUpdateRecord(NewHistoryRecord("feed", newItem, "dispatched", "", nil))

	if err := h.SaveHistory(24 * time.Hour); err != nil {
		t.Fatalf("SaveHistory failed: %v", err)
	}

	if len(h.Records) != 1 {
		t.Fatalf("expected 1 record after pruning, got %d", len(h.Records))
	}
	if h.Records[0].GUID != "new-guid" {
		t.Errorf("expected new-guid to survive pruning, got %s", h.Records[0].GUID)
	}
	if _, ok := h.guidIndex[historyKey("feed", "new-guid")]; !ok {
		t.Error("GUID index should be rebuilt after pruning")
	}
	if _, ok := h.guidIndex[historyKey("feed", "old-guid")]; ok {
		t.Error("pruned GUID should be removed from index")
	}
}

func TestSaveHistory_OldPublishedRecentProcessedAt_Kept(t *testing.T) {
	// A record with an old Published date but recent ProcessedAt must NOT be pruned.
	dir := t.TempDir()
	h := &HistoryFile{
		Version:   HISTORY_VERSION,
		filename:  filepath.Join(dir, "history.json"),
		guidIndex: map[string]int{},
	}

	old := time.Now().Add(-365 * 24 * time.Hour)
	item := makeGofeedItem("Old Show", "old-published-guid")
	item.PublishedParsed = &old
	h.AddOrUpdateRecord(NewHistoryRecord("feed", item, "dispatched", "", nil))
	// ProcessedAt is set to time.Now() by AddOrUpdateRecord.

	if err := h.SaveHistory(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveHistory failed: %v", err)
	}
	if len(h.Records) != 1 {
		t.Errorf("expected record kept (recent ProcessedAt), got %d records", len(h.Records))
	}
}

func TestSaveHistory_OldProcessedAt_Pruned(t *testing.T) {
	// A record with an old ProcessedAt must be pruned even if Published is recent.
	dir := t.TempDir()
	h := &HistoryFile{
		Version:   HISTORY_VERSION,
		filename:  filepath.Join(dir, "history.json"),
		guidIndex: map[string]int{},
	}

	now := time.Now()
	item := makeGofeedItem("Show", "recent-published-guid")
	item.PublishedParsed = &now
	h.AddOrUpdateRecord(NewHistoryRecord("feed", item, "dispatched", "", nil))
	// Backdate ProcessedAt to simulate an old record.
	h.Records[0].ProcessedAt = time.Now().Add(-365 * 24 * time.Hour)

	if err := h.SaveHistory(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveHistory failed: %v", err)
	}
	if len(h.Records) != 0 {
		t.Errorf("expected record pruned (old ProcessedAt), got %d records", len(h.Records))
	}
}

func TestSaveHistory_KeepsAllWhenWithinWindow(t *testing.T) {
	dir := t.TempDir()
	h := &HistoryFile{
		Version:   HISTORY_VERSION,
		filename:  filepath.Join(dir, "history.json"),
		guidIndex: map[string]int{},
	}

	now := time.Now()
	for i := 0; i < 3; i++ {
		item := makeGofeedItem("Show", fmt.Sprintf("guid%d", i))
		item.PublishedParsed = &now
		h.AddOrUpdateRecord(NewHistoryRecord("feed", item, "dispatched", "", nil))
	}

	if err := h.SaveHistory(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveHistory failed: %v", err)
	}
	if len(h.Records) != 3 {
		t.Errorf("expected 3 records (all within window), got %d", len(h.Records))
	}
}

func TestSaveHistory_WritesValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	h := &HistoryFile{
		Version:   HISTORY_VERSION,
		filename:  path,
		guidIndex: map[string]int{},
	}

	now := time.Now()
	item := makeGofeedItem("Show S01E01", "guid1")
	item.PublishedParsed = &now
	h.AddOrUpdateRecord(NewHistoryRecord("feed", item, "dispatched", "", map[string]string{"res": "1080p"}))

	if err := h.SaveHistory(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveHistory failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}

	var raw struct {
		Version int             `json:"Version"`
		Records []HistoryRecord `json:"Records"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
	if raw.Version != HISTORY_VERSION {
		t.Errorf("Version = %d, want %d", raw.Version, HISTORY_VERSION)
	}
	if len(raw.Records) != 1 {
		t.Fatalf("expected 1 record in file, got %d", len(raw.Records))
	}
	if raw.Records[0].GUID != "guid1" {
		t.Errorf("GUID = %q, want guid1", raw.Records[0].GUID)
	}
}

func TestOpenHistory_NonNotExistError(t *testing.T) {
	// Passing a directory as the path gives "is a directory" — not os.IsNotExist.
	// OpenHistory must propagate that error rather than silently creating a new file.
	dir := t.TempDir()
	_, err := OpenHistory(dir)
	if err == nil {
		t.Error("OpenHistory should return error for non-file path (e.g. a directory)")
	}
}

func TestOpenHistory_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")

	h, err := OpenHistory(path)
	if err != nil {
		t.Fatalf("OpenHistory failed for new file: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil HistoryFile")
	}
	if len(h.Records) != 0 {
		t.Errorf("expected 0 records for new file, got %d", len(h.Records))
	}
	if h.guidIndex == nil {
		t.Error("guidIndex should be initialized after OpenHistory")
	}
}

func TestOpenHistory_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")

	h1 := &HistoryFile{
		Version:   HISTORY_VERSION,
		filename:  path,
		guidIndex: map[string]int{},
	}
	now := time.Now()
	item := makeGofeedItem("Show", "guid1")
	item.PublishedParsed = &now
	h1.AddOrUpdateRecord(NewHistoryRecord("feed", item, "dispatched", "", nil))
	if err := h1.SaveHistory(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveHistory failed: %v", err)
	}

	h2, err := OpenHistory(path)
	if err != nil {
		t.Fatalf("OpenHistory failed: %v", err)
	}
	if len(h2.Records) != 1 {
		t.Fatalf("expected 1 record after reload, got %d", len(h2.Records))
	}
	if h2.Records[0].GUID != "guid1" {
		t.Errorf("GUID = %q, want guid1", h2.Records[0].GUID)
	}
	if _, ok := h2.guidIndex[historyKey("feed", "guid1")]; !ok {
		t.Error("GUID index should be rebuilt after OpenHistory")
	}
}

func TestOpenHistory_UsesProcessedAtWhenPublishedZero(t *testing.T) {
	dir := t.TempDir()
	h := &HistoryFile{
		Version:   HISTORY_VERSION,
		filename:  filepath.Join(dir, "history.json"),
		guidIndex: map[string]int{},
	}
	// Item with no PublishedParsed — ProcessedAt is used for pruning
	h.AddOrUpdateRecord(NewHistoryRecord("feed", makeGofeedItem("Show", "guid1"), "dispatched", "", nil))

	// Should survive 30-day window (ProcessedAt is just set to now)
	if err := h.SaveHistory(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveHistory failed: %v", err)
	}
	if len(h.Records) != 1 {
		t.Errorf("expected record to survive when using ProcessedAt, got %d records", len(h.Records))
	}
}
