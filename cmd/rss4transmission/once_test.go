package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCandidate(guid string, labels map[string]string, fileLabels []map[string]string) *candidate {
	return &candidate{
		item: &FeedItem{
			Feed: "testfeed",
			Item: &gofeed.Item{Title: guid, GUID: guid},
		},
		titleLabels: labels,
		fileLabels:  fileLabels,
	}
}

func makeFeed(identity []string, prefer []PreferDimension, groups []Group) Feed {
	return Feed{
		Identity: identity,
		Prefer:   prefer,
		Groups:   groups,
	}
}

func emptyCache() *CacheFile {
	return &CacheFile{identityIndex: map[string][]map[string]string{}}
}

// --- candidate.coverages ---

func TestCoverages_TitleOnly(t *testing.T) {
	c := makeCandidate("t1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	covs := c.coverages([]string{"series", "round", "session"})
	if len(covs) != 1 {
		t.Fatalf("expected 1 coverage from title-only candidate, got %d", len(covs))
	}
	if covs[0].labels["resolution"] != "1080p" {
		t.Errorf("resolution = %q, want 1080p", covs[0].labels["resolution"])
	}
}

func TestCoverages_MultiFile(t *testing.T) {
	c := makeCandidate("bundle",
		map[string]string{"round": "RD01", "session": "Race", "resolution": "1080p"},
		[]map[string]string{
			{"series": "MotoGP"},
			{"series": "Moto2"},
			{"series": "Moto3"},
		},
	)
	covs := c.coverages([]string{"series", "round", "session"})
	if len(covs) != 3 {
		t.Fatalf("expected 3 coverages (one per file series), got %d", len(covs))
	}
	series := map[string]bool{}
	for _, cov := range covs {
		series[cov.labels["series"]] = true
	}
	if !series["MotoGP"] || !series["Moto2"] || !series["Moto3"] {
		t.Errorf("expected all three series in coverages, got %v", series)
	}
}

func TestCoverages_MissingIdentityLabel(t *testing.T) {
	c := makeCandidate("t1",
		map[string]string{"series": "MotoGP", "session": "Race"}, // round missing
		nil,
	)
	covs := c.coverages([]string{"series", "round", "session"})
	if len(covs) != 0 {
		t.Errorf("expected 0 coverages when identity label missing, got %d", len(covs))
	}
}

func TestCoverages_DefaultFillsMissingLabel(t *testing.T) {
	c := makeCandidate("t1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"}, // no language
		nil,
	)
	c.defaults = map[string]string{"language": "English"}
	covs := c.coverages([]string{"series", "round", "session", "language"})
	if len(covs) != 1 {
		t.Fatalf("expected 1 coverage, got %d", len(covs))
	}
	if covs[0].labels["language"] != "English" {
		t.Errorf("language = %q, want English (from default)", covs[0].labels["language"])
	}
}

func TestCoverages_DefaultDoesNotOverrideExplicit(t *testing.T) {
	c := makeCandidate("t1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "language": "German"},
		nil,
	)
	c.defaults = map[string]string{"language": "English"}
	covs := c.coverages([]string{"series", "round", "session", "language"})
	if len(covs) != 1 {
		t.Fatalf("expected 1 coverage, got %d", len(covs))
	}
	if covs[0].labels["language"] != "German" {
		t.Errorf("language = %q, want German (explicit overrides default)", covs[0].labels["language"])
	}
}

func TestCoverages_DefaultDoesNotOverrideFileLabel(t *testing.T) {
	// Title has no language; a file label provides German explicitly.
	// Default English must not override the file's German.
	c := makeCandidate("t1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"},
		[]map[string]string{{"language": "German"}},
	)
	c.defaults = map[string]string{"language": "English"}
	covs := c.coverages([]string{"series", "round", "session", "language"})
	if len(covs) != 1 {
		t.Fatalf("expected 1 coverage, got %d", len(covs))
	}
	if covs[0].labels["language"] != "German" {
		t.Errorf("language = %q, want German (file label beats default)", covs[0].labels["language"])
	}
}

