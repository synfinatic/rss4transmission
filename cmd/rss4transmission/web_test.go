package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- /healthz ---

func TestHealthzHandler(t *testing.T) {
	mux := newWebMux(nil, nil, nil)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// --- /cancel helpers ---

func makeCancelCfg(secret, baseURL string) CancelConfig {
	return CancelConfig{HMACSecret: secret, BaseURL: baseURL, TokenTTLH: 24}
}

func makeRemoveFunc(called *bool) removeFunc {
	return func(_ context.Context, _ []int64) error {
		*called = true
		return nil
	}
}

func makeProgressFunc(downloadedBytes int64, percentDone float64) progressFunc {
	return func(_ context.Context, _ int64) (int64, float64, error) {
		return downloadedBytes, percentDone, nil
	}
}

func noProgressFunc() progressFunc {
	return func(_ context.Context, _ int64) (int64, float64, error) {
		return 0, 0, nil
	}
}

func makeCancelFormBody(id string, expires int64, sig string) *strings.Reader {
	v := url.Values{}
	v.Set("id", id)
	v.Set("expires", fmt.Sprintf("%d", expires))
	v.Set("sig", sig)
	return strings.NewReader(v.Encode())
}

// --- GET /cancel ---

func TestGetCancelHandler_RendersForm(t *testing.T) {
	store := NewStore(time.Hour)
	meta := CancelMetadata{
		Title:    "My Show S01E01",
		FeedName: "shows",
		Labels:   map[string]string{"show": "My Show"},
		Files:    []string{"My.Show.S01E01.mkv"},
	}
	store.Register("test-id", 42, meta)

	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.Contains(t, body, "My Show S01E01", "title should appear in form")
	assert.Contains(t, body, "shows", "feed name should appear in form")
	assert.Contains(t, body, "My.Show.S01E01.mkv", "file name should appear in form")
}

func TestGetCancelHandler_RendersProgress(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{Title: "Show"})

	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	// 2.5 GiB downloaded, 25% done
	getProgress := makeProgressFunc(int64(2.5*float64(1<<30)), 0.25)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), getProgress, nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.Contains(t, body, "25.0%", "percent complete should appear in form")
	assert.Contains(t, body, "GB", "downloaded size in GB should appear in form")
}

func TestGetCancelHandler_ProgressUnknownOnError(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{Title: "Show"})

	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	errProgress := func(_ context.Context, _ int64) (int64, float64, error) {
		return 0, 0, fmt.Errorf("transmission unavailable")
	}

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), errProgress, nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Unknown", "should show Unknown when progress query fails")
}

func TestGetCancelHandler_MissingParams(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("GET", "/cancel?id=test-id", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGetCancelHandler_BadSignature(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, _ := GenerateToken([]byte("secret"), "test-id", time.Hour)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=badsig", expires), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGetCancelHandler_Expired(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", -time.Second)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusGone, rr.Code)
}

func TestGetCancelHandler_NotFound(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "ghost-id", time.Hour)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=ghost-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGetCancelHandler_DoesNotConsumeEntry(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{Title: "Keep Me"})

	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	mux := newWebMux(nil, nil, nil)
	removed := false
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(&removed), noProgressFunc(), nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.False(t, removed, "GET should not remove the torrent")

	// Entry must still be in the store for the POST to work.
	_, ok := store.Take("test-id")
	assert.True(t, ok, "entry must still be present after GET")
}

// --- POST /cancel ---

func TestPostCancelHandler_Valid(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})

	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	removed := false
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(&removed), noProgressFunc(), nil)

	body := makeCancelFormBody("test-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, removed, "Transmission remove should have been called")
}

