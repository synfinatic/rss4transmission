package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- token store TTL follows the config ---

func TestStore_SetTTL_AppliesToNewEntries(t *testing.T) {
	s := NewStore(time.Hour)
	s.SetTTL(4 * time.Hour)
	s.Register("id", 1, CancelMetadata{})

	val, ok := s.m.Load("id")
	require.True(t, ok)
	got := time.Until(val.(*storeEntry).expiresAt)
	assert.Greater(t, got, 3*time.Hour, "entry must expire on the new TTL")
}

func TestStartStore_SetTTL_AppliesToNewEntries(t *testing.T) {
	s := NewStartStore(time.Hour)
	s.SetTTL(4 * time.Hour)
	s.Register("id", StartMetadata{})

	val, ok := s.m.Load("id")
	require.True(t, ok)
	got := time.Until(val.(*startEntry).expiresAt)
	assert.Greater(t, got, 3*time.Hour, "entry must expire on the new TTL")
}

// The reaper wakes on the TTL. A reload that shortens the TTL must shorten the
// sweep too, otherwise expired tokens sit in memory for the old interval.
func TestStore_Reaper_FollowsTTLChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := NewStore(time.Hour)
	s.StartReaper(ctx)

	// Registered after the change, so the entry expires on the new TTL. The
	// sweep that removes it can only happen on the new interval: the reaper
	// started on a one-hour ticker.
	s.SetTTL(10 * time.Millisecond)
	s.Register("id", 1, CancelMetadata{})

	assert.Eventually(t, func() bool {
		_, _, ok := s.Peek("id")
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "reaper must sweep on the new TTL")
}

func TestStartStore_Reaper_FollowsTTLChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := NewStartStore(time.Hour)
	s.StartReaper(ctx)

	s.SetTTL(10 * time.Millisecond)
	s.Register("id", StartMetadata{})

	assert.Eventually(t, func() bool {
		_, ok := s.Peek("id")
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "reaper must sweep on the new TTL")
}

// A store built with no TTL still has to start reaping once a reload gives it
// one, since the stores are now created before the config is known to enable
// them.
func TestStore_Reaper_StartsAfterTTLBecomesPositive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := NewStore(0)
	s.StartReaper(ctx)
	s.SetTTL(10 * time.Millisecond)
	s.Register("id", 1, CancelMetadata{})

	assert.Eventually(t, func() bool {
		_, _, ok := s.Peek("id")
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "reaper must start once the TTL is set")
}

// --- the port monitor consumes a pushed config update ---

// A reload must not mutate Gluetun from the reload goroutine: check() holds
// m.mu for the whole of CheckVpnTunnel, which can run for tens of seconds
// during a rotation. The update is queued instead and applied on the next
// check.
func TestPortMonitor_ApplyConfig_ConsumedOnNextCheck(t *testing.T) {
	open := true
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()

	client := newTestTransmissionClient(t, transmissionSrv.URL)
	m := NewPortMonitor(client, nil, NtfyConfig{})

	newNtfy := mustValidateNtfyConfig(t, NtfyConfig{BaseURL: "https://ntfy.example.com", AlertTopic: "alerts"})
	m.ApplyConfig(portMonitorUpdate{
		Ntfy:         newNtfy,
		PortCheckOn:  true,
		Transmission: client,
	})

	assert.Equal(t, "", m.Ntfy.BaseURL, "a queued update must not be applied before the next check")

	_, checked, err := m.check()
	require.NoError(t, err)
	assert.True(t, checked)
	assert.Equal(t, "https://ntfy.example.com", m.Ntfy.BaseURL, "the queued update must be applied by check")
}

// Adding a Gluetun block to a running daemon attaches a client; removing it
// detaches one. Neither needs a restart.
func TestPortMonitor_ApplyConfig_AttachesAndDetachesGluetun(t *testing.T) {
	open := true
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()
	gluetunSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports":[54321]}`))
	}))
	defer gluetunSrv.Close()

	u, err := url.Parse(gluetunSrv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	client := newTestTransmissionClient(t, transmissionSrv.URL)
	m := NewPortMonitor(client, nil, NtfyConfig{})
	require.Nil(t, m.Gluetun)

	m.ApplyConfig(portMonitorUpdate{
		Gluetun:      GluetunConfig{Host: u.Hostname(), Port: port},
		GluetunOn:    true,
		Transmission: client,
	})
	_, _, err = m.check()
	require.NoError(t, err)
	require.NotNil(t, m.Gluetun, "a new Gluetun block must attach a client")
	assert.Equal(t, gluetunSrv.URL, m.Gluetun.URL)

	m.ApplyConfig(portMonitorUpdate{Transmission: client, PortCheckOn: true})
	_, _, err = m.check()
	require.NoError(t, err)
	assert.Nil(t, m.Gluetun, "removing the Gluetun block must detach the client")
}

