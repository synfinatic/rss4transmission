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
	"fmt"
	"regexp"
	"strconv"
	"text/template"

	"github.com/mmcdole/gofeed"
)

var ConfigDefaults = map[string]interface{}{
	"Transmission.Host":     "localhost",
	"Transmission.Port":     9091,
	"Transmission.HTTPS":    false,
	"Transmission.Path":     "/transmission/rpc",
	"Transmission.Username": "admin",
	"Transmission.Password": "admin",
	"SeenCacheDays":         30,
	"Cancel.TokenTTLH":      24,
}

type Config struct {
	Feeds         map[string]Feed          `koanf:"Feeds"`
	Extractors    map[string]*ExtractorSet `koanf:"Extractors"`
	Transmission  Transmission             `koanf:"Transmission"`
	Gluetun       GluetunConfig            `koanf:"Gluetun"`
	Ntfy          NtfyConfig               `koanf:"Ntfy"`
	Cancel        CancelConfig             `koanf:"Cancel"`
	SeenFile      string                   `koanf:"SeenFile"`
	SeenCacheDays int                      `koanf:"SeenCacheDays"`
}

type NtfyConfig struct {
	BaseURL           string `koanf:"BaseURL"`
	Topic             string `koanf:"Topic"`
	Token             string `koanf:"Token"` //nolint:gosec
	StartedTitle      string `koanf:"StartedTitle"`
	StartedBody       string `koanf:"StartedBody"`
	StartedPriority   string `koanf:"StartedPriority"`
	CompletedTitle    string `koanf:"CompletedTitle"`
	CompletedBody     string `koanf:"CompletedBody"`
	CompletedPriority string `koanf:"CompletedPriority"`

	startedTitleTmpl   *template.Template
	startedBodyTmpl    *template.Template
	completedTitleTmpl *template.Template
	completedBodyTmpl  *template.Template
}

type CancelConfig struct {
	HMACSecret string `koanf:"HMACSecret"` //nolint:gosec
	BaseURL    string `koanf:"BaseURL"`
	TokenTTLH  int    `koanf:"TokenTTLH"`
}

type Transmission struct {
	Host     string `koanf:"Host"`
	Port     int    `koanf:"Port"`
	HTTPS    bool   `koanf:"HTTPS"`
	Path     string `koanf:"Path"`
	Username string `koanf:"Username"`
	Password string `koanf:"Password"` // nolint:gosec
}

type GluetunConfig struct {
	Host             string `koanf:"Host"`
	Port             int    `koanf:"Port"`
	HTTPS            bool   `koanf:"HTTPS"`
	RotateTime       string `koanf:"Rotate"`
	ClosedPortChecks int    `koanf:"ClosedPortChecks"`
	AuthUsername     string `koanf:"AuthUsername"`
	AuthPassword     string `koanf:"AuthPassword"`
	AuthAPIKey       string `koanf:"AuthAPIKey"`
}

type Feed struct {
	URL            string   `koanf:"URL"`
	Exclude        []string `koanf:"Exclude"`
	DownloadPath   string   `koanf:"DownloadPath"`
	NoValidateCert bool     `koanf:"NoValidateCert"`
	NoSubmit       bool     `koanf:"NoSubmit"`
	NoNotify       bool     `koanf:"NoNotify"`
	MaxSize        string   `koanf:"MaxSize"`
	MinSize        string   `koanf:"MinSize"`

	// Label-mode fields
	Extractor string            `koanf:"Extractor"`
	Identity  []string          `koanf:"Identity"`
	Prefer    []PreferDimension `koanf:"Prefer"`
	Groups    []Group           `koanf:"Groups"`

	// internal
	compiled bool
	exclude  []*regexp.Regexp
	minSize  uint64
	maxSize  uint64
}

// Validate checks that the feed config is self-consistent.
func (f *Feed) Validate(name string, extractors map[string]*ExtractorSet) error {
	if f.Extractor == "" {
		return fmt.Errorf("feed %q: Extractor is required", name)
	}
	if _, ok := extractors[f.Extractor]; !ok {
		return fmt.Errorf("feed %q: Extractor %q not defined", name, f.Extractor)
	}
	if len(f.Identity) == 0 {
		return fmt.Errorf("feed %q: Identity must list at least one label", name)
	}
	if len(f.Groups) == 0 {
		return fmt.Errorf("feed %q: Groups must contain at least one entry", name)
	}
	return nil
}

// Check is the pre-filter applied before label extraction. It returns false and
// a human-readable reason if the item matches any Exclude pattern or falls
// outside the MinSize/MaxSize bounds. All other items return (true, "").
func (f *Feed) Check(item *gofeed.Item) (bool, string) {
	f.compile()

	for _, r := range f.exclude {
		if r.Find([]byte(item.Title)) != nil {
			return false, "matched exclude filter"
		}
	}

	var totalSize uint64
	for _, e := range item.Enclosures {
		size, err := strconv.ParseUint(e.Length, 10, 64)
		if err != nil {
			log.WithError(err).Errorf("Unable to parse enclosure length: %s", e.Length)
			continue
		}
		totalSize += size
	}

	if f.minSize > 0 && totalSize < f.minSize {
		log.Debugf("Too small: %s [%d]", item.Title, totalSize)
		return false, "below minimum size"
	}

	if f.maxSize > 0 && totalSize > f.maxSize {
		log.Debugf("Too large: %s [%d]", item.Title, totalSize)
		return false, "above maximum size"
	}

	return true, ""
}
