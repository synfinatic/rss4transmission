package main

import (
	"fmt"
	"net/http"
	"strings"
)

// NtfyClient sends notifications to an ntfy server.
type NtfyClient struct {
	cfg NtfyConfig
}

func NewNtfyClient(cfg NtfyConfig) *NtfyClient {
	return &NtfyClient{cfg: cfg}
}

func (c *NtfyClient) post(title, body, actions string) error {
	url := fmt.Sprintf("%s/%s", c.cfg.BaseURL, c.cfg.Topic)
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", "default")
	if actions != "" {
		req.Header.Set("Actions", actions)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *NtfyClient) SendTorrentStarted(title, size, cancelURL string) error {
	body := title
	if size != "" {
		body = title + "\n" + size
	}
	var actions string
	if cancelURL != "" {
		actions = fmt.Sprintf("view, Cancel Download, %s", cancelURL)
	}
	return c.post("Torrent Started", body, actions)
}

func (c *NtfyClient) SendTorrentCompleted(title string) error {
	return c.post("Torrent Complete", title, "")
}
