package main

import (
	"context"
	"sync"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

const (
	portCheckInterval     = 5 * time.Minute
	portCheckStartupGrace = 60 * time.Second
)

// PortMonitor periodically checks whether Transmission's peer port is
// reachable, logging every check and sending ntfy alerts on open<->closed
// transitions and on a still-closed port 60s after startup. When Gluetun is
// configured, the check is delegated to Gluetun.CheckVpnTunnel() so rotation
// and peer-port sync keep happening exactly as before; otherwise it calls
// Transmission.PortTest() directly.
type PortMonitor struct {
	Transmission *transmissionrpc.Client
	Gluetun      *Gluetun // nil when Gluetun isn't configured
	Ntfy         NtfyConfig

	mu       sync.Mutex // serializes check() (incl. Gluetun struct access) and lastOpen
	lastOpen *bool      // nil until the first check

	// lastPublicIP is the exit IP Gluetun last reported for itself, refreshed
	// on every check. The VPN page reads it through LastPublicIP rather than
	// querying Gluetun from the HTTP goroutine: Gluetun has no locking of its
	// own, and everything that touches it is serialized by mu.
	lastPublicIP string
	publicIPErr  bool // the last refresh failed; used to log the transition once

	// lastPeerPort is the port Gluetun last said it forwards, refreshed on
	// every check. It is read from Gluetun's control server rather than from
	// Gluetun.peerPort: that field is the port last pushed into Transmission,
	// which is refreshed only when the port test reports closed and is reset
	// by a rotation.
	lastPeerPort int64
	peerPortErr  bool // the last refresh failed; used to log the transition once

	trigger chan struct{} // buffered(1): an out-of-band check request

	// enabled is PortCheck.Enabled. With it off and no Gluetun there is
	// nothing to check, so check() does nothing and the goroutine stays
	// running. That makes the setting a live toggle instead of a
	// start-a-goroutine-at-startup decision.
	enabled bool

	// pendingMu guards pending, which the config-reload goroutine writes via
	// ApplyConfig and check() consumes. It is deliberately not m.mu: check()
	// holds that for the whole of CheckVpnTunnel, which runs for tens of
	// seconds during a rotation, and a reload must not block that long.
	pendingMu sync.Mutex
	pending   *portMonitorUpdate
}

// portMonitorUpdate is a config change waiting to be applied to the monitor.
// It carries everything a reload can alter, because it replaces any update
// that has not been consumed yet rather than merging with it.
type portMonitorUpdate struct {
	// Gluetun is the new Gluetun block; GluetunOn says whether one is
	// configured at all. An existing client is reconfigured in place so the
	// rotation policy does not read a reload as a fresh tunnel.
	Gluetun   GluetunConfig
	GluetunOn bool

	// Client is a client the caller already built, for the case where the
	// Gluetun block was added by a reload. The caller keeps the same pointer,
	// so it can wire the rest of the process to it without reading m.Gluetun
	// from another goroutine. It is nil when the block was only edited.
	Client *Gluetun

	Ntfy NtfyConfig

	// PortCheckOn is PortCheck.Enabled.
	PortCheckOn bool

	// Transmission is the RPC client in effect, which a reload can replace
	// when the Transmission origin changes.
	Transmission *transmissionrpc.Client

	// OnRotated is the hook Gluetun calls after a rotation. It is rebuilt on
	// reload because it captures the ntfy config and the speed store.
	OnRotated func(RotationOutcome)
}

// NewPortMonitor builds a monitor that checks on every tick. The live
// PortCheck.Enabled value arrives through ApplyConfig, which the caller pushes
// before Run starts.
func NewPortMonitor(t *transmissionrpc.Client, g *Gluetun, ntfyCfg NtfyConfig) *PortMonitor {
	return &PortMonitor{
		Transmission: t,
		Gluetun:      g,
		Ntfy:         ntfyCfg,
		enabled:      true,
		trigger:      make(chan struct{}, 1),
	}
}

// ApplyConfig queues a config change for the monitor to adopt on its next
// check. It never blocks on m.mu, so a reload cannot be held up by a rotation
// in progress.
//
// A queued update that has not been consumed is replaced, not merged: each
// update is a complete picture of the config, so the newest one is the right
// one to apply.
//
// It is safe to call from another goroutine while Run() is going. Trigger() is
// the only other method with that property.
func (m *PortMonitor) ApplyConfig(u portMonitorUpdate) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	m.pending = &u
}

