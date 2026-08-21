package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
	vpn := newVPNState()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			vpn.put(r)
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(vpn.statusBody()))
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
// vpnState tracks what a fake control server reports, mirroring Gluetun: a PUT
// sets the tunnel state and it stays there until the next PUT. Gluetun does not
// restart itself, so a fake that always answers "running" would hide exactly
// the bug these tests exist to catch.
type vpnState struct{ running atomic.Bool }

func newVPNState() *vpnState {
	v := &vpnState{}
	v.running.Store(true)
	return v
}

func (v *vpnState) put(r *http.Request) {
	var req StatusResponse
	_ = json.NewDecoder(r.Body).Decode(&req)
	v.running.Store(req.Status == "running")
}

func (v *vpnState) statusBody() string {
	if v.running.Load() {
		return `{"status":"running"}`
	}
	return `{"status":"stopped"}`
}

// /v1/publicip/ip returns the next entry in ips, sticking on the last one, so a
// test can describe "the IP changed on the second poll" as a list.
func newRotateServer(ips []string) *httptest.Server {
	var n int32
	vpn := newVPNState()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			vpn.put(r)
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
		_, _ = w.Write([]byte(vpn.statusBody()))
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
	vpn := newVPNState()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/publicip/ip" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			vpn.put(r)
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(vpn.statusBody()))
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

// Gluetun's control server answers an unauthorized request with a plain-text
// body and a 401. Parsing that as JSON produced "invalid character 'U'", which
// says nothing about the real problem -- and restartVPN() did not look at the
// status at all, so a rejected PUT read as a successful one and the rotation
// went on to wait for a tunnel it had never asked to stop. Every control call
// must fail loudly, naming the status and what the server said.
func TestControlCalls_RejectNon2xx(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		call func(*Gluetun) error
	}{
		{"getPort", http.StatusUnauthorized, "Unauthorized",
			func(g *Gluetun) error { _, err := g.getPort(); return err }},
		{"getStatus", http.StatusUnauthorized, "Unauthorized",
			func(g *Gluetun) error { _, err := g.getStatus(); return err }},
		{"getPublicIp", http.StatusUnauthorized, "Unauthorized",
			func(g *Gluetun) error { _, err := g.getPublicIp(); return err }},
		// The role that grants the GET routes need not grant the PUT, so this
		// is the one that fails on a working-looking deployment.
		{"restartVPN", http.StatusForbidden, "Forbidden",
			func(g *Gluetun) error { return g.restartVPN() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()

			err := tc.call(newTestGluetun(ts.URL))
			if err == nil {
				t.Fatal("no error for a rejected control request")
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%d", tc.code)) {
				t.Errorf("error does not name the HTTP status: %s", err)
			}
			if !strings.Contains(err.Error(), tc.body) {
				t.Errorf("error does not repeat what the server said: %s", err)
			}
		})
	}
}

// A rejected restartVPN() must abort the rotation immediately rather than
// polling for 30 seconds -- the tunnel was never asked to stop.
func TestRotate_FailsFastWhenRestartRejected(t *testing.T) {
	var statusPolls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Forbidden"))
		case r.URL.Path == "/v1/vpn/status":
			statusPolls++
			_, _ = w.Write([]byte(`{"status":"running"}`))
		default:
			_, _ = w.Write([]byte(`{"public_ip":"1.2.3.4"}`))
		}
	}))
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	err := g.rotate()
	if err == nil {
		t.Fatal("rotate() succeeded despite a rejected restart")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error does not name the rejection: %s", err)
	}
	if statusPolls != 0 {
		t.Errorf("polled status %d times after a rejected restart, want 0", statusPolls)
	}
}

// The retry loop incremented i twice per pass, so it gave up after 5 polls
// instead of 10 -- half the intended window for the tunnel to come back.
func TestRotate_PollsStatusTenTimes(t *testing.T) {
	var started bool
	var statusPolls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			var req StatusResponse
			_ = json.NewDecoder(r.Body).Decode(&req)
			started = started || req.Status == "running"
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/v1/vpn/status":
			// The tunnel never comes back, so every poll after the start is
			// one of the ten this test is counting.
			if started {
				statusPolls++
			}
			_, _ = w.Write([]byte(`{"status":"stopped"}`))
		default:
			_, _ = w.Write([]byte(`{"public_ip":"1.2.3.4"}`))
		}
	}))
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	if err := g.rotate(); err == nil {
		t.Fatal("rotate() succeeded while the tunnel stayed down")
	}
	if statusPolls != 10 {
		t.Errorf("polled status %d times, want 10", statusPolls)
	}
}

// gluetunServer models Gluetun's control server: PUT /v1/vpn/status sets the
// tunnel state and GET reports it. Gluetun does *not* restart itself after a
// stop, which is the behaviour that matters here.
type gluetunServer struct {
	mu        sync.Mutex
	running   bool
	puts      []string
	events    []string
	stopErr   int // non-zero => answer the stop with this status code
	statusErr int // non-zero => answer every GET status with this status code
	stopLag   int // how many GETs still report "running" after a stop request
	lagLeft   int
}