func TestPostCancelHandler_MissingParams(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	body := strings.NewReader("id=test-id") // missing expires and sig
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPostCancelHandler_BadSignature(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, _ := GenerateToken([]byte("secret"), "test-id", time.Hour)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	body := makeCancelFormBody("test-id", expires, "badsig")
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPostCancelHandler_Expired(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", -time.Second)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	body := makeCancelFormBody("test-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusGone, rr.Code)
}

func TestPostCancelHandler_NotFound(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "ghost-id", time.Hour)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	body := makeCancelFormBody("ghost-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// --- newCancelMux ---

func TestNewCancelMux_HealthzReachable(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestNewCancelMux_CancelReachable(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	// A POST with missing params should return 400, not 404 — proving the route exists.
	body := strings.NewReader("id=x")
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestNewCancelMux_HistoryNotReachable(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestNewCancelMux_NilStoreHealthzStillWorks(t *testing.T) {
	cfg := makeCancelCfg("", "")
	mux := newCancelMux(nil, cfg, nil, nil, nil)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestNewCancelMux_NilStoreCancelReturns404(t *testing.T) {
	cfg := makeCancelCfg("", "")
	mux := newCancelMux(nil, cfg, nil, nil, nil)

	body := strings.NewReader("id=x&expires=1&sig=y")
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPostCancelHandler_RemoveErrorPreservesStoreEntry(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 77, CancelMetadata{Title: "Keep Me"})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	failRemove := func(_ context.Context, _ []int64) error {
		return fmt.Errorf("transmission unreachable")
	}
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, failRemove, noProgressFunc(), nil)

	body := makeCancelFormBody("test-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	_, _, ok := store.Peek("test-id")
	assert.True(t, ok, "store entry must survive a failed remove so the user can retry")
}

func TestGetCancelHandler_ZeroBytesProgressBothUnknown(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{Title: "Show"})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	// brand-new torrent: 0 bytes downloaded, 0% done
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), makeProgressFunc(0, 0.0), nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), "0.0%",
		"zero bytes downloaded must not show a contradictory 0.0% alongside Unknown bytes")
}

func TestNewCancelMux_NilRemovePostReturns404(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	// non-nil store, nil remove → POST /cancel must not be registered (no panic)
	mux := newCancelMux(store, cfg, nil, nil, nil)

	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)
	body := makeCancelFormBody("test-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	// Go's mux returns 405 when GET /cancel is registered but POST is not — safe, not a panic.
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code,
		"POST /cancel must not be registered when remove is nil")
}

// --- GET / (history page torrent button) ---

func makeGofeedItemWithEnclosure(title, guid, torrentURL string) *gofeed.Item {
	return &gofeed.Item{
		Title: title,
		GUID:  guid,
		Enclosures: []*gofeed.Enclosure{
			{URL: torrentURL, Type: "application/x-bittorrent"},
		},
	}
}

func TestHistoryPage_RendersTorrentButtonForSkippedWithURL(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("myfeed",
		makeGofeedItemWithEnclosure("My Title", "guid-1", "https://example.com/my.torrent"),
		"skipped", "lost preference contest", nil)
	h.AddOrUpdateRecord(rec)

	mux := newWebMux(h, makeRetryFunc(new(bool), nil, 0, nil), nil)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.Contains(t, body, `class="btn-torrent"`)
	assert.Contains(t, body, `data-feed="myfeed"`)
	assert.Contains(t, body, `data-guid="guid-1"`)
}

func TestHistoryPage_NoTorrentButtonWithoutURL(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("myfeed", makeGofeedItem("No Enclosure", "guid-2"), "excluded", "matched exclude", nil)
	h.AddOrUpdateRecord(rec)

	mux := newWebMux(h, makeRetryFunc(new(bool), nil, 0, nil), nil)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), `class="btn-torrent"`)
}

func TestHistoryPage_NoTorrentButtonForDispatched(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("myfeed",
		makeGofeedItemWithEnclosure("Already Dispatched", "guid-3", "https://example.com/my.torrent"),
		"dispatched", "", nil)
	h.AddOrUpdateRecord(rec)

	mux := newWebMux(h, makeRetryFunc(new(bool), nil, 0, nil), nil)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), `class="btn-torrent"`)
}

func TestHistoryPage_TorrentButtonShownWhenFeedConfiguredNil(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("myfeed",
		makeGofeedItemWithEnclosure("My Title", "guid-1", "https://example.com/my.torrent"),
		"skipped", "lost preference contest", nil)
	h.AddOrUpdateRecord(rec)

	// A nil feedConfigured means "no filter" — button shows unconditionally,
	// matching every existing caller that doesn't care about live config.
	mux := newWebMux(h, makeRetryFunc(new(bool), nil, 0, nil), nil)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `class="btn-torrent"`)
}

