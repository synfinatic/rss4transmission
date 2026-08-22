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
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

//go:embed web/history.html
var historyTmpl string

// navTmpl is the nav bar every page shares. It is parsed into each page's
// template set alongside the page itself, and needs a speedtestEnabled func in
// that set's FuncMap to decide whether the VPN pages are linked.
//
//go:embed web/nav.html
var navTmpl string

// navConfig says which optional nav items exist. The shared nav partial only
// links a page whose routes were actually registered, so a link never leads to
// a 404.
type navConfig struct {
	Speedtest    bool
	Transmission bool
}

// navFuncs returns the FuncMap entries that web/nav.html needs. Every template
// set that parses navTmpl must merge these in.
func (n navConfig) navFuncs() template.FuncMap {
	return template.FuncMap{
		"speedtestEnabled":    func() bool { return n.Speedtest },
		"transmissionEnabled": func() bool { return n.Transmission },
	}
}

//go:embed web/cancel.html
var cancelTmpl string

//go:embed web/start.html
var startTmpl string

//go:embed web/favicon.svg
var faviconSVG string

// faviconHandler serves the browser tab icon shared by every page.
func faviconHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	fmt.Fprint(w, faviconSVG) //nolint:errcheck
}

// removeFunc is the signature for removing torrents from Transmission.
type removeFunc func(ctx context.Context, ids []int64) error

// progressFunc fetches live download progress for a single torrent from Transmission.
// Returns bytes downloaded so far and percentDone in [0,1]. If unavailable, callers
// should show "Unknown" rather than failing the request.
type progressFunc func(ctx context.Context, torrentID int64) (downloadedBytes int64, percentDone float64, err error)

// retryFunc re-submits a previously skipped/excluded/error history record to
// Transmission. Returns the new Transmission torrent ID.
type retryFunc func(rec HistoryRecord) (int64, error)

// forgetFunc removes a (feed, guid) pair from both the seen cache and history,
// so the item can be freshly re-evaluated on the next run. Returns whether a
// history record was found and removed.
type forgetFunc func(feed, guid string) (bool, error)

// cancelPageData is passed to the cancel confirmation template.
type cancelPageData struct {
	Title         string
	FeedName      string
	Labels        map[string]string
	Files         []string
	SizeFormatted string
	Downloaded    string // bytes downloaded so far, formatted (e.g. "234.5 MB"), or "Unknown"
	Percent       string // percent done (e.g. "12.3%"), or "Unknown"
	ID            string
	Expires       int64
	Sig           string
}

// startPageData is passed to the /start confirmation template. Unlike
// cancelPageData it carries no Files list: HistoryRecord does not persist
// file names, only the torrent-level TorrentURL/SizeBytes used to resubmit.
type startPageData struct {
	Title         string
	FeedName      string
	Labels        map[string]string
	SizeFormatted string
	Published     string
	ID            string
	Expires       int64
	Sig           string
}

// clientIP extracts the real client IP from a request. It checks Cloudflare
// headers first (CF-Connecting-IP, CF-Connecting-IPv6), then X-Forwarded-For
// (first entry), then X-Real-IP, and falls back to RemoteAddr.
func clientIP(r *http.Request) string {
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return strings.TrimSpace(cf)
	}
	if cf6 := r.Header.Get("CF-Connecting-IPv6"); cf6 != "" {
		return strings.TrimSpace(cf6)
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// openAccessLog opens the access log at path and returns a logrus logger for it.
// Returns nil when path is empty. Fatals on open failure.
func openAccessLog(path string) *logrus.Logger {
	if path == "" {
		return nil
	}
	al, err := newAccessLogger(path)
	if err != nil {
		log.WithError(err).Fatalf("Failed to open access log: %s", path)
	}
	return al
}

// newAccessLogger creates a logrus logger that appends structured log lines with
// timestamps to path. Used for fail2ban-compatible access logging.
func newAccessLogger(path string) (*logrus.Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open access log %q: %w", path, err)
	}
	lg := logrus.New()
	lg.SetOutput(f)
	lg.SetFormatter(&logrus.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: false,
		FullTimestamp:    true,
	})
	lg.SetLevel(logrus.InfoLevel)
	return lg, nil
}

