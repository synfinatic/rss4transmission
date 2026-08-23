package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTorrent is the minimal torrent-get shape completionTestTransmissionServer
// serves back over RPC. LeftUntilDone == 0 means fully downloaded.
type fakeTorrent struct {
	ID            int64
	Name          string
	DownloadDir   string
	LeftUntilDone int64
	SizeWhenDone  int64 // bytes
}

// completionTestTransmissionServer simulates Transmission's "torrent-get" RPC
// method. torrents is read through a pointer so a test can change the
// reported list between check() calls.
func completionTestTransmissionServer(t *testing.T, torrents *[]fakeTorrent) *httptest.Server {
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
		case "torrent-get":
			list := make([]map[string]any, 0, len(*torrents))
			for _, tor := range *torrents {
				list = append(list, map[string]any{
					"id":            tor.ID,
					"name":          tor.Name,
					"downloadDir":   tor.DownloadDir,
					"leftUntilDone": tor.LeftUntilDone,
					"sizeWhenDone":  tor.SizeWhenDone,
				})
			}
			resp["result"] = "success"
			resp["arguments"] = map[string]any{"torrents": list}
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
}

// completionTestFailingTransmissionServer simulates Transmission where
// "torrent-get" always fails.
func completionTestFailingTransmissionServer(t *testing.T) *httptest.Server {
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
		case "torrent-get":
			resp["result"] = "error fetching torrents"
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
}

// newTestNtfyCaptureServer records both the Title header and the raw body of
// every request it receives, so tests can assert on rendered notification
// content beyond just the title.
func newTestNtfyCaptureServer(t *testing.T) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var titles, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		titles = append(titles, r.Header.Get("Title"))
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	return srv, &titles, &bodies
}

func TestCompletionMonitor_Check_AlreadyFinished_NoNotifyOnFirstSight(t *testing.T) {
	torrents := []fakeTorrent{{ID: 1, Name: "Foo", DownloadDir: "/downloads", LeftUntilDone: 0, SizeWhenDone: 1024}}
	transmissionSrv := completionTestTransmissionServer(t, &torrents)
	defer transmissionSrv.Close()
	ntfySrv, titles, _ := newTestNtfyCaptureServer(t)
	defer ntfySrv.Close()

	m := NewCompletionMonitor(newTestTransmissionClient(t, transmissionSrv.URL),
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "torrents"}), time.Minute)

	next := m.check()
	assert.Equal(t, time.Minute, next)
	assert.Empty(t, *titles, "no notification expected on first-ever sighting of a torrent")
}

func TestCompletionMonitor_Check_FalseToTrue_NotifiesOnce(t *testing.T) {
	torrents := []fakeTorrent{{ID: 1, Name: "Foo", DownloadDir: "/downloads", LeftUntilDone: 100}}
	transmissionSrv := completionTestTransmissionServer(t, &torrents)
	defer transmissionSrv.Close()
	ntfySrv, titles, _ := newTestNtfyCaptureServer(t)
	defer ntfySrv.Close()

	m := NewCompletionMonitor(newTestTransmissionClient(t, transmissionSrv.URL),
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "torrents"}), time.Minute)

	m.check()
	assert.Empty(t, *titles)

	torrents[0].LeftUntilDone = 0
	m.check()
	require.Len(t, *titles, 1)
	assert.Equal(t, "Torrent Complete", (*titles)[0])

	// Finished -> finished again must not re-notify.
	m.check()
	assert.Len(t, *titles, 1)
}

func TestCompletionMonitor_ApplyConfig_AdoptedOnNextCheck(t *testing.T) {
	torrents := []fakeTorrent{{ID: 1, Name: "Foo", DownloadDir: "/downloads", LeftUntilDone: 100}}
	transmissionSrv := completionTestTransmissionServer(t, &torrents)
	defer transmissionSrv.Close()
	ntfySrv, titles, _ := newTestNtfyCaptureServer(t)
	defer ntfySrv.Close()

	// Start with ntfy unconfigured: even a completion transition must not notify.
	m := NewCompletionMonitor(newTestTransmissionClient(t, transmissionSrv.URL), NtfyConfig{}, time.Minute)

	m.check()
	m.ApplyConfig(completionMonitorUpdate{
		Transmission: m.Transmission,
		Ntfy:         mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "torrents"}),
		Interval:     30 * time.Second,
	})

	// The queued update is not adopted until the next check().
	torrents[0].LeftUntilDone = 0
	next := m.check()
	assert.Equal(t, 30*time.Second, next)
	require.Len(t, *titles, 1)
	assert.Equal(t, "Torrent Complete", (*titles)[0])
}

func TestCompletionMonitor_Check_StaysFinished_NoRenotify(t *testing.T) {
	torrents := []fakeTorrent{{ID: 1, Name: "Foo", LeftUntilDone: 100}}
	transmissionSrv := completionTestTransmissionServer(t, &torrents)
	defer transmissionSrv.Close()
	ntfySrv, titles, _ := newTestNtfyCaptureServer(t)
	defer ntfySrv.Close()

	m := NewCompletionMonitor(newTestTransmissionClient(t, transmissionSrv.URL),
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "torrents"}), time.Minute)

	m.check()
	torrents[0].LeftUntilDone = 0
	m.check()
	require.Len(t, *titles, 1)

	// Two more consecutive polls with the torrent still finished.
	m.check()
	m.check()
	assert.Len(t, *titles, 1, "a torrent that stays finished must not re-notify")
}