func TestCoverages_DeduplicatesKeys(t *testing.T) {
	// Two files that produce the same identity key should only appear once.
	c := makeCandidate("dup",
		map[string]string{"round": "RD01", "session": "Race", "resolution": "1080p"},
		[]map[string]string{
			{"series": "MotoGP"},
			{"series": "MotoGP"}, // duplicate
		},
	)
	covs := c.coverages([]string{"series", "round", "session"})
	if len(covs) != 1 {
		t.Errorf("expected 1 coverage after dedup, got %d", len(covs))
	}
}

// --- candidate.allLabels ---

func TestAllLabels_MergesFileLabelsOverTitle(t *testing.T) {
	// resolution is only present in the file name, not the RSS title.
	c := makeCandidate("t1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"},
		[]map[string]string{{"resolution": "1080p"}},
	)
	got := c.allLabels([]string{"series", "round", "session"})
	if got["resolution"] != "1080p" {
		t.Errorf("allLabels()[resolution] = %q, want 1080p (from file label)", got["resolution"])
	}
	if got["series"] != "MotoGP" {
		t.Errorf("allLabels()[series] = %q, want MotoGP (from title label)", got["series"])
	}
}

func TestAllLabels_FileLabelOverridesTitleLabel(t *testing.T) {
	c := makeCandidate("t1",
		map[string]string{"series": "MotoGP", "resolution": "720p"},
		[]map[string]string{{"resolution": "1080p"}},
	)
	got := c.allLabels([]string{"series"})
	if got["resolution"] != "1080p" {
		t.Errorf("allLabels()[resolution] = %q, want 1080p (file label overrides title)", got["resolution"])
	}
}

func TestAllLabels_AppliesDefaultsForMissingLabels(t *testing.T) {
	c := makeCandidate("t1", map[string]string{"series": "MotoGP"}, nil)
	c.defaults = map[string]string{"language": "English"}
	got := c.allLabels([]string{"series"})
	if got["language"] != "English" {
		t.Errorf("allLabels()[language] = %q, want English (from default)", got["language"])
	}
}

func TestAllLabels_ExcludesFileWithoutValidIdentityKey(t *testing.T) {
	// Regression: a file that matches only the resolution regex (e.g. a
	// sample clip) but not the identity labels must not be allowed to
	// overwrite the resolution from the file that actually won selection.
	c := makeCandidate("bundle",
		map[string]string{"round": "RD01", "session": "Race"}, // no series in title
		[]map[string]string{
			{"series": "MotoGP", "resolution": "1080p"}, // valid coverage
			{"resolution": "720p"},                      // sample: no series -> no valid coverage
		},
	)
	got := c.allLabels([]string{"series", "round", "session"})
	if got["resolution"] != "1080p" {
		t.Errorf("allLabels()[resolution] = %q, want 1080p (sample file's 720p must not override the winning coverage's value)", got["resolution"])
	}
}

// --- dispatch cache recording ---

// Regression test: Prefer.order ranking is computed from merged title+file
// labels (candidate.coverages), but the cache used to be seeded from
// titleLabels only. When a Prefer dimension (like resolution) is only present
// in the torrent's file names, the cached record lost that label, so future
// runs saw a "worst possible" cached rank and would re-dispatch a lower
// preference candidate, silently ignoring the configured order.
func TestDispatch_Skip_RecordsFileDerivedLabelsInCache(t *testing.T) {
	c := makeCandidate("guid1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"},
		[]map[string]string{{"resolution": "1080p"}},
	)
	feedCfg := makeFeed([]string{"series", "round", "session"}, nil, nil)
	keys := []string{"series=MotoGP|round=RD01|session=Race"}

	ctx := &RunContext{Cache: emptyCache()}
	cmd := &OnceCmd{Skip: true}
	cmd.dispatch(ctx, feedCfg, "testfeed", c, keys)

	require.Len(t, ctx.Cache.Seen, 1)
	assert.Equal(t, "1080p", ctx.Cache.Seen[0].Labels["resolution"],
		"cached labels should include the file-derived resolution label used to rank this candidate")
}

