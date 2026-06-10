package main

import (
	"regexp"
	"testing"
	"time"
)

// --- Phase A: resolution ordering ---

func TestResolutionOrder(t *testing.T) {
	if resolutionRank("1080p") <= resolutionRank("720p") {
		t.Errorf("expected 1080p rank > 720p rank")
	}
	if resolutionRank("2160p") <= resolutionRank("1080p") {
		t.Errorf("expected 2160p rank > 1080p rank")
	}
	if resolutionRank("720p") <= resolutionRank("540p") {
		t.Errorf("expected 720p rank > 540p rank")
	}
	if resolutionRank("unknown") != 0 {
		t.Errorf("expected unknown resolution rank to be 0")
	}
}

// --- Phase D: seen-index normalized key methods ---

func makeNormKey(series, session, sourceFeed string, year, round int) *NormalizedKey {
	return &NormalizedKey{
		Series:     series,
		Year:       year,
		Round:      round,
		Session:    session,
		SourceFeed: sourceFeed,
	}
}

func makeTestCache() *CacheFile {
	return &CacheFile{
		Version:        CACHE_VERSION,
		Errors:         map[string]int64{},
		Seen:           []CacheRecord{},
		NormalizeCache: map[string]NormalizedTorrent{},
	}
}

func addNormRecord(cache *CacheFile, series, session, sourceFeed string, year, round int) {
	cache.Seen = append(cache.Seen, CacheRecord{
		Feed:       "TestFeed",
		Published:  time.Now(),
		AddTime:    time.Now(),
		GUID:       series + session + sourceFeed,
		Normalized: makeNormKey(series, session, sourceFeed, year, round),
	})
}

func TestCacheExistsByKey_Hit(t *testing.T) {
	cache := makeTestCache()
	addNormRecord(cache, "MotoGP", "gp_race", "TNT Sports", 2026, 8)

	if !cache.ExistsByKey("MotoGP", 2026, 8, "gp_race") {
		t.Error("ExistsByKey should return true for existing record")
	}
}

func TestCacheExistsByKey_Miss(t *testing.T) {
	cache := makeTestCache()
	addNormRecord(cache, "MotoGP", "gp_race", "TNT Sports", 2026, 8)

	if cache.ExistsByKey("MotoGP", 2026, 9, "gp_race") {
		t.Error("ExistsByKey should return false for different round")
	}
	if cache.ExistsByKey("Moto2", 2026, 8, "gp_race") {
		t.Error("ExistsByKey should return false for different series")
	}
	if cache.ExistsByKey("MotoGP", 2026, 8, "sprint_race") {
		t.Error("ExistsByKey should return false for different session")
	}
}

func TestCacheFindByKey(t *testing.T) {
	cache := makeTestCache()
	addNormRecord(cache, "MotoGP", "gp_race", "TNT Sports", 2026, 8)

	nk := cache.FindByKey("MotoGP", 2026, 8, "gp_race")
	if nk == nil {
		t.Fatal("FindByKey returned nil")
	}
	if nk.SourceFeed != "TNT Sports" {
		t.Errorf("SourceFeed = %q, want TNT Sports", nk.SourceFeed)
	}
}

func TestCacheAddNormalizedItem(t *testing.T) {
	cache := makeTestCache()
	item := makeFeedItem("test-guid-001")
	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "720p", "English", 2026, 8)

	cache.AddNormalizedItem(item, norm)

	if len(cache.Seen) != 1 {
		t.Fatalf("expected 1 Seen record, got %d", len(cache.Seen))
	}
	nk := cache.Seen[0].Normalized
	if nk == nil {
		t.Fatal("Normalized field is nil")
	}
	if nk.Series != "MotoGP" {
		t.Errorf("Series = %q, want MotoGP", nk.Series)
	}
	if nk.SourceFeed != "TNT Sports" {
		t.Errorf("SourceFeed = %q, want TNT Sports", nk.SourceFeed)
	}
}

// --- Helpers for AISelect tests ---

