package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
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

// --- getTorrentContents ---

var sentinelTorrent = []byte("d8:announce27:http://example.com/announcee")

func makeFeedItemWithURL(title, torrentURL string) *FeedItem {
	return &FeedItem{
		Item: &gofeed.Item{
			Title: title,
			Enclosures: []*gofeed.Enclosure{
				{URL: torrentURL, Type: "application/x-bittorrent"},
			},
		},
	}
}

func TestGetTorrentContents_CacheHit(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, sanitizeFilename("My.Title")+".torrent")
	if err := os.WriteFile(cachePath, sentinelTorrent, 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// No valid enclosure URL — network would fail if reached.
	fi := &FeedItem{Item: &gofeed.Item{Title: "My.Title"}}
	got, err := fi.getTorrentContents(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, sentinelTorrent) {
		t.Errorf("got %q, want sentinel bytes", got)
	}
}

func TestGetTorrentContents_CacheMiss(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Write(sentinelTorrent) //nolint:errcheck
	}))
	defer srv.Close()

	fi := makeFeedItemWithURL("My.Title", srv.URL+"/my.torrent")
	got, err := fi.getTorrentContents(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, sentinelTorrent) {
		t.Errorf("got %q, want sentinel bytes", got)
	}

	// Verify cache file was written.
	cachePath := filepath.Join(dir, sanitizeFilename("My.Title")+".torrent")
	written, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	if !bytes.Equal(written, sentinelTorrent) {
		t.Errorf("cache file content = %q, want sentinel bytes", written)
	}

	// Second call with server stopped should hit the cache.
	srv.Close()
	got2, err := fi.getTorrentContents(dir)
	if err != nil {
		t.Fatalf("second call unexpected error: %v", err)
	}
	if !bytes.Equal(got2, sentinelTorrent) {
		t.Errorf("second call got %q, want sentinel bytes from cache", got2)
	}
}

func TestGetTorrentContents_NoCacheDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Write(sentinelTorrent) //nolint:errcheck
	}))
	defer srv.Close()

	fi := makeFeedItemWithURL("My.Title", srv.URL+"/my.torrent")
	got, err := fi.getTorrentContents("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, sentinelTorrent) {
		t.Errorf("got %q, want sentinel bytes", got)
	}
}

func TestGetTorrentContents_HTTPError(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	fi := makeFeedItemWithURL("Error.Title", srv.URL+"/my.torrent")
	_, err := fi.getTorrentContents(dir)
	if err == nil {
		t.Error("expected error for non-2xx HTTP response, got nil")
	}

	// Verify no bad bytes were written to the cache.
	cachePath := filepath.Join(dir, sanitizeFilename("Error.Title")+".torrent")
	if _, statErr := os.Stat(cachePath); statErr == nil {
		t.Error("cache file should not be written on HTTP error")
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
