package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

func newTestGluetun(url string) *Gluetun {
	return &Gluetun{
		URL:             url,
		lastRotate:      time.Now(),
		peerPort:        -1,
		portCheckFailed: 0,
	}
}

func TestRotateNow_TimeBased(t *testing.T) {
	g := &Gluetun{
		RotateTime: 1 * time.Hour,
		lastRotate: time.Now().Add(-2 * time.Hour), // last rotate was 2 hours ago
	}
	if !g.rotateNow() {
		t.Error("rotateNow should return true when RotateTime has elapsed")
	}
}

func TestRotateNow_PortCheckFailures(t *testing.T) {
	g := &Gluetun{
		ClosedPortChecks: 3,
		portCheckFailed:  5, // exceeded threshold
		lastRotate:       time.Now(),
	}
	if !g.rotateNow() {
		t.Error("rotateNow should return true when portCheckFailed > ClosedPortChecks")
	}
}

func TestRotateNow_NoRotation(t *testing.T) {
	g := &Gluetun{
		RotateTime:       1 * time.Hour,
		ClosedPortChecks: 5,
		portCheckFailed:  2,
		lastRotate:       time.Now(), // just rotated
	}
	if g.rotateNow() {
		t.Error("rotateNow should return false when neither condition is met")
	}
}

func TestRotateNow_ZeroRotateTime(t *testing.T) {
	g := &Gluetun{
		RotateTime:       0, // disabled
		ClosedPortChecks: 0, // disabled
		portCheckFailed:  0,
		lastRotate:       time.Now().Add(-24 * time.Hour),
	}
	if g.rotateNow() {
		t.Error("rotateNow should return false when both thresholds are zero/disabled")
	}
}

func TestGetPort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/portforward" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports":[51820]}`))
	}))
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	port, err := g.getPort()
	if err != nil {
		t.Fatalf("getPort returned error: %v", err)
	}
	if port != 51820 {
		t.Errorf("port = %d, want 51820", port)
	}
}

func TestGetStatus_Running(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	status, err := g.getStatus()
	if err != nil {
		t.Fatalf("getStatus returned error: %v", err)
	}
	if status != VPNUp {
		t.Errorf("status = %v, want VPNUp", status)
	}
}

func TestGetStatus_Stopped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"stopped"}`))
	}))
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	status, err := g.getStatus()
	if err != nil {
		t.Fatalf("getStatus returned error: %v", err)
	}
	if status != VPNDown {
		t.Errorf("status = %v, want VPNDown", status)
	}
}

