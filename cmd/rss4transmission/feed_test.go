package main

import (
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