func TestHistoryPage_TorrentButtonHiddenWhenFeedNotConfigured(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("gone-feed",
		makeGofeedItemWithEnclosure("My Title", "guid-1", "https://example.com/my.torrent"),
		"skipped", "lost preference contest", nil)
	h.AddOrUpdateRecord(rec)

	feedConfigured := func(name string) bool { return name == "still-here" }
	mux := newWebMux(h, makeRetryFunc(new(bool), nil, 0, nil), feedConfigured)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), `class="btn-torrent"`,
		"button must be hidden when the record's feed is no longer configured")
}

func TestHistoryPage_TorrentButtonShownWhenFeedStillConfigured(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("still-here",
		makeGofeedItemWithEnclosure("My Title", "guid-1", "https://example.com/my.torrent"),
		"skipped", "lost preference contest", nil)
	h.AddOrUpdateRecord(rec)

	feedConfigured := func(name string) bool { return name == "still-here" }
	mux := newWebMux(h, makeRetryFunc(new(bool), nil, 0, nil), feedConfigured)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `class="btn-torrent"`)
}

// --- groupHistoryRows ---

func TestGroupHistoryRows_DispatchedIsPrimaryOverSkipped(t *testing.T) {
	records := []HistoryRecord{
		NewHistoryRecord("WSBK", makeGofeedItem("BSB Title", "guid-1"), "skipped", "no group matched labels", nil),
		NewHistoryRecord("WSS", makeGofeedItem("BSB Title", "guid-1"), "skipped", "no group matched labels", nil),
		NewHistoryRecord("BSB", makeGofeedItem("BSB Title", "guid-1"), "dispatched", "", nil),
		NewHistoryRecord("IOMTT", makeGofeedItem("BSB Title", "guid-1"), "skipped", "no group matched labels", nil),
	}

	rows := groupHistoryRows(records)

	require.Len(t, rows, 4)
	assert.True(t, rows[0].IsPrimary)
	assert.Equal(t, "BSB", rows[0].Feed, "the dispatched record must be the primary row")
	assert.Equal(t, 4, rows[0].GroupSize)

	for _, r := range rows[1:] {
		assert.False(t, r.IsPrimary)
		assert.Equal(t, 4, r.GroupSize)
	}
	// Non-primary rows are sorted alphabetically by feed for deterministic rendering.
	assert.Equal(t, "IOMTT", rows[1].Feed)
	assert.Equal(t, "WSBK", rows[2].Feed)
	assert.Equal(t, "WSS", rows[3].Feed)
}

func TestGroupHistoryRows_TieBreaksAlphabeticallyByFeed(t *testing.T) {
	records := []HistoryRecord{
		NewHistoryRecord("WSBK", makeGofeedItem("BSB Title", "guid-1"), "skipped", "no group matched labels", nil),
		NewHistoryRecord("BSB", makeGofeedItem("BSB Title", "guid-1"), "skipped", "better version already in cache", nil),
		NewHistoryRecord("IOMTT", makeGofeedItem("BSB Title", "guid-1"), "skipped", "no group matched labels", nil),
	}

	rows := groupHistoryRows(records)

	require.Len(t, rows, 3)
	assert.True(t, rows[0].IsPrimary)
	assert.Equal(t, "BSB", rows[0].Feed, "equal outcomeRank ties break alphabetically by feed name")
	assert.Equal(t, "IOMTT", rows[1].Feed)
	assert.Equal(t, "WSBK", rows[2].Feed)
}

func TestGroupHistoryRows_PrefersInformativeReasonOverNoGroupMatched(t *testing.T) {
	records := []HistoryRecord{
		NewHistoryRecord("Moto2", makeGofeedItem("MotoGP Title", "guid-1"), "skipped", skipReasonNoGroupMatched, nil),
		NewHistoryRecord("Moto3", makeGofeedItem("MotoGP Title", "guid-1"), "skipped", skipReasonNoGroupMatched, nil),
		NewHistoryRecord("MotoGP", makeGofeedItem("MotoGP Title", "guid-1"), "skipped", skipReasonCacheBetter, nil),
	}

	rows := groupHistoryRows(records)

	require.Len(t, rows, 3)
	assert.True(t, rows[0].IsPrimary)
	assert.Equal(t, "MotoGP", rows[0].Feed,
		"a record that actually matched this feed's groups but lost a preference contest is more "+
			"informative than a sibling feed that never matched at all, even though it sorts later alphabetically")
	assert.Equal(t, "Moto2", rows[1].Feed)
	assert.Equal(t, "Moto3", rows[2].Feed)
}

