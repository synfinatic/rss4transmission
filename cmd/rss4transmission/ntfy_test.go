package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendTorrentStarted_Headers(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewNtfyClient(NtfyConfig{
		BaseURL: srv.URL,
		Topic:   "mytopic",
		Token:   "tk_testtoken",
	})
	err := c.SendTorrentStarted("My.Show.S01E01", "4.32 GB", "https://example.com/cancel?id=x")
	require.NoError(t, err)
	require.NotNil(t, captured)

	assert.Equal(t, "POST", captured.Method)
	assert.Equal(t, "/mytopic", captured.URL.Path)
	assert.Equal(t, "Torrent Started", captured.Header.Get("Title"))
	assert.Equal(t, "default", captured.Header.Get("Priority"))

	wantAction := "view, Cancel Download, https://example.com/cancel?id=x"
	assert.Equal(t, wantAction, captured.Header.Get("Actions"))

	assert.Equal(t, "Bearer tk_testtoken", captured.Header.Get("Authorization"))
}

func TestSendTorrentStarted_Body(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewNtfyClient(NtfyConfig{BaseURL: srv.URL, Topic: "t"})
	require.NoError(t, c.SendTorrentStarted("My.Show.S01E01", "4.32 GB", "https://example.com/cancel"))
	assert.Contains(t, string(body), "My.Show.S01E01")
	assert.Contains(t, string(body), "4.32 GB")
}

func TestSendTorrentStarted_NoSize(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewNtfyClient(NtfyConfig{BaseURL: srv.URL, Topic: "t"})
	require.NoError(t, c.SendTorrentStarted("My.Show.S01E01", "", ""))
	assert.Equal(t, "My.Show.S01E01", string(body))
}

func TestSendTorrentCompleted_Headers(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewNtfyClient(NtfyConfig{
		BaseURL: srv.URL,
		Topic:   "mytopic",
		Token:   "tk_testtoken",
	})
	err := c.SendTorrentCompleted("My.Show.S01E01")
	require.NoError(t, err)
	require.NotNil(t, captured)

	assert.Equal(t, "POST", captured.Method)
	assert.Equal(t, "/mytopic", captured.URL.Path)
	assert.Equal(t, "Torrent Complete", captured.Header.Get("Title"))
	assert.Equal(t, "default", captured.Header.Get("Priority"))
	assert.Empty(t, captured.Header.Get("Actions"), "completed notification should have no actions")
}

func TestSendTorrentStarted_NoCancelURL(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewNtfyClient(NtfyConfig{BaseURL: srv.URL, Topic: "mytopic", Token: "tk_testtoken"})
	err := c.SendTorrentStarted("My.Show.S01E01", "", "")
	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Empty(t, captured.Header.Get("Actions"), "empty cancelURL must produce no Actions header")
}

func TestSendTorrentStarted_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewNtfyClient(NtfyConfig{BaseURL: srv.URL, Topic: "t"})
	err := c.SendTorrentStarted("title", "1.23 GB", "https://example.com/cancel")
	require.Error(t, err)
}
