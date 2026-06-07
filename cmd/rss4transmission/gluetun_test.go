package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		_, _ = w.Write([]byte(`{"port":51820}`))
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
}

func TestNewRequest_BasicAuth(t *testing.T) {
	g := &Gluetun{
		URL:          "http://localhost",
		AuthUsername: "user",
		AuthPassword: "pass",
	}
	req := g.newRequest(http.MethodGet, "http://localhost/test", nil)
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
	req := g.newRequest(http.MethodGet, "http://localhost/test", nil)
	apiKey := req.Header.Get("X-API-Key")
	if apiKey != "secret-key" {
		t.Errorf("X-API-Key = %q, want secret-key", apiKey)
	}
}

func TestNewRequest_NoAuth(t *testing.T) {
	g := &Gluetun{URL: "http://localhost"}
	req := g.newRequest(http.MethodGet, "http://localhost/test", nil)
	if req.Header.Get("Authorization") != "" {
		t.Error("Authorization header should not be set when no credentials configured")
	}
	if req.Header.Get("X-API-Key") != "" {
		t.Error("X-API-Key header should not be set when no API key configured")
	}
}
