package main

import (
	"fmt"
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

// captureNtfyRequest spins up a stub ntfy server, sends a notification via
// send, and returns the captured request for header assertions.
func captureNtfyRequest(t *testing.T, alertTopic string, send func(*NtfyClient) error) *http.Request {
	t.Helper()
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: alertTopic})
	require.NoError(t, send(NewNtfyClient(cfg)))
	require.NotNil(t, captured)
	return captured
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

	wantAction := "view, More Info, https://example.com/cancel?id=x"
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

// --- Config-reload notification: NtfyConfig.Validate() ---

func TestNtfyConfig_Validate_AlertTopicDefaults(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic"}
	require.NoError(t, cfg.Validate())

	assert.Equal(t, "Config Reloaded", cfg.ConfigReloadedTitle)
	assert.Equal(t, "{{.ConfigFile}}", cfg.ConfigReloadedBody)
	assert.Equal(t, "low", cfg.ConfigReloadedPriority)
	assert.Equal(t, "Config Reload FAILED", cfg.ConfigFailedTitle)
	assert.Equal(t, "{{.ConfigFile}}\n{{.Error}}", cfg.ConfigFailedBody)
	assert.Equal(t, "high", cfg.ConfigFailedPriority)
	assert.Equal(t, "Transmission Port Closed", cfg.PortClosedTitle)
	assert.Equal(t, "{{.Reason}}", cfg.PortClosedBody)
	assert.Equal(t, "high", cfg.PortClosedPriority)
	assert.Equal(t, "Transmission Port Open", cfg.PortOpenedTitle)
	assert.Equal(t, "{{.Reason}}", cfg.PortOpenedBody)
	assert.Equal(t, "default", cfg.PortOpenedPriority)

	// Topic wasn't set, so the Started/Completed block must not have run.
	assert.Nil(t, cfg.startedTitleTmpl)
	assert.Nil(t, cfg.completedTitleTmpl)
	assert.NotNil(t, cfg.configReloadedTitleTmpl)
	assert.NotNil(t, cfg.configFailedBodyTmpl)
	assert.NotNil(t, cfg.portClosedTitleTmpl)
	assert.NotNil(t, cfg.portClosedBodyTmpl)
	assert.NotNil(t, cfg.portOpenedTitleTmpl)
	assert.NotNil(t, cfg.portOpenedBodyTmpl)
}

func TestNtfyConfig_Validate_TopicOnlyDoesNotRequireAlertTopic(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "t"}
	require.NoError(t, cfg.Validate())

	assert.Equal(t, "Torrent Started", cfg.StartedTitle)
	assert.NotNil(t, cfg.startedTitleTmpl)
	assert.Nil(t, cfg.configReloadedTitleTmpl)
}

func TestNtfyConfig_Validate_BothTopicsIndependentlyConfigurable(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "t", AlertTopic: "cfgtopic"}
	require.NoError(t, cfg.Validate())

	assert.NotNil(t, cfg.startedTitleTmpl)
	assert.NotNil(t, cfg.startedBodyTmpl)
	assert.NotNil(t, cfg.completedTitleTmpl)
	assert.NotNil(t, cfg.completedBodyTmpl)
	assert.NotNil(t, cfg.configReloadedTitleTmpl)
	assert.NotNil(t, cfg.configReloadedBodyTmpl)
	assert.NotNil(t, cfg.configFailedTitleTmpl)
	assert.NotNil(t, cfg.configFailedBodyTmpl)
}

func TestNtfyConfig_Validate_SkipsConfigBlockWhenAlertTopicEmpty(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "t", ConfigReloadedTitle: "{{.Unclosed"}
	require.NoError(t, cfg.Validate(), "invalid ConfigReloadedTitle should be ignored when AlertTopic is unset")
}