func makeAICfg(series, sessions []string, feedPriority []string, minRes string, langs, excludeFlags []string) *AISelection {
	return &AISelection{
		Series:        series,
		Sessions:      sessions,
		FeedPriority:  feedPriority,
		MinResolution: minRes,
		Languages:     langs,
		ExcludeFlags:  excludeFlags,
	}
}

func makeFeedForAI(excludePatterns []string) *Feed {
	feed := &Feed{}
	feed.Exclude = excludePatterns
	if len(excludePatterns) > 0 {
		feed.exclude = make([]*regexp.Regexp, len(excludePatterns))
		for i, p := range excludePatterns {
			feed.exclude[i] = regexp.MustCompile(p)
		}
		feed.compiled = true
	}
	return feed
}

// --- Phase E: AISelect selection logic ---

func TestSelect_SeriesFilter(t *testing.T) {
	norm := makeNorm("Moto2", "gp_race", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, nil, "", nil, nil)
	cache := makeTestCache()

	ok, reason := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected Moto2 to be rejected when Series=[MotoGP]")
	}
	if reason == "" {
		t.Error("expected a rejection reason")
	}
}

func TestSelect_SessionFilter(t *testing.T) {
	norm := makeNorm("MotoGP", "warm_up", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race", "qualifying"}, nil, "", nil, nil)
	cache := makeTestCache()

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected warm_up to be rejected when Sessions=[gp_race, qualifying]")
	}
}

func TestSelect_LanguageFilter(t *testing.T) {
	norm := makeNorm("MotoGP", "gp_race", "DAZN", "1080p", "Spanish", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, nil, "", []string{"English"}, nil)
	cache := makeTestCache()

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected Spanish language to be rejected when Languages=[English]")
	}
}

func TestSelect_ResolutionFilter(t *testing.T) {
	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "720p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, nil, "1080p", nil, nil)
	cache := makeTestCache()

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected 720p to be rejected when MinResolution=1080p")
	}
}

func TestSelect_ResolutionFilter_Passes(t *testing.T) {
	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, nil, "1080p", nil, nil)
	cache := makeTestCache()

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if !ok {
		t.Error("expected 1080p to pass when MinResolution=1080p")
	}
}

func TestSelect_ExcludeFlags_Upscaled(t *testing.T) {
	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "2160p", "English", 2026, 8, "upscaled")
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, nil, "", nil, []string{"upscaled"})
	cache := makeTestCache()

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected upscaled torrent to be rejected")
	}
}

func TestSelect_ExcludeFlags_Highlights(t *testing.T) {
	norm := makeNorm("MotoGP", "full_weekend", "TNT Sports", "1080p", "English", 2026, 8, "highlights")
	cfg := makeAICfg([]string{"MotoGP"}, []string{"full_weekend"}, nil, "", nil, []string{"highlights"})
	cache := makeTestCache()

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected highlights-flagged torrent to be rejected")
	}
}

func TestSelect_ExcludeRegexp_Applied(t *testing.T) {
	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, nil, "", nil, nil)
	cache := makeTestCache()
	feed := makeFeedForAI([]string{`(?i)\.CAM\.`})
	rawTitle := "MotoGP.2026.Round08.Hungary.Race.CAM.TNT.1080p.X264.English"

	ok, reason := AISelect(norm, cfg, cache, rawTitle, feed)
	if ok {
		t.Error("expected Exclude pattern to reject the item")
	}
	if reason != "matched exclude pattern" {
		t.Errorf("reason = %q, want 'matched exclude pattern'", reason)
	}
}

func TestSelect_ExcludeRegexp_NoMatchPassesThrough(t *testing.T) {
	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, nil, "", nil, nil)
	cache := makeTestCache()
	feed := makeFeedForAI([]string{`(?i)\.CAM\.`})
	rawTitle := "MotoGP.2026.Round08.Hungary.Race.TNT.1080p.X264.English"

	ok, _ := AISelect(norm, cfg, cache, rawTitle, feed)
	if !ok {
		t.Error("expected item without CAM to pass exclude pattern check")
	}
}