// Regression: the History UI/ntfy metadata used to be seeded from titleLabels
// while the cache used the merged labels, so a dispatched item's history
// record wouldn't show the file-derived label that actually decided its
// preference rank.
func TestDispatch_Skip_RecordsFileDerivedLabelsInHistory(t *testing.T) {
	c := makeCandidate("guid1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"},
		[]map[string]string{{"resolution": "1080p"}},
	)
	feedCfg := makeFeed([]string{"series", "round", "session"}, nil, nil)
	keys := []string{"series=MotoGP|round=RD01|session=Race"}

	ctx := &RunContext{
		Cache:   emptyCache(),
		History: &HistoryFile{guidIndex: map[string]int{}},
	}
	cmd := &OnceCmd{Skip: true}
	cmd.dispatch(ctx, feedCfg, "testfeed", c, keys)

	records := ctx.History.GetRecords()
	require.Len(t, records, 1)
	assert.Equal(t, "1080p", records[0].Labels["resolution"],
		"history record should include the file-derived resolution label, matching what was cached")
}

// --- selectWinners ---

func TestSelectWinners_SingleCandidate(t *testing.T) {
	c := makeCandidate("guid1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "network": "TNT", "resolution": "1080p"},
		nil,
	)
	feed := makeFeed(
		[]string{"series", "round", "session"},
		[]PreferDimension{
			{Label: "network", Order: []string{"TNT", "Global"}},
			{Label: "resolution", Order: []string{"1080p", "720p"}},
		},
		[]Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	)
	winners, _ := selectWinners([]*candidate{c}, feed, emptyCache())
	if len(winners) != 1 {
		t.Errorf("expected 1 winner, got %d", len(winners))
	}
}

func TestSelectWinners_CacheHit_Equal(t *testing.T) {
	c := makeCandidate("guid1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "network": "TNT", "resolution": "1080p"},
		nil,
	)
	feed := makeFeed(
		[]string{"series", "round", "session"},
		[]PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}},
		[]Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	)
	key := "series=MotoGP|round=RD01|session=Race"
	cache := &CacheFile{
		identityIndex: map[string][]map[string]string{
			key: {{"resolution": "1080p"}},
		},
	}
	winners, _ := selectWinners([]*candidate{c}, feed, cache)
	if len(winners) != 0 {
		t.Errorf("expected 0 winners (cache at equal preference), got %d", len(winners))
	}
}

func TestSelectWinners_CacheHit_BetterAvailable(t *testing.T) {
	c := makeCandidate("guid1",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	feed := makeFeed(
		[]string{"series", "round", "session"},
		[]PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}},
		[]Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	)
	key := "series=MotoGP|round=RD01|session=Race"
	cache := &CacheFile{
		identityIndex: map[string][]map[string]string{
			key: {{"resolution": "720p"}}, // worse than new candidate
		},
	}
	winners, _ := selectWinners([]*candidate{c}, feed, cache)
	if len(winners) != 1 {
		t.Errorf("expected 1 winner (beats cached 720p), got %d", len(winners))
	}
}

func TestSelectWinners_PicksHighestPreference(t *testing.T) {
	c1 := makeCandidate("tnt-1080p",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "network": "TNT", "resolution": "1080p"},
		nil,
	)
	c2 := makeCandidate("global-720p",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "network": "Global", "resolution": "720p"},
		nil,
	)
	feed := makeFeed(
		[]string{"series", "round", "session"},
		[]PreferDimension{
			{Label: "network", Order: []string{"TNT", "Global"}},
			{Label: "resolution", Order: []string{"1080p", "720p"}},
		},
		[]Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	)
	winners, _ := selectWinners([]*candidate{c1, c2}, feed, emptyCache())
	if len(winners) != 1 {
		t.Fatalf("expected 1 winner, got %d", len(winners))
	}
	if winners[0].item.Item.GUID != "tnt-1080p" {
		t.Errorf("expected TNT+1080p winner, got %s", winners[0].item.Item.GUID)
	}
}

