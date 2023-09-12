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
	"time"

	"github.com/hekmon/transmissionrpc/v2"
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
			log.WithError(err).Fatalf("Unable to parse RotateTime: %s", g.RotateTime)
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
	}
}

var ForceRotate bool // flag to force rotation again due to failure

// checkVpnTunnel restarts / rotates the VPN tunnel as necessary
func (g *Gluetun) CheckVpnTunnel() {
	var err error

	if g.rotateNow() || ForceRotate {
		err = g.rotate()
		if err != nil {
			log.WithError(err).Errorf("Rotate() failed")
			ForceRotate = true
			return
		}
	}
	ForceRotate = false

	var open bool
	err = fmt.Errorf("force execution")
	for i := 0; err != nil && i < 3; i++ {
		open, err = g.isPortOpen()
		if err != nil {
			time.Sleep(3 * time.Second)
		}
	}
	if err != nil {
		log.WithError(err).Errorf("Unable to check IsPortOpen()")
		return
	}

	if !open {
		err = fmt.Errorf("force execution")
		for i := 0; err != nil && i < 3; i++ {
			err = g.updatePort()
			if err != nil {
				time.Sleep(3 * time.Second)
			}
		}
	}
	if err != nil {
		log.WithError(err).Errorf("Unable to UpdatePort()")
	}
}

type VPNStatus bool

const (
	VPNUp   VPNStatus = true
	VPNDown VPNStatus = false
)

type PortResponse struct {
	Port int64 `json:"port"`
}

// getPort returns the forwarded port from Gluetun
func (g *Gluetun) getPort() (int64, error) {
	resp, err := http.Get(fmt.Sprintf("%s/v1/openvpn/portforwarded", g.URL))
	if err != nil {
		return int64(0), err
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return int64(0), fmt.Errorf("Unable to read body: %s", err.Error())
	}

	pr := PortResponse{}
	if err = json.Unmarshal(body, &pr); err != nil {
		return int64(0), fmt.Errorf("Unable to parse json: %s", err.Error())
	}

	return pr.Port, nil
}

type StatusResponse struct {
	Status string `json:"status"`
}

// getStatus returns the status of the VPN tunnel from Gluetun
func (g *Gluetun) getStatus() (VPNStatus, error) {
	resp, err := http.Get(fmt.Sprintf("%s/v1/openvpn/status", g.URL))
	if err != nil {
		return VPNDown, err
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return VPNDown, fmt.Errorf("Unable to read body: %s", err.Error())
	}

	sr := StatusResponse{}
	if err = json.Unmarshal(body, &sr); err != nil {
		return VPNDown, fmt.Errorf("Unable to parse json: %s", err.Error())
	}

	switch sr.Status {
	case "running":
		return VPNUp, nil
	case "stopped":
		log.Infof("VPN tunnel is down")
		return VPNDown, nil
	default:
		return VPNDown, fmt.Errorf("Unsupported status: %s", sr.Status)
	}
}

// restartVPN tells Gluetun to stop OpenVPN which will cause it to be auto-restarted
func (g *Gluetun) restartVPN() error {
	body := []byte("{\"status\":\"stopped\"}")

	log.Infof("restarting VPN tunnel")
	_, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/v1/openvpn/status", g.URL), bytes.NewReader(body))
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

// isPortOpen checks Transmission to see if it detects the peer port as open
func (g *Gluetun) isPortOpen() (bool, error) {
	// check the port
	open, err := g.Transmission.PortTest(context.TODO())
	if err != nil {
		return false, err
	}

	// track that this failed.
	if !open {
		g.portCheckFailed += 1
	} else {
		g.portCheckFailed = 0
	}

	return open, nil
}

// rotateNow tells us if we should rotate now or not
func (g *Gluetun) rotateNow() bool {
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
	err := g.restartVPN()
	if err != nil {
		return fmt.Errorf("Unable to RestartVPN(): %s", err.Error())
	}

	status := VPNDown
	for i := 0; status != VPNUp && i < 10; i++ {
		i += 1

		status, err = g.getStatus()
		if err != nil {
			log.WithError(err).Errorf("Unable to GetStatus")
			time.Sleep(time.Duration(3 * time.Second))
		} else if status == VPNDown {
			time.Sleep(time.Duration(3 * time.Second))
		}
	}

	if status != VPNUp {
		return fmt.Errorf("Aborting rotation: VPN Failed to come back up")
	}

	g.lastRotate = time.Now()
	g.portCheckFailed = 0
	g.peerPort = -1
	err = fmt.Errorf("force execution")
	for i := 0; err != nil && i < 3; i++ {
		err = g.updatePort()
		if err != nil {
			time.Sleep(3 * time.Second)
		}
	}
	if err != nil {
		return fmt.Errorf("Unable to UpdatePort() after rotation: %s", err.Error())
	}
	return nil
}
