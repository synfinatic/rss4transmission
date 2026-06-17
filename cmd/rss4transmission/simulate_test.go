package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
)

// --- helpers ---

func writeRSSFile(t *testing.T, items []struct{ title, guid string }) string {
	t.Helper()
	type enclosure struct {
		URL    string `xml:"url,attr"`
		Length string `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	}
	type rssItem struct {
		Title     string    `xml:"title"`
		GUID      string    `xml:"guid"`
		PubDate   string    `xml:"pubDate"`
		Enclosure enclosure `xml:"enclosure"`
	}
	type channel struct {
		XMLName xml.Name  `xml:"channel"`
		Items   []rssItem `xml:"item"`
	}
	type rss struct {
		XMLName xml.Name `xml:"rss"`
		Channel channel  `xml:"channel"`
	}

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ch := channel{}
	for i, it := range items {
		ch.Items = append(ch.Items, rssItem{
			Title:   it.title,
			GUID:    it.guid,
			PubDate: base.Add(time.Duration(i) * time.Hour).Format(time.RFC1123Z),
			Enclosure: enclosure{
				URL:    "https://example.com/" + it.guid + ".torrent",
				Length: "1000000",
				Type:   "application/x-bittorrent",
			},
		})
	}

	b, err := xml.MarshalIndent(rss{Channel: ch}, "", "  ")
	if err != nil {
		t.Fatalf("marshal RSS: %v", err)
	}
	// gofeed needs the <?xml?> and rss version attr
	content := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + strings.Replace(string(b), "<rss>", `<rss version="2.0">`, 1)

	f, err := os.CreateTemp(t.TempDir(), "*.rss")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer func() {
		_ = f.Close()
	}()
	if _, err = f.WriteString(content); err != nil {
		t.Fatalf("write rss file: %v", err)
	}
	return f.Name()
}

func makeSimulateCtx(t *testing.T, feeds map[string]Feed, xmlPath string, newItem int, feedFilter []string) (*RunContext, *SimulateCmd) {
	t.Helper()
	cache := &CacheFile{
		Version:        CACHE_VERSION,
		Errors:         map[string]int64{},
		Seen:           []CacheRecord{},
		NormalizeCache: map[string]NormalizedTorrent{},
	}
	cmd := &SimulateCmd{
		File:    xmlPath,
		Feed:    feedFilter,
		NewItem: newItem,
	}
	cli := &CLI{Simulate: *cmd}
	rc := &RunContext{
		Cli: cli,
		Config: Config{
			Feeds: feeds,
		},
		Cache: cache,
	}
	return rc, cmd
}

// --- Phase A: sortItemsByDate ---

func TestSortItemsByDate_Sorted(t *testing.T) {
	now := time.Now()
	items := []*gofeed.Item{
		{Title: "newest", PublishedParsed: func() *time.Time { t := now.Add(3 * time.Hour); return &t }()},
		{Title: "oldest", PublishedParsed: func() *time.Time { t := now.Add(1 * time.Hour); return &t }()},
		{Title: "middle", PublishedParsed: func() *time.Time { t := now.Add(2 * time.Hour); return &t }()},
	}
	sorted := sortItemsByDate(items)
	if sorted[0].Title != "oldest" {
		t.Errorf("[0] = %q, want oldest", sorted[0].Title)
	}
	if sorted[1].Title != "middle" {
		t.Errorf("[1] = %q, want middle", sorted[1].Title)
	}
	if sorted[2].Title != "newest" {
		t.Errorf("[2] = %q, want newest", sorted[2].Title)
	}
}

func TestSortItemsByDate_NilDate(t *testing.T) {
	now := time.Now()
	items := []*gofeed.Item{
		{Title: "has-date", PublishedParsed: &now},
		{Title: "no-date", PublishedParsed: nil},
	}
	sorted := sortItemsByDate(items)
	if sorted[0].Title != "no-date" {
		t.Errorf("[0] = %q, want no-date (nil sorts first)", sorted[0].Title)
	}
}

func TestSortItemsByDate_NoMutation(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)
	original := []*gofeed.Item{
		{Title: "b", PublishedParsed: &later},
		{Title: "a", PublishedParsed: &now},
	}
	_ = sortItemsByDate(original)
	if original[0].Title != "b" {
		t.Error("sortItemsByDate must not mutate the input slice")
	}
}

// --- Phase B: splitBatches ---

func TestSplitBatches_ZeroAllAtOnce(t *testing.T) {
	items := make([]*gofeed.Item, 5)
	batches := splitBatches(items, 0)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 5 {
		t.Errorf("batch[0] len = %d, want 5", len(batches[0]))
	}
}

func TestSplitBatches_NegativeAllAtOnce(t *testing.T) {
	items := make([]*gofeed.Item, 3)
	batches := splitBatches(items, -1)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
}

func TestSplitBatches_EvenDivision(t *testing.T) {
	items := make([]*gofeed.Item, 6)
	batches := splitBatches(items, 3)
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}
	for i, b := range batches {
		if len(b) != 3 {
			t.Errorf("batch[%d] len = %d, want 3", i, len(b))
		}
	}
}

func TestSplitBatches_UnevenDivision(t *testing.T) {
	items := make([]*gofeed.Item, 7)
	batches := splitBatches(items, 3)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	if len(batches[2]) != 1 {
		t.Errorf("last batch len = %d, want 1", len(batches[2]))
	}
}

func TestSplitBatches_LargerThanItems(t *testing.T) {
	items := make([]*gofeed.Item, 3)
	batches := splitBatches(items, 10)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 3 {
		t.Errorf("batch[0] len = %d, want 3", len(batches[0]))
	}
}

func TestSplitBatches_Empty(t *testing.T) {
	batches := splitBatches([]*gofeed.Item{}, 5)
	if len(batches) != 0 {
		t.Errorf("expected 0 batches for empty input, got %d", len(batches))
	}
}

// --- Phase C: inFeedFilter ---

func TestInFeedFilter_EmptyMeansAll(t *testing.T) {
	cmd := &SimulateCmd{}
	for _, name := range []string{"foo", "bar", "anything"} {
		if !cmd.inFeedFilter(name) {
			t.Errorf("inFeedFilter(%q) = false, want true when Feed is empty", name)
		}
	}
}

func TestInFeedFilter_MatchSpecific(t *testing.T) {
	cmd := &SimulateCmd{Feed: []string{"foo", "baz"}}
	if !cmd.inFeedFilter("foo") {
		t.Error("inFeedFilter(foo) = false, want true")
	}
	if !cmd.inFeedFilter("baz") {
		t.Error("inFeedFilter(baz) = false, want true")
	}
	if cmd.inFeedFilter("bar") {
		t.Error("inFeedFilter(bar) = true, want false")
	}
}

// --- Phase D: SimulateCmd.Run() integration ---

func TestSimulateRun_RegexpFeed_Reports(t *testing.T) {
	xmlPath := writeRSSFile(t, []struct{ title, guid string }{
		{"MyShow.S01E01.720p", "guid-01"},
		{"OtherShow.S01E01.720p", "guid-02"},
		{"MyShow.S01E02.720p", "guid-03"},
	})

	feeds := map[string]Feed{
		"myshow": {Regexp: []string{`(?i)myshow`}},
	}
	rc, cmd := makeSimulateCtx(t, feeds, xmlPath, 0, nil)

	if err := cmd.Run(rc); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// MyShow items should be in cache; OtherShow should not
	if !rc.Cache.Exists("myshow", &FeedItem{Feed: "myshow", Item: &gofeed.Item{GUID: "guid-01"}}) {
		t.Error("guid-01 (MyShow S01E01) should be in cache after simulate")
	}
	if !rc.Cache.Exists("myshow", &FeedItem{Feed: "myshow", Item: &gofeed.Item{GUID: "guid-03"}}) {
		t.Error("guid-03 (MyShow S01E02) should be in cache after simulate")
	}
	if rc.Cache.Exists("myshow", &FeedItem{Feed: "myshow", Item: &gofeed.Item{GUID: "guid-02"}}) {
		t.Error("guid-02 (OtherShow) should NOT be in cache after simulate")
	}
}

func TestSimulateRun_CacheDedupsAcrossBatches(t *testing.T) {
	// 4 items, batch size 2 → 2 batches
	// item guid-01 matches; after batch 1 it's in cache; batch 2 must not re-add it
	xmlPath := writeRSSFile(t, []struct{ title, guid string }{
		{"MyShow.S01E01.720p", "guid-01"},
		{"MyShow.S01E02.720p", "guid-02"},
		{"MyShow.S01E03.720p", "guid-03"},
		{"MyShow.S01E04.720p", "guid-04"},
	})

	feeds := map[string]Feed{
		"myshow": {Regexp: []string{`(?i)myshow`}},
	}
	rc, cmd := makeSimulateCtx(t, feeds, xmlPath, 2, nil)

	if err := cmd.Run(rc); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// All 4 items should be cached exactly once
	seen := 0
	for _, rec := range rc.Cache.Seen {
		if rec.Feed == "myshow" {
			seen++
		}
	}
	if seen != 4 {
		t.Errorf("expected 4 cached items, got %d", seen)
	}
}

func TestSimulateRun_DoesNotSaveCache(t *testing.T) {
	xmlPath := writeRSSFile(t, []struct{ title, guid string }{
		{"MyShow.S01E01.720p", "guid-01"},
	})

	feeds := map[string]Feed{
		"myshow": {Regexp: []string{`(?i)myshow`}},
	}
	rc, cmd := makeSimulateCtx(t, feeds, xmlPath, 0, nil)

	// Set the cache filename to a temp path and note the initial state
	cacheFile := fmt.Sprintf("%s/seen.json", t.TempDir())
	rc.Cache.filename = cacheFile

	if err := cmd.Run(rc); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The cache file should NOT have been written
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Error("simulate should not write the cache file to disk")
	}
}

func TestSimulateRun_FeedFilter_LimitsProcessing(t *testing.T) {
	xmlPath := writeRSSFile(t, []struct{ title, guid string }{
		{"MyShow.S01E01.720p", "guid-01"},
	})

	feeds := map[string]Feed{
		"myshow":    {Regexp: []string{`(?i)myshow`}},
		"othershow": {Regexp: []string{`(?i)othershow`}},
	}
	// Only process "myshow" feed
	rc, cmd := makeSimulateCtx(t, feeds, xmlPath, 0, []string{"myshow"})

	if err := cmd.Run(rc); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// guid-01 should be cached under "myshow" only
	if !rc.Cache.Exists("myshow", &FeedItem{Feed: "myshow", Item: &gofeed.Item{GUID: "guid-01"}}) {
		t.Error("expected guid-01 cached under myshow")
	}
	// "othershow" feed should never have been consulted — its cache entry should not exist
	if rc.Cache.Exists("othershow", &FeedItem{Feed: "othershow", Item: &gofeed.Item{GUID: "guid-01"}}) {
		t.Error("othershow feed should be skipped by feed filter")
	}
}

func TestSimulateRun_BatchOrder_OldestFirst(t *testing.T) {
	// 2 items with explicit dates; they are written in newest-first order in the XML helper
	// but sortItemsByDate should reverse them so oldest is processed first
	now := time.Now()
	older := now.Add(-2 * time.Hour)
	newer := now.Add(-1 * time.Hour)

	// Build the feed manually to control ordering
	feed := &gofeed.Feed{
		Items: []*gofeed.Item{
			// newest first (as RSS feeds normally deliver)
			{Title: "newer-item", GUID: "guid-newer", PublishedParsed: &newer,
				Enclosures: []*gofeed.Enclosure{{URL: "https://x.com/newer.torrent", Type: "application/x-bittorrent"}}},
			{Title: "older-item", GUID: "guid-older", PublishedParsed: &older,
				Enclosures: []*gofeed.Enclosure{{URL: "https://x.com/older.torrent", Type: "application/x-bittorrent"}}},
		},
	}

	sorted := sortItemsByDate(feed.Items)
	if sorted[0].GUID != "guid-older" {
		t.Errorf("first sorted item = %q, want guid-older", sorted[0].GUID)
	}
	if sorted[1].GUID != "guid-newer" {
		t.Errorf("second sorted item = %q, want guid-newer", sorted[1].GUID)
	}
}

func TestSimulateRun_AIFeed_WithMockNormalizer(t *testing.T) {
	xmlPath := writeRSSFile(t, []struct{ title, guid string }{
		{"MotoGP.2026.Round08.Hungary.Race.TNT.720p.X264.English-VNL", "guid-race"},
		{"MotoGP.2026.Round08.Hungary.Warm.Up.TNT.720p.X264.English-VNL", "guid-warmup"},
	})

	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "720p", "English", 2026, 8)
	normWarmup := makeNorm("MotoGP", "warm_up", "TNT Sports", "720p", "English", 2026, 8)
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		"MotoGP.2026.Round08.Hungary.Race.TNT.720p.X264.English-VNL":    norm,
		"MotoGP.2026.Round08.Hungary.Warm.Up.TNT.720p.X264.English-VNL": normWarmup,
	})

	feeds := map[string]Feed{
		"motogp": {
			AISelection: &AISelection{
				Series:   []string{"MotoGP"},
				Sessions: []string{"gp_race"}, // only race, not warm_up
			},
		},
	}
	rc, cmd := makeSimulateCtx(t, feeds, xmlPath, 0, nil)
	rc.Normalizer = mock

	if err := cmd.Run(rc); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !rc.Cache.Exists("motogp", &FeedItem{Feed: "motogp", Item: &gofeed.Item{GUID: "guid-race"}}) {
		t.Error("gp_race item should be in cache")
	}
	if rc.Cache.Exists("motogp", &FeedItem{Feed: "motogp", Item: &gofeed.Item{GUID: "guid-warmup"}}) {
		t.Error("warm_up item should not be in cache (session filtered out)")
	}
}