// parseListenAddr normalises a listen address to a "host:port" address.
// A bare port number is expanded to "127.0.0.1:<port>". Returns an error for
// invalid or out-of-range values.
func parseListenAddr(s string) (string, error) {
	// If it already contains a colon it is a host:port or [ipv6]:port.
	if _, portStr, err := net.SplitHostPort(s); err == nil {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return "", fmt.Errorf("invalid port in %q", s)
		}
		return s, nil
	}
	// Treat as a bare port number.
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("invalid listen address %q: must be a port number or host:port", s)
	}
	return fmt.Sprintf("127.0.0.1:%d", p), nil
}

// historyRow decorates a HistoryRecord with grouping metadata for template
// rendering: rows sharing a GUID (the same RSS item seen by multiple feed
// configs that share an RSS URL) are grouped into one expandable entry so a
// torrent that only matches one of several sibling feeds doesn't produce a
// wall of "no group matched labels" rows.
type historyRow struct {
	HistoryRecord
	IsPrimary bool
	GroupSize int // total records sharing this GUID; 1 = ungrouped
}

// isNoGroupMatched reports whether a record's feed never applied to the item
// at all (its Groups never matched the item's labels). This is the least
// informative reason a record can carry — even below an excluded or errored
// record, which at least mean the feed engaged with the item — so it's
// checked before outcomeRank when picking a group's primary row.
func isNoGroupMatched(r HistoryRecord) bool {
	return r.Outcome == "skipped" && r.Reason == skipReasonNoGroupMatched
}

// bestGroupScore returns the highest Group.MatchScore across groups for the
// given labels, treating a feed with no groups — or whose every group is
// disqualified (contradicted) or simply uninformative (no positive evidence)
// — as 0, the same as a feed with no distinguishing evidence at all.
func bestGroupScore(groups []Group, labels map[string]string) int {
	best := 0
	for _, g := range groups {
		if s := g.MatchScore(labels); s > best {
			best = s
		}
	}
	return best
}

// groupHistoryRows reorders records so rows sharing a GUID are contiguous.
// Within a group, the primary row is chosen by: "no group matched labels"
// records always sort last (see isNoGroupMatched); among the rest, the most
// interesting outcome wins (outcomeRank); ties then prefer the record whose
// own extracted labels give the strongest positive match against its own
// feed's Groups (see bestGroupScore) — this matters because sibling feeds
// sharing an Extractor can extract identical labels for an item that isn't
// theirs, so only labels that actually satisfy a feed's own Require count as
// evidence; any remaining tie breaks alphabetically by Feed. The chosen
// record is marked IsPrimary and placed first; the rest follow sorted the
// same way. Group order follows first appearance in the input. Records with
// an empty GUID are never grouped with each other or anything else.
func groupHistoryRows(records []HistoryRecord, feedGroups func(name string) []Group) []historyRow {
	if feedGroups == nil {
		feedGroups = func(string) []Group { return nil }
	}
	type group struct {
		key     string
		records []HistoryRecord
	}

	order := make([]string, 0, len(records))
	groups := make(map[string]*group, len(records))
	empties := 0
	for _, r := range records {
		var key string
		if r.GUID == "" {
			// Each empty-GUID record is its own singleton group.
			empties++
			key = fmt.Sprintf("\x00empty-%d", empties)
		} else {
			key = r.GUID
		}

		g, ok := groups[key]
		if !ok {
			g = &group{key: key}
			groups[key] = g
			order = append(order, key)
		}
		g.records = append(g.records, r)
	}

	rows := make([]historyRow, 0, len(records))
	for _, key := range order {
		g := groups[key]
		recs := append([]HistoryRecord(nil), g.records...)
		sort.SliceStable(recs, func(i, j int) bool {
			ni, nj := isNoGroupMatched(recs[i]), isNoGroupMatched(recs[j])
			if ni != nj {
				return nj // i sorts first when i is NOT a no-group-matched record
			}
			ri, rj := outcomeRank(recs[i].Outcome), outcomeRank(recs[j].Outcome)
			if ri != rj {
				return ri < rj
			}
			si := bestGroupScore(feedGroups(recs[i].Feed), recs[i].Labels)
			sj := bestGroupScore(feedGroups(recs[j].Feed), recs[j].Labels)
			if si != sj {
				return si > sj // higher score sorts first
			}
			return recs[i].Feed < recs[j].Feed
		})
		for i, r := range recs {
			rows = append(rows, historyRow{
				HistoryRecord: r,
				IsPrimary:     i == 0,
				GroupSize:     len(recs),
			})
		}
	}
	return rows
}

