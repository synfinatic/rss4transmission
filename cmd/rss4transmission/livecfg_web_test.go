package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The handlers below take getters rather than values so that a config reload is
// visible to the next request. Each test flips the getter mid-test and asserts
// the change took effect without re-registering the route.

func staticNotif(cfg NotificationsConfig) func() NotificationsConfig {
	return func() NotificationsConfig { return cfg }
}

func staticNtfy(cfg NtfyConfig) func() NtfyConfig {
	return func() NtfyConfig { return cfg }
}

func staticTx(cfg Transmission) func() Transmission {
	return func() Transmission { return cfg }
}

func staticSpeed(s *SpeedFile) func() *SpeedFile {
	return func() *SpeedFile { return s }
}

// staticExitIP pins one exit-IP source for a test. Production hands
// registerSpeedRoutes a getter so a Gluetun block added or removed by a reload
// changes the answer.
func staticExitIP(f exitIPFunc) func() exitIPFunc {
	return func() exitIPFunc { return f }
}

func staticActions(a speedActions) func() speedActions {
	return func() speedActions { return a }
}

func navOn() func() bool { return func() bool { return true } }

// txConfigFor builds a Transmission block that points at a test server.
func txConfigFor(t *testing.T, srv *httptest.Server, user, pass string) Transmission {
	t.Helper()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return Transmission{
		WebUI:    true,
		Host:     u.Hostname(),
		Port:     port,
		Username: user,
		Password: pass,
	}
}

// --- /cancel and /start read the live HMAC secret ---

func TestCancelRoutes_SecretIsReadPerRequest(t *testing.T) {
	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{Title: "My Show S01E01", FeedName: "shows"})

	var live atomic.Value
	live.Store(makeCancelCfg("first", "https://example.com"))

	mux := http.NewServeMux()
	registerCancelRoutes(mux, store, func() NotificationsConfig {
		return live.Load().(NotificationsConfig)
	}, makeRemoveFunc(new(bool)), noProgressFunc(), nil)

	// A token signed with the second secret must fail while the first is live.
	expires, sig := GenerateToken([]byte("second"), "test-id", time.Hour)
	get := func() int {
		req := httptest.NewRequest("GET",
			fmt.Sprintf("/cancel?id=test-id&expires=%d&sig=%s", expires, sig), nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}
	assert.Equal(t, http.StatusBadRequest, get(), "token signed with the new secret must not verify yet")

	live.Store(makeCancelCfg("second", "https://example.com"))
	assert.Equal(t, http.StatusOK, get(), "handler must verify against the reloaded secret")
}