func TestSelect_AlreadyDownloaded(t *testing.T) {
	cache := makeTestCache()
	addNormRecord(cache, "MotoGP", "gp_race", "TNT Sports", 2026, 8)

	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected already-downloaded item to be rejected")
	}
}

func TestSelect_FeedPriority_PreferredAlreadyDownloaded(t *testing.T) {
	cache := makeTestCache()
	// TNT Sports (priority 0) already downloaded
	addNormRecord(cache, "MotoGP", "gp_race", "TNT Sports", 2026, 8)

	// Now DAZN (priority 1) appears — should be skipped since TNT Sports is better or equal
	norm := makeNorm("MotoGP", "gp_race", "DAZN", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, []string{"TNT Sports", "DAZN"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected DAZN to be rejected because TNT Sports (higher priority) already downloaded")
	}
}

func TestSelect_FeedPriority_NoBetterExists(t *testing.T) {
	cache := makeTestCache()
	// nothing downloaded yet

	// MotoGP Official (priority 1) appears — should be accepted because TNT Sports (0) not available
	norm := makeNorm("MotoGP", "gp_race", "MotoGP Official", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, []string{"TNT Sports", "MotoGP Official"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if !ok {
		t.Error("expected MotoGP Official to be accepted when no better feed exists")
	}
}

func TestSelect_FeedPriority_LowerAlreadyDownloaded_NoBetter(t *testing.T) {
	cache := makeTestCache()
	// MotoGP Official (priority 1) already downloaded
	addNormRecord(cache, "MotoGP", "gp_race", "MotoGP Official", 2026, 8)

	// TNT Sports (priority 0) now appears — no upgrade path, skip
	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"gp_race"}, []string{"TNT Sports", "MotoGP Official"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected TNT Sports to be rejected — no upgrades once a session is downloaded")
	}
}

func TestSelect_ExcludeRegexp_OnlyOnAIFeeds(t *testing.T) {
	// Non-AI feed: AISelection is nil, Feed.Check() is the gating path
	// This test verifies the existing regexp path works unchanged (regression guard).
	feed := &Feed{
		Regexp:  []string{`(?i)MotoGP`},
		Exclude: []string{`(?i)\.CAM\.`},
	}

	// Item that matches include pattern but NOT exclude — should pass
	itemPass := makeItem("MotoGP.2026.Round08.Race.TNT.1080p", "0")
	if !feed.Check(itemPass) {
		t.Error("expected non-AI feed to accept MotoGP item without CAM")
	}

	// Item that matches include AND exclude — should fail
	itemFail := makeItem("MotoGP.2026.Round08.Race.CAM.TNT.1080p", "0")
	if feed.Check(itemFail) {
		t.Error("expected non-AI feed to reject item matching Exclude pattern")
	}
}

// --- Phase F: Bundle gating ---

func TestBundle_FridayAllDay_NotDownloadedYet(t *testing.T) {
	cache := makeTestCache() // empty
	norm := makeNorm("MotoGP", "friday_all_day", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"friday_all_day"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if !ok {
		t.Error("expected friday_all_day to be accepted when no individual sessions downloaded")
	}
}

func TestBundle_FridayAllDay_AlreadyHaveFP1(t *testing.T) {
	cache := makeTestCache()
	addNormRecord(cache, "MotoGP", "fp1", "MotoGP Official", 2026, 8)

	norm := makeNorm("MotoGP", "friday_all_day", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"friday_all_day"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected friday_all_day to be rejected when fp1 already downloaded")
	}
}

func TestBundle_SaturdayAllDay_MotoGP_HasSprint(t *testing.T) {
	cache := makeTestCache()
	addNormRecord(cache, "MotoGP", "sprint_race", "TNT Sports", 2026, 8)

	norm := makeNorm("MotoGP", "saturday_all_day", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"saturday_all_day"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected saturday_all_day (MotoGP) to be rejected when sprint_race downloaded")
	}
}

func TestBundle_SaturdayAllDay_Moto2_SprintNotCovered(t *testing.T) {
	cache := makeTestCache()
	// sprint_race doesn't apply to Moto2 — should NOT block the saturday bundle
	addNormRecord(cache, "Moto2", "sprint_race", "TNT Sports", 2026, 8)

	norm := makeNorm("Moto2", "saturday_all_day", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"Moto2"}, []string{"saturday_all_day"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if !ok {
		t.Error("expected Moto2 saturday_all_day to pass — sprint_race is not a Moto2 bundle session")
	}
}

func TestBundle_FullWeekend_PartialCoverage(t *testing.T) {
	cache := makeTestCache()
	addNormRecord(cache, "MotoGP", "qualifying", "TNT Sports", 2026, 8)

	norm := makeNorm("MotoGP", "full_weekend", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"full_weekend"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected full_weekend to be rejected when any session already downloaded")
	}
}

func TestBundle_RacePack_NoRace(t *testing.T) {
	cache := makeTestCache() // no gp_race downloaded

	norm := makeNorm("MotoGP", "race_pack", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"race_pack"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if !ok {
		t.Error("expected race_pack to be accepted when gp_race not downloaded")
	}
}

func TestBundle_RacePack_HaveRace(t *testing.T) {
	cache := makeTestCache()
	addNormRecord(cache, "MotoGP", "gp_race", "TNT Sports", 2026, 8)

	norm := makeNorm("MotoGP", "race_pack", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"race_pack"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if ok {
		t.Error("expected race_pack to be rejected when gp_race already downloaded")
	}
}

func TestBundle_Highlights_NoGating(t *testing.T) {
	cache := makeTestCache()
	// all individual sessions downloaded — highlights should still pass (no gating)
	for _, session := range []string{"fp1", "fp2", "qualifying", "sprint_race", "gp_race"} {
		addNormRecord(cache, "MotoGP", session, "TNT Sports", 2026, 8)
	}

	norm := makeNorm("MotoGP", "highlights", "TNT Sports", "1080p", "English", 2026, 8)
	cfg := makeAICfg([]string{"MotoGP"}, []string{"highlights"}, []string{"TNT Sports"}, "", nil, nil)

	ok, _ := AISelect(norm, cfg, cache, "", nil)
	if !ok {
		t.Error("expected highlights to pass bundle gating (no individual sessions to check)")
	}
}

// --- Phase G: API fallback ---

func TestAPIFallback_OnError_MockReturnsNil(t *testing.T) {
	// When Normalize returns an error, the item should fall back to regexp path.
	// This tests that the MockNormalizer error behavior works correctly.
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{})
	mock.ErrorOnMiss = true

	_, err := mock.Normalize(nil, "unknown title") //nolint:staticcheck
	if err == nil {
		t.Error("expected error from MockNormalizer for unknown title")
	}
}

// --- Phase H: Config validation ---

func TestConfig_AIAndRegexpMutuallyExclusive(t *testing.T) {
	feed := Feed{
		Regexp: []string{`(?i)MotoGP`},
		AISelection: &AISelection{
			Series: []string{"MotoGP"},
		},
	}
	if err := feed.Validate("TestFeed"); err == nil {
		t.Error("expected validation error when both Regexp and AISelection are set")
	}
}

func TestConfig_NoAISelection_LoadsOK(t *testing.T) {
	feed := Feed{
		Regexp: []string{`(?i)MotoGP`},
	}
	if err := feed.Validate("TestFeed"); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestConfig_AISelection_NoRegexp_LoadsOK(t *testing.T) {
	feed := Feed{
		AISelection: &AISelection{Series: []string{"MotoGP"}},
		Exclude:     []string{`(?i)\.CAM\.`}, // Exclude is allowed alongside AISelection
	}
	if err := feed.Validate("TestFeed"); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}
