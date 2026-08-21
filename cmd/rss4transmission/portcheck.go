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
