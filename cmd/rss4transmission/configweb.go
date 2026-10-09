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
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web/config.html
var configTmpl string

// maxConfigUploadBytes caps an upload. A real config is a few kilobytes.
const maxConfigUploadBytes = 1 << 20

// configUploadDeps is what the upload page needs from the rest of the program.
// The fields are plain values and funcs so that tests can fake them.
type configUploadDeps struct {
	// Path is the config file in effect.
	Path func() string
	// User and PasswordHash are the HTTP Basic credentials. They come from flags,
	// not from the config file, so a bad upload cannot lock the user out.
	// PasswordHash is a bcrypt hash, as made by `htpasswd -nbB`.
	User         string
	PasswordHash string
	// Reload applies the file on disk and returns the error. It is
	// configReloader.reloadNow in production.
	Reload func() error
}

// registerConfigRoutes adds GET /config and POST /config to mux. The caller
// registers them only when the feature is on, and only on the private mux.
//
// Both routes need the Basic credentials. POST also refuses a request from
// another origin: the private listener has no other protection, and a page on
// another site must not be able to replace the config.
func registerConfigRoutes(mux *http.ServeMux, deps configUploadDeps, nav navConfig) {
	pageNav := nav
	pageNav.Config = alwaysNav
	tmpl := template.Must(template.Must(
		template.New("config").Funcs(pageNav.navFuncs()).Parse(navTmpl)).Parse(configTmpl))

	var uploadMu sync.Mutex

	mux.HandleFunc("GET /config", basicAuth(deps, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, nil); err != nil {
			log.WithError(err).Error("Failed to render config template")
		}
	}))

	mux.HandleFunc("POST /config", basicAuth(deps, func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeUploadResult(w, http.StatusForbidden, "cross-origin upload refused")
			return
		}

		data, status, err := readUpload(w, r)
		if err != nil {
			writeUploadResult(w, status, err.Error())
			return
		}

		// Serialize uploads: two at once would race on the temp and .bak files.
		uploadMu.Lock()
		defer uploadMu.Unlock()

		if _, err := validateConfigBytes(data); err != nil {
			writeUploadResult(w, http.StatusUnprocessableEntity, err.Error())
			return
		}

		if status, err := applyUpload(deps, data); err != nil {
			writeUploadResult(w, status, err.Error())
			return
		}
		writeUploadResult(w, http.StatusOK, "")
	}))
}

// basicAuth wraps next in an HTTP Basic check. The user name is compared in
// constant time. The password is checked against a bcrypt hash, so the
// cleartext is never configured. The bcrypt check always runs, even for a
// wrong user or a missing header, so the time does not tell which field failed.
func basicAuth(deps configUploadDeps, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		userOK := secureEqual(user, deps.User)
		passOK := bcrypt.CompareHashAndPassword([]byte(deps.PasswordHash), []byte(pass)) == nil
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="rss4transmission config", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// secureEqual compares two strings in constant time. Both are padded to the
// longer length first, because subtle.ConstantTimeCompare returns at once when
// the lengths differ, and that would leak the length of the secret.
func secureEqual(a, b string) bool {
	n := max(len(a), len(b))
	pa := make([]byte, n)
	pb := make([]byte, n)
	copy(pa, a)
	copy(pb, b)
	sameLen := subtle.ConstantTimeEq(int32(len(a)), int32(len(b))) //nolint:gosec // G115: lengths are far below MaxInt32
	return subtle.ConstantTimeCompare(pa, pb)&sameLen == 1
}

// sameOrigin reports whether a browser request came from this site. A request
// with no Origin and no Sec-Fetch-Site header (curl, scripts) passes: it
// cannot come from a page on another site, and it still needs the credentials.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return true
}

// readUpload returns the config bytes from a multipart "file" part or from a
// raw request body. The status is meaningful only when err is not nil.
func readUpload(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxConfigUploadBytes)

	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var data []byte
	var err error
	switch mediaType {
	case "multipart/form-data":
		data, err = readFilePart(r)
	case "application/x-www-form-urlencoded":
		return nil, http.StatusBadRequest, errors.New("no file in the request")
	default:
		data, err = io.ReadAll(r.Body)
	}

	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, http.StatusRequestEntityTooLarge,
				fmt.Errorf("file is larger than %d bytes", maxConfigUploadBytes)
		}
		return nil, http.StatusBadRequest, err
	}
	if len(data) == 0 {
		return nil, http.StatusBadRequest, errors.New("the file is empty")
	}
	return data, 0, nil
}

// readFilePart reads the form part named "file".
func readFilePart(r *http.Request) ([]byte, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no file in the request")
		}
		if err != nil {
			return nil, err
		}
		if part.FormName() == "file" {
			return io.ReadAll(part)
		}
	}
}

// applyUpload replaces the config file with data, keeps the old one as
// <path>.bak, and reloads. If the reload fails, it puts the old file back and
// reloads again, so the running program and the file on disk agree.
func applyUpload(deps configUploadDeps, data []byte) (int, error) {
	path := deps.Path()

	mode := os.FileMode(0600)
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
		if err := writeFileAtomic(path+".bak", old, mode); err != nil {
			return http.StatusInternalServerError, fmt.Errorf("unable to save a backup: %w", err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return http.StatusInternalServerError, fmt.Errorf("unable to read the current config: %w", err)
	}

	if err := writeFileAtomic(path, data, mode); err != nil {
		return http.StatusInternalServerError, fmt.Errorf(
			"unable to replace the config file (mount the config directory, not the file): %w", err)
	}

	reloadErr := deps.Reload()
	if reloadErr == nil {
		return 0, nil
	}

	// The new file passed validation but did not apply. Put the old one back.
	if old != nil {
		if err := writeFileAtomic(path, old, mode); err != nil {
			return http.StatusInternalServerError, fmt.Errorf(
				"reload failed (%v) and the old config could not be restored: %w", reloadErr, err)
		}
		if err := deps.Reload(); err != nil {
			log.WithError(err).Error("failed to reload the restored config")
		}
	}
	return http.StatusInternalServerError, fmt.Errorf("reload failed: %w", reloadErr)
}

// writeFileAtomic writes data to a temp file in the same directory and renames
// it over path, so a reader never sees a half-written file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path) //nolint:gosec // G703: path is the operator's config path, not request input
}

// writeUploadResult sends the JSON the page script reads. An empty message
// means success.
func writeUploadResult(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": msg == "", "error": msg})
}