func TestSelectWinners_GroupFilter(t *testing.T) {
	c := makeCandidate("guid1",
		map[string]string{"series": "WorldSBK", "round": "RD01", "session": "Race"},
		nil,
	)
	feed := makeFeed(
		[]string{"series", "round", "session"},
		[]PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}},
		[]Group{{Require: map[string][]string{"series": {"MotoGP", "Moto2", "Moto3"}}}},
	)
	winners, _ := selectWinners([]*candidate{c}, feed, emptyCache())
	if len(winners) != 0 {
		t.Errorf("expected 0 winners (series not in group), got %d", len(winners))
	}
}

func TestSelectWinners_MultiClassBundle_CountsOnce(t *testing.T) {
	c := makeCandidate("bundle",
		map[string]string{"round": "RD01", "session": "Race", "resolution": "1080p"},
		[]map[string]string{
			{"series": "MotoGP"},
			{"series": "Moto2"},
			{"series": "Moto3"},
		},
	)
	feed := makeFeed(
		[]string{"series", "round", "session"},
		[]PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}},
		[]Group{{Require: map[string][]string{"series": {"MotoGP", "Moto2", "Moto3"}}}},
	)
	winners, _ := selectWinners([]*candidate{c}, feed, emptyCache())
	// Bundle satisfies 3 identity keys but is one torrent — appears once.
	if len(winners) != 1 {
		t.Errorf("expected 1 winner (bundle counts once), got %d", len(winners))
	}
}

func TestSelectWinners_LanguageDefault_AllowsEnglish(t *testing.T) {
	// Torrent with no language label gets English from the default and matches
	// an English-only group.
	c := makeCandidate("no-lang",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"},
		nil,
	)
	c.defaults = map[string]string{"language": "English"}
	feed := makeFeed(
		[]string{"series", "round", "session", "language"},
		nil,
		[]Group{{Require: map[string][]string{"series": {"MotoGP"}, "language": {"English"}}}},
	)
	winners, _ := selectWinners([]*candidate{c}, feed, emptyCache())
	if len(winners) != 1 {
		t.Errorf("expected 1 winner (no-language defaults to English), got %d", len(winners))
	}
}

func TestSelectWinners_GermanNotDispatchedWhenEnglishRequired(t *testing.T) {
	// Explicit language=German must NOT match an English-only group.
	c := makeCandidate("german",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "language": "German"},
		nil,
	)
	c.defaults = map[string]string{"language": "English"}
	feed := makeFeed(
		[]string{"series", "round", "session", "language"},
		nil,
		[]Group{{Require: map[string][]string{"series": {"MotoGP"}, "language": {"English"}}}},
	)
	winners, _ := selectWinners([]*candidate{c}, feed, emptyCache())
	if len(winners) != 0 {
		t.Errorf("expected 0 winners (German does not satisfy English-only group), got %d", len(winners))
	}
}

func TestSelectWinners_MultiClassBundle_SuppressesDedicated(t *testing.T) {
	// Bundle covers MotoGP+Moto2+Moto3. A dedicated MotoGP torrent at the same
	// preference should NOT also be selected (bundle already covers MotoGP).
	bundle := makeCandidate("bundle",
		map[string]string{"round": "RD01", "session": "Race", "resolution": "1080p"},
		[]map[string]string{
			{"series": "MotoGP"},
			{"series": "Moto2"},
			{"series": "Moto3"},
		},
	)
	dedicated := makeCandidate("motogp-only",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"},
		nil,
	)
	feed := makeFeed(
		[]string{"series", "round", "session"},
		[]PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}},
		[]Group{{Require: map[string][]string{"series": {"MotoGP", "Moto2", "Moto3"}}}},
	)
	// Process bundle first so it claims MotoGP identity key.
	winners, _ := selectWinners([]*candidate{bundle, dedicated}, feed, emptyCache())
	// Only one torrent should be selected (either bundle or dedicated, not both).
	if len(winners) != 1 {
		t.Errorf("expected 1 winner (no duplicate downloads), got %d", len(winners))
	}
}