func TestGroupHistoryRows_NoGroupMatchedIsLowestPriorityAcrossOutcomes(t *testing.T) {
	records := []HistoryRecord{
		NewHistoryRecord("BSB", makeGofeedItem("BSB Title", "guid-1"), "skipped", skipReasonNoGroupMatched, nil),
		NewHistoryRecord("WSBK", makeGofeedItem("BSB Title", "guid-1"), "excluded", "matched exclude filter", nil),
	}

	rows := groupHistoryRows(records)

	require.Len(t, rows, 2)
	assert.True(t, rows[0].IsPrimary)
	assert.Equal(t, "WSBK", rows[0].Feed,
		"\"no group matched labels\" must be the lowest priority of all, even below an excluded outcome "+
			"which normally ranks worse than skipped per outcomeRank")
	assert.Equal(t, "BSB", rows[1].Feed)
}

func TestGroupHistoryRows_EmptyGUIDNeverMerges(t *testing.T) {
	records := []HistoryRecord{
		NewHistoryRecord("feedA", makeGofeedItem("Title A", ""), "skipped", "no group matched labels", nil),
		NewHistoryRecord("feedB", makeGofeedItem("Title B", ""), "skipped", "no group matched labels", nil),
	}

	rows := groupHistoryRows(records)

	require.Len(t, rows, 2)
	for _, r := range rows {
		assert.True(t, r.IsPrimary)
		assert.Equal(t, 1, r.GroupSize, "records with an empty GUID must never be grouped together")
	}
}

func TestGroupHistoryRows_SingletonGroupIsPrimary(t *testing.T) {
	records := []HistoryRecord{
		NewHistoryRecord("onlyfeed", makeGofeedItem("Solo Title", "guid-solo"), "skipped", "excluded by regex", nil),
	}

	rows := groupHistoryRows(records)

	require.Len(t, rows, 1)
	assert.True(t, rows[0].IsPrimary)
	assert.Equal(t, 1, rows[0].GroupSize)
}

func TestGroupHistoryRows_GroupsAreContiguousAtFirstAppearancePosition(t *testing.T) {
	records := []HistoryRecord{
		NewHistoryRecord("feedA", makeGofeedItem("First Title", "guid-1"), "skipped", "", nil),
		NewHistoryRecord("feedB", makeGofeedItem("Second Title", "guid-2"), "skipped", "", nil),
		NewHistoryRecord("feedC", makeGofeedItem("First Title", "guid-1"), "dispatched", "", nil),
	}

	rows := groupHistoryRows(records)

	require.Len(t, rows, 3)
	// guid-1 first appears at index 0, so both its records land contiguously
	// there (dispatched primary first), pushing guid-2's single record last.
	assert.Equal(t, "guid-1", rows[0].GUID)
	assert.Equal(t, "feedC", rows[0].Feed)
	assert.True(t, rows[0].IsPrimary)
	assert.Equal(t, 2, rows[0].GroupSize)

	assert.Equal(t, "guid-1", rows[1].GUID)
	assert.Equal(t, "feedA", rows[1].Feed)
	assert.False(t, rows[1].IsPrimary)
	assert.Equal(t, 2, rows[1].GroupSize)

	assert.Equal(t, "guid-2", rows[2].GUID)
	assert.Equal(t, 1, rows[2].GroupSize)
}