// newWebMux builds the shared HTTP mux. If history is non-nil, the history
// page is served at "/". When history and retry are both non-nil, POST /torrent
// is also registered to re-submit a past skipped/excluded/error item. When
// history and forget are both non-nil, POST /forget is also registered to
// remove a (feed, guid) pair from the seen cache and history.
// feedConfigured reports whether a feed name is still present in the live
// config; the Torrent button is hidden on the history page for records whose
// feed no longer exists, since retrying one always fails with "feed ... is no
// longer configured". A nil feedConfigured shows the button unconditionally.
// feedGroups reports a feed's own configured Groups, used to break primary-row
// ties in groupHistoryRows; a nil feedGroups falls back to alphabetical
// ordering, same as if every feed had no groups.
// The /healthz route is always registered.
// nav says which optional pages this mux will also serve, so the history page
// only links a page whose routes exist.
func newWebMux(history *HistoryFile, retry retryFunc, feedConfigured func(name string) bool, feedGroups func(name string) []Group, forget forgetFunc, nav navConfig) *http.ServeMux {
	if feedConfigured == nil {
		feedConfigured = func(string) bool { return true }
	}
	funcMap := template.FuncMap{
		"outcomeClass": func(outcome string) string {
			switch outcome {
			case "dispatched", "downloaded":
				return "dispatched"
			case "notified":
				return "notified"
			case "error":
				return "error"
			default:
				return "skipped"
			}
		},
		"feedConfigured": feedConfigured,
		"sub":            func(a, b int) int { return a - b },
	}
	for name, fn := range nav.navFuncs() {
		funcMap[name] = fn
	}
	tmpl := template.Must(template.Must(
		template.New("history").Funcs(funcMap).Parse(navTmpl)).Parse(historyTmpl))

	mux := http.NewServeMux()

	if history != nil {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			records := history.GetRecords()
			for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
				records[i], records[j] = records[j], records[i]
			}
			rows := groupHistoryRows(records, feedGroups)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := tmpl.Execute(w, rows); err != nil {
				log.WithError(err).Error("Failed to render history template")
			}
		})
	}

	if history != nil && retry != nil {
		mux.HandleFunc("POST /torrent", makePostTorrentHandler(history, retry))
	}

	if history != nil && forget != nil {
		mux.HandleFunc("POST /forget", makePostForgetHandler(history, forget))
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /favicon.svg", faviconHandler)

	return mux
}

// makePostTorrentHandler processes a manual "torrent this" request from the
// history page. It resolves feed+guid to a HistoryRecord and delegates the
// actual re-submission to retry.
func makePostTorrentHandler(history *HistoryFile, retry retryFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form body", http.StatusBadRequest)
			return
		}

		feed := r.FormValue("feed")
		guid := r.FormValue("guid")
		rec, ok := history.FindRecord(feed, guid)
		if !ok {
			http.Error(w, "history record not found", http.StatusNotFound)
			return
		}

		if _, err := retry(rec); err != nil {
			log.WithError(err).Warnf("Failed to retry torrent for %q", rec.Title)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		log.Infof("Manually torrented %q from history page", rec.Title)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Torrent submitted.") //nolint:errcheck
	}
}