// An existing client is reconfigured in place rather than rebuilt, so the
// rotation policy does not see a reload as a fresh tunnel.
func TestPortMonitor_ApplyConfig_UpdatesGluetunInPlace(t *testing.T) {
	open := true
	transmissionSrv := portTestTransmissionServer(t, &open)
	defer transmissionSrv.Close()

	client := newTestTransmissionClient(t, transmissionSrv.URL)
	g := &Gluetun{URL: "http://gluetun:8000", Transmission: client, lastRotate: time.Now(), peerPort: -1}
	m := NewPortMonitor(client, g, NtfyConfig{})

	m.ApplyConfig(portMonitorUpdate{
		Gluetun:      GluetunConfig{Host: "gluetun", Port: 8000, ClosedPortChecks: 7},
		GluetunOn:    true,
		Transmission: client,
	})
	_, _, err := m.check()
	require.NoError(t, err)

	assert.Same(t, g, m.Gluetun, "an existing client must be reconfigured, not replaced")
	assert.Equal(t, 7, m.Gluetun.ClosedPortChecks)
}

// PortCheck.Enabled is a live toggle. With it off and no Gluetun there is
// nothing to check, so the monitor keeps running and does nothing.
func TestPortMonitor_Check_NoopWhileDisabled(t *testing.T) {
	open := true
	srv := portTestTransmissionServer(t, &open)
	defer srv.Close()

	client := newTestTransmissionClient(t, srv.URL)
	m := NewPortMonitor(client, nil, NtfyConfig{})
	m.ApplyConfig(portMonitorUpdate{Transmission: client})

	_, checked, err := m.check()
	require.NoError(t, err)
	assert.False(t, checked, "a disabled monitor must report that it did not check")
	_, known := m.LastOpen()
	assert.False(t, known, "a disabled monitor must not record port state")

	m.ApplyConfig(portMonitorUpdate{Transmission: client, PortCheckOn: true})
	_, checked, err = m.check()
	require.NoError(t, err)
	assert.True(t, checked, "the monitor must resume once PortCheck is enabled")
	gotOpen, known := m.LastOpen()
	assert.True(t, known)
	assert.True(t, gotOpen)
}

// --- applyConfig ---

// speedTestConfigFor is a minimal SpeedTest block that newSpeedMonitorFor
// accepts. Nothing here reaches the network: the monitor is built and then
// asked about its identity, never run.
func speedTestConfigFor(t *testing.T, minDown float64) SpeedTestConfig {
	t.Helper()
	cfg := SpeedTestConfig{
		Enabled:         true,
		Interval:        "1h",
		Cooldown:        "1h",
		Proxy:           "http://gluetun:8888",
		MinDownloadMbps: minDown,
		CaptureSeconds:  10,
		ResultsFile:     t.TempDir() + "/speed.json",
		RetentionDays:   30,
	}
	require.NoError(t, cfg.Validate())
	return cfg
}

// newReconfigureContext builds the RunContext applyConfig operates on: the
// stores, the port monitor, and the config that is running right now.
func newReconfigureContext(t *testing.T, cfg Config) *RunContext {
	t.Helper()
	rc := &RunContext{Config: cfg}
	rc.CancelStore = NewStore(time.Hour)
	rc.StartStore = NewStartStore(time.Hour)
	rc.PortMonitor = NewPortMonitor(nil, nil, cfg.Ntfy)
	return rc
}

// drainPending makes the monitor adopt a queued config change, which
// production does at the top of its next check.
func drainPending(m *PortMonitor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyPending()
}

func TestApplyConfig_UpdatesStoreTTLs(t *testing.T) {
	rc := newReconfigureContext(t, Config{})

	next := Config{}
	next.Notifications.TokenTTLH = 6
	require.NoError(t, rc.applyConfig(rc.Config, next))

	assert.Equal(t, 6*time.Hour, rc.CancelStore.TTL())
	assert.Equal(t, 6*time.Hour, rc.StartStore.TTL())
}

