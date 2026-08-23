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
	_ "embed"
	"html/template"
	"net/http"
	"strings"
)

//go:embed web/notifications.html
var notificationsTmpl string

//go:embed web/alerts.html
var alertsTmpl string

// ntfyPageData is passed to the notifications/alerts page templates.
type ntfyPageData struct {
	IframeSrc string
}

// ntfyTopicURL builds the ntfy web page URL for a topic, or returns "" when
// either baseURL or topic is empty -- callers read that as "nothing to
// frame" and 404 the route.
func ntfyTopicURL(baseURL, topic string) string {
	if baseURL == "" || topic == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/" + topic
}

// registerNtfyRoutes adds GET /notifications and GET /alerts to mux. Each
// frames the ntfy web page for Ntfy.Topic / Ntfy.AlertTopic directly.
//
// Unlike Transmission there is no reverse proxy here: ntfy's web app serves
// its static assets from the domain root (/static/..., /manifest.json), so
// proxying it under a path prefix would 404 those assets, and a root-domain
// proxy would collide with rss4transmission's own routes. Ntfy.BaseURL is
// already documented as a server the browser can reach directly -- the same
// one phones subscribe to -- so the iframe just points at it.
//
// ntfy reads the live Ntfy config block; both routes are registered once and
// resolve per request, so editing BaseURL/Topic/AlertTopic in the config file
// takes effect without a restart. Either route 404s while its own topic (and
// BaseURL) is unset.
//
// Notifications and Alerts are gated independently of each other -- unlike
// /speedtest and /rotations, which share one gate -- so each page needs its
// own template set: one forces only its own nav predicate to alwaysNav and
// leaves the other page's predicate live, otherwise visiting one page while
// the other topic is unset would still link to a page that 404s.
func registerNtfyRoutes(mux *http.ServeMux, ntfy func() NtfyConfig, nav navConfig) {
	notifNav := nav
	notifNav.Notifications = alwaysNav
	notifTmpl := template.Must(template.Must(
		template.New("notifications").Funcs(notifNav.navFuncs()).Parse(navTmpl)).Parse(notificationsTmpl))

	alertNav := nav
	alertNav.Alerts = alwaysNav
	alertTmpl := template.Must(template.Must(
		template.New("alerts").Funcs(alertNav.navFuncs()).Parse(navTmpl)).Parse(alertsTmpl))

	mux.HandleFunc("GET /notifications", func(w http.ResponseWriter, r *http.Request) {
		src := ntfyTopicURL(ntfy().BaseURL, ntfy().Topic)
		if src == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := notifTmpl.Execute(w, ntfyPageData{IframeSrc: src}); err != nil {
			log.WithError(err).Error("Failed to render notifications template")
		}
	})

	mux.HandleFunc("GET /alerts", func(w http.ResponseWriter, r *http.Request) {
		src := ntfyTopicURL(ntfy().BaseURL, ntfy().AlertTopic)
		if src == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := alertTmpl.Execute(w, ntfyPageData{IframeSrc: src}); err != nil {
			log.WithError(err).Error("Failed to render alerts template")
		}
	})
}
