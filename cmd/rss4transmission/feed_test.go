package main

import (
	"path/filepath"
	"testing"

	"github.com/mmcdole/gofeed"
)

func TestTorrentURL_Found(t *testing.T) {
	fi := &FeedItem{
		Item: &gofeed.Item{
			Title: "Test",
			Enclosures: []*gofeed.Enclosure{
				{URL: "https://example.com/test.torrent", Type: "application/x-bittorrent"},
			},
		},
	}
	url, err := fi.TorrentURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://example.com/test.torrent" {
		t.Errorf("URL = %q, want https://example.com/test.torrent", url)
	}
}

func TestTorrentURL_NotFound(t *testing.T) {
	fi := &FeedItem{
		Item: &gofeed.Item{
			Title: "Test",
			Enclosures: []*gofeed.Enclosure{
				{URL: "https://example.com/image.jpg", Type: "image/jpeg"},
			},
		},
	}
	_, err := fi.TorrentURL()
	if err == nil {
		t.Error("expected error when no bittorrent enclosure exists")
	}
}

func TestTorrentURL_MultipleEnclosures(t *testing.T) {
	fi := &FeedItem{
		Item: &gofeed.Item{
			Title: "Test",
			Enclosures: []*gofeed.Enclosure{
				{URL: "https://example.com/image.jpg", Type: "image/jpeg"},
				{URL: "https://example.com/test.torrent", Type: "application/x-bittorrent"},
				{URL: "https://example.com/other.torrent", Type: "application/x-bittorrent"},
			},
		},
	}
	url, err := fi.TorrentURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://example.com/test.torrent" {
		t.Errorf("URL = %q, want first bittorrent enclosure URL", url)
	}
}

func TestTorrentURL_NoEnclosures(t *testing.T) {
	fi := &FeedItem{Item: &gofeed.Item{Title: "Test"}}
	_, err := fi.TorrentURL()
	if err == nil {
		t.Error("expected error when enclosures list is empty")
	}
}

func TestIsComplete_False(t *testing.T) {
	fi := &FeedItem{Complete: false}
	if fi.IsComplete() {
		t.Error("IsComplete should return false")
	}
}

func TestIsComplete_True(t *testing.T) {
	fi := &FeedItem{Complete: true}
	if !fi.IsComplete() {
		t.Error("IsComplete should return true")
	}
}

func TestFeedNewItems_Match(t *testing.T) {
	f := &Feed{
		Regexp:       []string{`(?i)^MyShow.*`},
		DownloadPath: "/downloads",
	}
	feed := &gofeed.Feed{
		Items: []*gofeed.Item{
			{Title: "MyShow S01E01", GUID: "g1"},
			{Title: "OtherShow S01E01", GUID: "g2"},
		},
	}
	items := f.NewItems("feed1", feed)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Item.GUID != "g1" {
		t.Errorf("GUID = %q, want g1", items[0].Item.GUID)
	}
	if items[0].Feed != "feed1" {
		t.Errorf("Feed = %q, want feed1", items[0].Feed)
	}
}

func TestFeedNewItems_NoMatch(t *testing.T) {
	f := &Feed{Regexp: []string{`(?i)^MyShow.*`}}
	feed := &gofeed.Feed{
		Items: []*gofeed.Item{
			{Title: "SomethingElse", GUID: "g1"},
		},
	}
	items := f.NewItems("feed1", feed)
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestFeedNewItems_Location(t *testing.T) {
	f := &Feed{
		Regexp:       []string{`.*`},
		DownloadPath: "/downloads/tv",
	}
	feed := &gofeed.Feed{
		Items: []*gofeed.Item{
			{Title: "MyShow S01E01", GUID: "g1"},
		},
	}
	items := f.NewItems("feed1", feed)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	want := filepath.Join("/downloads/tv", "MyShow S01E01")
	if items[0].Location != want {
		t.Errorf("Location = %q, want %q", items[0].Location, want)
	}
}

func TestFeedNewItems_Complete(t *testing.T) {
	f := &Feed{Regexp: []string{`.*`}}
	feed := &gofeed.Feed{
		Items: []*gofeed.Item{{Title: "Any", GUID: "g1"}},
	}
	items := f.NewItems("feed1", feed)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Complete {
		t.Error("new FeedItem should have Complete=false")
	}
}
