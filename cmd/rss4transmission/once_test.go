package main

import (
	"testing"

	"github.com/mmcdole/gofeed"
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
