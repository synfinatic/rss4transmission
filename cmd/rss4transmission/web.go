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
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strconv"
)

//go:embed web/history.html
var historyTmpl string

//go:embed web/cancel.html
var cancelTmpl string

// removeFunc is the signature for removing torrents from Transmission.
type removeFunc func(ctx context.Context, ids []int64) error

// progressFunc fetches live download progress for a single torrent from Transmission.
// Returns bytes downloaded so far and percentDone in [0,1]. If unavailable, callers
// should show "Unknown" rather than failing the request.
type progressFunc func(ctx context.Context, torrentID int64) (downloadedBytes int64, percentDone float64, err error)

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

// parseHistoryAddr normalises a --history-listen value to a "host:port" address.
// A bare port number is expanded to "127.0.0.1:<port>". Returns an error for
// invalid or out-of-range values.
func parseHistoryAddr(s string) (string, error) {
	// If it already contains a colon it is a host:port or [ipv6]:port.
	if _, _, err := net.SplitHostPort(s); err == nil {
		// Validate the port part.
		_, portStr, _ := net.SplitHostPort(s)
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

// newWebMux builds the shared HTTP mux. If history is non-nil, the history
// page is served at "/". The /healthz route is always registered.
func newWebMux(history *HistoryFile) *http.ServeMux {
	funcMap := template.FuncMap{
		"outcomeClass": func(outcome string) string {
			switch outcome {
			case "dispatched", "downloaded":
				return "dispatched"
			case "error":
				return "error"
			default:
				return "skipped"
			}
		},
	}
	tmpl := template.Must(template.New("history").Funcs(funcMap).Parse(historyTmpl))

	mux := http.NewServeMux()

	if history != nil {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			records := history.GetRecords()
			for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
				records[i], records[j] = records[j], records[i]
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := tmpl.Execute(w, records); err != nil {
				log.WithError(err).Error("Failed to render history template")
			}
		})
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return mux
}

// newCancelMux builds a public-facing mux serving only GET /cancel, POST /cancel,
// and GET /healthz. Use this when --cancel-listen is set to expose the cancel
// endpoint on its own port, keeping the history page on a separate internal listener.
// POST /cancel is only registered when both store and remove are non-nil.
func newCancelMux(store *Store, cfg CancelConfig, remove removeFunc, getProgress progressFunc) *http.ServeMux {
	mux := http.NewServeMux()
	if store != nil {
		mux.HandleFunc("GET /cancel", makeGetCancelHandler(store, cfg, getProgress))
		if remove != nil {
			mux.HandleFunc("POST /cancel", makePostCancelHandler(store, cfg, remove))
		}
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// registerCancelRoutes adds GET /cancel and POST /cancel handlers to mux.
func registerCancelRoutes(mux *http.ServeMux, store *Store, cfg CancelConfig, remove removeFunc, getProgress progressFunc) {
	mux.HandleFunc("GET /cancel", makeGetCancelHandler(store, cfg, getProgress))
	mux.HandleFunc("POST /cancel", makePostCancelHandler(store, cfg, remove))
}

// tokenErrorResponse translates a parseCancelToken error into the appropriate HTTP response.
func tokenErrorResponse(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrMissingCancelParams) {
		http.Error(w, "missing required parameters", http.StatusBadRequest)
	} else if errors.Is(err, ErrTokenExpired) {
		http.Error(w, "cancel link has expired", http.StatusGone)
	} else {
		http.Error(w, "invalid token", http.StatusBadRequest)
	}
}

// makeGetCancelHandler serves the confirmation form. It validates the token,
// peeks the store for metadata without consuming the entry, and queries
// Transmission for live download progress via getProgress.
func makeGetCancelHandler(store *Store, cfg CancelConfig, getProgress progressFunc) http.HandlerFunc {
	secret := []byte(cfg.HMACSecret)
	tmpl := template.Must(template.New("cancel").Parse(cancelTmpl))
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id := q.Get("id")
		expires, err := parseCancelToken(secret, id, q.Get("expires"), q.Get("sig"))
		if err != nil {
			tokenErrorResponse(w, err)
			return
		}
		sig := q.Get("sig")

		torrentID, meta, ok := store.Peek(id)
		if !ok {
			http.Error(w, "download not found or already cancelled", http.StatusNotFound)
			return
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
func makePostCancelHandler(store *Store, cfg CancelConfig, remove removeFunc) http.HandlerFunc {
	secret := []byte(cfg.HMACSecret)
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form body", http.StatusBadRequest)
			return
		}

		id := r.FormValue("id")
		_, err := parseCancelToken(secret, id, r.FormValue("expires"), r.FormValue("sig"))
		if err != nil {
			tokenErrorResponse(w, err)
			return
		}

		// Peek (not Take) so the entry survives a failed remove and the user can retry.
		torrentID, _, ok := store.Peek(id)
		if !ok {
			http.Error(w, "download not found or already cancelled", http.StatusNotFound)
			return
		}

		if err := remove(r.Context(), []int64{torrentID}); err != nil {
			log.WithError(err).Errorf("Failed to remove torrent %d from Transmission", torrentID)
			http.Error(w, "failed to cancel download", http.StatusInternalServerError)
			return
		}

		// Remove succeeded: consume the store entry.
		store.Take(id) //nolint:errcheck

		log.Infof("Cancelled download via web confirmation: torrent %d (cancel-id %s)", torrentID, id)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Download cancelled.") //nolint:errcheck
	}
}

// startWebServer starts the HTTP server on addr. Blocks until the server
// stops; intended to be called in a goroutine.
func startWebServer(mux *http.ServeMux, addr string) {
	log.Infof("Starting web server on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil { //nolint:gosec
		log.WithError(err).Error("Web server stopped")
	}
}
