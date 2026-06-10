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
	"Anthropic.Model":       "claude-haiku-4-5-20251001",
}

type Config struct {
	Feeds         map[string]Feed `koanf:"Feeds"`
	Transmission  Transmission    `koanf:"Transmission"`
	Gluetun       GluetunConfig   `koanf:"Gluetun"`
	Anthropic     AnthropicConfig `koanf:"Anthropic"`
	SeenFile      string          `koanf:"SeenFile"`
	SeenCacheDays int             `koanf:"SeenCacheDays"`
}

// AnthropicConfig holds API credentials for the Anthropic normalizer.
type AnthropicConfig struct {
	APIKey string `koanf:"APIKey"` //nolint:gosec
	Model  string `koanf:"Model"`
}

// AISelection defines structured selection rules that replace regexp matching.
type AISelection struct {
	Series        []string `koanf:"Series"`
	Sessions      []string `koanf:"Sessions"`
	FeedPriority  []string `koanf:"FeedPriority"`
	MinResolution string   `koanf:"MinResolution"`
	Languages     []string `koanf:"Languages"`
	ExcludeFlags  []string `koanf:"ExcludeFlags"`
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
	URL            string       `koanf:"URL"`
	Regexp         []string     `koanf:"Regexp"`
	Exclude        []string     `koanf:"Exclude"`
	Categories     []string     `koanf:"Categories"`
	DownloadPath   string       `koanf:"DownloadPath"`
	NoValidateCert bool         `koanf:"NoValidateCert"`
	NoSubmit       bool         `koanf:"NoSubmit"`
	NoNotify       bool         `koanf:"NoNotify"`
	MaxSize        string       `koanf:"MaxSize"`
	MinSize        string       `koanf:"MinSize"`
	AISelection    *AISelection `koanf:"AISelection"`

	// internal
	compiled bool
	regexp   []*regexp.Regexp
	exclude  []*regexp.Regexp
	minSize  uint64
	maxSize  uint64
}

// Validate checks the feed config for inconsistencies that cannot be caught by YAML parsing.
func (m *Feed) Validate(name string) error {
	if m.AISelection != nil && len(m.Regexp) > 0 {
		return fmt.Errorf("feed %q: AISelection and Regexp are mutually exclusive; use Exclude for the escape hatch", name)
	}
	return nil
}

// Check if a given item should be processed
func (m *Feed) Check(item *gofeed.Item) bool {
	m.compile()

	// first see if we exclude it
	for _, r := range m.exclude {
		// use compiled
		match := r.Find([]byte(item.Title))
		if match != nil {
			// log.Debugf("Exclude %s => %s", item.Title, m.Exclude[i])
			return false
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

	// Check Min/MaxSize
	if m.minSize > 0 && totalSize < m.minSize {
		log.Debugf("Too small: %s [%d]", item.Title, totalSize)
		return false
	}

	if m.maxSize > 0 && totalSize > m.maxSize {
		log.Debugf("Too large: %s [%d]", item.Title, totalSize)
		return false
	}

	// then see if we match
	for _, r := range m.regexp {
		// use compiled
		match := r.Find([]byte(item.Title))
		if match != nil {
			return true
		}
	}

	// no match
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