func TestGetPublicIp(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"public_ip":"1.2.3.4"}`))
	}))
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	ip, err := g.getPublicIp()
	if err != nil {
		t.Fatalf("getPublicIp returned error: %v", err)
	}
	if ip != "1.2.3.4" {
		t.Errorf("ip = %q, want 1.2.3.4", ip)
	}
	if gotPath != "/v1/publicip/ip" {
		t.Errorf("path = %q, want /v1/publicip/ip", gotPath)
	}
}

func TestNewRequest_BasicAuth(t *testing.T) {
	g := &Gluetun{
		URL:          "http://localhost",
		AuthUsername: "user",
		AuthPassword: "pass",
	}
	req, err := g.newRequest(http.MethodGet, "http://localhost/test", nil)
	if err != nil {
		t.Fatalf("newRequest returned error: %v", err)
	}
	authHeader := req.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Basic ") {
		t.Fatalf("Authorization header = %q, want Basic ...", authHeader)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
	if err != nil {
		t.Fatalf("could not decode Authorization header: %v", err)
	}
	if string(decoded) != "user:pass" {
		t.Errorf("decoded auth = %q, want user:pass", string(decoded))
	}
}

func TestNewRequest_APIKey(t *testing.T) {
	g := &Gluetun{
		URL:        "http://localhost",
		AuthAPIKey: "secret-key",
	}
	req, err := g.newRequest(http.MethodGet, "http://localhost/test", nil)
	if err != nil {
		t.Fatalf("newRequest returned error: %v", err)
	}
	apiKey := req.Header.Get("X-API-Key")
	if apiKey != "secret-key" {
		t.Errorf("X-API-Key = %q, want secret-key", apiKey)
	}
}

func TestNewRequest_NoAuth(t *testing.T) {
	g := &Gluetun{URL: "http://localhost"}
	req, err := g.newRequest(http.MethodGet, "http://localhost/test", nil)
	if err != nil {
		t.Fatalf("newRequest returned error: %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("Authorization header should not be set when no credentials configured")
	}
	if req.Header.Get("X-API-Key") != "" {
		t.Error("X-API-Key header should not be set when no API key configured")
	}
}

// portTestFailingTransmissionServer simulates Transmission where the "port-test"
// RPC method always fails (e.g. because it can't reach the external port checker
// service), but records the peer-port set via "session-set".
func portTestFailingTransmissionServer(t *testing.T, gotPeerPort *int64) *httptest.Server {
	t.Helper()
	const sessionID = "test-session-id"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") != sessionID {
			w.Header().Set("X-Transmission-Session-Id", sessionID)
			w.WriteHeader(http.StatusConflict)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var req struct {
			Method    string `json:"method"`
			Tag       int    `json:"tag"`
			Arguments struct {
				PeerPort int64 `json:"peer-port"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}

		resp := map[string]any{"tag": req.Tag}
		switch req.Method {
		case "port-test":
			resp["result"] = "Couldn't test port: No Response (0)"
		case "session-set":
			atomic.StoreInt64(gotPeerPort, req.Arguments.PeerPort)
			resp["result"] = "success"
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestCheckVpnTunnel_PortTestErrors_StillSyncsPort(t *testing.T) {
	var gotPeerPort int64
	transmissionSrv := portTestFailingTransmissionServer(t, &gotPeerPort)
	defer transmissionSrv.Close()
	endpoint, err := url.Parse(transmissionSrv.URL)
	if err != nil {
		t.Fatalf("parse transmission url: %v", err)
	}
	client, err := transmissionrpc.New(endpoint, nil)
	if err != nil {
		t.Fatalf("new transmission client: %v", err)
	}

	gluetunSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports":[12345]}`))
	}))
	defer gluetunSrv.Close()

	g := &Gluetun{
		URL:           gluetunSrv.URL,
		Transmission:  client,
		lastRotate:    time.Now(),
		peerPort:      -1,
		retryAttempts: 1,
		retryDelay:    time.Millisecond,
	}

	open, err := g.CheckVpnTunnel()
	if err == nil {
		t.Errorf("CheckVpnTunnel() err = nil, want non-nil (port-test failure must be reported to the caller)")
	}
	if open {
		t.Errorf("CheckVpnTunnel() open = true, want false")
	}

	if got := atomic.LoadInt64(&gotPeerPort); got != 12345 {
		t.Errorf("transmission peer port = %d, want 12345 (port-test errors must not block syncing the known Gluetun port)", got)
	}
	if g.peerPort != 12345 {
		t.Errorf("g.peerPort = %d, want 12345", g.peerPort)
	}
}

func TestRequestRotate_MakesRotateNowTrue(t *testing.T) {
	g := &Gluetun{lastRotate: time.Now()}

	if g.rotateNow() {
		t.Fatal("rotateNow() = true before any request, want false")
	}
	if reason := g.PendingRotate(); reason != "" {
		t.Errorf("PendingRotate() = %q, want empty", reason)
	}

	g.RequestRotate(RotationSourceSpeedtest, "slow: 12.5 Mbps")

	if !g.rotateNow() {
		t.Error("rotateNow() = false after RequestRotate, want true")
	}
	if reason := g.PendingRotate(); reason != "slow: 12.5 Mbps" {
		t.Errorf("PendingRotate() = %q, want %q", reason, "slow: 12.5 Mbps")
	}
}

// RequestRotate must not clobber an already-pending reason: the first caller
// to notice a problem owns the explanation that eventually reaches the logs
// and the ntfy alert.
func TestRequestRotate_KeepsFirstReason(t *testing.T) {
	g := &Gluetun{lastRotate: time.Now()}

	g.RequestRotate(RotationSourceSpeedtest, "first")
	g.RequestRotate(RotationSourceSpeedtest, "second")

	if reason := g.PendingRotate(); reason != "first" {
		t.Errorf("PendingRotate() = %q, want %q", reason, "first")
	}
}

// An empty reason is not a rotation request -- it would be indistinguishable
// from "nothing pending" in the internal representation.
func TestRequestRotate_IgnoresEmptyReason(t *testing.T) {
	g := &Gluetun{lastRotate: time.Now()}

	g.RequestRotate(RotationSourceSpeedtest, "")

	if g.rotateNow() {
		t.Error("rotateNow() = true after empty RequestRotate, want false")
	}
}

func TestRotate_ClearsPendingRequest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	g.RequestRotate(RotationSourceSpeedtest, "slow")

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}

	if reason := g.PendingRotate(); reason != "" {
		t.Errorf("PendingRotate() = %q after rotate(), want empty", reason)
	}
	if g.rotateNow() {
		t.Error("rotateNow() = true after rotate(), want false")
	}
}

