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
	"regexp"
)

type Config struct {
	Pushover     Pushover        `koanf:"Pushover"`
	Feeds        map[string]Feed `koanf:"Feeds"`
	Transmission Transmission    `koanf:"Transmission"`
	SeenFile     string          `koanf:"SeenFile"`
}

type Pushover struct {
	AppToken string   `koanf:"AppToken"`
	Users    []string `koanf:"Users"`
}

type Transmission struct {
	Host     string `koanf:"Host"`
	Port     int    `koanf:"Port"`
	HTTPS    bool   `koanf:"HTTPS"`
	Path     string `koanf:"Path"`
	Username string `koanf:"Username"`
	Password string `koanf:"Password"`
}

type Feed struct {
	URL            string   `koanf:"URL"`
	Regexp         []string `koanf:"Regexp"`
	Exclude        []string `koanf:"Exclude"`
	Categories     []string `koanf:"Categories"`
	DownloadPath   string   `koanf:"DownloadPath"`
	NoValidateCert bool     `koanf:"NoValidateCert"`
	NoSubmit       bool     `koanf:"NoSubmit"`
	NoNotify       bool     `koanf:"NoNotify"`

	// internal
	compiled bool
	regexp   []*regexp.Regexp
	exclude  []*regexp.Regexp
}
