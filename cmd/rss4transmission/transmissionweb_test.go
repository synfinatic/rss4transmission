package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---- nav item ----

func TestNav_LinksTransmissionWhenEnabled(t *testing.T) {
	h := &HistoryFile{Records: []HistoryRecord{}, guidIndex: map[string]int{}}

	_, on := getBody(t, newWebMux(h, nil, nil, nil, nil, navConfig{Transmission: true}), "/")
	if !strings.Contains(navLine(t, on), `href="/transmission"`) {
		t.Errorf("torrents page nav is missing the Transmission link\ngot:\n%s", navLine(t, on))
	}

	_, off := getBody(t, newWebMux(h, nil, nil, nil, nil, navConfig{}), "/")
	if strings.Contains(navLine(t, off), `href="/transmission"`) {
		t.Errorf("torrents page links /transmission when the route is not registered\ngot:\n%s", navLine(t, off))
	}
}

// The VPN pages parse the same partial from a different template set, so they
// need the same func in their FuncMap.
func TestNav_SpeedPagesLinkTransmission(t *testing.T) {
	sf := &SpeedFile{}
	mux := http.NewServeMux()
	registerSpeedRoutes(mux, sf, nil, nil, speedActions{}, navConfig{Transmission: true})

	for _, page := range []string{"/speedtest", "/rotations"} {
		_, body := getBody(t, mux, page)
		if !strings.Contains(navLine(t, body), `href="/transmission"`) {
			t.Errorf("%s nav is missing the Transmission link\ngot:\n%s", page, navLine(t, body))
		}
	}
}

// ---- transmissionOrigin ----

func TestTransmissionOrigin(t *testing.T) {
	tests := []struct {
		name  string
		https bool
		host  string
		port  int
		want  string
	}{
		{"plain http", false, "gluetun", 9091, "http://gluetun:9091"},
		{"https", true, "tx.example.com", 443, "https://tx.example.com:443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := transmissionOrigin(tt.https, tt.host, tt.port)
			if err != nil {
				t.Fatalf("transmissionOrigin returned error: %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("transmissionOrigin() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTransmissionOrigin_RejectsEmptyHost(t *testing.T) {
	if _, err := transmissionOrigin(false, "", 9091); err == nil {
		t.Error("transmissionOrigin with an empty host returned nil error, want failure")
	}
}

// ---- page ----

// upstreamMux builds a mux with the Transmission routes pointed at srv.
func withUpstream(t *testing.T, srv *httptest.Server, user, pass string) *http.ServeMux {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("bad upstream URL: %v", err)
	}
	mux := http.NewServeMux()
	registerTransmissionRoutes(mux, target, user, pass, navConfig{Transmission: true})
	return mux
}

func TestTransmissionPage_FramesTheProxy(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	code, body := getBody(t, withUpstream(t, srv, "admin", "admin"), "/transmission")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, `src="/transmission/web/"`) {
		t.Errorf("page does not frame /transmission/web/\ngot:\n%s", body)
	}
	if !strings.Contains(navLine(t, body), `<span class="here">Transmission</span>`) {
		t.Errorf("page nav does not mark Transmission as current\ngot:\n%s", navLine(t, body))
	}
}

func TestTransmissionRoutes_NotRegistered(t *testing.T) {
	mux := http.NewServeMux()
	for _, path := range []string{"/transmission", "/transmission/web/"} {
		if code, _ := getBody(t, mux, path); code != http.StatusNotFound {
			t.Errorf("%s status = %d without the routes, want 404", path, code)
		}
	}
}

// On the real private mux the history page is the catch-all for "/", so an
// unregistered /transmission renders the torrents page instead of 404ing. What
// matters is that nothing reaches Transmission.
func TestTransmissionRoutes_NotRegisteredOnHistoryMux(t *testing.T) {
	h := &HistoryFile{Records: []HistoryRecord{}, guidIndex: map[string]int{}}
	mux := newWebMux(h, nil, nil, nil, nil, navConfig{})

	for _, path := range []string{"/transmission", "/transmission/web/"} {
		code, body := getBody(t, mux, path)
		if code != http.StatusOK {
			t.Errorf("%s status = %d, want the torrents page", path, code)
		}
		if !strings.Contains(body, "<title>RSS4Transmission Torrents</title>") {
			t.Errorf("%s did not render the torrents page", path)
		}
	}
}

// ---- proxy ----

func TestTransmissionProxy_ForwardsPathAndQuery(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("upstream body"))
	}))
	defer srv.Close()

	code, body := getBody(t, withUpstream(t, srv, "", ""), "/transmission/web/index.html?x=1")
	if gotURL != "/transmission/web/index.html?x=1" {
		t.Errorf("upstream saw %q, want the path and query unchanged", gotURL)
	}
	if code != http.StatusTeapot {
		t.Errorf("status = %d, want 418 passed through", code)
	}
	if body != "upstream body" {
		t.Errorf("body = %q, want the upstream body", body)
	}
}

