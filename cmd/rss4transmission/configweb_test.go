package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const (
	uploadUser = "admin"
	uploadPass = "s3cret"
	oldConfig  = "# old config\nSeenFile: old.json\n"
	newConfig  = "# new config\nSeenFile: new.json\n"
)

// uploadFixture is a config file on disk plus the handler that replaces it.
type uploadFixture struct {
	mux     *http.ServeMux
	path    string
	reloads int
	// reloadErrs is consumed one per reload call; nil entries mean success.
	reloadErrs []error
}

func newUploadFixture(t *testing.T) *uploadFixture {
	t.Helper()
	f := &uploadFixture{path: filepath.Join(t.TempDir(), "config.yaml")}
	require.NoError(t, os.WriteFile(f.path, []byte(oldConfig), 0640)) //nolint:gosec // the test needs a non-default mode

	hash, err := bcrypt.GenerateFromPassword([]byte(uploadPass), bcrypt.MinCost)
	require.NoError(t, err)

	f.mux = http.NewServeMux()
	registerConfigRoutes(f.mux, configUploadDeps{
		Path:         func() string { return f.path },
		User:         uploadUser,
		PasswordHash: string(hash),
		Reload: func() error {
			var err error
			if f.reloads < len(f.reloadErrs) {
				err = f.reloadErrs[f.reloads]
			}
			f.reloads++
			return err
		},
	}, navConfig{Config: navOn()})
	return f
}

func (f *uploadFixture) read(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.path)
	require.NoError(t, err)
	return string(b)
}

// post sends body as the "file" part of a multipart form.
func (f *uploadFixture) post(t *testing.T, body string, mod func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "config.yaml")
	require.NoError(t, err)
	_, err = part.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/config", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetBasicAuth(uploadUser, uploadPass)
	if mod != nil {
		mod(req)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func decodeUpload(t *testing.T, rec *httptest.ResponseRecorder) (ok bool, errMsg string) {
	t.Helper()
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return resp.OK, resp.Error
}

func TestConfigPage_RequiresAuth(t *testing.T) {
	f := newUploadFixture(t)

	for name, mod := range map[string]func(*http.Request){
		"no credentials": func(r *http.Request) {},
		"wrong password": func(r *http.Request) { r.SetBasicAuth(uploadUser, "nope") },
		"wrong user":     func(r *http.Request) { r.SetBasicAuth("root", uploadPass) },
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/config", nil)
			mod(req)
			rec := httptest.NewRecorder()
			f.mux.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "Basic")
		})
	}
}

func TestConfigPage_RendersUploadForm(t *testing.T) {
	f := newUploadFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	req.SetBasicAuth(uploadUser, uploadPass)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `type="file"`)
	assert.Contains(t, body, `<span class="here">Config</span>`)
	assert.NotContains(t, body, "old.json", "the page must not show the running config")
}

func TestConfigUpload_RequiresAuth(t *testing.T) {
	f := newUploadFixture(t)

	rec := f.post(t, newConfig, func(r *http.Request) { r.Header.Del("Authorization") })

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, oldConfig, f.read(t))
	assert.Zero(t, f.reloads)
}

func TestConfigUpload_RejectsForeignOrigin(t *testing.T) {
	f := newUploadFixture(t)

	for name, mod := range map[string]func(*http.Request){
		"foreign Origin":                    func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"cross-site fetch":                  func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site (not same-origin) fetch": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.post(t, newConfig, mod)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Equal(t, oldConfig, f.read(t))
			assert.Zero(t, f.reloads)
		})
	}
}

func TestConfigUpload_AcceptsSameOrigin(t *testing.T) {
	f := newUploadFixture(t)

	rec := f.post(t, newConfig, func(r *http.Request) {
		r.Header.Set("Origin", "http://"+r.Host)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	})

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestConfigUpload_RejectsOversizedBody(t *testing.T) {
	f := newUploadFixture(t)

	rec := f.post(t, "# "+strings.Repeat("x", maxConfigUploadBytes), nil)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, oldConfig, f.read(t))
	assert.Zero(t, f.reloads)
}