// makePostForgetHandler processes a "forget this" request from the history
// page. It resolves feed+guid to a HistoryRecord (so an unknown pair 404s the
// same way makePostTorrentHandler does) and delegates the actual removal from
// the seen cache and history to forget.
func makePostForgetHandler(history *HistoryFile, forget forgetFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form body", http.StatusBadRequest)
			return
		}

		feed := r.FormValue("feed")
		guid := r.FormValue("guid")
		rec, ok := history.FindRecord(feed, guid)
		if !ok {
			http.Error(w, "history record not found", http.StatusNotFound)
			return
		}

		if _, err := forget(feed, guid); err != nil {
			log.WithError(err).Warnf("Failed to forget %q", rec.Title)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		log.Infof("Forgot %q from history page", rec.Title)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Forgotten.") //nolint:errcheck
	}
}

// newCancelMux builds a public-facing mux serving GET/POST /cancel, GET/POST
// /start, GET /healthz, and GET /favicon.svg. Use this when --public-listen is set to expose
// these token-gated endpoints on their own port, keeping the history page on
// a separate private listener.
// POST /cancel is only registered when both store and remove are non-nil.
// GET /start is only registered when both startStore and history are
// non-nil; POST /start additionally requires retry to be non-nil.
// accessLog is optional; when non-nil each request outcome is written to it.
func newCancelMux(store *Store, cfg NotificationsConfig, remove removeFunc, getProgress progressFunc,
	startStore *StartStore, retry retryFunc, history *HistoryFile, accessLog *logrus.Logger,
) *http.ServeMux {
	mux := http.NewServeMux()
	if store != nil {
		mux.HandleFunc("GET /cancel", makeGetCancelHandler(store, cfg, getProgress, accessLog))
		if remove != nil {
			mux.HandleFunc("POST /cancel", makePostCancelHandler(store, cfg, remove, accessLog))
		}
	}
	if startStore != nil && history != nil {
		mux.HandleFunc("GET /start", makeGetStartHandler(startStore, cfg, history, accessLog))
		if retry != nil {
			mux.HandleFunc("POST /start", makePostStartHandler(startStore, cfg, history, retry, accessLog))
		}
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /favicon.svg", faviconHandler)
	return mux
}

// registerCancelRoutes adds GET /cancel and POST /cancel handlers to mux.
// accessLog is optional; when non-nil each request outcome is written to it.
func registerCancelRoutes(mux *http.ServeMux, store *Store, cfg NotificationsConfig, remove removeFunc, getProgress progressFunc, accessLog *logrus.Logger) {
	mux.HandleFunc("GET /cancel", makeGetCancelHandler(store, cfg, getProgress, accessLog))
	mux.HandleFunc("POST /cancel", makePostCancelHandler(store, cfg, remove, accessLog))
}

// registerStartRoutes adds GET /start and POST /start handlers to mux.
// accessLog is optional; when non-nil each request outcome is written to it.
func registerStartRoutes(mux *http.ServeMux, startStore *StartStore, cfg NotificationsConfig, retry retryFunc, history *HistoryFile, accessLog *logrus.Logger) {
	mux.HandleFunc("GET /start", makeGetStartHandler(startStore, cfg, history, accessLog))
	mux.HandleFunc("POST /start", makePostStartHandler(startStore, cfg, history, retry, accessLog))
}

// registerNotifyCompleteRoute adds POST /notify-complete to mux when ntfy is configured.
// When cancelCfg.HMACSecret is non-empty the endpoint requires
// Authorization: Bearer <HMACSecret>. accessLog is optional.
func registerNotifyCompleteRoute(mux *http.ServeMux, ntfyCfg NtfyConfig, cancelCfg NotificationsConfig, accessLog *logrus.Logger) {
	if ntfyCfg.BaseURL == "" || ntfyCfg.Topic == "" {
		return
	}
	mux.HandleFunc("POST /notify-complete", makeNotifyCompleteHandler(ntfyCfg, cancelCfg, accessLog))
}

// notifyCompleteRequest is the JSON body accepted by POST /notify-complete.
type notifyCompleteRequest struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	ID   int64  `json:"id"`
}

