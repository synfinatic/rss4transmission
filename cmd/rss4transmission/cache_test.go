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
		Version:  CACHE_VERSION,
		Errors:   map[string]int64{},
		Seen:     []CacheRecord{},
		needSave: false,
	}
	fi := makeFeedItem("guid-add")
	c.AddItem(fi)

	if len(c.Seen) != 1 {
		t.Fatalf("expected 1 Seen entry, got %d", len(c.Seen))
	}
	if c.Seen[0].GUID != "guid-add" {
		t.Errorf("GUID = %q, want guid-add", c.Seen[0].GUID)
	}
	if !c.needSave {
		t.Error("needSave should be true after AddItem")
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
