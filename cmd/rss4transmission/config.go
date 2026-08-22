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
	"net/url"
	"regexp"
	"strconv"
	"text/template"
	"time"

	"github.com/mmcdole/gofeed"
	str2duration "github.com/xhit/go-str2duration/v2"
)

var ConfigDefaults = map[string]interface{}{
	"Transmission.Host":       "localhost",
	"Transmission.Port":       9091,
	"Transmission.HTTPS":      false,
	"Transmission.Path":       "/transmission/rpc",
	"Transmission.Username":   "admin",
	"Transmission.Password":   "admin",
	"Transmission.WebUI":      true,
	"SeenCacheDays":           30,
	"Notifications.TokenTTLH": 24,

	"SpeedTest.Enabled":         false,
	"SpeedTest.Interval":        "1h",
	"SpeedTest.Proxy":           "http://gluetun:8888",
	"SpeedTest.MinDownloadMbps": 100.0,
	// 0 disables the upload floor. It has to default off: checking it costs a
	// second measurement leg (DownloadOnly must be false), and an exit that
	// uploads slowly is only a problem if you are seeding.
	"SpeedTest.MinUploadMbps":      0.0,
	"SpeedTest.Cooldown":           "2h",
	"SpeedTest.MaxRotationsPerDay": 6,
	"SpeedTest.CaptureSeconds":     5,
	"SpeedTest.Threads":            2,
	"SpeedTest.DownloadOnly":       true,
	"SpeedTest.SkipWhenActive":     true,
	"SpeedTest.RetentionDays":      30,
}

type Config struct {
	Feeds         []Feed                   `koanf:"Feeds"`
	Extractors    map[string]*ExtractorSet `koanf:"Extractors"`
	Transmission  Transmission             `koanf:"Transmission"`
	Gluetun       GluetunConfig            `koanf:"Gluetun"`
	Ntfy          NtfyConfig               `koanf:"Ntfy"`
	Notifications NotificationsConfig      `koanf:"Notifications"`
	PortCheck     PortCheckConfig          `koanf:"PortCheck"`
	SpeedTest     SpeedTestConfig          `koanf:"SpeedTest"`
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
	SeenTitle         string `koanf:"SeenTitle"`
	SeenBody          string `koanf:"SeenBody"`
	SeenPriority      string `koanf:"SeenPriority"`

	AlertTopic             string `koanf:"AlertTopic"`
	ConfigReloadedTitle    string `koanf:"ConfigReloadedTitle"`
	ConfigReloadedBody     string `koanf:"ConfigReloadedBody"`
	ConfigReloadedPriority string `koanf:"ConfigReloadedPriority"`
	ConfigFailedTitle      string `koanf:"ConfigFailedTitle"`
	ConfigFailedBody       string `koanf:"ConfigFailedBody"`
	ConfigFailedPriority   string `koanf:"ConfigFailedPriority"`
	PortClosedTitle        string `koanf:"PortClosedTitle"`
	PortClosedBody         string `koanf:"PortClosedBody"`
	PortClosedPriority     string `koanf:"PortClosedPriority"`
	PortOpenedTitle        string `koanf:"PortOpenedTitle"`
	PortOpenedBody         string `koanf:"PortOpenedBody"`
	PortOpenedPriority     string `koanf:"PortOpenedPriority"`
	VpnRotatingTitle       string `koanf:"VpnRotatingTitle"`
	VpnRotatingBody        string `koanf:"VpnRotatingBody"`
	VpnRotatingPriority    string `koanf:"VpnRotatingPriority"`
	VpnRotatedTitle        string `koanf:"VpnRotatedTitle"`
	VpnRotatedBody         string `koanf:"VpnRotatedBody"`
	VpnRotatedPriority     string `koanf:"VpnRotatedPriority"`

	startedTitleTmpl        *template.Template
	startedBodyTmpl         *template.Template
	completedTitleTmpl      *template.Template
	completedBodyTmpl       *template.Template
	seenTitleTmpl           *template.Template
	seenBodyTmpl            *template.Template
	configReloadedTitleTmpl *template.Template
	configReloadedBodyTmpl  *template.Template
	configFailedTitleTmpl   *template.Template
	configFailedBodyTmpl    *template.Template
	portClosedTitleTmpl     *template.Template
	portClosedBodyTmpl      *template.Template
	portOpenedTitleTmpl     *template.Template
	vpnRotatingTitleTmpl    *template.Template
	vpnRotatingBodyTmpl     *template.Template
	vpnRotatedTitleTmpl     *template.Template
	vpnRotatedBodyTmpl      *template.Template
	portOpenedBodyTmpl      *template.Template
}

type PortCheckConfig struct {
	Enabled bool `koanf:"Enabled"`
}