func TestNtfyConfig_Validate_InvalidConfigReloadedTemplate(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic", ConfigReloadedTitle: "{{.Unclosed"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ConfigReloadedTitle")
}

func TestNtfyConfig_Validate_InvalidConfigFailedTemplate(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic", ConfigFailedBody: "{{.Unclosed"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ConfigFailedBody")
}

func TestNtfyConfig_Validate_InvalidConfigReloadedPriority(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic", ConfigReloadedPriority: "urgent"}
	require.Error(t, cfg.Validate())
}

func TestNtfyConfig_Validate_InvalidConfigFailedPriority(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic", ConfigFailedPriority: "urgent"}
	require.Error(t, cfg.Validate())
}

func TestNtfyConfig_Validate_InvalidPortClosedTemplate(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic", PortClosedBody: "{{.Unclosed"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PortClosedBody")
}

func TestNtfyConfig_Validate_InvalidPortClosedPriority(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic", PortClosedPriority: "urgent"}
	require.Error(t, cfg.Validate())
}

func TestNtfyConfig_Validate_SkipsPortClosedBlockWhenAlertTopicEmpty(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "t", PortClosedBody: "{{.Unclosed"}
	require.NoError(t, cfg.Validate(), "invalid PortClosedBody should be ignored when AlertTopic is unset")
}

func TestNtfyConfig_Validate_InvalidPortOpenedTemplate(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic", PortOpenedBody: "{{.Unclosed"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PortOpenedBody")
}

func TestNtfyConfig_Validate_InvalidPortOpenedPriority(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "cfgtopic", PortOpenedPriority: "urgent"}
	require.Error(t, cfg.Validate())
}

func TestNtfyConfig_Validate_SkipsPortOpenedBlockWhenAlertTopicEmpty(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "t", PortOpenedBody: "{{.Unclosed"}
	require.NoError(t, cfg.Validate(), "invalid PortOpenedBody should be ignored when AlertTopic is unset")
}

// --- Port-closed notification: NtfyClient.SendPortClosed ---

func TestSendPortClosed_Headers(t *testing.T) {
	captured := captureNtfyRequest(t, "alerts", func(c *NtfyClient) error {
		return c.SendPortClosed(&NtfyPortContext{Reason: "port closed"})
	})

	assert.Equal(t, "POST", captured.Method)
	assert.Equal(t, "/alerts", captured.URL.Path)
	assert.Equal(t, "Transmission Port Closed", captured.Header.Get("Title"))
	assert.Equal(t, "high", captured.Header.Get("Priority"))
}

func TestSendPortClosed_BodyIncludesReason(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "alerts"})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendPortClosed(&NtfyPortContext{Reason: "port not open 60s after startup"}))
	assert.Equal(t, "port not open 60s after startup", string(body))
}

func TestSendPortClosed_CustomTemplate(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:         srv.URL,
		AlertTopic:      "alerts",
		PortClosedTitle: "Peer port down: {{.Reason}}",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendPortClosed(&NtfyPortContext{Reason: "closed"}))
	assert.Equal(t, "Peer port down: closed", captured.Header.Get("Title"))
}

// --- Port-reopened notification: NtfyClient.SendPortOpened ---

func TestSendPortOpened_Headers(t *testing.T) {
	captured := captureNtfyRequest(t, "alerts", func(c *NtfyClient) error {
		return c.SendPortOpened(&NtfyPortContext{Reason: "port reopened"})
	})

	assert.Equal(t, "POST", captured.Method)
	assert.Equal(t, "/alerts", captured.URL.Path)
	assert.Equal(t, "Transmission Port Open", captured.Header.Get("Title"))
	assert.Equal(t, "default", captured.Header.Get("Priority"))
}

func TestSendPortOpened_BodyIncludesReason(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "alerts"})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendPortOpened(&NtfyPortContext{Reason: "port reopened"}))
	assert.Equal(t, "port reopened", string(body))
}

func TestSendPortOpened_CustomTemplate(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:         srv.URL,
		AlertTopic:      "alerts",
		PortOpenedTitle: "Peer port back up: {{.Reason}}",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendPortOpened(&NtfyPortContext{Reason: "open"}))
	assert.Equal(t, "Peer port back up: open", captured.Header.Get("Title"))
}

// --- notifyPortOpened ---

func TestNotifyPortOpened_NoopWhenAlertTopicEmpty(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifyPortOpened(NtfyConfig{BaseURL: srv.URL}, "port reopened")
	assert.Equal(t, 0, requests)
}

func TestNotifyPortOpened_NoopWhenBaseURLEmpty(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifyPortOpened(NtfyConfig{AlertTopic: "alerts"}, "port reopened")
	assert.Equal(t, 0, requests)
}

func TestNotifyPortOpened_Sends(t *testing.T) {
	var captured *http.Request
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "alerts"})
	notifyPortOpened(cfg, "port reopened")
	assert.Equal(t, 1, requests)
	require.NotNil(t, captured)
	assert.Equal(t, "Transmission Port Open", captured.Header.Get("Title"))
}

func TestNotifyPortOpened_SendFailureIsNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "alerts"})
	assert.NotPanics(t, func() {
		notifyPortOpened(cfg, "port reopened")
	})
}

// --- notifyPortClosed ---

func TestNotifyPortClosed_NoopWhenAlertTopicEmpty(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifyPortClosed(NtfyConfig{BaseURL: srv.URL}, "port closed")
	assert.Equal(t, 0, requests)
}

func TestNotifyPortClosed_NoopWhenBaseURLEmpty(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifyPortClosed(NtfyConfig{AlertTopic: "alerts"}, "port closed")
	assert.Equal(t, 0, requests)
}

func TestNotifyPortClosed_Sends(t *testing.T) {
	var captured *http.Request
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "alerts"})
	notifyPortClosed(cfg, "port not open 60s after startup")
	assert.Equal(t, 1, requests)
	require.NotNil(t, captured)
	assert.Equal(t, "Transmission Port Closed", captured.Header.Get("Title"))
}

func TestNotifyPortClosed_SendFailureIsNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "alerts"})
	assert.NotPanics(t, func() {
		notifyPortClosed(cfg, "port closed")
	})
}