func TestSelectWinners_NoGroupMatchGetsWinnerReason(t *testing.T) {
	// MotoMundo's file labels include release=MotoMundo (matches the group);
	// VERUM's file labels include release=VERUM (doesn't match).
	// Both have the same title-only identity key (series+round+session).
	// VERUM should report "covered by winner: ..." rather than "no group matched labels".
	sharedTitleLabels := map[string]string{
		"series": "MotoGP", "round": "RD01", "session": "Race",
	}
	motoMundo := makeCandidate("MotoGP.RD01.Race.MotoMundo",
		sharedTitleLabels,
		[]map[string]string{{"release": "MotoMundo"}},
	)
	verum := makeCandidate("MotoGP.RD01.Race.VERUM",
		sharedTitleLabels,
		[]map[string]string{{"release": "VERUM"}},
	)
	feed := makeFeed(
		[]string{"series", "round", "session"},
		nil,
		[]Group{{Require: map[string][]string{"series": {"MotoGP"}, "release": {"MotoMundo"}}}},
	)
	winners, skipped := selectWinners([]*candidate{motoMundo, verum}, feed, emptyCache())
	if len(winners) != 1 {
		t.Fatalf("expected 1 winner, got %d", len(winners))
	}
	if winners[0] != motoMundo {
		t.Errorf("expected MotoMundo to win")
	}
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped, got %d", len(skipped))
	}
	if skipped[0].cand != verum {
		t.Errorf("expected VERUM to be skipped")
	}
	want := "covered by winner: MotoGP.RD01.Race.MotoMundo"
	if skipped[0].reason != want {
		t.Errorf("skip reason = %q, want %q", skipped[0].reason, want)
	}
}

// --- ensureTorrentBytes ---