func makeNotifyCompleteHandler(ntfyCfg NtfyConfig, cancelCfg NotificationsConfig, accessLog *logrus.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cancelCfg.HMACSecret != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(cancelCfg.HMACSecret)) != 1 {
				if accessLog != nil {
					accessLog.WithFields(logrus.Fields{
						"endpoint": "/notify-complete",
						"result":   "unauthorized",
					}).Warn("notify-complete access")
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
		var req notifyCompleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"endpoint": "/notify-complete",
					"result":   "bad_request",
				}).Warn("notify-complete access")
			}
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"endpoint": "/notify-complete",
					"result":   "bad_request",
				}).Warn("notify-complete access")
			}
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		ctx := &NtfyTemplateContext{
			Title:     req.Name,
			Dir:       req.Dir,
			TorrentID: req.ID,
			Size:      formatGB(0), // no size info available from Transmission hook
		}
		client := NewNtfyClient(ntfyCfg)
		if err := client.SendTorrentCompleted(ctx); err != nil {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"endpoint": "/notify-complete",
					"result":   "ntfy_error",
					"error":    err.Error(),
				}).Warn("notify-complete access")
			}
			http.Error(w, "failed to send notification", http.StatusInternalServerError)
			return
		}
		if accessLog != nil {
			accessLog.WithFields(logrus.Fields{
				"endpoint": "/notify-complete",
				"result":   "ok",
				"name":     req.Name,
			}).Info("notify-complete access")
		}
		w.WriteHeader(http.StatusOK)
	}
}

// tokenErrorResponse translates a parseCancelToken error into the appropriate
// HTTP response. Shared by /cancel and /start, so the wording stays generic.
func tokenErrorResponse(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrMissingCancelParams) {
		http.Error(w, "missing required parameters", http.StatusBadRequest)
	} else if errors.Is(err, ErrTokenExpired) {
		http.Error(w, "link has expired", http.StatusGone)
	} else {
		http.Error(w, "invalid token", http.StatusBadRequest)
	}
}

// makeGetCancelHandler serves the confirmation form. It validates the token,
// peeks the store for metadata without consuming the entry, and queries
// Transmission for live download progress via getProgress.
func makeGetCancelHandler(store *Store, cfg NotificationsConfig, getProgress progressFunc, accessLog *logrus.Logger) http.HandlerFunc {
	secret := []byte(cfg.HMACSecret)
	tmpl := template.Must(template.New("cancel").Parse(cancelTmpl))
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id := q.Get("id")
		expires, err := parseCancelToken(secret, id, q.Get("expires"), q.Get("sig"))
		if err != nil {
			if accessLog != nil {
				result := "invalid_token"
				if errors.Is(err, ErrTokenExpired) {
					result = "expired"
				}
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/cancel",
					"method":    r.Method,
					"result":    result,
				}).Warn("cancel access")
			}
			tokenErrorResponse(w, err)
			return
		}
		sig := q.Get("sig")

		torrentID, meta, ok := store.Peek(id)
		if !ok {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/cancel",
					"method":    r.Method,
					"result":    "not_found",
				}).Warn("cancel access")
			}
			http.Error(w, "download not found or already cancelled", http.StatusNotFound)
			return
		}

		if accessLog != nil {
			accessLog.WithFields(logrus.Fields{
				"client_ip": clientIP(r),
				"endpoint":  "/cancel",
				"method":    r.Method,
				"result":    "ok",
			}).Info("cancel access")
		}

		downloaded := "Unknown"
		percent := "Unknown"
		if getProgress != nil {
			if dlBytes, pct, err := getProgress(r.Context(), torrentID); err == nil && dlBytes > 0 {
				downloaded = formatGB(dlBytes)
				percent = fmt.Sprintf("%.1f%%", pct*100)
			}
		}

		data := cancelPageData{
			Title:         meta.Title,
			FeedName:      meta.FeedName,
			Labels:        meta.Labels,
			Files:         meta.Files,
			SizeFormatted: formatGB(meta.SizeBytes),
			Downloaded:    downloaded,
			Percent:       percent,
			ID:            id,
			Expires:       expires,
			Sig:           sig,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.WithError(err).Error("Failed to render cancel template")
		}
	}
}