// takePending removes the queued update, if any.
func (m *PortMonitor) takePending() *portMonitorUpdate {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	u := m.pending
	m.pending = nil
	return u
}

// applyPending adopts a queued config change. It must be called with m.mu
// held, which is what makes it safe to touch Gluetun: that struct has no
// locking of its own and every other access to it happens under m.mu.
func (m *PortMonitor) applyPending() {
	u := m.takePending()
	if u == nil {
		return
	}

	m.Ntfy = u.Ntfy
	m.enabled = u.PortCheckOn
	m.Transmission = u.Transmission

	switch {
	case !u.GluetunOn:
		// The block was removed. Rotation and peer-port sync stop; the port
		// test falls back to asking Transmission directly.
		m.Gluetun = nil
		return
	case u.Client != nil:
		// Either the client we already have, or one the caller built because
		// the block was added by a reload.
		m.Gluetun = u.Client
	case m.Gluetun == nil:
		m.Gluetun = NewGluetun(u.Gluetun, u.Transmission)
	}

	m.Gluetun.applyConfig(u.Gluetun)
	m.Gluetun.Transmission = u.Transmission
	m.Gluetun.OnRotated = u.OnRotated
}

// gluetunConfigured reports whether a Gluetun sidecar is currently attached.
// The VPN page uses it to decide whether Gluetun is the authority on the exit
// IP or whether it must fall back to what the last measurement observed.
func (m *PortMonitor) gluetunConfigured() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Gluetun != nil
}

