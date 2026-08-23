package main

import (
	"context"
	"sync"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

// torrentCompletionFields is the minimal set of torrent-get fields the
// completion poll needs.
var torrentCompletionFields = []string{"id", "name", "downloadDir", "isFinished", "sizeWhenDone"}

// CompletionMonitor periodically polls Transmission for torrents whose
// IsFinished state has flipped from false to true, sending an ntfy
// "torrent completed" notification for each transition observed. It replaces
// the old "torrent done" shell-script hook: rss4transmission already holds a
// live RPC client, so it can find out for itself.
type CompletionMonitor struct {
	Transmission *transmissionrpc.Client
	Ntfy         NtfyConfig

	mu sync.Mutex
	// interval is the poll interval in effect, read by Run() after each
	// check() to decide whether the ticker needs to be reset.
	interval time.Duration
	// finished is the IsFinished state observed on the previous poll, keyed
	// by torrent ID. It is rebuilt from scratch on every check(), so a
	// torrent no longer reported by Transmission falls out of it instead of
	// leaking memory. A torrent absent from the map (never seen, or seen and
	// then dropped) is treated as an unknown baseline: it can only notify
	// starting from the poll after it is first recorded, never the poll it
	// first appears (or reappears) on, mirroring PortMonitor's rule that the
	// very first observation can't itself be a transition.
	finished map[int64]bool

	trigger chan struct{} // buffered(1): an out-of-band check request

	// pendingMu guards pending, which the config-reload goroutine writes via
	// ApplyConfig and check() consumes. It is deliberately not m.mu: a reload
	// must not block on a poll in flight.
	pendingMu sync.Mutex
	pending   *completionMonitorUpdate
}

// completionMonitorUpdate is a config change waiting to be applied to the
// monitor. It carries everything a reload can alter, because it replaces any
// update that has not been consumed yet rather than merging with it.
type completionMonitorUpdate struct {
	Transmission *transmissionrpc.Client
	Ntfy         NtfyConfig
	Interval     time.Duration
}

// NewCompletionMonitor builds a monitor that polls Transmission every
// interval, once Run is called.
func NewCompletionMonitor(t *transmissionrpc.Client, ntfyCfg NtfyConfig, interval time.Duration) *CompletionMonitor {
	return &CompletionMonitor{
		Transmission: t,
		Ntfy:         ntfyCfg,
		interval:     interval,
		finished:     make(map[int64]bool),
		trigger:      make(chan struct{}, 1),
	}
}

// ApplyConfig queues a config change for the monitor to adopt on its next
// check. It never blocks on m.mu, so a reload cannot be held up by a poll in
// progress.
//
// A queued update that has not been consumed is replaced, not merged: each
// update is a complete picture of the config, so the newest one is the right
// one to apply.
//
// It is safe to call from another goroutine while Run() is going. Trigger()
// is the only other method with that property.
func (m *CompletionMonitor) ApplyConfig(u completionMonitorUpdate) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	m.pending = &u
}

// takePending removes the queued update, if any.
func (m *CompletionMonitor) takePending() *completionMonitorUpdate {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	u := m.pending
	m.pending = nil
	return u
}

// applyPending adopts a queued config change. It must be called with m.mu
// held.
func (m *CompletionMonitor) applyPending() {
	u := m.takePending()
	if u == nil {
		return
	}
	m.Transmission = u.Transmission
	m.Ntfy = u.Ntfy
	m.interval = u.Interval
}

// Trigger asks for a completion check without waiting for the next tick. It
// returns false when a check is already queued -- the buffered channel
// coalesces, so a burst of requests costs one check, not one per request.
func (m *CompletionMonitor) Trigger() bool {
	select {
	case m.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

// Run blocks forever, polling Transmission every interval (as last set by
// ApplyConfig, or the value passed to NewCompletionMonitor) and whenever
// Trigger() is called. Call it in its own goroutine.
//
// Unlike PortMonitor's fixed check interval, TorrentComplete.PollInterval is
// user-configurable and live-reloadable, so check() reports the interval in
// effect after adopting any pending config change, and the ticker is reset
// whenever that differs from what is currently running.
func (m *CompletionMonitor) Run() {
	m.mu.Lock()
	interval := m.interval
	m.mu.Unlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-m.trigger:
		}
		next := m.check()
		if next > 0 && next != interval {
			interval = next
			ticker.Reset(interval)
		}
	}
}

// check adopts any queued config change, polls Transmission for every
// torrent's IsFinished state, and fires a completion notification for each
// torrent observed to transition from false to true since the previous poll.
// It holds m.mu for its entire body, matching PortMonitor.check().
//
// A TorrentGet error is logged and leaves m.finished untouched: a transient
// RPC failure must not be read as "every torrent went back to unfinished".
func (m *CompletionMonitor) check() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.applyPending()

	torrents, err := m.Transmission.TorrentGet(context.TODO(), torrentCompletionFields, nil)
	if err != nil {
		log.WithError(err).Warn("Unable to check Transmission torrents for completion")
		return m.interval
	}

	next := make(map[int64]bool, len(torrents))
	for _, t := range torrents {
		if t.ID == nil || t.IsFinished == nil {
			continue
		}
		id := *t.ID
		isFinished := *t.IsFinished

		if wasFinished, seen := m.finished[id]; seen && !wasFinished && isFinished {
			m.notifyCompleted(t)
		}
		next[id] = isFinished
	}
	m.finished = next

	return m.interval
}

// notifyCompleted sends the "torrent completed" ntfy notification for t. It
// is a no-op when ntfy is not configured for torrent notifications.
func (m *CompletionMonitor) notifyCompleted(t transmissionrpc.Torrent) {
	if m.Ntfy.BaseURL == "" || m.Ntfy.Topic == "" {
		return
	}

	var name, dir string
	if t.Name != nil {
		name = *t.Name
	}
	if t.DownloadDir != nil {
		dir = *t.DownloadDir
	}
	var id int64
	if t.ID != nil {
		id = *t.ID
	}
	var sizeBytes int64
	if t.SizeWhenDone != nil {
		sizeBytes = int64(t.SizeWhenDone.Byte())
	}

	ntfyCtx := &NtfyTemplateContext{
		Title:     name,
		Dir:       dir,
		TorrentID: id,
		SizeBytes: sizeBytes,
		Size:      formatGB(sizeBytes),
	}
	client := NewNtfyClient(m.Ntfy)
	if err := client.SendTorrentCompleted(ntfyCtx); err != nil {
		log.WithError(err).Warn("Failed to send ntfy torrent-completed notification")
	}
}
