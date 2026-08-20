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

	// rotateMu guards rotateReason, which is written by the SpeedMonitor
	// goroutine via RequestRotate and read by the PortMonitor goroutine via
	// rotateNow(). Every other Gluetun field is serialized by PortMonitor.mu,
	// but RequestRotate is deliberately callable from outside that lock.
	rotateMu     sync.Mutex
	rotateReason string // non-empty => a rotation is pending
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
	req, err := g.newRequest(http.MethodGet, fmt.Sprintf("%s/v1/portforward", g.URL), nil)
	if err != nil {
		return int64(0), err
	}
	resp, err := http.DefaultClient.Do(req) // nolint:gosec
	if err != nil {
		return int64(0), err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return int64(0), fmt.Errorf("unable to read body: %s", err.Error())
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

type StatusResponse struct {
	Status string `json:"status"`
}

// getStatus returns the status of the VPN tunnel from Gluetun
func (g *Gluetun) getStatus() (VPNStatus, error) {
	req, err := g.newRequest(http.MethodGet, fmt.Sprintf("%s/v1/vpn/status", g.URL), nil)
	if err != nil {
		return VPNDown, err
	}
	resp, err := http.DefaultClient.Do(req) // nolint:gosec
	if err != nil {
		return VPNDown, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return VPNDown, fmt.Errorf("unable to read body: %s", err.Error())
	}

	sr := StatusResponse{}
	if err = json.Unmarshal(bodyBytes, &sr); err != nil {
		return VPNDown, fmt.Errorf("unable to parse json: %s", err.Error())
	}

	switch sr.Status {
	case "running":
		return VPNUp, nil
	case "stopped":
		log.Infof("VPN tunnel is down")
		return VPNDown, nil
	default:
		return VPNDown, fmt.Errorf("unsupported status: %s", sr.Status)
	}
}

// restartVPN tells Gluetun to stop OpenVPN which will cause it to be auto-restarted
func (g *Gluetun) restartVPN() error {
	body := []byte("{\"status\":\"stopped\"}")

	log.Infof("restarting VPN tunnel")
	req, err := g.newRequest(http.MethodPut, fmt.Sprintf("%s/v1/vpn/status", g.URL), bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req) // nolint:gosec
	if err != nil {
		return err
	}
	return resp.Body.Close()
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
	req, err := g.newRequest(http.MethodGet, fmt.Sprintf("%s/v1/publicip/ip", g.URL), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req) // nolint:gosec
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("unable to read body: %s", err.Error())
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
func (g *Gluetun) RequestRotate(reason string) {
	if reason == "" {
		return
	}
	g.rotateMu.Lock()
	defer g.rotateMu.Unlock()
	if g.rotateReason != "" {
		return
	}
	g.rotateReason = reason
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
	g.rotateReason = ""
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
	if reason := g.PendingRotate(); reason != "" {
		log.Infof("Rotating VPN: %s", reason)
	} else {
		log.Info("Rotating VPN...")
	}
	err := g.restartVPN()
	if err != nil {
		return fmt.Errorf("unable to RestartVPN(): %s", err.Error())
	}

	status := VPNDown
	for i := 0; status != VPNUp && i < 10; i++ {
		i += 1

		status, err = g.getStatus()
		if err != nil {
			log.WithError(err).Errorf("Unable to GetStatus")
			time.Sleep(g.statusPollDelay)
		} else if status == VPNDown {
			time.Sleep(g.statusPollDelay)
		}
	}

	if status != VPNUp {
		return fmt.Errorf("aborting rotation: VPN Failed to come back up")
	}

	g.clearPendingRotate()
	g.lastRotate = time.Now()
	g.portCheckFailed = 0
	g.peerPort = -1
	return nil
}
