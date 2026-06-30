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

// removeFunc is the signature for removing torrents from Transmission.
type removeFunc func(ctx context.Context, ids []int64) error

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

// newCancelMux builds a public-facing mux serving only DELETE /cancel and GET /healthz.
// Use this when --cancel-listen is set to expose the cancel endpoint on its own port,
// keeping the history page on a separate internal listener.
func newCancelMux(store *Store, cfg CancelConfig, remove removeFunc) *http.ServeMux {
	mux := http.NewServeMux()
	if store != nil {
		mux.HandleFunc("DELETE /cancel", makeCancelHandler(store, cfg, remove))
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// registerCancelRoutes adds the DELETE /cancel handler to mux.
func registerCancelRoutes(mux *http.ServeMux, store *Store, cfg CancelConfig, remove removeFunc) {
	mux.HandleFunc("DELETE /cancel", makeCancelHandler(store, cfg, remove))
}

func makeCancelHandler(store *Store, cfg CancelConfig, remove removeFunc) http.HandlerFunc {
	secret := []byte(cfg.HMACSecret)
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id := q.Get("id")
		expiresStr := q.Get("expires")
		sig := q.Get("sig")
		if id == "" || expiresStr == "" || sig == "" {
			http.Error(w, "missing required query parameters", http.StatusBadRequest)
			return
		}

		expires, err := strconv.ParseInt(expiresStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid expires parameter", http.StatusBadRequest)
			return
		}

		if err := ValidateToken(secret, id, expires, sig); err != nil {
			if errors.Is(err, ErrTokenExpired) {
				http.Error(w, "cancel link has expired", http.StatusGone)
			} else {
				http.Error(w, "invalid token", http.StatusBadRequest)
			}
			return
		}

		torrentID, ok := store.Take(id)
		if !ok {
			http.Error(w, "download not found or already cancelled", http.StatusNotFound)
			return
		}

		if err := remove(r.Context(), []int64{torrentID}); err != nil {
			log.WithError(err).Errorf("Failed to remove torrent %d from Transmission", torrentID)
			http.Error(w, "failed to cancel download", http.StatusInternalServerError)
			return
		}

		log.Infof("Cancelled download via ntfy action: torrent %d (cancel-id %s)", torrentID, id)
		w.WriteHeader(http.StatusOK)
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
