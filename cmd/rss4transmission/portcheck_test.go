package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// portTestTransmissionServer simulates Transmission's "port-test" and
// "session-set" RPC methods. open controls the reported port-is-open value;
// swap it (via a pointer) between check() calls to simulate a transition.
func portTestTransmissionServer(t *testing.T, open *bool) *httptest.Server {
	t.Helper()
	const sessionID = "test-session-id"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") != sessionID {
			w.Header().Set("X-Transmission-Session-Id", sessionID)
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Method string `json:"method"`
			Tag    int    `json:"tag"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		resp := map[string]any{"tag": req.Tag}
		switch req.Method {
		case "port-test":
			resp["result"] = "success"
			resp["arguments"] = map[string]any{"port-is-open": *open}
		case "session-set":
			resp["result"] = "success"
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
}

func newTestTransmissionClient(t *testing.T, srvURL string) *transmissionrpc.Client {
	t.Helper()
	endpoint, err := url.Parse(srvURL)
	require.NoError(t, err)
	client, err := transmissionrpc.New(endpoint, nil)
	require.NoError(t, err)
	return client
}

func newTestNtfyServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var titles []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		titles = append(titles, r.Header.Get("Title"))
		w.WriteHeader(http.StatusOK)
	}))
	return srv, &titles
}

func TestPortMonitor_Check_NoGluetun_Open(t *testing.T) {
	open := true
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()
	ntfySrv, titles := newTestNtfyServer(t)
	defer ntfySrv.Close()

	m := NewPortMonitor(newTestTransmissionClient(t, transmissionSrv.URL), nil,
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, AlertTopic: "alerts"}))

	gotOpen, err := m.check()
	require.NoError(t, err)
	assert.True(t, gotOpen)
	assert.Empty(t, *titles, "no notification expected on first-ever check")
	require.NotNil(t, m.lastOpen)
	assert.True(t, *m.lastOpen)
}

func TestPortMonitor_Check_NoGluetun_Closed(t *testing.T) {
	open := false
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()
	ntfySrv, titles := newTestNtfyServer(t)
	defer ntfySrv.Close()

	m := NewPortMonitor(newTestTransmissionClient(t, transmissionSrv.URL), nil,
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, AlertTopic: "alerts"}))

	gotOpen, err := m.check()
	require.NoError(t, err)
	assert.False(t, gotOpen)
	assert.Empty(t, *titles, "no notification expected without a prior 'open' state")
	require.NotNil(t, m.lastOpen)
	assert.False(t, *m.lastOpen)
}

func TestPortMonitor_Check_OpenToClosed_NotifiesOnce(t *testing.T) {
	open := true
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()
	ntfySrv, titles := newTestNtfyServer(t)
	defer ntfySrv.Close()

	m := NewPortMonitor(newTestTransmissionClient(t, transmissionSrv.URL), nil,
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, AlertTopic: "alerts"}))

	_, err := m.check()
	require.NoError(t, err)
	assert.Empty(t, *titles)

	open = false
	_, err = m.check()
	require.NoError(t, err)
	require.Len(t, *titles, 1)
	assert.Equal(t, "Transmission Port Closed", (*titles)[0])

	// Closed -> closed again must not re-notify.
	_, err = m.check()
	require.NoError(t, err)
	assert.Len(t, *titles, 1)
}

func TestPortMonitor_Check_ClosedToOpen_NotifiesOnce(t *testing.T) {
	open := false
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()
	ntfySrv, titles := newTestNtfyServer(t)
	defer ntfySrv.Close()

	m := NewPortMonitor(newTestTransmissionClient(t, transmissionSrv.URL), nil,
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, AlertTopic: "alerts"}))

	_, err := m.check()
	require.NoError(t, err)
	assert.Empty(t, *titles, "no prior state, so closed on first check must not notify")

	open = true
	_, err = m.check()
	require.NoError(t, err)
	require.Len(t, *titles, 1)
	assert.Equal(t, "Transmission Port Open", (*titles)[0])

	// Open -> open again must not re-notify.
	_, err = m.check()
	require.NoError(t, err)
	assert.Len(t, *titles, 1)
}

func TestPortMonitor_Check_PortTestError_LeavesStateUnchangedAndDoesNotNotify(t *testing.T) {
	open := true
	transmissionSrv := portTestFailingTransmissionServer(t, new(int64))
	defer transmissionSrv.Close()
	ntfySrv, titles := newTestNtfyServer(t)
	defer ntfySrv.Close()

	m := NewPortMonitor(newTestTransmissionClient(t, transmissionSrv.URL), nil,
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, AlertTopic: "alerts"}))
	// Seed a known "open" state directly (bypassing the failing server), then
	// verify a port-test error neither flips lastOpen nor notifies.
	trueVal := true
	m.lastOpen = &trueVal

	_, err := m.check()
	require.Error(t, err)
	assert.Empty(t, *titles)
	require.NotNil(t, m.lastOpen)
	assert.True(t, *m.lastOpen, "lastOpen must be left unchanged on a port-test error")

	// A later real transition must still be correctly detected as open->closed.
	realSrv := portTestTransmissionServer(t, &open)
	defer realSrv.Close()
	m.Transmission = newTestTransmissionClient(t, realSrv.URL)
	open = false
	_, err = m.check()
	require.NoError(t, err)
	require.Len(t, *titles, 1)
	assert.Equal(t, "Transmission Port Closed", (*titles)[0])
}

func TestPortMonitor_Check_WithGluetun_UsesCheckVpnTunnel(t *testing.T) {
	open := true
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()
	ntfySrv, titles := newTestNtfyServer(t)
	defer ntfySrv.Close()

	gluetunSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports":[54321]}`))
	}))
	defer gluetunSrv.Close()

	client := newTestTransmissionClient(t, transmissionSrv.URL)
	g := &Gluetun{
		URL:           gluetunSrv.URL,
		Transmission:  client,
		lastRotate:    time.Now(),
		peerPort:      -1,
		retryAttempts: 1,
		retryDelay:    time.Millisecond,
	}

	m := NewPortMonitor(client, g, mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, AlertTopic: "alerts"}))

	gotOpen, err := m.check()
	require.NoError(t, err)
	assert.True(t, gotOpen)
	assert.Empty(t, *titles)

	open = false
	gotOpen, err = m.check()
	require.NoError(t, err)
	assert.False(t, gotOpen)
	require.Len(t, *titles, 1)
	assert.Equal(t, "Transmission Port Closed", (*titles)[0])
	// Gluetun's port-sync side effect (session-set on a closed port) should
	// still have run through CheckVpnTunnel().
	assert.Equal(t, int64(54321), g.peerPort)
}

func TestPortMonitor_CheckStartup_StillClosed_Notifies(t *testing.T) {
	open := false
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()
	ntfySrv, titles := newTestNtfyServer(t)
	defer ntfySrv.Close()

	m := NewPortMonitor(newTestTransmissionClient(t, transmissionSrv.URL), nil,
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, AlertTopic: "alerts"}))

	m.checkStartup()
	require.Len(t, *titles, 1)
	assert.Equal(t, "Transmission Port Closed", (*titles)[0])
}

func TestPortMonitor_CheckStartup_Open_DoesNotNotify(t *testing.T) {
	open := true
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()
	ntfySrv, titles := newTestNtfyServer(t)
	defer ntfySrv.Close()

	m := NewPortMonitor(newTestTransmissionClient(t, transmissionSrv.URL), nil,
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, AlertTopic: "alerts"}))

	m.checkStartup()
	assert.Empty(t, *titles)
}