// Trigger asks for a port check without waiting for the next tick. It returns
// false when a check is already queued -- the buffered channel coalesces, so a
// burst of requests costs one check, not one per request.
//
// It is the only PortMonitor method safe to call from another goroutine while
// Run() is going; everything else that touches monitor state runs under m.mu on
// the Run goroutine.
func (m *PortMonitor) Trigger() bool {
	select {
	case m.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

// Run blocks forever, checking the port every portCheckInterval, once more
// separately after portCheckStartupGrace, and whenever Trigger() is called.
// Call it in its own goroutine.
func (m *PortMonitor) Run() {
	time.AfterFunc(portCheckStartupGrace, m.checkStartup)

	ticker := time.NewTicker(portCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-m.trigger:
		}
		if _, _, err := m.check(); err != nil {
			log.WithError(err).Warn("Unable to check Transmission port state")
		}
	}
}

// checkStartup runs the first-ever check and, if the port is still closed,
// sends the startup-specific ntfy alert (separate from the transition alert,
// since lastOpen starts nil and can't itself trigger a transition).
func (m *PortMonitor) checkStartup() {
	open, checked, err := m.check()
	if err != nil {
		log.WithError(err).Warn("Unable to check Transmission port state")
		return
	}
	if !checked {
		return
	}
	if !open {
		notifyPortClosed(m.Ntfy, "port not open 60s after startup")
	}
}

// check adopts any queued config change, performs a single port-open check,
// logs the result, and fires a transition notification when the state changed
// since the previous check. It holds m.mu for its entire body:
// Gluetun.CheckVpnTunnel mutates unexported Gluetun fields with no locking of
// its own, and time.AfterFunc's callback runs in its own goroutine, so the
// startup check and the ticker loop's periodic checks could otherwise overlap.
//
// checked is false when there is nothing to check -- no Gluetun and PortCheck
// turned off -- so a caller can tell that apart from a port that is closed. A
// queued config change is still adopted first, which is how turning PortCheck
// back on takes effect.
func (m *PortMonitor) check() (open bool, checked bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.applyPending()
	if m.Gluetun == nil && !m.enabled {
		return false, false, nil
	}

	if m.Gluetun != nil {
		open, err = m.Gluetun.CheckVpnTunnel()
	} else {
		open, err = m.Transmission.PortTest(context.TODO())
	}
	// Refreshed before the error return: a flaky port-test says nothing about
	// which exit we are on, and blanking the IP because of one would make the
	// VPN page look like the tunnel went away.
	m.refreshPublicIP()
	m.refreshPeerPort()

	if err != nil {
		return false, true, err
	}

	if open {
		log.Debug("Transmission peer port is open")
	} else {
		log.Warn("Transmission peer port is closed")
	}

	if m.lastOpen != nil {
		if *m.lastOpen && !open {
			notifyPortClosed(m.Ntfy, "port closed")
		} else if !*m.lastOpen && open {
			notifyPortOpened(m.Ntfy, "port reopened")
		}
	}
	m.lastOpen = &open

	return open, true, nil
}

// refreshPublicIP asks Gluetun which exit it is on and caches the answer. It
// must be called with m.mu held.
//
// A failed query keeps the previous address: an unreachable control server (or
// an API key whose role does not cover the publicip route) says nothing about
// whether the tunnel moved, and showing a blank exit IP would read as one. For
// the same reason the failure is logged only on the transition into it --
// otherwise a permanently missing route would warn every portCheckInterval
// forever.
func (m *PortMonitor) refreshPublicIP() {
	if m.Gluetun == nil {
		return
	}

	ip, err := m.Gluetun.getPublicIp()
	if err != nil {
		if !m.publicIPErr {
			log.WithError(err).Warn("Unable to read the VPN exit IP from Gluetun")
			m.publicIPErr = true
		}
		return
	}
	m.publicIPErr = false

	if ip != "" {
		m.lastPublicIP = ip
	}
}

// refreshPeerPort asks Gluetun which port it forwards and caches the answer.
// It must be called with m.mu held.
//
// A failed query keeps the previous port, for the same reason refreshPublicIP
// keeps the previous address: an unreachable control server says nothing about
// whether the forwarded port changed, and blanking it would read as "no port is
// forwarded". The failure is logged only on the transition into it. A port of
// zero or less is Gluetun saying it does not know the port yet, which is also
// not a reason to drop a port we already saw.
func (m *PortMonitor) refreshPeerPort() {
	if m.Gluetun == nil {
		return
	}

	port, err := m.Gluetun.getPort()
	if err != nil {
		if !m.peerPortErr {
			log.WithError(err).Warn("Unable to read the forwarded port from Gluetun")
			m.peerPortErr = true
		}
		return
	}
	m.peerPortErr = false

	if port > 0 {
		m.lastPeerPort = port
	}
}

// LastPeerPort reports the port Gluetun last said it forwards. known is false
// until Gluetun has answered once, and always when Gluetun is not configured,
// so callers can say "not known yet" rather than render a zero port as fact.
func (m *PortMonitor) LastPeerPort() (port int64, known bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPeerPort, m.lastPeerPort > 0
}

// LastPublicIP reports the VPN exit IP Gluetun last claimed for itself. known
// is false until the first successful query, and always when Gluetun is not
// configured, so callers can say "not known yet" rather than render an empty
// address as fact.
//
// This is deliberately not the exit IP a speedtest observed: some providers NAT
// per destination, so speedtest.net's view drifts over a tunnel that never
// moved.
func (m *PortMonitor) LastPublicIP() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPublicIP, m.lastPublicIP != ""
}

// LastOpen reports the peer-port state observed by the most recent check.
// known is false until the first check completes, so callers can distinguish
// "not checked yet" from "checked and closed" rather than reporting a guess.
func (m *PortMonitor) LastOpen() (open bool, known bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastOpen == nil {
		return false, false
	}
	return *m.lastOpen, true
}