func TestApplyConfig_PushesPortCheckAndNtfyToTheMonitor(t *testing.T) {
	rc := newReconfigureContext(t, Config{})

	next := Config{}
	next.PortCheck.Enabled = true
	next.Ntfy.BaseURL = "https://ntfy.example.com"
	require.NoError(t, rc.applyConfig(rc.Config, next))

	m := rc.PortMonitor
	drainPending(m)

	assert.True(t, m.enabled, "PortCheck.Enabled must reach the monitor")
	assert.Equal(t, "https://ntfy.example.com", m.Ntfy.BaseURL)
}

func TestApplyConfig_AttachesAndDetachesGluetun(t *testing.T) {
	rc := newReconfigureContext(t, Config{})

	next := Config{}
	next.Gluetun = GluetunConfig{Host: "gluetun", Port: 8000, ClosedPortChecks: 4}
	require.NoError(t, next.Gluetun.Validate())
	require.NoError(t, rc.applyConfig(rc.Config, next))

	require.NotNil(t, rc.Gluetun, "a Gluetun block added by a reload must build a client")
	assert.Equal(t, "http://gluetun:8000", rc.Gluetun.URL)
	drainPending(rc.PortMonitor)
	assert.True(t, rc.PortMonitor.gluetunConfigured(), "the monitor must adopt the new client")

	rc.Config = next
	require.NoError(t, rc.applyConfig(next, Config{}))
	assert.Nil(t, rc.Gluetun, "removing the block must detach the client")
	drainPending(rc.PortMonitor)
	assert.False(t, rc.PortMonitor.gluetunConfigured())
}

func TestApplyConfig_UpdatesGluetunInPlace(t *testing.T) {
	rc := newReconfigureContext(t, Config{})

	on := Config{}
	on.Gluetun = GluetunConfig{Host: "gluetun", Port: 8000, ClosedPortChecks: 4}
	require.NoError(t, rc.applyConfig(rc.Config, on))
	first := rc.Gluetun
	require.NotNil(t, first)

	next := on
	next.Gluetun.ClosedPortChecks = 9
	require.NoError(t, rc.applyConfig(on, next))

	assert.Same(t, first, rc.Gluetun, "an edited block must reconfigure the same client")
	drainPending(rc.PortMonitor)
	assert.Equal(t, 9, rc.Gluetun.ClosedPortChecks)
}

func TestApplyConfig_RebuildsSpeedMonitorWhenSpeedTestChanges(t *testing.T) {
	cfg := Config{SpeedTest: speedTestConfigFor(t, 100)}
	rc := newReconfigureContext(t, cfg)
	require.NoError(t, rc.applyConfig(Config{}, cfg))

	first := rc.SpeedMonitor
	require.NotNil(t, first, "an enabled SpeedTest block must build a monitor")
	require.NotNil(t, rc.Speed, "the results store must be open")

	next := cfg
	next.SpeedTest.MinDownloadMbps = 250
	require.NoError(t, rc.applyConfig(cfg, next))

	require.NotNil(t, rc.SpeedMonitor)
	assert.NotSame(t, first, rc.SpeedMonitor, "an edited SpeedTest block must rebuild the monitor")
	assert.Equal(t, 250.0, rc.SpeedMonitor.cfg.MinDownloadMbps)
}

func TestApplyConfig_KeepsSpeedMonitorWhenNothingRelevantChanged(t *testing.T) {
	cfg := Config{SpeedTest: speedTestConfigFor(t, 100)}
	rc := newReconfigureContext(t, cfg)
	require.NoError(t, rc.applyConfig(Config{}, cfg))
	first := rc.SpeedMonitor
	require.NotNil(t, first)

	next := cfg
	next.SeenCacheDays = 90
	require.NoError(t, rc.applyConfig(cfg, next))

	assert.Same(t, first, rc.SpeedMonitor, "an unrelated edit must not restart the monitor")
}

func TestApplyConfig_StopsSpeedMonitorWhenDisabled(t *testing.T) {
	cfg := Config{SpeedTest: speedTestConfigFor(t, 100)}
	rc := newReconfigureContext(t, cfg)
	require.NoError(t, rc.applyConfig(Config{}, cfg))
	require.NotNil(t, rc.SpeedMonitor)

	next := cfg
	next.SpeedTest.Enabled = false
	require.NoError(t, rc.applyConfig(cfg, next))

	assert.Nil(t, rc.SpeedMonitor, "disabling SpeedTest must stop the monitor")
	assert.Nil(t, rc.Speed, "the VPN page must stop serving a stale store")
}

