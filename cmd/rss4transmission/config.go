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

	"github.com/mmcdole/gofeed"
)

var ConfigDefaults = map[string]interface{}{
	"Transmission.Host":     "localhost",
	"Transmission.Port":     9091,
	"Transmission.HTTPS":    false,
	"Transmission.Path":     "/transmission/rpc",
	"Transmission.Username": "admin",
	"Transmission.Password": "admin",
}

type Config struct {
	Pushover      Pushover        `koanf:"Pushover"`
	Feeds         map[string]Feed `koanf:"Feeds"`
	Transmission  Transmission    `koanf:"Transmission"`
	SeenFile      string          `koanf:"SeenFile"`
	SeenCacheDays int             `koanf:"SeenCacheDays"`
}

type Pushover struct {
	AppToken string   `koanf:"AppToken"`
	Users    []string `koanf:"Users"`
}

type Transmission struct {
	Host     string `koanf:"Host"`
	Port     int    `koanf:"Port"`
	HTTPS    bool   `koanf:"HTTPS"`
	Path     string `koanf:"Path""`
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

// Check if a given item should be processed
func (m *Feed) Check(item *gofeed.Item) bool {
	m.compile()

	// first see if we exclude it
	for i, r := range m.exclude {
		// use compiled
		match := r.Find([]byte(item.Title))
		if match != nil {
			log.Debugf("Exclude %s => %s", item.Title, m.Exclude[i])
			return false
		}
	}

	// then see if we match
	for i, r := range m.regexp {
		// use compiled
		match := r.Find([]byte(item.Title))
		if match != nil {
			log.Infof("Matched %s => %s", item.Title, m.Regexp[i])
			return true
		}
	}

	// no match
	log.Debugf("Skipped %s", item.Title)
	return false
}

// Does the RssFilter have the given category?
func (m *Feed) HasCategory(category string) bool {
	for _, c := range m.Categories {
		if c == category {
			return true
		}
	}
	return false
}
