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
	RotateTime       time.Duration
	RotateFailure    int
	lastRotate       time.Time
	peerPort         int64
	portUpdateFailed int
	Transmission     *transmissionrpc.Client
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
		URL:           fmt.Sprintf("%s://%s:%d", proto, g.Host, g.Port),
		RotateTime:    r,
		RotateFailure: g.RotateFailure,
		lastRotate:    time.Now(),
		Transmission:  t,
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

// GetPort returns the forwarded port
func (g *Gluetun) GetPort() (int64, error) {
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

// GetStatus returns the status of the VPN tunnel
func (g *Gluetun) GetStatus() (VPNStatus, error) {
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

// RestartVPN tells Gluetun to stop OpenVPN which will cause it to be auto-restarted
func (g *Gluetun) RestartVPN() error {
	body := []byte("{\"status\":\"stopped\"}")

	log.Infof("restarting VPN tunnel")
	_, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/v1/openvpn/status", g.URL), bytes.NewReader(body))
	return err
}

func (g *Gluetun) UpdatePort() error {
	port, err := g.GetPort()
	if err != nil {
		return err
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

func (g *Gluetun) IsPortOpen() (bool, error) {
	// check the port
	open, err := g.Transmission.PortTest(context.TODO())
	if err != nil {
		return false, err
	}

	// track that this failed.
	if !open {
		g.portUpdateFailed += 1
	} else {
		g.portUpdateFailed = 0
	}

	return open, nil
}

// RotateNow tells us if we should rotate now or not
func (g *Gluetun) RotateNow() bool {
	if g.RotateFailure > 0 && g.portUpdateFailed > g.RotateFailure {
		return true
	}

	now := time.Now()
	if g.RotateTime.Seconds() > 0 && g.lastRotate.Add(g.RotateTime).Before(now) {
		return true
	}
	return false
}

func (g *Gluetun) Rotate() error {
	err := g.RestartVPN()
	if err != nil {
		return fmt.Errorf("Unable to RestartVPN(): %s", err.Error())
	}

	status := VPNDown
	for i := 0; status != VPNUp && i < 10; i++ {
		i += 1

		status, err = g.GetStatus()
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
	g.portUpdateFailed = 0
	err = fmt.Errorf("force execution")
	for i := 0; err != nil && i < 3; i++ {
		err = g.UpdatePort()
		if err != nil {
			time.Sleep(3 * time.Second)
		}
	}
	if err != nil {
		return fmt.Errorf("Unable to UpdatePort() after rotation: %s", err.Error())
	}
	return nil
}