func TestExportedEqual_IgnoresUnexportedFields(t *testing.T) {
	a := NtfyConfig{BaseURL: "https://ntfy.example.com", Topic: "torrents"}
	b := a
	require.NoError(t, a.Validate())
	require.NoError(t, b.Validate())

	assert.True(t, exportedEqual(a, b),
		"two loads of the same block compile their own templates and must still compare equal")

	b.Topic = "other"
	assert.False(t, exportedEqual(a, b))
}

// --- the reload path applies what it loads ---

func TestNewConfigReloader_ReloadAppliesTheConfig(t *testing.T) {
	cfgPath := t.TempDir() + "/config.yaml"
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
Notifications:
  TokenTTLH: 6
PortCheck:
  Enabled: true
`), 0600))

	rc := newReconfigureContext(t, Config{})
	rc.configFile = cfgPath
	r := newConfigReloader(rc)

	require.NoError(t, r.reload())

	assert.Equal(t, 6*time.Hour, rc.CancelStore.TTL(),
		"a reload must apply the config, not only load it")
	drainPending(rc.PortMonitor)
	assert.True(t, rc.PortMonitor.enabled)
}

func TestConfigReloader_NotifyReload_ReceivesTheLiveConfig(t *testing.T) {
	var got Config
	r := &configReloader{
		reload:       func() error { return nil },
		snapshot:     func() Config { return Config{SeenFile: "after.json"} },
		notifyReload: func(cfg Config, err error) { got = cfg },
	}

	r.doReload()

	assert.Equal(t, "after.json", got.SeenFile,
		"the notification must be built from the config that is now in effect")
}

// --- the Transmission client and the seen cache ---

func TestApplyConfig_RebuildsTransmissionClientWhenOriginChanges(t *testing.T) {
	prev := Config{Transmission: Transmission{Host: "old", Port: 9091, Path: "/transmission/rpc"}}
	rc := newReconfigureContext(t, prev)
	require.NoError(t, rc.applyConfig(Config{}, prev))
	first := rc.Tx()
	require.NotNil(t, first)

	next := prev
	next.Transmission.Host = "new"
	require.NoError(t, rc.applyConfig(prev, next))

	assert.NotSame(t, first, rc.Tx(), "a new Transmission origin must build a new client")
	drainPending(rc.PortMonitor)
	assert.Same(t, rc.Tx(), rc.PortMonitor.Transmission,
		"the port monitor must be handed the new client")
}

func TestApplyConfig_KeepsTransmissionClientWhenOriginIsUnchanged(t *testing.T) {
	prev := Config{Transmission: Transmission{Host: "transmission", Port: 9091, Path: "/transmission/rpc"}}
	rc := newReconfigureContext(t, prev)
	require.NoError(t, rc.applyConfig(Config{}, prev))
	first := rc.Tx()
	require.NotNil(t, first)

	// The proxy reads the credentials from the live config on every request,
	// so editing them must not throw away the RPC client's session ID.
	next := prev
	next.Transmission.Username = "alice"
	require.NoError(t, rc.applyConfig(prev, next))

	assert.Same(t, first, rc.Tx(), "an unchanged origin must keep the same client")
}

func TestApplyConfig_ReopensSeenCacheWhenSeenFileChanges(t *testing.T) {
	dir := t.TempDir()
	prev := Config{SeenFile: dir + "/seen.json", SeenCacheDays: 30}
	rc := newReconfigureContext(t, prev)
	cache, err := OpenCache(prev.SeenFile)
	require.NoError(t, err)
	rc.Cache = cache
	rc.Cache.AddSkippedItem(&FeedItem{Feed: "Example", Item: &gofeed.Item{GUID: "one"}})

	next := prev
	next.SeenFile = dir + "/other.json"
	require.NoError(t, rc.applyConfig(prev, next))

	assert.NotSame(t, cache, rc.Cache, "a new SeenFile must open a new cache")
	assert.Equal(t, next.SeenFile, rc.Cache.filename)
	assert.FileExists(t, prev.SeenFile, "the cache in use must be saved before the swap")
}

func TestApplyConfig_KeepsSeenCacheWhenTheFlagPinsIt(t *testing.T) {
	dir := t.TempDir()
	prev := Config{SeenFile: dir + "/seen.json"}
	rc := newReconfigureContext(t, prev)
	rc.seenFileOverride = dir + "/pinned.json"
	cache, err := OpenCache(rc.seenFile())
	require.NoError(t, err)
	rc.Cache = cache

	next := prev
	next.SeenFile = dir + "/other.json"
	require.NoError(t, rc.applyConfig(prev, next))

	assert.Same(t, cache, rc.Cache, "--seen-file must win over a reloaded SeenFile")
}