func TestEnsureTorrentBytes_UsesExisting(t *testing.T) {
	fi := &FeedItem{Item: &gofeed.Item{Title: "no-network"}}
	existing := []byte("existing bytes")
	got, err := ensureTorrentBytes(fi, "", existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "existing bytes" {
		t.Errorf("got %q, want existing bytes", got)
	}
}

func TestEnsureTorrentBytes_FetchesWhenEmpty(t *testing.T) {
	want := []byte("fetched torrent content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(want) //nolint:errcheck
	}))
	defer srv.Close()

	fi := &FeedItem{
		Item: &gofeed.Item{
			Title: "fetched",
			Enclosures: []*gofeed.Enclosure{
				{URL: srv.URL + "/my.torrent", Type: "application/x-bittorrent"},
			},
		},
	}
	got, err := ensureTorrentBytes(fi, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- markCacheRejectedSeen ---

func TestMarkCacheRejectedSeen_AddsCacheRejected(t *testing.T) {
	c := makeCandidate("guid-rej",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"},
		nil,
	)
	cache := &CacheFile{
		Version:       CACHE_VERSION,
		Errors:        map[string]int64{},
		Seen:          []CacheRecord{},
		identityIndex: map[string][]map[string]string{},
	}
	skipped := []skippedCandidate{
		{cand: c, reason: skipReasonCacheBetter},
	}
	markCacheRejectedSeen(skipped, cache)

	if !cache.Exists("testfeed", c.item) {
		t.Error("expected GUID to be in Seen after markCacheRejectedSeen")
	}
}

func TestMarkCacheRejectedSeen_IgnoresOtherReasons(t *testing.T) {
	c := makeCandidate("guid-other",
		map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"},
		nil,
	)
	cache := &CacheFile{
		Version:       CACHE_VERSION,
		Errors:        map[string]int64{},
		Seen:          []CacheRecord{},
		identityIndex: map[string][]map[string]string{},
	}
	skipped := []skippedCandidate{
		{cand: c, reason: "no group matched labels"},
		{cand: c, reason: "outranked by better candidate in this run"},
	}
	markCacheRejectedSeen(skipped, cache)

	if len(cache.Seen) != 0 {
		t.Errorf("expected no Seen entries for non-cache-rejection reasons, got %d", len(cache.Seen))
	}
}

// --- sendNtfyStarted ---

func makeSendNtfyRunContext(t *testing.T, ntfyCfg NtfyConfig) *RunContext {
	t.Helper()
	require.NoError(t, ntfyCfg.Validate())
	rc := &RunContext{}
	rc.Config.Ntfy = ntfyCfg
	return rc
}

func TestSendNtfyStarted_FullContext(t *testing.T) {
	var captured *http.Request
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	ctx := makeSendNtfyRunContext(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"})
	meta := CancelMetadata{
		Title:     "My.Show.S01E01",
		FeedName:  "shows",
		SizeBytes: 1 << 30,
	}
	item := &gofeed.Item{Title: "My.Show.S01E01", GUID: "guid1", Link: "https://example.com/item"}
	sendNtfyStarted(ctx, Feed{}, 42, meta, item)

	require.NotNil(t, captured, "ntfy should have been called")
	assert.Equal(t, "Torrent Started", captured.Header.Get("Title"))
	assert.Contains(t, captured.Header.Get("Priority"), "default")
}

func TestSendNtfyStarted_GUIDAndLinkInContext(t *testing.T) {
	var body []byte
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	ctx := makeSendNtfyRunContext(t, NtfyConfig{
		BaseURL:     ntfySrv.URL,
		Topic:       "t",
		StartedBody: "{{.GUID}}|{{.Link}}",
	})
	meta := CancelMetadata{Title: "My.Show.S01E01"}
	item := &gofeed.Item{
		Title: "My.Show.S01E01",
		GUID:  "guid1",
		Link:  "https://example.com/item",
	}
	sendNtfyStarted(ctx, Feed{}, 0, meta, item)
	assert.Equal(t, "guid1|https://example.com/item", string(body))
}

func TestSendNtfyStarted_NoNotify(t *testing.T) {
	requestCount := 0
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	ctx := makeSendNtfyRunContext(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"})
	sendNtfyStarted(ctx, Feed{NoNotify: true}, 0, CancelMetadata{Title: "T"}, nil)
	assert.Equal(t, 0, requestCount, "NoNotify=true must send no notification")
}

func TestSendNtfyStarted_MissingBaseURL(t *testing.T) {
	requestCount := 0
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	ctx := makeSendNtfyRunContext(t, NtfyConfig{Topic: "t"}) // no BaseURL
	sendNtfyStarted(ctx, Feed{}, 0, CancelMetadata{Title: "T"}, nil)
	assert.Equal(t, 0, requestCount, "missing BaseURL must send no notification")
}

// --- pruneTorrentCache ---

func TestPruneTorrentCache(t *testing.T) {
	dir := t.TempDir()
	maxAge := 60 * 24 * time.Hour

	// Fresh file — should survive pruning.
	freshPath := filepath.Join(dir, "fresh.torrent")
	if err := os.WriteFile(freshPath, []byte("fresh"), 0600); err != nil {
		t.Fatalf("setup fresh: %v", err)
	}

	// Old file — should be deleted.
	oldPath := filepath.Join(dir, "old.torrent")
	if err := os.WriteFile(oldPath, []byte("old"), 0600); err != nil {
		t.Fatalf("setup old: %v", err)
	}
	oldMtime := time.Now().Add(-(maxAge + 24*time.Hour))
	if err := os.Chtimes(oldPath, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	pruneTorrentCache(dir, maxAge)

	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("fresh file was wrongly deleted: %v", err)
	}
	if _, err := os.Stat(oldPath); err == nil {
		t.Error("old file should have been deleted but still exists")
	}
}
