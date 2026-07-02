package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
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

	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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

// --- access log behavioral tests ---

func TestGetCancelHandler_AccessLog_InvalidToken(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, _ := GenerateToken([]byte("secret"), "test-id", time.Hour)

	lg, buf := makeTestAccessLogger()
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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

	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
	mux := newWebMux(nil)
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