// A failed rotation must leave the request pending so the next tick retries it.
func TestRotate_FailureKeepsPendingRequest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"status":"stopped"}`))
	}))
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.RequestRotate(RotationSourceSpeedtest, "slow")

	if err := g.rotate(); err == nil {
		t.Fatal("rotate() returned nil error, want failure when VPN stays down")
	}

	if reason := g.PendingRotate(); reason != "slow" {
		t.Errorf("PendingRotate() = %q after failed rotate(), want %q", reason, "slow")
	}
}

// RequestRotate is called from the SpeedMonitor goroutine while the PortMonitor
// goroutine reads it via rotateNow(); run under -race to prove rotateMu covers it.
func TestRequestRotate_ConcurrentAccess(t *testing.T) {
	g := &Gluetun{lastRotate: time.Now()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			g.RequestRotate(RotationSourceSpeedtest, "slow")
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = g.rotateNow()
		_ = g.PendingRotate()
	}
	<-done
}

// --- post-rotation exit IP reporting ---

// newRotateServer fakes the Gluetun endpoints rotate() touches. Each call to
// /v1/publicip/ip returns the next entry in ips, sticking on the last one, so a
// test can describe "the IP changed on the second poll" as a list.
func newRotateServer(ips []string) *httptest.Server {
	var n int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/v1/publicip/ip" {
			i := int(atomic.AddInt32(&n, 1)) - 1
			if i >= len(ips) {
				i = len(ips) - 1
			}
			_, _ = w.Write([]byte(`{"public_ip":"` + ips[i] + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
}

func TestRotate_ReportsNewExitIP(t *testing.T) {
	ts := newRotateServer([]string{"1.1.1.1", "2.2.2.2"})
	defer ts.Close()

	var gotPrev, gotNew string
	var calls int
	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = time.Second
	g.OnRotated = func(o RotationOutcome) {
		previousIP, newIP := o.PreviousIP, o.NewIP
		calls++
		gotPrev, gotNew = previousIP, newIP
	}

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}

	if calls != 1 {
		t.Errorf("OnRotated called %d times, want 1", calls)
	}
	if gotPrev != "1.1.1.1" {
		t.Errorf("previousIP = %q, want %q", gotPrev, "1.1.1.1")
	}
	if gotNew != "2.2.2.2" {
		t.Errorf("newIP = %q, want %q", gotNew, "2.2.2.2")
	}
}

// A small server pool can hand back the same exit. That is a real outcome the
// user needs to see, not a reason to keep polling forever or report nothing.
func TestRotate_ReportsSameExitIP(t *testing.T) {
	ts := newRotateServer([]string{"1.1.1.1"})
	defer ts.Close()

	var gotPrev, gotNew string
	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = 5 * time.Millisecond
	g.OnRotated = func(o RotationOutcome) {
		previousIP, newIP := o.PreviousIP, o.NewIP
		gotPrev, gotNew = previousIP, newIP
	}

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}

	if gotPrev != "1.1.1.1" || gotNew != "1.1.1.1" {
		t.Errorf("OnRotated(%q, %q), want both %q", gotPrev, gotNew, "1.1.1.1")
	}
}

// Gluetun reports the VPN running before it has refreshed its public IP, so the
// first poll can still answer with the old address. Reporting that as the new
// exit would claim the rotation changed nothing when it did.
func TestRotate_WaitsForExitIPToRefresh(t *testing.T) {
	ts := newRotateServer([]string{"1.1.1.1", "1.1.1.1", "2.2.2.2"})
	defer ts.Close()

	var gotNew string
	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = time.Second
	g.OnRotated = func(o RotationOutcome) { gotNew = o.NewIP }

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}
	if gotNew != "2.2.2.2" {
		t.Errorf("newIP = %q, want %q once Gluetun refreshed it", gotNew, "2.2.2.2")
	}
}

