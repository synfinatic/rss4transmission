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

// NtfyConfigContext holds the data available to config-reload notification templates.
type NtfyConfigContext struct {
	ConfigFile string
	Error      string // empty on success
}

// NtfyPortContext holds the data available to port-closed notification templates.
type NtfyPortContext struct {
	Reason string
}

// NtfyVpnContext holds the data available to VPN-rotation notification templates.
type NtfyVpnContext struct {
	Reason       string
	DownloadMbps float64
	ExitIP       string
}

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
	StartURL  string
}

var validNtfyPriorities = map[string]struct{}{
	"min": {}, "low": {}, "default": {}, "high": {}, "max": {},
}

// compileNotificationTemplates applies defaults to title/body/priority in
// place, validates priority, and compiles title/body as text/template.
// titleField/bodyField/priorityField name the config fields in error
// messages, e.g. "StartedTitle".
func compileNotificationTemplates(title, body, priority *string, titleDefault, bodyDefault, priorityDefault, titleField, bodyField, priorityField string) (*template.Template, *template.Template, error) {
	if *title == "" {
		*title = titleDefault
	}
	if *body == "" {
		*body = bodyDefault
	}
	if *priority == "" {
		*priority = priorityDefault
	}

	if _, ok := validNtfyPriorities[*priority]; !ok {
		return nil, nil, fmt.Errorf("ntfy %s %q is not valid (min/low/default/high/max)", priorityField, *priority)
	}

	titleTmpl, err := template.New(titleField).Parse(*title)
	if err != nil {
		return nil, nil, fmt.Errorf("ntfy %s template: %w", titleField, err)
	}
	bodyTmpl, err := template.New(bodyField).Parse(*body)
	if err != nil {
		return nil, nil, fmt.Errorf("ntfy %s template: %w", bodyField, err)
	}
	return titleTmpl, bodyTmpl, nil
}

// Validate applies template defaults and compiles notification templates. It
// also validates that priority fields contain ntfy-accepted values.
// Returns nil immediately when ntfy is disabled (BaseURL not set). The
// Started/Completed (torrent) and ConfigReloaded/ConfigFailed/PortClosed/
// PortOpened (alert) notification groups are independently opt-in, gated by
// Topic and AlertTopic respectively, so either can be enabled without the
// other.
func (c *NtfyConfig) Validate() error {
	if c.BaseURL == "" {
		return nil
	}

	if c.Topic != "" {
		var err error
		c.startedTitleTmpl, c.startedBodyTmpl, err = compileNotificationTemplates(
			&c.StartedTitle, &c.StartedBody, &c.StartedPriority,
			"Torrent Started", "{{.Title}}\n{{.Size}}", "default",
			"StartedTitle", "StartedBody", "StartedPriority")
		if err != nil {
			return err
		}
		c.completedTitleTmpl, c.completedBodyTmpl, err = compileNotificationTemplates(
			&c.CompletedTitle, &c.CompletedBody, &c.CompletedPriority,
			"Torrent Complete", "{{.Title}}\n{{.Dir}}", "default",
			"CompletedTitle", "CompletedBody", "CompletedPriority")
		if err != nil {
			return err
		}
		c.seenTitleTmpl, c.seenBodyTmpl, err = compileNotificationTemplates(
			&c.SeenTitle, &c.SeenBody, &c.SeenPriority,
			"Torrent Found", "{{.Title}}\n{{.Size}}", "default",
			"SeenTitle", "SeenBody", "SeenPriority")
		if err != nil {
			return err
		}
	}

	if c.AlertTopic != "" {
		var err error
		c.configReloadedTitleTmpl, c.configReloadedBodyTmpl, err = compileNotificationTemplates(
			&c.ConfigReloadedTitle, &c.ConfigReloadedBody, &c.ConfigReloadedPriority,
			"Config Reloaded", "{{.ConfigFile}}", "low",
			"ConfigReloadedTitle", "ConfigReloadedBody", "ConfigReloadedPriority")
		if err != nil {
			return err
		}
		c.configFailedTitleTmpl, c.configFailedBodyTmpl, err = compileNotificationTemplates(
			&c.ConfigFailedTitle, &c.ConfigFailedBody, &c.ConfigFailedPriority,
			"Config Reload FAILED", "{{.ConfigFile}}\n{{.Error}}", "high",
			"ConfigFailedTitle", "ConfigFailedBody", "ConfigFailedPriority")
		if err != nil {
			return err
		}
		c.portClosedTitleTmpl, c.portClosedBodyTmpl, err = compileNotificationTemplates(
			&c.PortClosedTitle, &c.PortClosedBody, &c.PortClosedPriority,
			"Transmission Port Closed", "{{.Reason}}", "high",
			"PortClosedTitle", "PortClosedBody", "PortClosedPriority")
		if err != nil {
			return err
		}
		c.portOpenedTitleTmpl, c.portOpenedBodyTmpl, err = compileNotificationTemplates(
			&c.PortOpenedTitle, &c.PortOpenedBody, &c.PortOpenedPriority,
			"Transmission Port Open", "{{.Reason}}", "default",
			"PortOpenedTitle", "PortOpenedBody", "PortOpenedPriority")
		if err != nil {
			return err
		}
		c.vpnRotatedTitleTmpl, c.vpnRotatedBodyTmpl, err = compileNotificationTemplates(
			&c.VpnRotatedTitle, &c.VpnRotatedBody, &c.VpnRotatedPriority,
			"VPN Rotating", "{{.Reason}}\nExit IP: {{.ExitIP}}", "default",
			"VpnRotatedTitle", "VpnRotatedBody", "VpnRotatedPriority")
		if err != nil {
			return err
		}
	}

	return nil
}

