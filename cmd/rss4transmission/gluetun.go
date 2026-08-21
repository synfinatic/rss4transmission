package main

/*
 * Copyright (c) 2023 Aaron Turner  <aturner at synfin dot net>
 *
 * This program is free software: you can redistribute it
 * and/or modify it under the terms of the GNU General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or with the authors permission any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
	str2duration "github.com/xhit/go-str2duration/v2"
)

type Gluetun struct {
	URL              string
	RotateTime       time.Duration // how often to rotate
	ClosedPortChecks int           // force rotation after X Port Forward Checks
	Transmission     *transmissionrpc.Client
	lastRotate       time.Time
	peerPort         int64
	portCheckFailed  int
	AuthUsername     string
	AuthPassword     string
	AuthAPIKey       string
	retryAttempts    int
	retryDelay       time.Duration
	statusPollDelay  time.Duration
	publicIPWait     time.Duration // how long to wait for the exit IP to change after a rotation

	// rotateMu guards rotateReason, which is written by the SpeedMonitor
	// goroutine via RequestRotate and read by the PortMonitor goroutine via
	// rotateNow(). Every other Gluetun field is serialized by PortMonitor.mu,
	// but RequestRotate is deliberately callable from outside that lock.
	rotateMu     sync.Mutex
	rotateReason string // non-empty => a rotation is pending
	rotateSource string // what asked for the pending rotation
	rotating     bool   // a rotation is running right now

	// OnRotated, when set, is called after a rotation completes, describing what
	// asked for it and where it moved us. It runs on the PortMonitor goroutine,
	// inside PortMonitor.mu, so it must not block for long or call back into
	// Gluetun.
	OnRotated func(RotationOutcome)
}

func NewGluetun(g GluetunConfig, t *transmissionrpc.Client) *Gluetun {
	proto := "http"
	if g.HTTPS {
		proto = "https"
	}

	var err error
	var r time.Duration

	if g.RotateTime != "" {
		r, err = str2duration.ParseDuration(g.RotateTime)
		if err != nil {
			log.WithError(err).Fatalf("unable to parse RotateTime: %s", g.RotateTime)
		}
	}

	return &Gluetun{
		URL:              fmt.Sprintf("%s://%s:%d", proto, g.Host, g.Port),
		RotateTime:       r,
		ClosedPortChecks: g.ClosedPortChecks,
		Transmission:     t,
		lastRotate:       time.Now(),
		peerPort:         -1,
		portCheckFailed:  0,
		AuthUsername:     g.AuthUsername,
		AuthPassword:     g.AuthPassword,
		AuthAPIKey:       g.AuthAPIKey,
		retryAttempts:    5,
		retryDelay:       3 * time.Second,
		statusPollDelay:  3 * time.Second,
		publicIPWait:     30 * time.Second,
	}
}

func (g *Gluetun) newRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if g.AuthUsername != "" && g.AuthPassword != "" {
		req.SetBasicAuth(g.AuthUsername, g.AuthPassword)
	}
	if g.AuthAPIKey != "" {
		req.Header.Set("X-API-Key", g.AuthAPIKey)
	}
	return req, nil
}

// control performs a request against Gluetun's control server and returns the
// response body. A non-2xx reply is an error naming the status and what the
// server said: Gluetun answers a request it will not serve -- unauthenticated,
// or authenticated for a role that does not include this route -- with a
// plain-text body, which otherwise surfaces as an unhelpful JSON parse error,
// or on a request whose body nobody parses, not at all.
func (g *Gluetun) control(method, path string, body io.Reader) ([]byte, error) {
	req, err := g.newRequest(method, g.URL+path, body)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req) // nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("unable to read body: %s", err.Error())
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		said := strings.TrimSpace(string(bodyBytes))
		if len(said) > 256 {
			said = said[:256] + "..."
		}
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, said)
	}
	return bodyBytes, nil
}

var ForceRotate bool // flag to force rotation again due to failure

// checkVpnTunnel restarts / rotates the VPN tunnel as necessary. It returns
// the observed port-open state and whether the port-test itself succeeded --
// callers use this to distinguish "confirmed closed" from "couldn't tell"
// (e.g. a flaky port-test), which the internal sync-below logic deliberately
// collapses into "sync defensively either way".
func (g *Gluetun) CheckVpnTunnel() (bool, error) {
	if g.rotateNow() || ForceRotate {
		if err := g.rotate(); err != nil {
			log.WithError(err).Errorf("Rotate() failed")
			ForceRotate = true
			return false, err
		}
		time.Sleep(15 * time.Second) // let things settle down
	}
	ForceRotate = false

	var open bool
	var checkErr error
	for range g.retryAttempts {
		open, checkErr = g.isPortOpen()
		if checkErr == nil {
			break
		}
		time.Sleep(g.retryDelay)
	}

	syncOpen := open
	if checkErr != nil {
		// We couldn't determine whether the port is open (e.g. Transmission's
		// port-test call to its external checker failed/timed out). Don't treat
		// that as "closed" for rotation purposes, but still make sure Transmission
		// has the latest port Gluetun is forwarding -- otherwise a persistently
		// failing port-test would mean the port is never synced at all.
		log.WithError(checkErr).Warnf("Unable to check IsPortOpen(); syncing port with Gluetun anyway")
		syncOpen = false
	}

	if !syncOpen {
		var updateErr error
		for range g.retryAttempts {
			updateErr = g.updatePort()
			if updateErr == nil {
				break
			}
			time.Sleep(g.retryDelay)
		}
		if updateErr != nil {
			log.WithError(updateErr).Errorf("Unable to UpdatePort()")
		}
	}

	return open, checkErr
}

type VPNStatus bool

const (
	VPNUp   VPNStatus = true
	VPNDown VPNStatus = false
)

type PortResponse struct {
	Ports []int64 `json:"ports"`
}

// getPort returns the forwarded port from Gluetun
func (g *Gluetun) getPort() (int64, error) {
	bodyBytes, err := g.control(http.MethodGet, "/v1/portforward", nil)
	if err != nil {
		return int64(0), err
	}

	pr := PortResponse{}
	if err = json.Unmarshal(bodyBytes, &pr); err != nil {
		return int64(0), fmt.Errorf("unable to parse json: %s", err.Error())
	}

	if len(pr.Ports) == 0 {
		return int64(0), fmt.Errorf("no ports returned")
	}
	return pr.Ports[0], nil
}

const (
	// stopConfirmChecks bounds the wait for Gluetun to acknowledge a stop.
	stopConfirmChecks = 5
	// vpnUpChecks bounds the wait for the tunnel to reconnect after a restart.
	vpnUpChecks = 10
)

type StatusResponse struct {
	Status string `json:"status"`
}

// getStatus returns the status of the VPN tunnel from Gluetun
func (g *Gluetun) getStatus() (VPNStatus, error) {
	bodyBytes, err := g.control(http.MethodGet, "/v1/vpn/status", nil)
	if err != nil {
		return VPNDown, err
	}

	sr := StatusResponse{}
	if err = json.Unmarshal(bodyBytes, &sr); err != nil {
		return VPNDown, fmt.Errorf("unable to parse json: %s", err.Error())
	}

	switch sr.Status {
	case "running":
		return VPNUp, nil
	case "stopped":
		log.Debug("VPN tunnel is down")
		return VPNDown, nil
	default:
		return VPNDown, fmt.Errorf("unsupported status: %s", sr.Status)
	}
}

// restartVPN cycles the VPN tunnel so Gluetun reconnects, picking a new exit.
//
// Gluetun does not restart itself: `{"status":"stopped"}` stops the tunnel and
// leaves it stopped indefinitely. The start has to be a second, explicit call,
// and skipping it does not merely fail to rotate -- it takes the tunnel down
// and keeps it there, with Transmission's traffic blocked behind a killswitch
// that is doing its job.
func (g *Gluetun) restartVPN() error {
	log.Infof("stopping VPN tunnel")
	if err := g.setVPNStatus("stopped"); err != nil {
		// A rejected stop leaves the tunnel up and serving traffic, so there is
		// nothing to start and nothing to clean up.
		return fmt.Errorf("unable to stop the tunnel: %s", err.Error())
	}

	// Gluetun can report the old state briefly after accepting the stop, so
	// confirm rather than sleeping a fixed interval: this costs one request
	// when the stop lands immediately, which is the usual case.
	switch status, known := g.waitForStatus(VPNDown, stopConfirmChecks); {
	case known && status == VPNUp:
		return fmt.Errorf("Gluetun still reports the tunnel running after the stop request")
	case !known:
		// The tunnel may well be down, and a tunnel left down blocks
		// Transmission behind the killswitch indefinitely. Starting one that
		// turns out to be running is much the cheaper mistake.
		log.Warn("Unable to confirm the VPN tunnel stopped; starting it anyway")
	}

	log.Infof("starting VPN tunnel")
	if err := g.setVPNStatus("running"); err != nil {
		return fmt.Errorf("tunnel stopped, but unable to start it again: %s", err.Error())
	}
	return nil
}

// waitForStatus polls Gluetun until it reports want, and returns the last
// status it managed to read along with whether it read one at all.
//
// The first check happens before the first sleep, so a state change that has
// already landed costs a single request. The "did we read anything" half
// matters after a stop request: "still running" and "no idea" call for
// opposite responses, and collapsing them would either abandon a rotation
// needlessly or leave the tunnel down.
func (g *Gluetun) waitForStatus(want VPNStatus, checks int) (VPNStatus, bool) {
	last, known := VPNDown, false
	for i := 0; i < checks; i++ {
		if i > 0 {
			time.Sleep(g.statusPollDelay)
		}
		status, err := g.getStatus()
		if err != nil {
			log.WithError(err).Errorf("Unable to GetStatus")
			continue
		}
		last, known = status, true
		if status == want {
			return status, true
		}
	}
	return last, known
}

// setVPNStatus asks Gluetun to move the tunnel to the given state.
func (g *Gluetun) setVPNStatus(status string) error {
	body := []byte(fmt.Sprintf("{\"status\":%q}", status))
	_, err := g.control(http.MethodPut, "/v1/vpn/status", bytes.NewReader(body))
	return err
}

// updatePort queries Gluetun and updates the peer port in Transmission if it changed
func (g *Gluetun) updatePort() error {
	port, err := g.getPort()
	if err != nil {
		return err
	}
	if port == 0 {
		return fmt.Errorf("gluetun doesn't know the port yet")
	}
	// if the port didn't change, we're good
	if g.peerPort == port {
		return nil
	}

	// port changed, update Transmission
	log.Infof("updating peer port in transmission to %d", port)
	g.peerPort = port

	payload := transmissionrpc.SessionArguments{
		PeerPort: &port,
	}
	return g.Transmission.SessionArgumentsSet(context.TODO(), payload)
}

func (g *Gluetun) getPublicIp() (string, error) {
	bodyBytes, err := g.control(http.MethodGet, "/v1/publicip/ip", nil)
	if err != nil {
		return "", err
	}

	type IPResponse struct {
		IP string `json:"public_ip"`
	}
	ipResp := IPResponse{}
	if err = json.Unmarshal(bodyBytes, &ipResp); err != nil {
		return "", fmt.Errorf("unable to parse json: %s", err.Error())
	}

	return ipResp.IP, nil
}

// isPortOpen checks Transmission to see if it detects the peer port as open
func (g *Gluetun) isPortOpen() (bool, error) {
	// check the port
	open, err := g.Transmission.PortTest(context.TODO())
	if err != nil {
		return false, err
	}

	if !open {
		g.portCheckFailed++
	} else {
		g.portCheckFailed = 0
	}

	return open, nil
}

// RotationOutcome describes a rotation that just finished: what asked for it,
// and where it moved us. PreviousIP or NewIP may be empty when Gluetun could
// not answer; NewIP == PreviousIP means the reconnect landed on the same exit.
type RotationOutcome struct {
	Source     string
	Reason     string
	PreviousIP string
	NewIP      string
}

// RequestRotate asks for the VPN to be rotated on the next port check, with a
// human-readable reason that ends up in the log line and the ntfy alert. It is
// safe to call from any goroutine.
//
// The rotation is deliberately not performed here: Gluetun has no internal
// locking and all of its other state is serialized by PortMonitor.mu, so the
// request is picked up by rotateNow() on the PortMonitor goroutine instead.
// That also means the rotation lands within one portCheckInterval rather than
// immediately, and it reuses CheckVpnTunnel's existing peer-port resync.
//
// An already-pending request is not overwritten -- the first caller to spot a
// problem owns the explanation. An empty reason is ignored, since it can't be
// distinguished from "nothing pending".
//
// It returns whether the request was accepted, which is false when a rotation
// is already pending or already running. Covering the running case is what
// stops a second request from rotating the tunnel all over again: rotate()
// clears the pending reason before it finishes, so its tail would otherwise
// look idle.
func (g *Gluetun) RequestRotate(source, reason string) bool {
	if reason == "" {
		return false
	}
	g.rotateMu.Lock()
	defer g.rotateMu.Unlock()
	if g.rotateReason != "" || g.rotating {
		return false
	}
	g.rotateSource = source
	g.rotateReason = reason
	return true
}

// PendingRotate returns the reason for a pending rotation, or "" if none.
func (g *Gluetun) PendingRotate() string {
	g.rotateMu.Lock()
	defer g.rotateMu.Unlock()
	return g.rotateReason
}

// clearPendingRotate drops any pending rotation request.
func (g *Gluetun) clearPendingRotate() {
	g.rotateMu.Lock()
	defer g.rotateMu.Unlock()
	g.rotateSource = ""
	g.rotateReason = ""
}

// rotationTrigger reports what this rotation should be attributed to. A pending
// request names itself; otherwise rotate() was reached through rotateNow(), so
// the trigger is whichever of its remaining conditions holds. The closed-port
// check is tested first because it is the more specific answer -- with a short
// RotateTime both can be true at once, and "the peer port has been shut for N
// checks" explains the restart better than "the rotation interval elapsed".
//
// It must be called before clearPendingRotate(), and before the restart resets
// portCheckFailed.
func (g *Gluetun) rotationTrigger() (source, reason string) {
	g.rotateMu.Lock()
	source, reason = g.rotateSource, g.rotateReason
	g.rotateMu.Unlock()

	if reason != "" {
		return source, reason
	}

	if g.ClosedPortChecks > 0 && g.portCheckFailed > g.ClosedPortChecks {
		return RotationSourceClosedPort,
			fmt.Sprintf("peer port closed for %d consecutive checks", g.portCheckFailed)
	}

	return RotationSourceSchedule, fmt.Sprintf("RotateTime %s elapsed", g.RotateTime)
}

// beginRotating marks a rotation as running so RequestRotate refuses new
// requests until it finishes. The returned func clears the flag.
func (g *Gluetun) beginRotating() func() {
	g.rotateMu.Lock()
	g.rotating = true
	g.rotateMu.Unlock()

	return func() {
		g.rotateMu.Lock()
		g.rotating = false
		g.rotateMu.Unlock()
	}
}

// rotateNow tells us if we should rotate now or not
func (g *Gluetun) rotateNow() bool {
	if g.PendingRotate() != "" {
		return true
	}

	if g.ClosedPortChecks > 0 && g.portCheckFailed > g.ClosedPortChecks {
		return true
	}

	now := time.Now()
	if g.RotateTime.Seconds() > 0 && g.lastRotate.Add(g.RotateTime).Before(now) {
		return true
	}
	return false
}

// rotate shuts down the VPN tunnel and updates the peer port for Transmission
func (g *Gluetun) rotate() error {
	defer g.beginRotating()()

	// Captured up front: clearPendingRotate() drops the reason and the restart
	// resets portCheckFailed, so by the end of this function neither is
	// available to explain what happened.
	source, reason := g.rotationTrigger()
	log.Infof("Rotating VPN: %s", reason)

	// Read the exit IP before tearing the tunnel down: afterwards there is
	// nothing left to compare the new one against, and "which exit did we
	// leave" is half of what makes the post-rotation report useful.
	previousIP, ipErr := g.getPublicIp()
	if ipErr != nil {
		log.WithError(ipErr).Warn("Unable to read the pre-rotation public IP")
	}

	if err := g.restartVPN(); err != nil {
		return fmt.Errorf("unable to RestartVPN(): %s", err.Error())
	}

	if status, _ := g.waitForStatus(VPNUp, vpnUpChecks); status != VPNUp {
		return fmt.Errorf("aborting rotation: VPN Failed to come back up")
	}

	g.clearPendingRotate()
	g.lastRotate = time.Now()
	g.portCheckFailed = 0
	g.peerPort = -1

	newIP := g.publicIPAfterRotate(previousIP)
	switch newIP {
	case "":
		log.Warn("VPN rotated, but Gluetun did not report a public IP")
	case previousIP:
		log.Warnf("VPN rotated, but the exit IP is unchanged: %s", newIP)
	default:
		log.Infof("VPN rotated to exit IP %s", newIP)
	}
	if g.OnRotated != nil {
		g.OnRotated(RotationOutcome{
			Source:     source,
			Reason:     reason,
			PreviousIP: previousIP,
			NewIP:      newIP,
		})
	}

	return nil
}

// publicIPAfterRotate returns the exit IP Gluetun reports once the tunnel is
// back up. Gluetun marks the VPN running slightly before it has refreshed its
// public IP, so a single query can still answer with the pre-rotation address;
// poll for up to publicIPWait for it to change.
//
// Waiting the full window is not a failure. With a small server pool the
// reconnect really can land on the same exit, and that is exactly the outcome
// worth reporting -- so the last address seen is returned either way, and the
// caller decides what an unchanged IP means. The window is generous because the
// alternative error is worse: giving up early reports a stale address as the new
// one, which reads as "the rotation changed nothing" when it did.
func (g *Gluetun) publicIPAfterRotate(previousIP string) string {
	deadline := time.Now().Add(g.publicIPWait)
	var ip string
	for first := true; first || time.Now().Before(deadline); first = false {
		if !first {
			time.Sleep(g.statusPollDelay)
		}
		current, err := g.getPublicIp()
		if err != nil {
			log.WithError(err).Debug("Unable to read the post-rotation public IP")
			continue
		}
		ip = current
		if ip != "" && ip != previousIP {
			break
		}
	}
	return ip
}