func TestConfigUpload_MissingFileIsBadRequest(t *testing.T) {
	f := newUploadFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader("x=y"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(uploadUser, uploadPass)
	rec := httptest.NewRecorder()

	f.mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	ok, msg := decodeUpload(t, rec)
	assert.False(t, ok)
	assert.NotEmpty(t, msg)
}

func TestConfigUpload_InvalidConfigIsRejectedWithoutWriting(t *testing.T) {
	f := newUploadFixture(t)

	rec := f.post(t, "Feeds:\n  - Name: Dup\n    URL: https://a\n  - Name: Dup\n    URL: https://b\n", nil)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	ok, msg := decodeUpload(t, rec)
	assert.False(t, ok)
	assert.Contains(t, msg, "invalid feed configuration")
	assert.Equal(t, oldConfig, f.read(t), "a rejected upload must leave the file alone")
	assert.NoFileExists(t, f.path+".bak")
	assert.Zero(t, f.reloads)
}

func TestConfigUpload_ValidConfigReplacesFileAndReloads(t *testing.T) {
	f := newUploadFixture(t)

	rec := f.post(t, newConfig, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ok, _ := decodeUpload(t, rec)
	assert.True(t, ok)
	assert.Equal(t, newConfig, f.read(t))
	bak, err := os.ReadFile(f.path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, oldConfig, string(bak))
	assert.Equal(t, 1, f.reloads)

	info, err := os.Stat(f.path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0640), info.Mode().Perm(), "the new file keeps the old mode")

	entries, err := os.ReadDir(filepath.Dir(f.path))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "temp file left behind")
	}
}

func TestConfigUpload_AcceptsRawBody(t *testing.T) {
	f := newUploadFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(newConfig))
	req.Header.Set("Content-Type", "application/x-yaml")
	req.SetBasicAuth(uploadUser, uploadPass)
	rec := httptest.NewRecorder()

	f.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, newConfig, f.read(t))
}

func TestConfigUpload_ReloadFailureRestoresOldFile(t *testing.T) {
	f := newUploadFixture(t)
	f.reloadErrs = []error{assert.AnError, nil}

	rec := f.post(t, newConfig, nil)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	ok, msg := decodeUpload(t, rec)
	assert.False(t, ok)
	assert.Contains(t, msg, assert.AnError.Error())
	assert.Equal(t, oldConfig, f.read(t), "a failed reload must restore the old file")
	assert.Equal(t, 2, f.reloads, "the old file must be reloaded after the restore")
}

func TestConfigUpload_ErrorDoesNotEchoSecrets(t *testing.T) {
	f := newUploadFixture(t)

	rec := f.post(t, "Transmission:\n  Password: hunter2\nFeeds: [unclosed", nil)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.NotContains(t, rec.Body.String(), "hunter2")
}

func TestConfigNav_LinkOnlyWhenEnabled(t *testing.T) {
	on := navConfig{Config: navOn()}
	off := navConfig{}

	mux := http.NewServeMux()
	registerNtfyRoutes(mux, staticNtfy(NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "t"}), on)
	_, body := getBody(t, mux, "/notifications")
	assert.Contains(t, navLine(t, body), `href="/config"`)

	mux = http.NewServeMux()
	registerNtfyRoutes(mux, staticNtfy(NtfyConfig{BaseURL: "https://ntfy.sh", Topic: "t"}), off)
	_, body = getBody(t, mux, "/notifications")
	assert.NotContains(t, navLine(t, body), "/config")
}

func TestSecureEqual(t *testing.T) {
	tests := map[string]struct {
		a, b string
		want bool
	}{
		"equal":              {"s3cret", "s3cret", true},
		"both empty":         {"", "", true},
		"different":          {"s3cret", "s3creT", false},
		"prefix of secret":   {"s3cre", "s3cret", false},
		"secret is a prefix": {"s3cretX", "s3cret", false},
		"empty vs set":       {"", "s3cret", false},
		"set vs empty":       {"s3cret", "", false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, secureEqual(tc.a, tc.b))
		})
	}
}

func TestBasicAuth_RejectsTheHashAsPassword(t *testing.T) {
	f := newUploadFixture(t)
	hash, err := bcrypt.GenerateFromPassword([]byte(uploadPass), bcrypt.MinCost)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	req.SetBasicAuth(uploadUser, string(hash))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestBasicAuth_BadHashRefusesEveryone(t *testing.T) {
	mux := http.NewServeMux()
	registerConfigRoutes(mux, configUploadDeps{
		Path:         func() string { return "unused" },
		User:         uploadUser,
		PasswordHash: uploadPass, // cleartext, not a hash
		Reload:       func() error { return nil },
	}, navConfig{Config: navOn()})

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	req.SetBasicAuth(uploadUser, uploadPass)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRegisterConfigRoutes_LogsThatThePageIsOn(t *testing.T) {
	origLog := log
	defer func() { log = origLog }()
	lg, buf := makeTestAccessLogger()
	log = lg

	newUploadFixture(t)

	out := buf.String()
	assert.Contains(t, out, "/config")
	assert.Contains(t, out, uploadUser)
	assert.NotContains(t, out, "$2", "the log must not show the password hash")
}