// --- Config-reload notification: NtfyClient.SendConfigReload{Success,Failure} ---

func TestSendConfigReloadSuccess_Headers(t *testing.T) {
	captured := captureNtfyRequest(t, "cfgtopic", func(c *NtfyClient) error {
		return c.SendConfigReloadSuccess(&NtfyConfigContext{ConfigFile: "config.yaml"})
	})

	assert.Equal(t, "POST", captured.Method)
	assert.Equal(t, "/cfgtopic", captured.URL.Path)
	assert.Equal(t, "Config Reloaded", captured.Header.Get("Title"))
	assert.Equal(t, "low", captured.Header.Get("Priority"))
}

func TestSendConfigReloadSuccess_Body(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "cfgtopic"})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendConfigReloadSuccess(&NtfyConfigContext{ConfigFile: "/etc/rss4transmission/config.yaml"}))
	assert.Equal(t, "/etc/rss4transmission/config.yaml", string(body))
}

func TestSendConfigReloadFailure_Headers(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "cfgtopic"})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendConfigReloadFailure(&NtfyConfigContext{ConfigFile: "config.yaml", Error: "boom"}))
	require.NotNil(t, captured)

	assert.Equal(t, "/cfgtopic", captured.URL.Path)
	assert.Equal(t, "Config Reload FAILED", captured.Header.Get("Title"))
	assert.Equal(t, "high", captured.Header.Get("Priority"))
}

func TestSendConfigReloadFailure_BodyIncludesError(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "cfgtopic"})
	c := NewNtfyClient(cfg)
	wantErr := "yaml: line 4: did not find expected key"
	require.NoError(t, c.SendConfigReloadFailure(&NtfyConfigContext{ConfigFile: "config.yaml", Error: wantErr}))
	assert.Contains(t, string(body), wantErr)
}

func TestSendConfigReloadSuccess_CustomTitleTemplate(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:             srv.URL,
		AlertTopic:          "cfgtopic",
		ConfigReloadedTitle: "Reloaded: {{.ConfigFile}}",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendConfigReloadSuccess(&NtfyConfigContext{ConfigFile: "config.yaml"}))
	assert.Equal(t, "Reloaded: config.yaml", captured.Header.Get("Title"))
}

func TestSendConfigReloadFailure_CustomBodyTemplate(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{
		BaseURL:          srv.URL,
		AlertTopic:       "cfgtopic",
		ConfigFailedBody: "FAILED {{.ConfigFile}}: {{.Error}}",
	})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendConfigReloadFailure(&NtfyConfigContext{ConfigFile: "config.yaml", Error: "boom"}))
	assert.Equal(t, "FAILED config.yaml: boom", string(body))
}

func TestSendConfigReload_UsesAlertTopicNotTopic(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, Topic: "torrents", AlertTopic: "configevents"})
	c := NewNtfyClient(cfg)
	require.NoError(t, c.SendConfigReloadSuccess(&NtfyConfigContext{ConfigFile: "config.yaml"}))
	require.NotNil(t, captured)
	assert.Equal(t, "/configevents", captured.URL.Path)
}

// --- notifyConfigReload ---

func TestNotifyConfigReload_NoopWhenAlertTopicEmpty(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifyConfigReload(NtfyConfig{BaseURL: srv.URL}, "config.yaml", nil)
	assert.Equal(t, 0, requests)
}

func TestNotifyConfigReload_NoopWhenBaseURLEmpty(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifyConfigReload(NtfyConfig{AlertTopic: "cfgtopic"}, "config.yaml", nil)
	assert.Equal(t, 0, requests)
}

func TestNotifyConfigReload_SendsSuccessOnNilError(t *testing.T) {
	var captured *http.Request
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "cfgtopic"})
	notifyConfigReload(cfg, "config.yaml", nil)
	assert.Equal(t, 1, requests)
	require.NotNil(t, captured)
	assert.Equal(t, "Config Reloaded", captured.Header.Get("Title"))
}

func TestNotifyConfigReload_SendsFailureWithErrorTextOnNonNilError(t *testing.T) {
	var body []byte
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: srv.URL, AlertTopic: "cfgtopic"})
	notifyConfigReload(cfg, "config.yaml",
		fmt.Errorf("yaml: line 4: did not find expected key"))
	assert.Equal(t, 1, requests)
	assert.Contains(t, string(body), "yaml: line 4: did not find expected key")
}

func TestNotifyConfigReload_SendFailureIsNonFatal(t *testing.T) {
	blocked := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer func() {
		close(blocked)
		slow.Close()
	}()

	cfg := NtfyConfig{BaseURL: slow.URL, AlertTopic: "cfgtopic"}
	require.NoError(t, cfg.Validate())

	done := make(chan struct{})
	go func() {
		notifyConfigReload(cfg, "config.yaml", fmt.Errorf("boom"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(ntfyTimeout + 5*time.Second):
		t.Fatal("notifyConfigReload should return once the client times out, not hang forever")
	}
}
