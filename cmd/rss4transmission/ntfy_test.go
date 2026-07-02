package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustValidateNtfyConfig(t *testing.T, cfg NtfyConfig) NtfyConfig {
	t.Helper()
	require.NoError(t, cfg.Validate())
	return cfg
}

func TestSendTorrentStarted_Headers(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL: srv.URL,
		Topic:   "mytopic",
		Token:   "tk_testtoken",
	})
	c := NewNtfyClient(cfg)
	err := c.SendTorrentStarted(&NtfyTemplateContext{
		Title:     "My.Show.S01E01",
		Size:      "4.32 GB",
		CancelURL: "https://example.com/cancel?id=x",
	})
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

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, Topic: "t"})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentStarted(&NtfyTemplateContext{
		Title:     "My.Show.S01E01",
		Size:      "4.32 GB",
		CancelURL: "https://example.com/cancel",
	}))
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

	// In production Size is always set by sendNtfyStarted via formatGB().
	// The default template always renders both fields; when Size is "" the body
	// ends with a newline but no size text.
	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, Topic: "t"})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentStarted(&NtfyTemplateContext{Title: "My.Show.S01E01"}))
	assert.Equal(t, "My.Show.S01E01\n", string(body))
}

func TestSendTorrentCompleted_Headers(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL: srv.URL,
		Topic:   "mytopic",
		Token:   "tk_testtoken",
	})
	c := NewNtfyClient(cfg)
	err := c.SendTorrentCompleted(&NtfyTemplateContext{Title: "My.Show.S01E01"})
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

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, Topic: "mytopic", Token: "tk_testtoken"})
	c := NewNtfyClient(cfg)
	err := c.SendTorrentStarted(&NtfyTemplateContext{Title: "My.Show.S01E01"})
	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Empty(t, captured.Header.Get("Actions"), "empty cancelURL must produce no Actions header")
}

func TestNtfyClient_TrailingSlashNormalized(t *testing.T) {
	var requestPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL + "/", Topic: "mytopic"})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentStarted(&NtfyTemplateContext{Title: "title"}))
	assert.Equal(t, "/mytopic", requestPath,
		"trailing slash on BaseURL must not produce double-slash path")
}

func TestNtfyClient_Timeout(t *testing.T) {
	blocked := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer func() {
		close(blocked)
		slow.Close()
	}()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: slow.URL, Topic: "t"})
	c := NewNtfyClient(cfg)
	c.client.Timeout = 100 * time.Millisecond
	err := c.SendTorrentStarted(&NtfyTemplateContext{Title: "title"})
	require.Error(t, err, "a hung server should trigger the client timeout")
}

func TestSendTorrentStarted_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, Topic: "t"})
	c := NewNtfyClient(cfg)
	err := c.SendTorrentStarted(&NtfyTemplateContext{Title: "title", Size: "1.23 GB", CancelURL: "https://example.com/cancel"})
	require.Error(t, err)
}

// --- Template customization tests ---

func TestSendTorrentStarted_CustomTitleTemplate(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:      srv.URL,
		Topic:        "t",
		StartedTitle: "{{.FeedName}}: {{.Title}}",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentStarted(&NtfyTemplateContext{
		Title:    "My.Show.S01E01",
		FeedName: "shows",
	}))
	assert.Equal(t, "shows: My.Show.S01E01", captured.Header.Get("Title"))
}

func TestSendTorrentStarted_CustomBodyTemplate(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:     srv.URL,
		Topic:       "t",
		StartedBody: "{{.FeedName}}: {{.Title}}",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentStarted(&NtfyTemplateContext{
		Title:    "My.Show.S01E01",
		FeedName: "shows",
	}))
	assert.Equal(t, "shows: My.Show.S01E01", string(body))
}

func TestSendTorrentStarted_CustomPriority(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:         srv.URL,
		Topic:           "t",
		StartedPriority: "high",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentStarted(&NtfyTemplateContext{Title: "My.Show"}))
	assert.Equal(t, "high", captured.Header.Get("Priority"))
}

func TestSendTorrentCompleted_CustomTitleTemplate(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:        srv.URL,
		Topic:          "t",
		CompletedTitle: "Done: {{.Title}}",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentCompleted(&NtfyTemplateContext{Title: "My.Show"}))
	assert.Equal(t, "Done: My.Show", captured.Header.Get("Title"))
}

func TestSendTorrentCompleted_CustomBodyTemplate(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:       srv.URL,
		Topic:         "t",
		CompletedBody: "Saved to {{.Dir}}",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentCompleted(&NtfyTemplateContext{
		Title: "My.Show",
		Dir:   "/downloads",
	}))
	assert.Equal(t, "Saved to /downloads", string(body))
}

func TestSendTorrentCompleted_CustomPriority(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:           srv.URL,
		Topic:             "t",
		CompletedPriority: "low",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendTorrentCompleted(&NtfyTemplateContext{Title: "My.Show"}))
	assert.Equal(t, "low", captured.Header.Get("Priority"))
}

// --- NtfyConfig.Validate() ---

func TestNtfyConfig_Validate_Defaults(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "test"}
	require.NoError(t, cfg.Validate())

	assert.Equal(t, "Torrent Started", cfg.StartedTitle)
	assert.Equal(t, "Torrent Complete", cfg.CompletedTitle)
	assert.Equal(t, "default", cfg.StartedPriority)
	assert.Equal(t, "default", cfg.CompletedPriority)

	ctx := &NtfyTemplateContext{Title: "T", Size: "4.32 GB"}
	out, err := renderTemplate(cfg.startedTitleTmpl, ctx)
	require.NoError(t, err)
	assert.Equal(t, "Torrent Started", out)

	out, err = renderTemplate(cfg.startedBodyTmpl, ctx)
	require.NoError(t, err)
	assert.Equal(t, "T\n4.32 GB", out)

	out, err = renderTemplate(cfg.completedTitleTmpl, ctx)
	require.NoError(t, err)
	assert.Equal(t, "Torrent Complete", out)
}

func TestNtfyConfig_Validate_CustomTemplate(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "test", StartedTitle: "{{.FeedName}} started"}
	require.NoError(t, cfg.Validate())

	ctx := &NtfyTemplateContext{FeedName: "shows"}
	out, err := renderTemplate(cfg.startedTitleTmpl, ctx)
	require.NoError(t, err)
	assert.Equal(t, "shows started", out)
}

func TestNtfyConfig_Validate_InvalidTemplate(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "test", StartedTitle: "{{.Unclosed"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "StartedTitle")
}

func TestNtfyConfig_Validate_InvalidPriority(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "test", StartedPriority: "urgent"}
	err := cfg.Validate()
	require.Error(t, err)
}

func TestNtfyConfig_Validate_ValidPriorities(t *testing.T) {
	for _, p := range []string{"min", "low", "default", "high", "max"} {
		cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "test", StartedPriority: p, CompletedPriority: p}
		require.NoError(t, cfg.Validate(), "priority %q should be valid", p)
	}
}

func TestNtfyConfig_Validate_SkipsWhenDisabled(t *testing.T) {
	// Invalid template should not cause an error when ntfy is disabled (no BaseURL/Topic).
	cfg := NtfyConfig{StartedTitle: "{{.Unclosed"}
	require.NoError(t, cfg.Validate(), "invalid template should be ignored when ntfy is disabled")
}