func TestHistoryPage_GroupsRecordsSharingGUID(t *testing.T) {
	h := emptyHistory()
	h.AddOrUpdateRecord(NewHistoryRecord("BSB",
		makeGofeedItemWithEnclosure("BSB Title", "guid-1", "https://example.com/bsb.torrent"),
		"dispatched", "", nil))
	h.AddOrUpdateRecord(NewHistoryRecord("WSS",
		makeGofeedItemWithEnclosure("BSB Title", "guid-1", "https://example.com/bsb.torrent"),
		"skipped", "no group matched labels", nil))
	h.AddOrUpdateRecord(NewHistoryRecord("WSBK",
		makeGofeedItemWithEnclosure("BSB Title", "guid-1", "https://example.com/bsb.torrent"),
		"skipped", "no group matched labels", nil))
	h.AddOrUpdateRecord(NewHistoryRecord("IOMTT",
		makeGofeedItemWithEnclosure("BSB Title", "guid-1", "https://example.com/bsb.torrent"),
		"skipped", "no group matched labels", nil))

	mux := newWebMux(h, makeRetryFunc(new(bool), nil, 0, nil), nil)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()

	assert.Equal(t, 1, strings.Count(body, `class="group-toggle"`),
		"exactly one primary row should carry the expand toggle")
	assert.Contains(t, body, "+3")
	assert.Equal(t, 3, strings.Count(body, `class="group-child"`),
		"the three skipped records must render as hidden-by-default child rows")
	assert.Equal(t, 3, strings.Count(body, `class="btn-torrent"`),
		"each skipped child keeps its own retry button; the dispatched primary has none")
}

func TestHistoryPage_UniqueGUIDRendersUngrouped(t *testing.T) {
	h := emptyHistory()
	h.AddOrUpdateRecord(NewHistoryRecord("myfeed", makeGofeedItem("Solo Title", "guid-solo"), "skipped", "excluded by regex", nil))

	mux := newWebMux(h, makeRetryFunc(new(bool), nil, 0, nil), nil)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.NotContains(t, body, `class="group-toggle"`)
	assert.NotContains(t, body, `class="group-child"`)
}

// --- POST /torrent ---

func makeRetryFunc(called *bool, gotRec *HistoryRecord, id int64, err error) retryFunc {
	return func(rec HistoryRecord) (int64, error) {
		*called = true
		if gotRec != nil {
			*gotRec = rec
		}
		return id, err
	}
}

func makeTorrentFormBody(feed, guid string) *strings.Reader {
	v := url.Values{}
	v.Set("feed", feed)
	v.Set("guid", guid)
	return strings.NewReader(v.Encode())
}

func TestPostTorrentHandler_Success(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("myfeed", makeGofeedItem("My Title", "guid-1"), "skipped", "", nil)
	h.AddOrUpdateRecord(rec)

	called := false
	var gotRec HistoryRecord
	mux := newWebMux(h, makeRetryFunc(&called, &gotRec, 7, nil), nil)

	req := httptest.NewRequest("POST", "/torrent", makeTorrentFormBody("myfeed", "guid-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called, "retry should have been invoked")
	assert.Equal(t, "myfeed", gotRec.Feed)
	assert.Equal(t, "guid-1", gotRec.GUID)
}

func TestPostTorrentHandler_UnknownRecord(t *testing.T) {
	h := emptyHistory()
	called := false
	mux := newWebMux(h, makeRetryFunc(&called, nil, 0, nil), nil)

	req := httptest.NewRequest("POST", "/torrent", makeTorrentFormBody("ghost-feed", "ghost-guid"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.False(t, called, "retry must not be called for an unknown record")
}

func TestPostTorrentHandler_RetryError(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("myfeed", makeGofeedItem("My Title", "guid-1"), "skipped", "", nil)
	h.AddOrUpdateRecord(rec)

	called := false
	mux := newWebMux(h, makeRetryFunc(&called, nil, 0, fmt.Errorf("boom: no torrent URL")), nil)

	req := httptest.NewRequest("POST", "/torrent", makeTorrentFormBody("myfeed", "guid-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.True(t, called)
	assert.NotEqual(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "boom: no torrent URL")
}

func TestPostTorrentHandler_NotRegisteredWhenRetryNil(t *testing.T) {
	h := emptyHistory()
	rec := NewHistoryRecord("myfeed", makeGofeedItem("My Title", "guid-1"), "skipped", "", nil)
	h.AddOrUpdateRecord(rec)

	mux := newWebMux(h, nil, nil)

	req := httptest.NewRequest("POST", "/torrent", makeTorrentFormBody("myfeed", "guid-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// Go's mux returns 405 (path only registered for GET /) rather than 404 — safe, not a panic.
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code,
		"POST /torrent must not be registered when retry is nil")
}

func TestPostTorrentHandler_NotRegisteredOnCancelMux(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("POST", "/torrent", makeTorrentFormBody("myfeed", "guid-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// --- access log behavioral tests ---

func TestGetCancelHandler_AccessLog_InvalidToken(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, _ := GenerateToken([]byte("secret"), "test-id", time.Hour)

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), lg)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=badsig", expires), nil)
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, buf.String(), "level=warning")
	assert.Contains(t, buf.String(), "result=invalid_token")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

func TestGetCancelHandler_AccessLog_MissingParams(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), lg)

	req := httptest.NewRequest("GET", "/cancel?id=test-id", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, buf.String(), "level=warning")
	assert.Contains(t, buf.String(), "result=invalid_token")
}

func TestGetCancelHandler_AccessLog_Expired(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", -time.Second)

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), lg)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusGone, rr.Code)
	assert.Contains(t, buf.String(), "level=warning")
	assert.Contains(t, buf.String(), "result=expired")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

func TestGetCancelHandler_AccessLog_NotFound(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "ghost-id", time.Hour)

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), lg)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=ghost-id&expires=%d&sig=%s", expires, sig), nil)
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, buf.String(), "level=warning")
	assert.Contains(t, buf.String(), "result=not_found")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

func TestGetCancelHandler_AccessLog_Success(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{Title: "Show"})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), lg)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, buf.String(), "level=info")
	assert.Contains(t, buf.String(), "result=ok")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