func TestCompletionMonitor_Check_NtfyNotConfigured_NoNotify(t *testing.T) {
	torrents := []fakeTorrent{{ID: 1, Name: "Foo", LeftUntilDone: 100}}
	transmissionSrv := completionTestTransmissionServer(t, &torrents)
	defer transmissionSrv.Close()

	var posted bool
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	// BaseURL set but Topic empty: notifyCompleted must gate on both.
	m := NewCompletionMonitor(newTestTransmissionClient(t, transmissionSrv.URL),
		NtfyConfig{BaseURL: ntfySrv.URL}, time.Minute)

	m.check()
	torrents[0].LeftUntilDone = 0
	m.check()
	assert.False(t, posted, "ntfy must not be contacted when Topic is unset")
}

func TestCompletionMonitor_Check_RPCError_LeavesStateUnchanged(t *testing.T) {
	torrents := []fakeTorrent{{ID: 1, Name: "Foo", LeftUntilDone: 100}}
	goodSrv := completionTestTransmissionServer(t, &torrents)
	defer goodSrv.Close()
	ntfySrv, titles, _ := newTestNtfyCaptureServer(t)
	defer ntfySrv.Close()

	m := NewCompletionMonitor(newTestTransmissionClient(t, goodSrv.URL),
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "torrents"}), time.Minute)

	// Seed a known "seen, not finished" state via a real check.
	m.check()
	require.Contains(t, m.downloaded, int64(1))
	assert.False(t, m.downloaded[1])

	failingSrv := completionTestFailingTransmissionServer(t)
	defer failingSrv.Close()
	m.Transmission = newTestTransmissionClient(t, failingSrv.URL)

	next := m.check()
	assert.Equal(t, time.Minute, next)
	assert.Empty(t, *titles)
	require.Contains(t, m.downloaded, int64(1))
	assert.False(t, m.downloaded[1], "state must be left unchanged on an RPC error")

	// A later real transition must still be correctly detected.
	m.Transmission = newTestTransmissionClient(t, goodSrv.URL)
	torrents[0].LeftUntilDone = 0
	m.check()
	require.Len(t, *titles, 1)
}

func TestCompletionMonitor_Check_RemovedTorrentDropped_NoImpossibleState(t *testing.T) {
	torrents := []fakeTorrent{{ID: 1, Name: "Foo", LeftUntilDone: 0}}
	transmissionSrv := completionTestTransmissionServer(t, &torrents)
	defer transmissionSrv.Close()
	ntfySrv, titles, _ := newTestNtfyCaptureServer(t)
	defer ntfySrv.Close()

	m := NewCompletionMonitor(newTestTransmissionClient(t, transmissionSrv.URL),
		mustValidateNtfyConfig(t, NtfyConfig{BaseURL: ntfySrv.URL, Topic: "torrents"}), time.Minute)

	// Baseline sighting: already finished, no notify.
	m.check()
	assert.Empty(t, *titles)
	require.Contains(t, m.downloaded, int64(1))

	// The torrent disappears from Transmission's list entirely.
	torrents = torrents[:0]
	m.check()
	assert.NotContains(t, m.downloaded, int64(1))

	// It reappears, already finished. Treated as a fresh baseline sighting,
	// so it must not notify (and must not panic on the "impossible"
	// previously-finished-but-now-unknown state).
	torrents = append(torrents, fakeTorrent{ID: 1, Name: "Foo", LeftUntilDone: 0})
	assert.NotPanics(t, func() { m.check() })
	assert.Empty(t, *titles)
}

func TestCompletionMonitor_NotifyCompleted_PopulatesSizeFromSizeWhenDone(t *testing.T) {
	const gib = int64(1) << 30
	torrents := []fakeTorrent{{ID: 1, Name: "Foo", DownloadDir: "/downloads", LeftUntilDone: 100, SizeWhenDone: gib}}
	transmissionSrv := completionTestTransmissionServer(t, &torrents)
	defer transmissionSrv.Close()
	ntfySrv, titles, bodies := newTestNtfyCaptureServer(t)
	defer ntfySrv.Close()

	m := NewCompletionMonitor(newTestTransmissionClient(t, transmissionSrv.URL),
		mustValidateNtfyConfig(t, NtfyConfig{
			BaseURL:       ntfySrv.URL,
			Topic:         "torrents",
			CompletedBody: "{{.Size}} ({{.SizeBytes}} bytes)",
		}), time.Minute)

	m.check()
	torrents[0].LeftUntilDone = 0
	m.check()

	require.Len(t, *bodies, 1)
	assert.Equal(t, "1.00 GB (1073741824 bytes)", (*bodies)[0])
	require.Len(t, *titles, 1)
}
