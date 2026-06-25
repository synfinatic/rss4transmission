package main

import (
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
	if !f.Check(makeItem("MyShow.1080p.S01E01", "")) {
		t.Error("non-excluded item should return true")
	}
}

func TestFeedCheck_Excluded(t *testing.T) {
	f := &Feed{Exclude: []string{`(?i).*720p.*`}}
	if f.Check(makeItem("MyShow.720p.S01E01", "")) {
		t.Error("excluded item should return false")
	}
}

func TestFeedCheck_NoFilters(t *testing.T) {
	f := &Feed{}
	if !f.Check(makeItem("AnythingAtAll", "")) {
		t.Error("item should pass with no filters configured")
	}
}

func TestFeedCheck_MinSize(t *testing.T) {
	f := &Feed{MinSize: "1GB"}
	// 100MB enclosure — below 1GB minimum
	if f.Check(makeItem("Anything", "104857600")) {
		t.Error("item below MinSize should return false")
	}
}

func TestFeedCheck_MaxSize(t *testing.T) {
	f := &Feed{MaxSize: "100MB"}
	// 2GB enclosure — above 100MB maximum
	if f.Check(makeItem("Anything", "2147483648")) {
		t.Error("item above MaxSize should return false")
	}
}

func TestFeedCheck_SizeRange(t *testing.T) {
	f := &Feed{MinSize: "100MB", MaxSize: "10GB"}
	// 1GB — within range
	if !f.Check(makeItem("Anything", "1073741824")) {
		t.Error("item within [MinSize, MaxSize] should return true")
	}
}

func TestFeedCheck_NoEnclosureWithMinSize(t *testing.T) {
	f := &Feed{MinSize: "100MB"}
	// totalSize == 0, below 100MB minimum
	if f.Check(makeItem("Anything", "")) {
		t.Error("item with no enclosures should fail MinSize check")
	}
}

func TestFeedValidate_NoExtractor(t *testing.T) {
	f := &Feed{URL: "https://example.com/rss"}
	if err := f.Validate("myfeed", nil); err != nil {
		t.Errorf("feed with no Extractor should always validate: %v", err)
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
