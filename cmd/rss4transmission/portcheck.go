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
}

func NewPortMonitor(t *transmissionrpc.Client, g *Gluetun, ntfyCfg NtfyConfig) *PortMonitor {
	return &PortMonitor{
		Transmission: t,
		Gluetun:      g,
		Ntfy:         ntfyCfg,
		trigger:      make(chan struct{}, 1),
	}
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
		if _, err := m.check(); err != nil {
			log.WithError(err).Warn("Unable to check Transmission port state")
		}
	}
}

// checkStartup runs the first-ever check and, if the port is still closed,
// sends the startup-specific ntfy alert (separate from the transition alert,
// since lastOpen starts nil and can't itself trigger a transition).
func (m *PortMonitor) checkStartup() {
	open, err := m.check()
	if err != nil {
		log.WithError(err).Warn("Unable to check Transmission port state")
		return
	}
	if !open {
		notifyPortClosed(m.Ntfy, "port not open 60s after startup")
	}
}

// check performs a single port-open check, logs the result, and fires a
// transition notification when the state changed since the previous check.
// It holds m.mu for its entire body: Gluetun.CheckVpnTunnel mutates
// unexported Gluetun fields with no locking of its own, and time.AfterFunc's
// callback runs in its own goroutine, so the startup check and the ticker
// loop's periodic checks could otherwise overlap.
func (m *PortMonitor) check() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var open bool
	var err error
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
		return false, err
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

	return open, nil
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
