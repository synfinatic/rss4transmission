package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
)

func makeFeedItem(guid string) *FeedItem {
	return &FeedItem{
		Feed:     "testfeed",
		Complete: false,
		Item: &gofeed.Item{
			GUID:            guid,
			Title:           "Test Title",
			PublishedParsed: func() *time.Time { t := time.Now(); return &t }(),
		},
	}
}

func TestOpenCacheNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	c, err := OpenCache(path)
	if err != nil {
		t.Fatalf("OpenCache returned error: %v", err)
	}
	if c.Version != CACHE_VERSION {
		t.Errorf("Version = %d, want %d", c.Version, CACHE_VERSION)
	}
	if len(c.Seen) != 0 {
		t.Errorf("Seen should be empty, got %d entries", len(c.Seen))
	}
	if len(c.Errors) != 0 {
		t.Errorf("Errors should be empty, got %d entries", len(c.Errors))
	}
}

func TestOpenCacheExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	data := CacheFile{
		Version: CACHE_VERSION,
		Errors:  map[string]int64{"guid1": 999},
		Seen: []CacheRecord{
			{Feed: "f", GUID: "guid1", Complete: false},
		},
	}
	b, _ := json.Marshal(data)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}

	c, err := OpenCache(path)
	if err != nil {
		t.Fatalf("OpenCache returned error: %v", err)
	}
	if len(c.Seen) != 1 || c.Seen[0].GUID != "guid1" {
		t.Errorf("unexpected Seen contents: %v", c.Seen)
	}
	if c.Errors["guid1"] != 999 {
		t.Errorf("unexpected Errors contents: %v", c.Errors)
	}
}

func TestOpenCacheInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json {{{"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := OpenCache(path)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestAddItem(t *testing.T) {
	c := &CacheFile{
		Version:       CACHE_VERSION,
		Errors:        map[string]int64{},
		Seen:          []CacheRecord{},
		needSave:      false,
		identityIndex: map[string][]map[string]string{},
	}
	fi := makeFeedItem("guid-add")
	labels := map[string]string{"series": "MotoGP", "resolution": "1080p"}
	keys := []string{"series=MotoGP|round=RD01|session=Race"}
	c.AddItem(fi, labels, keys)

	if len(c.Seen) != 1 {
		t.Fatalf("expected 1 Seen entry, got %d", len(c.Seen))
	}
	if c.Seen[0].GUID != "guid-add" {
		t.Errorf("GUID = %q, want guid-add", c.Seen[0].GUID)
	}
	if !c.needSave {
		t.Error("needSave should be true after AddItem")
	}
	// Labels and identity keys should be recorded.
	if c.Seen[0].Labels["series"] != "MotoGP" {
		t.Errorf("Labels[series] = %q, want MotoGP", c.Seen[0].Labels["series"])
	}
	if len(c.Seen[0].IdentityKeys) != 1 {
		t.Errorf("expected 1 IdentityKey, got %d", len(c.Seen[0].IdentityKeys))
	}
	// Identity index should be updated immediately.
	if _, ok := c.identityIndex[keys[0]]; !ok {
		t.Error("identityIndex should be updated after AddItem")
	}
}

func TestBestRankForKey_Miss(t *testing.T) {
	c := &CacheFile{identityIndex: map[string][]map[string]string{}}
	prefer := []PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}}
	_, ok := c.BestRankForKey("series=MotoGP|round=RD01|session=Race", prefer)
	if ok {
		t.Error("expected ok=false for key not in index")
	}
}

func TestBestRankForKey_Single(t *testing.T) {
	prefer := []PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}}
	key := "series=MotoGP|round=RD01|session=Race"
	labels := map[string]string{"resolution": "720p"}
	c := &CacheFile{
		identityIndex: map[string][]map[string]string{
			key: {labels},
		},
	}
	rank, ok := c.BestRankForKey(key, prefer)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if rank[0] != 1 { // 720p is index 1
		t.Errorf("rank[0] = %d, want 1 (720p)", rank[0])
	}
}

func TestBestRankForKey_PicksBest(t *testing.T) {
	prefer := []PreferDimension{{Label: "resolution", Order: []string{"1080p", "720p"}}}
	key := "series=MotoGP|round=RD01|session=Race"
	c := &CacheFile{
		identityIndex: map[string][]map[string]string{
			key: {
				{"resolution": "720p"},
				{"resolution": "1080p"},
			},
		},
	}
	rank, ok := c.BestRankForKey(key, prefer)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if rank[0] != 0 { // 1080p is index 0 (best)
		t.Errorf("rank[0] = %d, want 0 (1080p)", rank[0])
	}
}

func TestRebuildIdentityIndex(t *testing.T) {
	key := "series=MotoGP|round=RD01|session=Race"
	labels := map[string]string{"resolution": "1080p"}
	c := &CacheFile{
		Seen: []CacheRecord{
			{GUID: "g1", Labels: labels, IdentityKeys: []string{key}},
		},
	}
	c.rebuildIdentityIndex()
	sets, ok := c.identityIndex[key]
	if !ok || len(sets) != 1 {
		t.Errorf("expected 1 label set in index, got %v", c.identityIndex)
	}
}