func TestStartRoutes_SecretIsReadPerRequest(t *testing.T) {
	store := NewStartStore(time.Hour)
	store.Register("test-id", StartMetadata{FeedName: "shows", GUID: "guid-1"})
	h := historyWithNotifiedRecord("shows", "guid-1", "My Show S01E01")

	var live atomic.Value
	live.Store(makeCancelCfg("first", "https://example.com"))

	mux := http.NewServeMux()
	registerStartRoutes(mux, store, func() NotificationsConfig {
		return live.Load().(NotificationsConfig)
	}, makeRetryFunc(new(bool), nil, 42, nil), h, nil)

	expires, sig := GenerateToken([]byte("second"), "test-id", time.Hour)
	get := func() int {
		req := httptest.NewRequest("GET",
			fmt.Sprintf("/start?id=test-id&expires=%d&sig=%s", expires, sig), nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}
	assert.Equal(t, http.StatusBadRequest, get(), "token signed with the new secret must not verify yet")

	live.Store(makeCancelCfg("second", "https://example.com"))
	assert.Equal(t, http.StatusOK, get(), "handler must verify against the reloaded secret")
}

// An empty secret disables the routes. They stay registered, so turning the
// secret back on does not need a restart.
func TestCancelAndStartRoutes_404WhileSecretIsEmpty(t *testing.T) {
	var live atomic.Value
	live.Store(NotificationsConfig{})
	get := func() NotificationsConfig { return live.Load().(NotificationsConfig) }

	store := NewStore(time.Hour)
	store.Register("test-id", 42, CancelMetadata{Title: "My Show", FeedName: "shows"})
	startStore := NewStartStore(time.Hour)
	startStore.Register("test-id", StartMetadata{FeedName: "shows", GUID: "guid-1"})
	h := historyWithNotifiedRecord("shows", "guid-1", "My Show S01E01")

	mux := http.NewServeMux()
	registerCancelRoutes(mux, store, get, makeRemoveFunc(new(bool)), noProgressFunc(), nil)
	registerStartRoutes(mux, startStore, get, makeRetryFunc(new(bool), nil, 42, nil), h, nil)

	for _, path := range []string{"/cancel", "/start"} {
		req := httptest.NewRequest("GET", path+"?id=test-id", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.Equalf(t, http.StatusNotFound, rr.Code, "%s with no secret configured", path)
	}

	live.Store(makeCancelCfg("secret", "https://example.com"))
	expires, sig := GenerateToken([]byte("secret"), "test-id", time.Hour)
	for _, path := range []string{"/cancel", "/start"} {
		req := httptest.NewRequest("GET",
			fmt.Sprintf("%s?id=test-id&expires=%d&sig=%s", path, expires, sig), nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.Equalf(t, http.StatusOK, rr.Code, "%s after the secret was configured", path)
	}
}

// --- /notify-complete ---

func TestNotifyCompleteRoute_GateIsLive(t *testing.T) {
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	var live atomic.Value
	live.Store(NtfyConfig{})

	mux := http.NewServeMux()
	registerNotifyCompleteRoute(mux, func() NtfyConfig {
		return live.Load().(NtfyConfig)
	}, staticNotif(NotificationsConfig{}), nil)

	post := func() int {
		body := bytes.NewBufferString(`{"name":"My.Show","dir":"/dl","id":1}`)
		req := httptest.NewRequest("POST", "/notify-complete", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}
	assert.Equal(t, http.StatusNotFound, post(), "ntfy is not configured yet")

	on := NtfyConfig{BaseURL: ntfySrv.URL, Topic: "t"}
	require.NoError(t, on.Validate())
	live.Store(on)
	assert.Equal(t, http.StatusOK, post(), "route must work once ntfy is configured")
}

// --- /transmission ---

func TestTransmissionRoutes_GateIsLive(t *testing.T) {
	// The upstream records the credentials it was given rather than echoing
	// them, so the assertion does not depend on the proxied body.
	var mu sync.Mutex
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		mu.Lock()
		gotUser, gotPass = user, pass
		mu.Unlock()
		_, _ = w.Write([]byte("upstream"))
	}))
	defer srv.Close()

	off := txConfigFor(t, srv, "admin", "first")
	off.WebUI = false
	var live atomic.Value
	live.Store(off)

	mux := http.NewServeMux()
	registerTransmissionRoutes(mux, func() Transmission {
		return live.Load().(Transmission)
	}, navConfig{Transmission: navOn()})

	for _, path := range []string{"/transmission", "/transmission/web/"} {
		if code, _ := getBody(t, mux, path); code != http.StatusNotFound {
			t.Errorf("%s status = %d while WebUI is false, want 404", path, code)
		}
	}

	live.Store(txConfigFor(t, srv, "admin", "second"))
	if code, body := getBody(t, mux, "/transmission"); code != http.StatusOK {
		t.Errorf("/transmission status = %d after WebUI was turned on, want 200", code)
	} else if !strings.Contains(body, `src="/transmission/web/"`) {
		t.Errorf("page does not frame /transmission/web/\ngot:\n%s", body)
	}
	if code, _ := getBody(t, mux, "/transmission/web/"); code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200", code)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "admin", gotUser)
	assert.Equal(t, "second", gotPass, "proxy must use the reloaded password")
}

// --- /speedtest ---

func TestSpeedRoutes_GateIsLive(t *testing.T) {
	var live atomic.Value
	live.Store((*SpeedFile)(nil))

	mux := http.NewServeMux()
	registerSpeedRoutes(mux, func() *SpeedFile {
		s, _ := live.Load().(*SpeedFile)
		return s
	}, nil, nil, nil, staticActions(speedActions{}), navConfig{})

	for _, path := range []string{"/speedtest", "/rotations"} {
		if code, _ := getBody(t, mux, path); code != http.StatusNotFound {
			t.Errorf("%s status = %d while SpeedTest is off, want 404", path, code)
		}
	}

	live.Store(tempSpeedFile(t))
	for _, path := range []string{"/speedtest", "/rotations"} {
		if code, _ := getBody(t, mux, path); code != http.StatusOK {
			t.Errorf("%s status = %d after SpeedTest was turned on, want 200", path, code)
		}
	}
}

// --- nav bar ---

func TestNav_ReflectsLiveToggles(t *testing.T) {
	h := &HistoryFile{Records: []HistoryRecord{}, guidIndex: map[string]int{}}
	var on atomic.Bool
	nav := navConfig{
		Speedtest:    on.Load,
		Transmission: on.Load,
	}
	mux := newWebMux(h, nil, nil, nil, nil, nav)

	_, body := getBody(t, mux, "/")
	assert.NotContains(t, navLine(t, body), `href="/speedtest"`)
	assert.NotContains(t, navLine(t, body), `href="/transmission"`)

	on.Store(true)
	_, body = getBody(t, mux, "/")
	assert.Contains(t, navLine(t, body), `href="/speedtest"`)
	assert.Contains(t, navLine(t, body), `href="/transmission"`)
}
