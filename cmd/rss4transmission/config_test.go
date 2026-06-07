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

func TestFeedCheckMatch(t *testing.T) {
	f := &Feed{Regexp: []string{`(?i)^MyShow.*`}}
	if !f.Check(makeItem("MyShow S01E01", "")) {
		t.Error("expected match for 'MyShow S01E01'")
	}
}

func TestFeedCheckNoMatch(t *testing.T) {
	f := &Feed{Regexp: []string{`(?i)^MyShow.*`}}
	if f.Check(makeItem("OtherShow S01E01", "")) {
		t.Error("expected no match for 'OtherShow S01E01'")
	}
}

func TestFeedCheckExcludeWins(t *testing.T) {
	f := &Feed{
		Regexp:  []string{`(?i)^MyShow.*`},
		Exclude: []string{`(?i).*720p.*`},
	}
	if f.Check(makeItem("MyShow S01E01 720p", "")) {
		t.Error("exclude pattern should take priority over match")
	}
}

func TestFeedCheckMinSize(t *testing.T) {
	f := &Feed{
		Regexp:  []string{`.*`},
		MinSize: "1GB",
	}
	// 100MB enclosure — below 1GB minimum
	if f.Check(makeItem("Anything", "104857600")) {
		t.Error("item below MinSize should not match")
	}
}

func TestFeedCheckMaxSize(t *testing.T) {
	f := &Feed{
		Regexp:  []string{`.*`},
		MaxSize: "100MB",
	}
	// 2GB enclosure — above 100MB maximum
	if f.Check(makeItem("Anything", "2147483648")) {
		t.Error("item above MaxSize should not match")
	}
}

func TestFeedCheckSizeRange(t *testing.T) {
	f := &Feed{
		Regexp:  []string{`.*`},
		MinSize: "100MB",
		MaxSize: "10GB",
	}
	// 1GB — within range
	if !f.Check(makeItem("Anything", "1073741824")) {
		t.Error("item within [MinSize, MaxSize] should match")
	}
}

func TestFeedCheckNoEnclosure(t *testing.T) {
	f := &Feed{Regexp: []string{`(?i)^MyShow.*`}}
	// No enclosures, no MinSize — size checks skipped; regexp match wins
	if !f.Check(makeItem("MyShow S01E01", "")) {
		t.Error("item with no enclosures and no MinSize should match on regexp alone")
	}
}

func TestFeedCheckNoEnclosureWithMinSize(t *testing.T) {
	f := &Feed{
		Regexp:  []string{`.*`},
		MinSize: "100MB",
	}
	// totalSize == 0 which is below 100MB minimum
	if f.Check(makeItem("Anything", "")) {
		t.Error("item with no enclosures should fail MinSize check")
	}
}

func TestFeedHasCategory_Found(t *testing.T) {
	f := &Feed{Categories: []string{"movies", "tv"}}
	if !f.HasCategory("tv") {
		t.Error("HasCategory should return true for 'tv'")
	}
}

func TestFeedHasCategory_NotFound(t *testing.T) {
	f := &Feed{Categories: []string{"movies", "tv"}}
	if f.HasCategory("books") {
		t.Error("HasCategory should return false for 'books'")
	}
}

func TestFeedHasCategory_Empty(t *testing.T) {
	f := &Feed{}
	if f.HasCategory("anything") {
		t.Error("HasCategory should return false when Categories is empty")
	}
}