func TestRebuildIdentityIndex_SkipsEmptyLabels(t *testing.T) {
	c := &CacheFile{
		Seen: []CacheRecord{
			{GUID: "g1"}, // no Labels, no IdentityKeys
		},
	}
	c.rebuildIdentityIndex()
	if len(c.identityIndex) != 0 {
		t.Errorf("expected empty index for records with no labels, got %v", c.identityIndex)
	}
}

func TestExists_Found(t *testing.T) {
	fi := makeFeedItem("guid-exists")
	c := &CacheFile{
		Version: CACHE_VERSION,
		Errors:  map[string]int64{},
		Seen:    []CacheRecord{{Feed: "testfeed", GUID: "guid-exists"}},
	}
	if !c.Exists("testfeed", fi) {
		t.Error("Exists should return true for matching GUID+feed")
	}
}

func TestExists_WrongFeed(t *testing.T) {
	fi := makeFeedItem("guid-exists")
	c := &CacheFile{
		Version: CACHE_VERSION,
		Errors:  map[string]int64{},
		Seen:    []CacheRecord{{Feed: "otherfeed", GUID: "guid-exists"}},
	}
	if c.Exists("testfeed", fi) {
		t.Error("Exists should return false when feed name does not match")
	}
}

func TestExists_NotFound(t *testing.T) {
	fi := makeFeedItem("guid-unknown")
	c := &CacheFile{
		Version: CACHE_VERSION,
		Errors:  map[string]int64{},
		Seen:    []CacheRecord{{Feed: "testfeed", GUID: "guid-other"}},
	}
	if c.Exists("testfeed", fi) {
		t.Error("Exists should return false for unknown GUID")
	}
}

func TestCheckError_NewEntry(t *testing.T) {
	fi := makeFeedItem("new-guid")
	c := &CacheFile{Errors: map[string]int64{}}
	if !c.CheckError(*fi) {
		t.Error("CheckError should return true for GUID not in Errors map")
	}
}

func TestCheckError_ExpiredEntry(t *testing.T) {
	fi := makeFeedItem("expired-guid")
	c := &CacheFile{Errors: map[string]int64{
		"expired-guid": time.Now().Add(-2 * time.Hour).Unix(),
	}}
	if !c.CheckError(*fi) {
		t.Error("CheckError should return true for expired (past) error entry")
	}
}

func TestCheckError_ActiveEntry(t *testing.T) {
	fi := makeFeedItem("active-guid")
	c := &CacheFile{Errors: map[string]int64{
		"active-guid": time.Now().Add(2 * time.Hour).Unix(),
	}}
	if c.CheckError(*fi) {
		t.Error("CheckError should return false for active (future) error entry")
	}
}

func TestAddError_NewEntry(t *testing.T) {
	fi := makeFeedItem("new-error-guid")
	c := &CacheFile{Errors: map[string]int64{}}
	if !c.AddError(*fi) {
		t.Error("AddError should return true for new entry")
	}
	if _, ok := c.Errors["new-error-guid"]; !ok {
		t.Error("AddError should insert entry into Errors map")
	}
}

func TestAddError_ActiveEntry(t *testing.T) {
	fi := makeFeedItem("active-error-guid")
	c := &CacheFile{Errors: map[string]int64{
		"active-error-guid": time.Now().Add(2 * time.Hour).Unix(),
	}}
	if c.AddError(*fi) {
		t.Error("AddError should return false for active (suppressed) error entry")
	}
}

func TestSaveCache_NoPruning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	now := time.Now()
	c := &CacheFile{
		Version: CACHE_VERSION,
		Errors:  map[string]int64{},
		Seen: []CacheRecord{
			{Feed: "f", GUID: "g1", Published: now, AddTime: now},
		},
		filename: path,
		needSave: true,
	}

	if err := c.SaveCache(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveCache returned error: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read saved cache: %v", err)
	}
	var loaded CacheFile
	if err := json.Unmarshal(b, &loaded); err != nil {
		t.Fatalf("saved cache is invalid JSON: %v", err)
	}
	if len(loaded.Seen) != 1 {
		t.Errorf("expected 1 Seen entry, got %d", len(loaded.Seen))
	}
}

func TestSaveCache_Pruning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	old := time.Now().Add(-60 * 24 * time.Hour)
	recent := time.Now()

	c := &CacheFile{
		Version: CACHE_VERSION,
		Errors:  map[string]int64{},
		Seen: []CacheRecord{
			{Feed: "f", GUID: "old", Published: old},
			{Feed: "f", GUID: "recent", Published: recent},
		},
		filename: path,
		needSave: true,
	}

	if err := c.SaveCache(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveCache returned error: %v", err)
	}
	if len(c.Seen) != 1 || c.Seen[0].GUID != "recent" {
		t.Errorf("expected only 'recent' entry after pruning, got: %v", c.Seen)
	}
}

func TestSaveCache_SkipsWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	now := time.Now()
	c := &CacheFile{
		Version: CACHE_VERSION,
		Errors:  map[string]int64{},
		Seen: []CacheRecord{
			{Feed: "f", GUID: "g1", Published: now},
		},
		filename: path,
		needSave: false,
	}

	if err := c.SaveCache(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveCache returned error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("SaveCache should not write file when needSave=false and no pruning")
	}
}