func TestGetCancelHandler_AccessLog_NilNoOp(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{Title: "Show"})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	req := httptest.NewRequest("GET",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// Must not panic and must still serve the page correctly.
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestPostCancelHandler_AccessLog_InvalidToken(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, _ := GenerateToken([]byte("secret"), "test-id", time.Hour)

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), lg)

	body := makeCancelFormBody("test-id", expires, "badsig")
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, buf.String(), "level=warning")
	assert.Contains(t, buf.String(), "result=invalid_token")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

func TestPostCancelHandler_AccessLog_Expired(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", -time.Second)

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), lg)

	body := makeCancelFormBody("test-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusGone, rr.Code)
	assert.Contains(t, buf.String(), "level=warning")
	assert.Contains(t, buf.String(), "result=expired")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

func TestPostCancelHandler_AccessLog_NotFound(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "ghost-id", time.Hour)

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc(), lg)

	body := makeCancelFormBody("ghost-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, buf.String(), "level=warning")
	assert.Contains(t, buf.String(), "result=not_found")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

func TestPostCancelHandler_AccessLog_Success(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	lg, buf := makeTestAccessLogger()
	removed := false
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(&removed), noProgressFunc(), lg)

	body := makeCancelFormBody("test-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, removed)
	assert.Contains(t, buf.String(), "level=info")
	assert.Contains(t, buf.String(), "result=cancelled")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

func TestPostCancelHandler_AccessLog_TransmissionError(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 77, CancelMetadata{})
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	lg, buf := makeTestAccessLogger()
	failRemove2 := func(_ context.Context, _ []int64) error {
		return fmt.Errorf("transmission unreachable")
	}
	mux := newWebMux(nil, nil, nil)
	registerCancelRoutes(mux, store, cfg, failRemove2, noProgressFunc(), lg)

	body := makeCancelFormBody("test-id", expires, sig)
	req := httptest.NewRequest("POST", "/cancel", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, buf.String(), "level=warning")
	assert.Contains(t, buf.String(), "result=error")
	assert.Contains(t, buf.String(), "client_ip=10.0.0.1")
}

// --- clientIP ---

func TestClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	// httptest.NewRequest sets RemoteAddr = "192.0.2.1:1234"
	assert.Equal(t, "192.0.2.1", clientIP(req))
}

func TestClientIP_RemoteAddr_IPv6(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::1]:1234"
	assert.Equal(t, "::1", clientIP(req))
}

func TestClientIP_RemoteAddr_NoPort(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1"
	assert.Equal(t, "192.0.2.1", clientIP(req))
}

func TestClientIP_CFConnectingIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("CF-Connecting-IP", "2.2.2.2")
	assert.Equal(t, "2.2.2.2", clientIP(req))
}

func TestClientIP_CFConnectingIP_BeatsXFF(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("CF-Connecting-IP", "2.2.2.2")
	req.Header.Set("X-Forwarded-For", "3.3.3.3")
	assert.Equal(t, "2.2.2.2", clientIP(req))
}

