package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// --- /cancel ---

func makeCancelCfg(secret, baseURL string) CancelConfig {
	return CancelConfig{HMACSecret: secret, BaseURL: baseURL, TokenTTLH: 24}
}

func makeRemoveFunc(called *bool) removeFunc {
	return func(_ context.Context, _ []int64) error {
		*called = true
		return nil
	}
}

func TestCancelHandler_Valid(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42)

	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)

	removed := false
	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(&removed))

	req := httptest.NewRequest("DELETE",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, removed, "Transmission remove should have been called")
}

func TestCancelHandler_MissingParams(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)))

	req := httptest.NewRequest("DELETE", "/cancel?id=test-id", nil) // missing expires and sig
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCancelHandler_BadSignature(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42)
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, _ := GenerateToken([]byte("secret"), "test-id", time.Hour)

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)))

	req := httptest.NewRequest("DELETE",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=badsig", expires), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCancelHandler_Expired(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42)
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "test-id", -time.Second) // already expired

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)))

	req := httptest.NewRequest("DELETE",
		fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusGone, rr.Code)
}

func TestCancelHandler_NotFound(t *testing.T) {
	store := NewStore(time.Hour)
	// nothing registered
	cfg := makeCancelCfg("secret", "https://example.com")
	expires, sig := GenerateToken([]byte("secret"), "ghost-id", time.Hour)

	mux := newWebMux(nil)
	registerCancelRoutes(mux, store, cfg, makeRemoveFunc(new(bool)))

	req := httptest.NewRequest("DELETE",
		fmt.Sprintf("/cancel?id=ghost-id&expires=%d&sig=%s", expires, sig), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// --- newCancelMux ---

func TestNewCancelMux_HealthzReachable(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)))

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestNewCancelMux_CancelReachable(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)))

	// A DELETE with missing params should return 400, not 404 — proving the route exists.
	req := httptest.NewRequest("DELETE", "/cancel?id=x", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestNewCancelMux_HistoryNotReachable(t *testing.T) {
	store := NewStore(time.Hour)
	cfg := makeCancelCfg("secret", "https://example.com")
	mux := newCancelMux(store, cfg, makeRemoveFunc(new(bool)))

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestNewCancelMux_NilStoreHealthzStillWorks(t *testing.T) {
	cfg := makeCancelCfg("", "")
	mux := newCancelMux(nil, cfg, nil)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestNewCancelMux_NilStoreCancelReturns404(t *testing.T) {
	cfg := makeCancelCfg("", "")
	mux := newCancelMux(nil, cfg, nil)

	req := httptest.NewRequest("DELETE", "/cancel?id=x&expires=1&sig=y", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
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