func renderTemplate(tmpl *template.Template, ctx any) (string, error) {
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

func (c *NtfyClient) post(topic, title, body, actions, priority string) error {
	url := fmt.Sprintf("%s/%s", strings.TrimRight(c.cfg.BaseURL, "/"), topic)
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
		actions = fmt.Sprintf("view, More Info, %s", ctx.CancelURL)
	}
	return c.post(c.cfg.Topic, title, body, actions, c.cfg.StartedPriority)
}

func (c *NtfyClient) SendTorrentSeen(ctx *NtfyTemplateContext) error {
	title, err := renderTemplate(c.cfg.seenTitleTmpl, ctx)
	if err != nil {
		return err
	}
	body, err := renderTemplate(c.cfg.seenBodyTmpl, ctx)
	if err != nil {
		return err
	}
	var actions string
	if ctx.StartURL != "" {
		actions = fmt.Sprintf("view, Start Download, %s", ctx.StartURL)
	}
	return c.post(c.cfg.Topic, title, body, actions, c.cfg.SeenPriority)
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
	return c.post(c.cfg.Topic, title, body, "", c.cfg.CompletedPriority)
}

func (c *NtfyClient) SendConfigReloadSuccess(ctx *NtfyConfigContext) error {
	title, err := renderTemplate(c.cfg.configReloadedTitleTmpl, ctx)
	if err != nil {
		return err
	}
	body, err := renderTemplate(c.cfg.configReloadedBodyTmpl, ctx)
	if err != nil {
		return err
	}
	return c.post(c.cfg.AlertTopic, title, body, "", c.cfg.ConfigReloadedPriority)
}

func (c *NtfyClient) SendConfigReloadFailure(ctx *NtfyConfigContext) error {
	title, err := renderTemplate(c.cfg.configFailedTitleTmpl, ctx)
	if err != nil {
		return err
	}
	body, err := renderTemplate(c.cfg.configFailedBodyTmpl, ctx)
	if err != nil {
		return err
	}
	return c.post(c.cfg.AlertTopic, title, body, "", c.cfg.ConfigFailedPriority)
}