func newGluetunServer(t *testing.T, s *gluetunServer) *httptest.Server {
	t.Helper()
	s.running = true
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch {
		case r.URL.Path == "/v1/vpn/status" && r.Method == http.MethodPut:
			var req StatusResponse
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("undecodable PUT body: %v", err)
			}
			if req.Status == "stopped" && s.stopErr != 0 {
				w.WriteHeader(s.stopErr)
				_, _ = w.Write([]byte("Forbidden"))
				return
			}
			s.puts = append(s.puts, req.Status)
			s.events = append(s.events, "put:"+req.Status)
			s.running = req.Status == "running"
			if req.Status == "stopped" {
				s.lagLeft = s.stopLag
			}
			_, _ = w.Write([]byte(`{"outcome":"` + req.Status + `"}`))
		case r.URL.Path == "/v1/vpn/status":
			s.events = append(s.events, "get")
			if s.statusErr != 0 {
				w.WriteHeader(s.statusErr)
				_, _ = w.Write([]byte("Internal Server Error"))
				return
			}
			state := "stopped"
			if s.running {
				state = "running"
			}
			// Gluetun can still report the old state for a moment after
			// accepting a stop.
			if s.lagLeft > 0 {
				s.lagLeft--
				state = "running"
			}
			_, _ = w.Write([]byte(`{"status":"` + state + `"}`))
		default:
			_, _ = w.Write([]byte(`{"public_ip":"1.2.3.4"}`))
		}
	}))
}

func (s *gluetunServer) sentPuts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.puts...)
}

func (s *gluetunServer) sentEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// Gluetun stops on request and stays stopped. Asking it to stop and then
// waiting for it to come back on its own leaves the tunnel down and the
// rotation reporting "VPN Failed to come back up" -- the start has to be a
// second, explicit call.
func TestRestartVPN_StopsThenStarts(t *testing.T) {
	state := &gluetunServer{}
	ts := newGluetunServer(t, state)
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	if err := g.restartVPN(); err != nil {
		t.Fatalf("restartVPN returned error: %v", err)
	}

	want := []string{"stopped", "running"}
	got := state.sentPuts()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("PUT statuses = %v, want %v", got, want)
	}
	if !state.running {
		t.Error("tunnel left stopped after restartVPN")
	}
}

// A rejected stop means the tunnel is still up and serving traffic. Starting
// what was never stopped is at best pointless, so the error has to stop there.
func TestRestartVPN_NoStartAfterFailedStop(t *testing.T) {
	state := &gluetunServer{stopErr: http.StatusForbidden}
	ts := newGluetunServer(t, state)
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	if err := g.restartVPN(); err == nil {
		t.Fatal("restartVPN succeeded despite a rejected stop")
	}
	if got := state.sentPuts(); len(got) != 0 {
		t.Errorf("sent %v after a rejected stop, want nothing", got)
	}
}

// End to end against the modelled server: the tunnel is up again and the
// rotation reports the exit IP.
func TestRotate_BringsTunnelBackUp(t *testing.T) {
	state := &gluetunServer{}
	ts := newGluetunServer(t, state)
	defer ts.Close()

	var got RotationOutcome
	g := newTestGluetun(ts.URL)
	g.OnRotated = func(o RotationOutcome) { got = o }

	if err := g.rotate(); err != nil {
		t.Fatalf("rotate returned error: %v", err)
	}
	if !state.running {
		t.Error("tunnel left stopped after a successful rotation")
	}
	if got.NewIP != "1.2.3.4" {
		t.Errorf("NewIP = %q, want 1.2.3.4", got.NewIP)
	}
}

// The start must not be issued until Gluetun confirms the tunnel actually
// stopped. Checking is better than sleeping a fixed interval: it costs nothing
// when the stop lands immediately, and it does not guess wrong when it doesn't.
func TestRestartVPN_WaitsForTheStopToLand(t *testing.T) {
	state := &gluetunServer{stopLag: 2}
	ts := newGluetunServer(t, state)
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	if err := g.restartVPN(); err != nil {
		t.Fatalf("restartVPN returned error: %v", err)
	}

	// stopLag: 2 means the third GET is the first to report "stopped", so the
	// start must not appear before it.
	want := []string{"put:stopped", "get", "get", "get", "put:running"}
	got := state.sentEvents()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// A tunnel that never stops is a tunnel that is still up and carrying traffic.
// Abandon the rotation rather than issue a start against a running tunnel.
func TestRestartVPN_AbortsWhenTheTunnelNeverStops(t *testing.T) {
	state := &gluetunServer{stopLag: 100}
	ts := newGluetunServer(t, state)
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	err := g.restartVPN()
	if err == nil {
		t.Fatal("restartVPN succeeded while the tunnel stayed up")
	}
	if !strings.Contains(err.Error(), "running") {
		t.Errorf("error does not say the tunnel is still running: %s", err)
	}
	for _, p := range state.sentPuts() {
		if p == "running" {
			t.Error("issued a start against a tunnel that never stopped")
		}
	}
}

// When the status cannot be read at all, the tunnel may well be down -- and a
// tunnel left down blocks Transmission behind the killswitch indefinitely.
// Starting one that turns out to be running is the cheaper mistake.
func TestRestartVPN_StartsAnywayWhenStatusIsUnreadable(t *testing.T) {
	state := &gluetunServer{statusErr: http.StatusInternalServerError}
	ts := newGluetunServer(t, state)
	defer ts.Close()

	g := newTestGluetun(ts.URL)
	if err := g.restartVPN(); err != nil {
		t.Fatalf("restartVPN returned error: %v", err)
	}

	var started bool
	for _, p := range state.sentPuts() {
		if p == "running" {
			started = true
		}
	}
	if !started {
		t.Error("left the tunnel stopped when the status could not be read")
	}
}
