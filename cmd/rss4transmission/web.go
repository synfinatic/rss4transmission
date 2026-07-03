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
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
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

// parseHistoryAddr normalises a --history-listen value to a "host:port" address.
// A bare port number is expanded to "127.0.0.1:<port>". Returns an error for
// invalid or out-of-range values.
func parseHistoryAddr(s string) (string, error) {
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
// accessLog is optional; when non-nil each request outcome is written to it.
func newCancelMux(store *Store, cfg CancelConfig, remove removeFunc, getProgress progressFunc, accessLog *logrus.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	if store != nil {
		mux.HandleFunc("GET /cancel", makeGetCancelHandler(store, cfg, getProgress, accessLog))
		if remove != nil {
			mux.HandleFunc("POST /cancel", makePostCancelHandler(store, cfg, remove, accessLog))
		}
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// registerCancelRoutes adds GET /cancel and POST /cancel handlers to mux.
// accessLog is optional; when non-nil each request outcome is written to it.
func registerCancelRoutes(mux *http.ServeMux, store *Store, cfg CancelConfig, remove removeFunc, getProgress progressFunc, accessLog *logrus.Logger) {
	mux.HandleFunc("GET /cancel", makeGetCancelHandler(store, cfg, getProgress, accessLog))
	mux.HandleFunc("POST /cancel", makePostCancelHandler(store, cfg, remove, accessLog))
}

// registerNotifyCompleteRoute adds POST /notify-complete to mux when ntfy is configured.
// When cancelCfg.HMACSecret is non-empty the endpoint requires
// Authorization: Bearer <HMACSecret>. accessLog is optional.
func registerNotifyCompleteRoute(mux *http.ServeMux, ntfyCfg NtfyConfig, cancelCfg CancelConfig, accessLog *logrus.Logger) {
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

func makeNotifyCompleteHandler(ntfyCfg NtfyConfig, cancelCfg CancelConfig, accessLog *logrus.Logger) http.HandlerFunc {
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
func makeGetCancelHandler(store *Store, cfg CancelConfig, getProgress progressFunc, accessLog *logrus.Logger) http.HandlerFunc {
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
func makePostCancelHandler(store *Store, cfg CancelConfig, remove removeFunc, accessLog *logrus.Logger) http.HandlerFunc {
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

// startWebServer starts the HTTP server on addr. Blocks until the server
// stops; intended to be called in a goroutine.
func startWebServer(mux *http.ServeMux, addr string) {
	log.Infof("Starting web server on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil { //nolint:gosec
		log.WithError(err).Error("Web server stopped")
	}
}