// makePostCancelHandler processes the confirmation form submission. It re-validates
// the token, removes the torrent from Transmission, and only then consumes the
// store entry so users can retry if the Transmission call fails.
func makePostCancelHandler(store *Store, cfg NotificationsConfig, remove removeFunc, accessLog *logrus.Logger) http.HandlerFunc {
	secret := []byte(cfg.HMACSecret)
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form body", http.StatusBadRequest)
			return
		}

		id := r.FormValue("id")
		_, err := parseCancelToken(secret, id, r.FormValue("expires"), r.FormValue("sig"))
		if err != nil {
			if accessLog != nil {
				result := "invalid_token"
				if errors.Is(err, ErrTokenExpired) {
					result = "expired"
				}
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/cancel",
					"method":    r.Method,
					"result":    result,
				}).Warn("cancel access")
			}
			tokenErrorResponse(w, err)
			return
		}

		// Peek (not Take) so the entry survives a failed remove and the user can retry.
		torrentID, _, ok := store.Peek(id)
		if !ok {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/cancel",
					"method":    r.Method,
					"result":    "not_found",
				}).Warn("cancel access")
			}
			http.Error(w, "download not found or already cancelled", http.StatusNotFound)
			return
		}

		if err := remove(r.Context(), []int64{torrentID}); err != nil {
			log.WithError(err).Errorf("Failed to remove torrent %d from Transmission", torrentID)
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/cancel",
					"method":    r.Method,
					"result":    "error",
				}).Warn("cancel access")
			}
			http.Error(w, "failed to cancel download", http.StatusInternalServerError)
			return
		}

		// Remove succeeded: consume the store entry.
		store.Take(id) //nolint:errcheck

		if accessLog != nil {
			accessLog.WithFields(logrus.Fields{
				"client_ip": clientIP(r),
				"endpoint":  "/cancel",
				"method":    r.Method,
				"result":    "cancelled",
			}).Info("cancel access")
		}
		log.Infof("Cancelled download via web confirmation: torrent %d (cancel-id %s)", torrentID, id)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Download cancelled.") //nolint:errcheck
	}
}