func (c *NtfyClient) SendPortClosed(ctx *NtfyPortContext) error {
	title, err := renderTemplate(c.cfg.portClosedTitleTmpl, ctx)
	if err != nil {
		return err
	}
	body, err := renderTemplate(c.cfg.portClosedBodyTmpl, ctx)
	if err != nil {
		return err
	}
	return c.post(c.cfg.AlertTopic, title, body, "", c.cfg.PortClosedPriority)
}

func (c *NtfyClient) SendPortOpened(ctx *NtfyPortContext) error {
	title, err := renderTemplate(c.cfg.portOpenedTitleTmpl, ctx)
	if err != nil {
		return err
	}
	body, err := renderTemplate(c.cfg.portOpenedBodyTmpl, ctx)
	if err != nil {
		return err
	}
	return c.post(c.cfg.AlertTopic, title, body, "", c.cfg.PortOpenedPriority)
}

func (c *NtfyClient) SendVpnRotated(ctx *NtfyVpnContext) error {
	title, err := renderTemplate(c.cfg.vpnRotatedTitleTmpl, ctx)
	if err != nil {
		return err
	}
	body, err := renderTemplate(c.cfg.vpnRotatedBodyTmpl, ctx)
	if err != nil {
		return err
	}
	return c.post(c.cfg.AlertTopic, title, body, "", c.cfg.VpnRotatedPriority)
}

// notifyConfigReload sends a config-reload outcome notification to ntfy's
// AlertTopic. No-op when BaseURL or AlertTopic is unset. Send failures are
// logged as warnings, never fatal — a broken notification channel must not
// block the config-reload feature itself.
func notifyConfigReload(cfg NtfyConfig, configFile string, reloadErr error) {
	if cfg.BaseURL == "" || cfg.AlertTopic == "" {
		return
	}

	ntfyCtx := &NtfyConfigContext{ConfigFile: configFile}
	client := NewNtfyClient(cfg)

	if reloadErr != nil {
		ntfyCtx.Error = reloadErr.Error()
		if err := client.SendConfigReloadFailure(ntfyCtx); err != nil {
			log.WithError(err).Warn("Failed to send ntfy config-reload-failure notification")
		}
		return
	}

	if err := client.SendConfigReloadSuccess(ntfyCtx); err != nil {
		log.WithError(err).Warn("Failed to send ntfy config-reload-success notification")
	}
}

// notifyPortClosed sends a port-closed alert to ntfy's AlertTopic. No-op when
// BaseURL or AlertTopic is unset. Send failures are logged as warnings, never
// fatal — a broken notification channel must not block port monitoring.
func notifyPortClosed(cfg NtfyConfig, reason string) {
	if cfg.BaseURL == "" || cfg.AlertTopic == "" {
		return
	}

	client := NewNtfyClient(cfg)
	if err := client.SendPortClosed(&NtfyPortContext{Reason: reason}); err != nil {
		log.WithError(err).Warn("Failed to send ntfy port-closed notification")
	}
}

// notifyPortOpened sends a port-reopened alert to ntfy's AlertTopic. No-op when
// BaseURL or AlertTopic is unset. Send failures are logged as warnings, never
// fatal — a broken notification channel must not block port monitoring.
func notifyPortOpened(cfg NtfyConfig, reason string) {
	if cfg.BaseURL == "" || cfg.AlertTopic == "" {
		return
	}

	client := NewNtfyClient(cfg)
	if err := client.SendPortOpened(&NtfyPortContext{Reason: reason}); err != nil {
		log.WithError(err).Warn("Failed to send ntfy port-opened notification")
	}
}

// notifyVpnRotated sends a VPN-rotation alert to ntfy's AlertTopic. No-op when
// BaseURL or AlertTopic is unset. Send failures are logged as warnings, never
// fatal — a broken notification channel must not block VPN rotation.
func notifyVpnRotated(cfg NtfyConfig, ctx *NtfyVpnContext) {
	if cfg.BaseURL == "" || cfg.AlertTopic == "" {
		return
	}

	client := NewNtfyClient(cfg)
	if err := client.SendVpnRotated(ctx); err != nil {
		log.WithError(err).Warn("Failed to send ntfy vpn-rotated notification")
	}
}
