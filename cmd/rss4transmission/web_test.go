package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- /healthz ---

func TestHealthzHandler(t *testing.T) {
	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), getProgress)

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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), errProgress)

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
	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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

	mux := newWebMux(nil)
	removed := false
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(&removed), noProgressFunc())

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
	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(&removed), noProgressFunc())

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
	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestNewCancelMux_CancelReachable(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

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
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)), noProgressFunc())

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestNewCancelMux_NilStoreHealthzStillWorks(t *testing.T) {
	cfg := makeCancelCfg("", "")
	mux := newCancelMux(nil, cfg, nil, nil)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestNewCancelMux_NilStoreCancelReturns404(t *testing.T) {
	cfg := makeCancelCfg("", "")
	mux := newCancelMux(nil, cfg, nil, nil)

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
	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, failRemove, noProgressFunc())

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
	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)), makeProgressFunc(0, 0.0))

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
	mux := newCancelMux(store, cfg, nil, nil)

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

func TestParseHistoryAddr(t *testing.T) {
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
			got, err := parseHistoryAddr(tc.input)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