func TestTransmissionProxy_ForwardsRPCPost(t *testing.T) {
	var gotMethod, gotBody, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotSession = r.Header.Get("X-Transmission-Session-Id")
		w.Header().Set("X-Transmission-Session-Id", "fresh-id")
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	req := httptest.NewRequest(http.MethodPost, "/transmission/rpc", strings.NewReader(`{"method":"session-get"}`))
	req.Header.Set("X-Transmission-Session-Id", "stale-id")
	rec := httptest.NewRecorder()
	withUpstream(t, srv, "", "").ServeHTTP(rec, req)

	if gotMethod != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", gotMethod)
	}
	if gotBody != `{"method":"session-get"}` {
		t.Errorf("upstream body = %q, want the request body", gotBody)
	}
	if gotSession != "stale-id" {
		t.Errorf("upstream session id = %q, want stale-id", gotSession)
	}
	if got := rec.Header().Get("X-Transmission-Session-Id"); got != "fresh-id" {
		t.Errorf("response session id = %q, want fresh-id", got)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 passed through", rec.Code)
	}
}

func TestTransmissionProxy_InjectsBasicAuth(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		pass     string
		wantUser string
		wantAuth bool
	}{
		{"credentials configured", "admin", "s3cret", "admin", true},
		{"no username means no header", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotUser, gotPass string
			var gotAuth bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUser, gotPass, gotAuth = r.BasicAuth()
			}))
			defer srv.Close()

			getBody(t, withUpstream(t, srv, tt.user, tt.pass), "/transmission/web/")

			if gotAuth != tt.wantAuth {
				t.Fatalf("upstream saw Basic auth = %v, want %v", gotAuth, tt.wantAuth)
			}
			if gotAuth && (gotUser != tt.wantUser || gotPass != tt.pass) {
				t.Errorf("upstream credentials = %q/%q, want %q/%q", gotUser, gotPass, tt.wantUser, tt.pass)
			}
		})
	}
}

// Transmission must not be able to break out of the frame. The proxy owns the
// response, so it removes the two headers that block framing and leaves the
// rest of the policy alone.
func TestTransmissionProxy_StripsFramingHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; default-src 'self'")
	}))
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/transmission/web/", nil)
	rec := httptest.NewRecorder()
	withUpstream(t, srv, "", "").ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options = %q, want it removed", got)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "frame-ancestors") {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors removed", csp)
	}
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want the other directives kept", csp)
	}
}

func TestTransmissionProxy_DropsCSPWhenOnlyFrameAncestors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	}))
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/transmission/web/", nil)
	rec := httptest.NewRecorder()
	withUpstream(t, srv, "", "").ServeHTTP(rec, req)

	if _, ok := rec.Header()["Content-Security-Policy"]; ok {
		t.Errorf("Content-Security-Policy = %q, want the empty header removed",
			rec.Header().Get("Content-Security-Policy"))
	}
}

// ---- target selection ----

func TestTransmissionProxyTarget(t *testing.T) {
	tests := []struct {
		name string
		cfg  Transmission
		want string
	}{
		{
			name: "enabled",
			cfg:  Transmission{WebUI: true, Host: "gluetun", Port: 9091},
			want: "http://gluetun:9091",
		},
		{
			name: "disabled by config",
			cfg:  Transmission{WebUI: false, Host: "gluetun", Port: 9091},
			want: "",
		},
		{
			name: "enabled but unusable host",
			cfg:  Transmission{WebUI: true, Host: "", Port: 9091},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transmissionProxyTarget(tt.cfg)
			if tt.want == "" {
				if got != nil {
					t.Errorf("transmissionProxyTarget() = %q, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("transmissionProxyTarget() = nil, want %q", tt.want)
			}
			if got.String() != tt.want {
				t.Errorf("transmissionProxyTarget() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The real private mux already serves "GET /" for the history page. Go rejects
// a "/transmission/" pattern next to it, because that pattern matches more
// methods on a more specific path. Register the proxy per method instead.
func TestTransmissionRoutes_CoexistWithHistoryMux(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Method))
	}))
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("bad upstream URL: %v", err)
	}

	h := &HistoryFile{Records: []HistoryRecord{}, guidIndex: map[string]int{}}
	mux := newWebMux(h, nil, nil, nil, nil, navConfig{Transmission: true})
	registerTransmissionRoutes(mux, target, "", "", navConfig{Transmission: true})

	if code, _ := getBody(t, mux, "/"); code != http.StatusOK {
		t.Errorf("history page status = %d, want 200", code)
	}
	if code, _ := getBody(t, mux, "/transmission"); code != http.StatusOK {
		t.Errorf("transmission page status = %d, want 200", code)
	}

	// Every method the web client uses has to reach the upstream.
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost,
		http.MethodPut, http.MethodDelete, http.MethodOptions} {
		req := httptest.NewRequest(method, "/transmission/rpc", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s /transmission/rpc status = %d, want 200", method, rec.Code)
		}
	}
}