// makeGetStartHandler serves the /start confirmation form. It validates the
// token, peeks the StartStore for the feed/GUID pair without consuming the
// entry, then resolves the HistoryRecord to render.
func makeGetStartHandler(store *StartStore, cfg NotificationsConfig, history *HistoryFile, accessLog *logrus.Logger) http.HandlerFunc {
	secret := []byte(cfg.HMACSecret)
	tmpl := template.Must(template.New("start").Parse(startTmpl))
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id := q.Get("id")
		expires, err := parseCancelToken(secret, id, q.Get("expires"), q.Get("sig"))
		if err != nil {
			if accessLog != nil {
				result := "invalid_token"
				if errors.Is(err, ErrTokenExpired) {
					result = "expired"
				}
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/start",
					"method":    r.Method,
					"result":    result,
				}).Warn("start access")
			}
			tokenErrorResponse(w, err)
			return
		}
		sig := q.Get("sig")

		meta, ok := store.Peek(id)
		if !ok {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/start",
					"method":    r.Method,
					"result":    "not_found",
				}).Warn("start access")
			}
			http.Error(w, "torrent not found or already started", http.StatusNotFound)
			return
		}

		rec, ok := history.FindRecord(meta.FeedName, meta.GUID)
		if !ok {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/start",
					"method":    r.Method,
					"result":    "not_found",
				}).Warn("start access")
			}
			http.Error(w, "torrent not found or already started", http.StatusNotFound)
			return
		}

		if accessLog != nil {
			accessLog.WithFields(logrus.Fields{
				"client_ip": clientIP(r),
				"endpoint":  "/start",
				"method":    r.Method,
				"result":    "ok",
			}).Info("start access")
		}

		published := ""
		if !rec.Published.IsZero() {
			published = rec.Published.Format(time.RFC1123)
		}

		data := startPageData{
			Title:         rec.Title,
			FeedName:      rec.Feed,
			Labels:        rec.Labels,
			SizeFormatted: formatGB(rec.SizeBytes),
			Published:     published,
			ID:            id,
			Expires:       expires,
			Sig:           sig,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.WithError(err).Error("Failed to render start template")
		}
	}
}

// makePostStartHandler processes the /start confirmation form submission. It
// re-validates the token, resolves the HistoryRecord, and submits it via
// retry (retryHistoryItem). Unlike /cancel, the StartStore entry is never
// consumed: retryHistoryItem's own outcome guard (dispatched/downloaded ->
// error) already makes a replayed /start link idempotent.
func makePostStartHandler(store *StartStore, cfg NotificationsConfig, history *HistoryFile, retry retryFunc, accessLog *logrus.Logger) http.HandlerFunc {
	secret := []byte(cfg.HMACSecret)
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form body", http.StatusBadRequest)
			return
		}

		id := r.FormValue("id")
		_, err := parseCancelToken(secret, id, r.FormValue("expires"), r.FormValue("sig"))
		if err != nil {
			if accessLog != nil {
				result := "invalid_token"
				if errors.Is(err, ErrTokenExpired) {
					result = "expired"
				}
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/start",
					"method":    r.Method,
					"result":    result,
				}).Warn("start access")
			}
			tokenErrorResponse(w, err)
			return
		}

		meta, ok := store.Peek(id)
		if !ok {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/start",
					"method":    r.Method,
					"result":    "not_found",
				}).Warn("start access")
			}
			http.Error(w, "torrent not found or already started", http.StatusNotFound)
			return
		}

		rec, ok := history.FindRecord(meta.FeedName, meta.GUID)
		if !ok {
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/start",
					"method":    r.Method,
					"result":    "not_found",
				}).Warn("start access")
			}
			http.Error(w, "torrent not found or already started", http.StatusNotFound)
			return
		}

		if _, err := retry(rec); err != nil {
			log.WithError(err).Warnf("Failed to start torrent for %q", rec.Title)
			if accessLog != nil {
				accessLog.WithFields(logrus.Fields{
					"client_ip": clientIP(r),
					"endpoint":  "/start",
					"method":    r.Method,
					"result":    "error",
				}).Warn("start access")
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if accessLog != nil {
			accessLog.WithFields(logrus.Fields{
				"client_ip": clientIP(r),
				"endpoint":  "/start",
				"method":    r.Method,
				"result":    "started",
			}).Info("start access")
		}
		log.Infof("Manually started download via web confirmation: %q (start-id %s)", rec.Title, id)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Torrent submitted.") //nolint:errcheck
	}
}

// startWebServer starts the HTTP server on addr, identifying it in the log
// as name (e.g. "public", "private"). Blocks until the server stops;
// intended to be called in a goroutine.
func startWebServer(name string, mux *http.ServeMux, addr string) {
	log.Infof("Starting %s web server on http://%s", name, addr)
	if err := http.ListenAndServe(addr, mux); err != nil { //nolint:gosec
		log.WithError(err).Errorf("%s web server stopped", name)
	}
}