func TestClientIP_CFConnectingIPv6(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("CF-Connecting-IPv6", "2001:db8::1")
	assert.Equal(t, "2001:db8::1", clientIP(req))
}

func TestClientIP_CFConnectingIP_BeatsCFIPv6(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("CF-Connecting-IP", "2.2.2.2")
	req.Header.Set("CF-Connecting-IPv6", "2001:db8::1")
	assert.Equal(t, "2.2.2.2", clientIP(req))
}

func TestClientIP_XForwardedFor_Single(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	assert.Equal(t, "1.2.3.4", clientIP(req))
}

func TestClientIP_XForwardedFor_Multiple(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	assert.Equal(t, "1.2.3.4", clientIP(req))
}

func TestClientIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "9.9.9.9")
	assert.Equal(t, "9.9.9.9", clientIP(req))
}

func TestClientIP_XForwardedFor_BeatsXRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "9.9.9.9")
	assert.Equal(t, "1.2.3.4", clientIP(req))
}

// --- newAccessLogger ---

func TestNewAccessLogger_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	lg, err := newAccessLogger(path)
	require.NoError(t, err)
	require.NotNil(t, lg)
	lg.Info("ping")
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	body := string(data)
	assert.Contains(t, body, "ping")
	assert.Contains(t, body, "time=")
}

func TestNewAccessLogger_AppendMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	require.NoError(t, os.WriteFile(path, []byte("existing line\n"), 0600))
	lg, err := newAccessLogger(path)
	require.NoError(t, err)
	lg.Info("new entry")
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	body := string(data)
	assert.Contains(t, body, "existing line")
	assert.Contains(t, body, "new entry")
}

func TestNewAccessLogger_InvalidPath(t *testing.T) {
	lg, err := newAccessLogger("/no_such_directory_xyz/access.log")
	assert.Error(t, err)
	assert.Nil(t, lg)
}

// makeTestAccessLogger returns a logrus logger writing to a buffer for testing.
func makeTestAccessLogger() (*logrus.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	lg := logrus.New()
	lg.SetOutput(buf)
	lg.SetLevel(logrus.InfoLevel)
	lg.SetFormatter(&logrus.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: false,
		FullTimestamp:    true,
	})
	return lg, buf
}