// SpeedTestConfig controls periodic throughput measurement over the VPN and
// the policy for asking Gluetun to re-pick an egress when it looks bad.
//
// Measurement runs in-process but is routed through Gluetun's built-in HTTP
// proxy (HTTPPROXY=on, port 8888), so the traffic egresses over the VPN while
// rss4transmission itself stays off the VPN network and keeps fetching RSS
// directly.
//
// Note the koanf keys match the field names exactly here, unlike
// GluetunConfig.RotateTime whose key is "Rotate".
type SpeedTestConfig struct {
	Enabled            bool    `koanf:"Enabled"`
	Interval           string  `koanf:"Interval"`
	Proxy              string  `koanf:"Proxy"`
	MinDownloadMbps    float64 `koanf:"MinDownloadMbps"`
	MinUploadMbps      float64 `koanf:"MinUploadMbps"`
	Cooldown           string  `koanf:"Cooldown"`
	MaxRotationsPerDay int     `koanf:"MaxRotationsPerDay"`
	CaptureSeconds     int     `koanf:"CaptureSeconds"`
	Threads            int     `koanf:"Threads"`
	DownloadOnly       bool    `koanf:"DownloadOnly"`
	SkipWhenActive     bool    `koanf:"SkipWhenActive"`
	ServerID           string  `koanf:"ServerID"`
	ResultsFile        string  `koanf:"ResultsFile"`
	RetentionDays      int     `koanf:"RetentionDays"`

	// parsed forms of Interval/Cooldown, filled in by Validate()
	interval time.Duration
	cooldown time.Duration
}

// Validate parses the duration strings and checks the config is usable. It
// returns an error rather than calling log.Fatalf (as NewGluetun does) so a
// bad live reload leaves the running config intact.
func (s *SpeedTestConfig) Validate() error {
	if !s.Enabled {
		return nil
	}

	var err error
	if s.interval, err = str2duration.ParseDuration(s.Interval); err != nil {
		return fmt.Errorf("unable to parse Interval %q: %w", s.Interval, err)
	}
	if s.cooldown, err = str2duration.ParseDuration(s.Cooldown); err != nil {
		return fmt.Errorf("unable to parse Cooldown %q: %w", s.Cooldown, err)
	}

	if s.Proxy == "" {
		return fmt.Errorf("SpeedTest.Proxy is required when SpeedTest is enabled")
	}
	u, err := url.Parse(s.Proxy)
	if err != nil {
		return fmt.Errorf("unable to parse Proxy %q: %w", s.Proxy, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("SpeedTest.Proxy %q must be a full URL, e.g. http://gluetun:8888", s.Proxy)
	}

	// Nothing measures upload under DownloadOnly, so UploadMbps stays at zero
	// and every measurement would look like it was below the floor.
	if s.MinUploadMbps > 0 && s.DownloadOnly {
		return fmt.Errorf(
			"SpeedTest.MinUploadMbps requires SpeedTest.DownloadOnly: false so upload is measured")
	}

	// Back-to-back tests would saturate the very link we are measuring.
	if capture := time.Duration(s.CaptureSeconds) * time.Second; s.interval <= capture {
		return fmt.Errorf("SpeedTest.Interval (%s) must be longer than SpeedTest.CaptureSeconds (%ds)",
			s.Interval, s.CaptureSeconds)
	}

	return nil
}

// IntervalDuration returns the parsed Interval. Only valid after Validate().
func (s *SpeedTestConfig) IntervalDuration() time.Duration { return s.interval }

// CooldownDuration returns the parsed Cooldown. Only valid after Validate().
func (s *SpeedTestConfig) CooldownDuration() time.Duration { return s.cooldown }

// RetentionDuration is how long results and rotation events are kept.
func (s *SpeedTestConfig) RetentionDuration() time.Duration {
	return time.Duration(s.RetentionDays) * 24 * time.Hour
}

type NotificationsConfig struct {
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
	// WebUI serves the Transmission page and its reverse proxy on the
	// private listener. Turn it off when Transmission is already reachable
	// through a proxy of your own, or when the private port is not
	// trustworthy: the proxy attaches the credentials above to every
	// request it forwards.
	WebUI bool `koanf:"WebUI"`
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
	Name           string   `koanf:"Name"`
	URL            string   `koanf:"URL"`
	Exclude        []string `koanf:"Exclude"`
	DownloadPath   string   `koanf:"DownloadPath"`
	NoValidateCert bool     `koanf:"NoValidateCert"`
	NoSubmit       bool     `koanf:"NoSubmit"`
	NoNotify       bool     `koanf:"NoNotify"`
	Action         string   `koanf:"Action"`
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

// validateFeedNames ensures every feed has a non-empty, unique Name. Since
// Feeds is an ordered list rather than a map, Name is the only identifier
// tying a feed to its cache records, history entries, and --feed filter.
func validateFeedNames(feeds []Feed) error {
	seen := make(map[string]bool, len(feeds))
	for _, f := range feeds {
		if f.Name == "" {
			return fmt.Errorf("feed with URL %q: Name is required", f.URL)
		}
		if seen[f.Name] {
			return fmt.Errorf("duplicate feed name %q", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
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
	if f.Action != "" && f.Action != "download" && f.Action != "notify" {
		return fmt.Errorf("feed %q: Action must be %q or %q, got %q", name, "download", "notify", f.Action)
	}
	if f.Action == "notify" && f.NoNotify {
		return fmt.Errorf("feed %q: NoNotify cannot be combined with Action: notify", name)
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
