package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mmcdole/gofeed"
)

func makeItem(title string, enclosureLength string) *gofeed.Item {
	item := &gofeed.Item{Title: title}
	if enclosureLength != "" {
		item.Enclosures = []*gofeed.Enclosure{
			{Length: enclosureLength, Type: "application/x-bittorrent"},
		}
	}
	return item
}

func TestFeedCheck_NotExcluded(t *testing.T) {
	f := &Feed{Exclude: []string{`(?i).*720p.*`}}
	if ok, _ := f.Check(makeItem("MyShow.1080p.S01E01", "")); !ok {
		t.Error("non-excluded item should return true")
	}
}

func TestFeedCheck_Excluded(t *testing.T) {
	f := &Feed{Exclude: []string{`(?i).*720p.*`}}
	if ok, _ := f.Check(makeItem("MyShow.720p.S01E01", "")); ok {
		t.Error("excluded item should return false")
	}
}

func TestFeedCheck_NoFilters(t *testing.T) {
	f := &Feed{}
	if ok, _ := f.Check(makeItem("AnythingAtAll", "")); !ok {
		t.Error("item should pass with no filters configured")
	}
}

func TestFeedCheck_MinSize(t *testing.T) {
	f := &Feed{MinSize: "1GB"}
	// 100MB enclosure — below 1GB minimum
	if ok, _ := f.Check(makeItem("Anything", "104857600")); ok {
		t.Error("item below MinSize should return false")
	}
}

func TestFeedCheck_MaxSize(t *testing.T) {
	f := &Feed{MaxSize: "100MB"}
	// 2GB enclosure — above 100MB maximum
	if ok, _ := f.Check(makeItem("Anything", "2147483648")); ok {
		t.Error("item above MaxSize should return false")
	}
}

func TestFeedCheck_SizeRange(t *testing.T) {
	f := &Feed{MinSize: "100MB", MaxSize: "10GB"}
	// 1GB — within range
	if ok, _ := f.Check(makeItem("Anything", "1073741824")); !ok {
		t.Error("item within [MinSize, MaxSize] should return true")
	}
}

func TestFeedCheck_NoEnclosureWithMinSize(t *testing.T) {
	f := &Feed{MinSize: "100MB"}
	// totalSize == 0, below 100MB minimum
	if ok, _ := f.Check(makeItem("Anything", "")); ok {
		t.Error("item with no enclosures should fail MinSize check")
	}
}

func TestFeedValidate_NoExtractor(t *testing.T) {
	f := &Feed{URL: "https://example.com/rss"}
	if err := f.Validate("myfeed", nil); err == nil {
		t.Error("feed with no Extractor should fail validation in v2")
	}
}

func TestFeedValidate_MissingExtractorDef(t *testing.T) {
	f := &Feed{
		Extractor: "nonexistent",
		Identity:  []string{"series"},
		Groups:    []Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	}
	err := f.Validate("myfeed", map[string]*ExtractorSet{})
	if err == nil {
		t.Error("expected error when Extractor name not in Extractors map")
	}
}

func TestFeedValidate_MissingIdentity(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		// Identity missing
		Groups: []Group{{Require: map[string][]string{}}},
	}
	err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es})
	if err == nil {
		t.Error("expected error when Identity is empty")
	}
}

func TestFeedValidate_MissingGroups(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		Identity:  []string{"series"},
		// Groups missing
	}
	err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es})
	if err == nil {
		t.Error("expected error when Groups is empty")
	}
}

func TestFeedValidate_Valid(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		Identity:  []string{"series", "round", "session"},
		Groups:    []Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	}
	if err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es}); err != nil {
		t.Errorf("expected valid feed to pass validation: %v", err)
	}
}

func TestValidateFeedNames_Unique(t *testing.T) {
	feeds := []Feed{{Name: "A", URL: "https://a"}, {Name: "B", URL: "https://b"}}
	if err := validateFeedNames(feeds); err != nil {
		t.Errorf("expected no error for unique names, got: %v", err)
	}
}

func TestValidateFeedNames_BlankName(t *testing.T) {
	feeds := []Feed{{Name: "", URL: "https://a"}}
	if err := validateFeedNames(feeds); err == nil {
		t.Error("expected error for blank feed name")
	}
}

func TestValidateFeedNames_Duplicate(t *testing.T) {
	feeds := []Feed{{Name: "A", URL: "https://a"}, {Name: "A", URL: "https://b"}}
	if err := validateFeedNames(feeds); err == nil {
		t.Error("expected error for duplicate feed name")
	}
}

func TestLoadConfig_FeedsPreserveFileOrder(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yamlContent := `
Feeds:
  - Name: Zzz
    URL: https://example.com/z
  - Name: Aaa
    URL: https://example.com/a
  - Name: Mmm
    URL: https://example.com/m
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if _, err := rc.loadConfig(cfgPath); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	want := []string{"Zzz", "Aaa", "Mmm"}
	if len(rc.Config.Feeds) != len(want) {
		t.Fatalf("expected %d feeds, got %d", len(want), len(rc.Config.Feeds))
	}
	for i, name := range want {
		if rc.Config.Feeds[i].Name != name {
			t.Errorf("feed[%d].Name = %q, want %q", i, rc.Config.Feeds[i].Name, name)
		}
	}
}

func TestLoadConfig_RejectsDuplicateFeedNames(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yamlContent := `
Feeds:
  - Name: Dup
    URL: https://example.com/a
  - Name: Dup
    URL: https://example.com/b
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if _, err := rc.loadConfig(cfgPath); err == nil {
		t.Error("expected loadConfig to reject duplicate feed names")
	}
}

func TestLoadConfig_RejectsBlankFeedName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yamlContent := `
Feeds:
  - URL: https://example.com/a
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if _, err := rc.loadConfig(cfgPath); err == nil {
		t.Error("expected loadConfig to reject a blank feed name")
	}
}

func TestLoadConfig_BadReload_KeepsPreviousGoodConfig(t *testing.T) {
	// Simulates watch's live config-reload path: loadConfig is called again on
	// the same RunContext. A reload with duplicate/blank feed names must be
	// rejected without corrupting the already-running, valid config.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	goodYAML := `
Feeds:
  - Name: Good1
    URL: https://example.com/1
  - Name: Good2
    URL: https://example.com/2
`
	if err := os.WriteFile(cfgPath, []byte(goodYAML), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if _, err := rc.loadConfig(cfgPath); err != nil {
		t.Fatalf("initial loadConfig failed: %v", err)
	}
	if len(rc.Config.Feeds) != 2 {
		t.Fatalf("expected 2 feeds after initial load, got %d", len(rc.Config.Feeds))
	}

	badYAML := `
Feeds:
  - Name: Dup
    URL: https://example.com/1
  - Name: Dup
    URL: https://example.com/2
`
	if err := os.WriteFile(cfgPath, []byte(badYAML), 0600); err != nil {
		t.Fatalf("failed to overwrite config: %v", err)
	}

	if _, err := rc.loadConfig(cfgPath); err == nil {
		t.Error("expected reload with duplicate feed names to fail")
	}

	if len(rc.Config.Feeds) != 2 {
		t.Fatalf("expected previous config to survive a bad reload, got %d feeds", len(rc.Config.Feeds))
	}
	want := []string{"Good1", "Good2"}
	for i, name := range want {
		if rc.Config.Feeds[i].Name != name {
			t.Errorf("feed[%d].Name = %q, want %q (config should be unchanged after bad reload)",
				i, rc.Config.Feeds[i].Name, name)
		}
	}
}