// A rotation that never brought the VPN back up hasn't rotated anything, so
// there is no new exit IP to announce.
func TestRotate_NoHookOnFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"status":"stopped"}`))
	}))
	defer ts.Close()

	called := false
	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.OnRotated = func(RotationOutcome) { called = true }

	if err := g.rotate(); err == nil {
		t.Fatal("rotate() returned nil error, want failure when VPN stays down")
	}
	if called {
		t.Error("OnRotated called after a failed rotation, want no call")
	}
}

// getPublicIp failing must not block the rotation itself.
func TestRotate_SucceedsWhenPublicIPUnavailable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/publicip/ip" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
	defer ts.Close()

	var gotNew string
	called := false
	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = 5 * time.Millisecond
	g.OnRotated = func(o RotationOutcome) {
		called = true
		gotNew = o.NewIP
	}

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}
	if !called {
		t.Fatal("OnRotated not called, want a call reporting an unknown IP")
	}
	if gotNew != "" {
		t.Errorf("newIP = %q, want empty when Gluetun can't report one", gotNew)
	}
}

func TestRequestRotate_ReportsWhetherAccepted(t *testing.T) {
	g := &Gluetun{}

	if !g.RequestRotate(RotationSourceSpeedtest, "first") {
		t.Error("RequestRotate = false on an idle Gluetun, want true")
	}
	if g.RequestRotate(RotationSourceSpeedtest, "second") {
		t.Error("RequestRotate = true with a request already pending, want false")
	}
	if g.RequestRotate(RotationSourceSpeedtest, "") {
		t.Error("RequestRotate = true for an empty reason, want false")
	}
	if got := g.PendingRotate(); got != "first" {
		t.Errorf("PendingRotate = %q, want the first reason to win", got)
	}
}

// rotate() clears the pending request before it finishes, so the tail of a
// rotation used to look idle: a second request landing there would be honored
// and rotate the tunnel all over again.
func TestRequestRotate_RefusedWhileRotationInProgress(t *testing.T) {
	ts := newRotateServer([]string{"1.1.1.1", "2.2.2.2"})
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = time.Second

	var accepted bool
	var pending string
	g.OnRotated = func(RotationOutcome) {
		accepted = g.RequestRotate(RotationSourceSpeedtest, "second click")
		pending = g.PendingRotate()
	}

	if !g.RequestRotate(RotationSourceSpeedtest, "first click") {
		t.Fatal("first RequestRotate = false, want true")
	}
	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}

	if accepted {
		t.Error("RequestRotate = true during a rotation, want false")
	}
	if pending != "" {
		t.Errorf("PendingRotate = %q during a rotation, want empty", pending)
	}
	if got := g.PendingRotate(); got != "" {
		t.Errorf("PendingRotate = %q after the rotation, want empty", got)
	}
}

func TestRequestRotate_AcceptedAgainAfterRotationFinishes(t *testing.T) {
	ts := newRotateServer([]string{"1.1.1.1", "2.2.2.2"})
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = time.Second

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}
	if !g.RequestRotate(RotationSourceSpeedtest, "later") {
		t.Error("RequestRotate = false after the rotation finished, want true")
	}
}

// The source has to come from rotate(), not from the caller that noticed the
// problem: RotateTime and ClosedPortChecks rotations have no requester at all.
func TestRotate_ReportsRequestedSource(t *testing.T) {
	ts := newRotateServer([]string{"1.1.1.1", "2.2.2.2"})
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = time.Second

	var got RotationOutcome
	g.OnRotated = func(o RotationOutcome) { got = o }
	g.RequestRotate(RotationSourceManual, "requested from the VPN page")

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}
	if got.Source != RotationSourceManual {
		t.Errorf("Source = %q, want %q", got.Source, RotationSourceManual)
	}
	if got.Reason != "requested from the VPN page" {
		t.Errorf("Reason = %q", got.Reason)
	}
}

func TestRotate_ReportsScheduleSource(t *testing.T) {
	ts := newRotateServer([]string{"1.1.1.1", "2.2.2.2"})
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = time.Second
	g.RotateTime = time.Hour
	g.lastRotate = time.Now().Add(-2 * time.Hour)

	var got RotationOutcome
	g.OnRotated = func(o RotationOutcome) { got = o }

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}
	if got.Source != RotationSourceSchedule {
		t.Errorf("Source = %q, want %q", got.Source, RotationSourceSchedule)
	}
	if got.Reason == "" {
		t.Error("Reason is empty, want an explanation for the history")
	}
}

func TestRotate_ReportsClosedPortSource(t *testing.T) {
	ts := newRotateServer([]string{"1.1.1.1", "2.2.2.2"})
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	g.statusPollDelay = time.Millisecond
	g.publicIPWait = time.Second
	g.ClosedPortChecks = 3
	g.portCheckFailed = 4

	var got RotationOutcome
	g.OnRotated = func(o RotationOutcome) { got = o }

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate() returned error: %v", err)
	}
	if got.Source != RotationSourceClosedPort {
		t.Errorf("Source = %q, want %q", got.Source, RotationSourceClosedPort)
	}
}
