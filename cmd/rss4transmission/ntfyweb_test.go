package main

/*
 * RSS4Transmission
 * Copyright (c) 2023 Aaron Turner  <aturner at synfin dot net>
 *
 * This program is free software: you can redistribute it
 * and/or modify it under the terms of the GNU General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or with the authors permission any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

import (
	"net/http"
	"strings"
	"testing"
)

// ---- nav item ----

func TestNav_LinksNotificationsAndAlertsWhenEnabled(t *testing.T) {
	h := &HistoryFile{Records: []HistoryRecord{}, guidIndex: map[string]int{}}

	_, both := getBody(t, newWebMux(h, nil, nil, nil, nil,
		navConfig{Notifications: navOn(), Alerts: navOn()}), "/")
	if !strings.Contains(navLine(t, both), `href="/notifications"`) {
		t.Errorf("torrents page nav is missing the Notifications link\ngot:\n%s", navLine(t, both))
	}
	if !strings.Contains(navLine(t, both), `href="/alerts"`) {
		t.Errorf("torrents page nav is missing the Alerts link\ngot:\n%s", navLine(t, both))
	}

	_, notifOnly := getBody(t, newWebMux(h, nil, nil, nil, nil,
		navConfig{Notifications: navOn()}), "/")
	if !strings.Contains(navLine(t, notifOnly), `href="/notifications"`) {
		t.Errorf("torrents page nav is missing the Notifications link\ngot:\n%s", navLine(t, notifOnly))
	}
	if strings.Contains(navLine(t, notifOnly), `href="/alerts"`) {
		t.Errorf("torrents page links /alerts when only Notifications is enabled\ngot:\n%s", navLine(t, notifOnly))
	}

	_, off := getBody(t, newWebMux(h, nil, nil, nil, nil, navConfig{}), "/")
	if strings.Contains(navLine(t, off), `href="/notifications"`) ||
		strings.Contains(navLine(t, off), `href="/alerts"`) {
		t.Errorf("torrents page links ntfy pages when neither is enabled\ngot:\n%s", navLine(t, off))
	}
}

// ---- ntfyTopicURL ----

func TestNtfyTopicURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		topic   string
		want    string
	}{
		{"basic", "https://ntfy.sh", "mytopic", "https://ntfy.sh/mytopic"},
		{"trailing slash trimmed", "https://ntfy.sh/", "mytopic", "https://ntfy.sh/mytopic"},
		{"empty base url", "", "mytopic", ""},
		{"empty topic", "https://ntfy.sh", "", ""},
		{"both empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ntfyTopicURL(tt.baseURL, tt.topic); got != tt.want {
				t.Errorf("ntfyTopicURL(%q, %q) = %q, want %q", tt.baseURL, tt.topic, got, tt.want)
			}
		})
	}
}

// ---- page ----

// ntfyNavFor mirrors how watch.go wires the two gates: each is derived from
// cfg the same way the handlers themselves decide whether to 404, so a test
// against it also proves the two pages are gated independently.
func ntfyNavFor(cfg NtfyConfig) navConfig {
	return navConfig{
		Notifications: func() bool { return ntfyTopicURL(cfg.BaseURL, cfg.Topic) != "" },
		Alerts:        func() bool { return ntfyTopicURL(cfg.BaseURL, cfg.AlertTopic) != "" },
	}
}

func withNtfy(t *testing.T, cfg NtfyConfig) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerNtfyRoutes(mux, staticNtfy(cfg), ntfyNavFor(cfg))
	return mux
}

func TestNotificationsPage_FramesTopicURL(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "torrents", AlertTopic: "alerts"}

	code, body := getBody(t, withNtfy(t, cfg), "/notifications")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, `src="https://ntfy.sh/torrents"`) {
		t.Errorf("page does not frame the topic URL\ngot:\n%s", body)
	}
	if !strings.Contains(navLine(t, body), `<span class="here">Notifications</span>`) {
		t.Errorf("page nav does not mark Notifications as current\ngot:\n%s", navLine(t, body))
	}
	if !strings.Contains(navLine(t, body), `href="/alerts"`) {
		t.Errorf("page nav is missing the Alerts link\ngot:\n%s", navLine(t, body))
	}
}

func TestAlertsPage_FramesAlertTopicURL(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "torrents", AlertTopic: "alerts"}

	code, body := getBody(t, withNtfy(t, cfg), "/alerts")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, `src="https://ntfy.sh/alerts"`) {
		t.Errorf("page does not frame the alert topic URL\ngot:\n%s", body)
	}
	if !strings.Contains(navLine(t, body), `<span class="here">Alerts</span>`) {
		t.Errorf("page nav does not mark Alerts as current\ngot:\n%s", navLine(t, body))
	}
	if !strings.Contains(navLine(t, body), `href="/notifications"`) {
		t.Errorf("page nav is missing the Notifications link\ngot:\n%s", navLine(t, body))
	}
}

// Notifications and Alerts are independently gated, unlike /speedtest and
// /rotations which share one gate. Visiting one page while the other topic is
// unset must not link to a page that will 404.
func TestNotificationsPage_AlertsLinkHiddenWhenAlertTopicEmpty(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "torrents"}

	_, body := getBody(t, withNtfy(t, cfg), "/notifications")
	if strings.Contains(navLine(t, body), `href="/alerts"`) {
		t.Errorf("notifications page links /alerts while AlertTopic is empty\ngot:\n%s", navLine(t, body))
	}
}

func TestAlertsPage_NotificationsLinkHiddenWhenTopicEmpty(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "alerts"}

	_, body := getBody(t, withNtfy(t, cfg), "/alerts")
	if strings.Contains(navLine(t, body), `href="/notifications"`) {
		t.Errorf("alerts page links /notifications while Topic is empty\ngot:\n%s", navLine(t, body))
	}
}

func TestNotificationsPage_404WhenTopicEmpty(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", AlertTopic: "alerts"} // Topic unset
	if code, _ := getBody(t, withNtfy(t, cfg), "/notifications"); code != http.StatusNotFound {
		t.Errorf("status = %d while Topic is empty, want 404", code)
	}
}

func TestAlertsPage_404WhenAlertTopicEmpty(t *testing.T) {
	cfg := NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "torrents"} // AlertTopic unset
	if code, _ := getBody(t, withNtfy(t, cfg), "/alerts"); code != http.StatusNotFound {
		t.Errorf("status = %d while AlertTopic is empty, want 404", code)
	}
}

func TestNtfyRoutes_NotRegistered(t *testing.T) {
	mux := http.NewServeMux()
	for _, path := range []string{"/notifications", "/alerts"} {
		if code, _ := getBody(t, mux, path); code != http.StatusNotFound {
			t.Errorf("%s status = %d without the routes, want 404", path, code)
		}
	}
}

// On the real private mux the history page is the catch-all for "/", so
// unregistered /notifications and /alerts render the torrents page instead of
// 404ing. What matters is that nothing reaches ntfy.
func TestNtfyRoutes_NotRegisteredOnHistoryMux(t *testing.T) {
	h := &HistoryFile{Records: []HistoryRecord{}, guidIndex: map[string]int{}}
	mux := newWebMux(h, nil, nil, nil, nil, navConfig{})

	for _, path := range []string{"/notifications", "/alerts"} {
		code, body := getBody(t, mux, path)
		if code != http.StatusOK {
			t.Errorf("%s status = %d, want the torrents page", path, code)
		}
		if !strings.Contains(body, "<title>RSS4Transmission Torrents</title>") {
			t.Errorf("%s did not render the torrents page", path)
		}
	}
}