func TestParseListenAddr(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"8080", "127.0.0.1:8080", false},
		{"127.0.0.1:8080", "127.0.0.1:8080", false},
		{"0.0.0.0:9090", "0.0.0.0:9090", false},
		{"[::1]:8080", "[::1]:8080", false},
		{"notaport", "", true},
		{"999999", "", true},
		{"0", "", true},
		{"-1", "", true},
		{"host:notaport", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseListenAddr(tc.input)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

// --- POST /notify-complete ---

func makeNotifyCompleteMux(t *testing.T, ntfyCfg NtfyConfig, cancelCfg CancelConfig) *http.ServeMux {
	t.Helper()
	require.NoError(t, ntfyCfg.Validate())
	mux := http.NewServeMux()
	registerNotifyCompleteRoute(mux, ntfyCfg, cancelCfg, nil)
	return mux
}

func TestNotifyComplete_Success(t *testing.T) {
	var captured *http.Request
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, CancelConfig{})
	body := bytes.NewBufferString(`{"name":"My.Show.S01E01","dir":"/downloads","id":42}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, captured, "ntfy should have received a request")
	assert.Equal(t, "Torrent Complete", captured.Header.Get("Title"))
	assert.Equal(t, "default", captured.Header.Get("Priority"))
}

func TestNotifyComplete_BadJSON(t *testing.T) {
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, CancelConfig{})
	req := httptest.NewRequest("POST", "/notify-complete", bytes.NewBufferString(`not-json`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestNotifyComplete_NtfyError(t *testing.T) {
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer ntfySrv.Close()

	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, CancelConfig{})
	body := bytes.NewBufferString(`{"name":"My.Show","dir":"/dl","id":1}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestNotifyComplete_CustomTemplate(t *testing.T) {
	var captured *http.Request
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	mux := makeNotifyCompleteMux(t, NtfyConfig{
		BaseURL:        ntfySrv.URL,
		Topic:          "t",
		CompletedTitle: "Done: {{.Title}}",
	}, CancelConfig{})
	body := bytes.NewBufferString(`{"name":"My.Show.S01E01","dir":"/dl","id":7}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Done: My.Show.S01E01", captured.Header.Get("Title"))
}

func TestNotifyComplete_CustomPriority(t *testing.T) {
	var captured *http.Request
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	mux := makeNotifyCompleteMux(t, NtfyConfig{
		BaseURL:           ntfySrv.URL,
		Topic:             "t",
		CompletedPriority: "high",
	}, CancelConfig{})
	body := bytes.NewBufferString(`{"name":"My.Show","dir":"/dl","id":3}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "high", captured.Header.Get("Priority"))
}

func TestNotifyComplete_NotRegisteredWhenNtfyUnconfigured(t *testing.T) {
	mux := http.NewServeMux()
	ntfyCfg := NtfyConfig{} // no BaseURL/Topic
	require.NoError(t, ntfyCfg.Validate())
	registerNotifyCompleteRoute(mux, ntfyCfg, CancelConfig{}, nil)

	req := httptest.NewRequest("POST", "/notify-complete", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestNotifyComplete_Auth_NoSecretConfigured(t *testing.T) {
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	// HMACSecret empty → no auth required
	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, CancelConfig{})
	body := bytes.NewBufferString(`{"name":"My.Show","dir":"/dl","id":1}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestNotifyComplete_Auth_MissingHeader(t *testing.T) {
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	cancelCfg := CancelConfig{HMACSecret: "supersecret"}
	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, cancelCfg)
	body := bytes.NewBufferString(`{"name":"My.Show","dir":"/dl","id":1}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	// No Authorization header
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestNotifyComplete_Auth_WrongToken(t *testing.T) {
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	cancelCfg := CancelConfig{HMACSecret: "supersecret"}
	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, cancelCfg)
	body := bytes.NewBufferString(`{"name":"My.Show","dir":"/dl","id":1}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestNotifyComplete_Auth_CorrectToken(t *testing.T) {
	var captured *http.Request
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	cancelCfg := CancelConfig{HMACSecret: "supersecret"}
	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, cancelCfg)
	body := bytes.NewBufferString(`{"name":"My.Show","dir":"/dl","id":1}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	req.Header.Set("Authorization", "Bearer supersecret")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotNil(t, captured, "ntfy should have been called with correct token")
}

func TestNotifyComplete_EmptyName(t *testing.T) {
	requestCount := 0
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, CancelConfig{})
	body := bytes.NewBufferString(`{"name":"","dir":"/dl","id":1}`)
	req := httptest.NewRequest("POST", "/notify-complete", body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, 0, requestCount, "ntfy should not be called with empty name")
}

func TestNotifyComplete_BodyTooLarge(t *testing.T) {
	requestCount := 0
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	mux := makeNotifyCompleteMux(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}, CancelConfig{})
	// Build a valid JSON body that exceeds 1 MB.
	huge := `{"name":"` + strings.Repeat("x", 2<<20) + `","dir":"/dl","id":1}`
	req := httptest.NewRequest("POST", "/notify-complete", strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, 0, requestCount, "ntfy should not be called for oversized body")
}

func TestNotifyComplete_SizeIsUnknown(t *testing.T) {
	var body []byte
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	// When no size is available in the completion hook, Size should be "Unknown".
	mux := makeNotifyCompleteMux(t, NtfyConfig{
		BaseURL:       ntfySrv.URL,
		Topic:         "t",
		CompletedBody: "{{.Size}}",
	}, CancelConfig{})
	req := httptest.NewRequest("POST", "/notify-complete", bytes.NewBufferString(`{"name":"My.Show","dir":"/dl","id":1}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Unknown", string(body))
}

func TestStartWebServer_LogsName(t *testing.T) {
	origLog := log
	defer func() { log = origLog }()

	lg, buf := makeTestAccessLogger()
	log = lg

	// An invalid address makes net.Listen fail immediately, so
	// startWebServer returns without blocking.
	startWebServer("public", http.NewServeMux(), "not-a-valid-addr")

	assert.Contains(t, buf.String(), "Starting public web server on http://not-a-valid-addr")
}
