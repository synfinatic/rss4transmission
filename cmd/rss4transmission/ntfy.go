package main

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

const ntfyTimeout = 30 * time.Second

// NtfyTemplateContext holds all torrent data available to notification templates.
type NtfyTemplateContext struct {
	Title     string
	FeedName  string
	Dir       string // download directory (populated for completions)
	Files     []string
	Labels    map[string]string
	SizeBytes int64
	Size      string // formatGB(SizeBytes); may be "Unknown" when SizeBytes <= 0
	GUID      string
	Link      string
	Published *time.Time // may be nil; guard with {{if .Published}}
	TorrentID int64
	CancelURL string
}

var validNtfyPriorities = map[string]struct{}{
	"min": {}, "low": {}, "default": {}, "high": {}, "max": {},
}

// Validate applies template defaults and compiles all four notification templates.
// It also validates that priority fields contain ntfy-accepted values.
func (c *NtfyConfig) Validate() error {
	if c.StartedTitle == "" {
		c.StartedTitle = "Torrent Started"
	}
	if c.StartedBody == "" {
		c.StartedBody = "{{.Title}}\n{{.Size}}"
	}
	if c.StartedPriority == "" {
		c.StartedPriority = "default"
	}
	if c.CompletedTitle == "" {
		c.CompletedTitle = "Torrent Complete"
	}
	if c.CompletedBody == "" {
		c.CompletedBody = "{{.Title}}\n{{.Dir}}"
	}
	if c.CompletedPriority == "" {
		c.CompletedPriority = "default"
	}

	if _, ok := validNtfyPriorities[c.StartedPriority]; !ok {
		return fmt.Errorf("ntfy StartedPriority %q is not valid (min/low/default/high/max)", c.StartedPriority)
	}
	if _, ok := validNtfyPriorities[c.CompletedPriority]; !ok {
		return fmt.Errorf("ntfy CompletedPriority %q is not valid (min/low/default/high/max)", c.CompletedPriority)
	}

	var err error
	if c.startedTitleTmpl, err = template.New("StartedTitle").Parse(c.StartedTitle); err != nil {
		return fmt.Errorf("ntfy StartedTitle template: %w", err)
	}
	if c.startedBodyTmpl, err = template.New("StartedBody").Parse(c.StartedBody); err != nil {
		return fmt.Errorf("ntfy StartedBody template: %w", err)
	}
	if c.completedTitleTmpl, err = template.New("CompletedTitle").Parse(c.CompletedTitle); err != nil {
		return fmt.Errorf("ntfy CompletedTitle template: %w", err)
	}
	if c.completedBodyTmpl, err = template.New("CompletedBody").Parse(c.CompletedBody); err != nil {
		return fmt.Errorf("ntfy CompletedBody template: %w", err)
	}
	return nil
}

func renderTemplate(tmpl *template.Template, ctx *NtfyTemplateContext) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// NtfyClient sends notifications to an ntfy server.
type NtfyClient struct {
	cfg    NtfyConfig
	client *http.Client
}

func NewNtfyClient(cfg NtfyConfig) *NtfyClient {
	return &NtfyClient{cfg: cfg, client: &http.Client{Timeout: ntfyTimeout}}
}

func (c *NtfyClient) post(title, body, actions, priority string) error {
	url := fmt.Sprintf("%s/%s", strings.TrimRight(c.cfg.BaseURL, "/"), c.cfg.Topic)
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	if actions != "" {
		req.Header.Set("Actions", actions)
	}

	resp, err := c.client.Do(req) //nolint:gosec
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *NtfyClient) SendTorrentStarted(ctx *NtfyTemplateContext) error {
	title, err := renderTemplate(c.cfg.startedTitleTmpl, ctx)
	if err != nil {
		return err
	}
	body, err := renderTemplate(c.cfg.startedBodyTmpl, ctx)
	if err != nil {
		return err
	}
	var actions string
	if ctx.CancelURL != "" {
		actions = fmt.Sprintf("view, Cancel Download, %s", ctx.CancelURL)
	}
	return c.post(title, body, actions, c.cfg.StartedPriority)
}

func (c *NtfyClient) SendTorrentCompleted(ctx *NtfyTemplateContext) error {
	title, err := renderTemplate(c.cfg.completedTitleTmpl, ctx)
	if err != nil {
		return err
	}
	body, err := renderTemplate(c.cfg.completedBodyTmpl, ctx)
	if err != nil {
		return err
	}
	return c.post(title, body, "", c.cfg.CompletedPriority)
}
