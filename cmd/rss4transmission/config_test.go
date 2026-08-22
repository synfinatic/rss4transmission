package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"
)

func makeItem(title string, enclosureLength string) *gofeed.Item {
	item := &gofeed.Item{Title: title}
	if enclosureLength != "" {
		item.Enclosures = []*gofeed.Enclosure{
			{Length: enclosureLength, Type: "application/x-bittorrent"},
		}
	}
	return item
}

func TestFeedCheck_NotExcluded(t *testing.T) {
	f := &Feed{Exclude: []string{`(?i).*720p.*`}}
	if ok, _ := f.Check(makeItem("MyShow.1080p.S01E01", "")); !ok {
		t.Error("non-excluded item should return true")
	}
}

func TestFeedCheck_Excluded(t *testing.T) {
	f := &Feed{Exclude: []string{`(?i).*720p.*`}}
	if ok, _ := f.Check(makeItem("MyShow.720p.S01E01", "")); ok {
		t.Error("excluded item should return false")
	}
}

func TestFeedCheck_NoFilters(t *testing.T) {
	f := &Feed{}
	if ok, _ := f.Check(makeItem("AnythingAtAll", "")); !ok {
		t.Error("item should pass with no filters configured")
	}
}

func TestFeedCheck_MinSize(t *testing.T) {
	f := &Feed{MinSize: "1GB"}
	// 100MB enclosure — below 1GB minimum
	if ok, _ := f.Check(makeItem("Anything", "104857600")); ok {
		t.Error("item below MinSize should return false")
	}
}

func TestFeedCheck_MaxSize(t *testing.T) {
	f := &Feed{MaxSize: "100MB"}
	// 2GB enclosure — above 100MB maximum
	if ok, _ := f.Check(makeItem("Anything", "2147483648")); ok {
		t.Error("item above MaxSize should return false")
	}
}

func TestFeedCheck_SizeRange(t *testing.T) {
	f := &Feed{MinSize: "100MB", MaxSize: "10GB"}
	// 1GB — within range
	if ok, _ := f.Check(makeItem("Anything", "1073741824")); !ok {
		t.Error("item within [MinSize, MaxSize] should return true")
	}
}

func TestFeedCheck_NoEnclosureWithMinSize(t *testing.T) {
	f := &Feed{MinSize: "100MB"}
	// totalSize == 0, below 100MB minimum
	if ok, _ := f.Check(makeItem("Anything", "")); ok {
		t.Error("item with no enclosures should fail MinSize check")
	}
}

func TestFeedValidate_NoExtractor(t *testing.T) {
	f := &Feed{URL: "https://example.com/rss"}
	if err := f.Validate("myfeed", nil); err == nil {
		t.Error("feed with no Extractor should fail validation in v2")
	}
}

func TestFeedValidate_MissingExtractorDef(t *testing.T) {
	f := &Feed{
		Extractor: "nonexistent",
		Identity:  []string{"series"},
		Groups:    []Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	}
	err := f.Validate("myfeed", map[string]*ExtractorSet{})
	if err == nil {
		t.Error("expected error when Extractor name not in Extractors map")
	}
}

func TestFeedValidate_MissingIdentity(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		// Identity missing
		Groups: []Group{{Require: map[string][]string{}}},
	}
	err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es})
	if err == nil {
		t.Error("expected error when Identity is empty")
	}
}

func TestFeedValidate_MissingGroups(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		Identity:  []string{"series"},
		// Groups missing
	}
	err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es})
	if err == nil {
		t.Error("expected error when Groups is empty")
	}
}

func TestFeedValidate_Valid(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		Identity:  []string{"series", "round", "session"},
		Groups:    []Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	}
	if err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es}); err != nil {
		t.Errorf("expected valid feed to pass validation: %v", err)
	}
}

func TestFeedValidate_InvalidAction(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		Identity:  []string{"series"},
		Groups:    []Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
		Action:    "bogus",
	}
	if err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es}); err == nil {
		t.Error("expected error for invalid Action value")
	}
}

func TestFeedValidate_ActionNotifyConflictsWithNoNotify(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		Identity:  []string{"series"},
		Groups:    []Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
		Action:    "notify",
		NoNotify:  true,
	}
	if err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es}); err == nil {
		t.Error("expected error when Action: notify is combined with NoNotify")
	}
}

func TestFeedValidate_ActionNotifyValid(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		Identity:  []string{"series"},
		Groups:    []Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
		Action:    "notify",
	}
	if err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es}); err != nil {
		t.Errorf("expected Action: notify to be valid on its own: %v", err)
	}
}

func TestFeedValidate_ActionDefaultEmpty(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	f := &Feed{
		Extractor: "racing",
		Identity:  []string{"series"},
		Groups:    []Group{{Require: map[string][]string{"series": {"MotoGP"}}}},
	}
	if err := f.Validate("myfeed", map[string]*ExtractorSet{"racing": es}); err != nil {
		t.Errorf("expected empty Action (default download) to be valid: %v", err)
	}
}

func TestValidateFeedNames_Unique(t *testing.T) {
	feeds := []Feed{{Name: "A", URL: "https://a"}, {Name: "B", URL: "https://b"}}
	if err := validateFeedNames(feeds); err != nil {
		t.Errorf("expected no error for unique names, got: %v", err)
	}
}

func TestValidateFeedNames_BlankName(t *testing.T) {
	feeds := []Feed{{Name: "", URL: "https://a"}}
	if err := validateFeedNames(feeds); err == nil {
		t.Error("expected error for blank feed name")
	}
}

func TestValidateFeedNames_Duplicate(t *testing.T) {
	feeds := []Feed{{Name: "A", URL: "https://a"}, {Name: "A", URL: "https://b"}}
	if err := validateFeedNames(feeds); err == nil {
		t.Error("expected error for duplicate feed name")
	}
}

const validExtractorYAML = `
Extractors:
  demo:
    Labels:
      series:
        Regexp: '(.+)'
`

func TestLoadConfig_FeedsPreserveFileOrder(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yamlContent := validExtractorYAML + `
Feeds:
  - Name: Zzz
    URL: https://example.com/z
    Extractor: demo
    Identity: [series]
    Groups:
      - Require:
          series: [X]
  - Name: Aaa
    URL: https://example.com/a
    Extractor: demo
    Identity: [series]
    Groups:
      - Require:
          series: [X]
  - Name: Mmm
    URL: https://example.com/m
    Extractor: demo
    Identity: [series]
    Groups:
      - Require:
          series: [X]
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if err := rc.loadConfig(cfgPath); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	want := []string{"Zzz", "Aaa", "Mmm"}
	if len(rc.Config.Feeds) != len(want) {
		t.Fatalf("expected %d feeds, got %d", len(want), len(rc.Config.Feeds))
	}
	for i, name := range want {
		if rc.Config.Feeds[i].Name != name {
			t.Errorf("feed[%d].Name = %q, want %q", i, rc.Config.Feeds[i].Name, name)
		}
	}
}

func TestLoadConfig_PortCheckEnabledDefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(validExtractorYAML), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if err := rc.loadConfig(cfgPath); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if rc.Config.PortCheck.Enabled {
		t.Error("PortCheck.Enabled should default to false when unset")
	}
}

func TestLoadConfig_RejectsDuplicateFeedNames(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yamlContent := `
Feeds:
  - Name: Dup
    URL: https://example.com/a
  - Name: Dup
    URL: https://example.com/b
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if err := rc.loadConfig(cfgPath); err == nil {
		t.Error("expected loadConfig to reject duplicate feed names")
	}
}

func TestLoadConfig_RejectsBlankFeedName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yamlContent := `
Feeds:
  - URL: https://example.com/a
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if err := rc.loadConfig(cfgPath); err == nil {
		t.Error("expected loadConfig to reject a blank feed name")
	}
}

func TestLoadConfig_BadReload_KeepsPreviousGoodConfig(t *testing.T) {
	// Simulates watch's live config-reload path: loadConfig is called again on
	// the same RunContext. A reload with duplicate/blank feed names must be
	// rejected without corrupting the already-running, valid config.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	goodYAML := validExtractorYAML + `
Feeds:
  - Name: Good1
    URL: https://example.com/1
    Extractor: demo
    Identity: [series]
    Groups:
      - Require:
          series: [X]
  - Name: Good2
    URL: https://example.com/2
    Extractor: demo
    Identity: [series]
    Groups:
      - Require:
          series: [Y]
`
	if err := os.WriteFile(cfgPath, []byte(goodYAML), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if err := rc.loadConfig(cfgPath); err != nil {
		t.Fatalf("initial loadConfig failed: %v", err)
	}
	if len(rc.Config.Feeds) != 2 {
		t.Fatalf("expected 2 feeds after initial load, got %d", len(rc.Config.Feeds))
	}

	badYAML := `
Feeds:
  - Name: Dup
    URL: https://example.com/1
  - Name: Dup
    URL: https://example.com/2
`
	if err := os.WriteFile(cfgPath, []byte(badYAML), 0600); err != nil {
		t.Fatalf("failed to overwrite config: %v", err)
	}

	if err := rc.loadConfig(cfgPath); err == nil {
		t.Error("expected reload with duplicate feed names to fail")
	}

	if len(rc.Config.Feeds) != 2 {
		t.Fatalf("expected previous config to survive a bad reload, got %d feeds", len(rc.Config.Feeds))
	}
	want := []string{"Good1", "Good2"}
	for i, name := range want {
		if rc.Config.Feeds[i].Name != name {
			t.Errorf("feed[%d].Name = %q, want %q (config should be unchanged after bad reload)",
				i, rc.Config.Feeds[i].Name, name)
		}
	}
}

// TestLoadConfig_ReloadRejectsStructurallyInvalidFeed guards against a live
// config-reload silently accepting a feed that is missing required
// label-mode fields (Extractor/Identity/Groups). validateFeedNames alone
// only checks name uniqueness and would let this through; loadConfig must
// also run the same Feed.Validate() checks main.go used to run only once,
// at startup, so a bad reload is rejected exactly like a bad initial load.
func TestLoadConfig_ReloadRejectsStructurallyInvalidFeed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	goodYAML := validExtractorYAML + `
Feeds:
  - Name: Good1
    URL: https://example.com/1
    Extractor: demo
    Identity: [series]
    Groups:
      - Require:
          series: [X]
`
	if err := os.WriteFile(cfgPath, []byte(goodYAML), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if err := rc.loadConfig(cfgPath); err != nil {
		t.Fatalf("initial loadConfig failed: %v", err)
	}

	// Missing Identity/Groups; unique name, so validateFeedNames alone
	// would accept it.
	badYAML := validExtractorYAML + `
Feeds:
  - Name: Good1
    URL: https://example.com/1
    Extractor: demo
`
	if err := os.WriteFile(cfgPath, []byte(badYAML), 0600); err != nil {
		t.Fatalf("failed to overwrite config: %v", err)
	}

	if err := rc.loadConfig(cfgPath); err == nil {
		t.Error("expected reload with a feed missing Identity/Groups to fail")
	}
	if len(rc.Config.Feeds[0].Groups) == 0 || len(rc.Config.Feeds[0].Identity) == 0 {
		t.Error("expected previous config (with Identity/Groups) to survive a bad reload")
	}
}

// TestLoadConfig_ReloadRejectsUnknownExtractor guards the same gap for a
// feed whose Extractor no longer refers to a defined ExtractorSet (e.g. it
// was renamed elsewhere in the same reload).
func TestLoadConfig_ReloadRejectsUnknownExtractor(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	goodYAML := validExtractorYAML + `
Feeds:
  - Name: Good1
    URL: https://example.com/1
    Extractor: demo
    Identity: [series]
    Groups:
      - Require:
          series: [X]
`
	if err := os.WriteFile(cfgPath, []byte(goodYAML), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rc := &RunContext{}
	if err := rc.loadConfig(cfgPath); err != nil {
		t.Fatalf("initial loadConfig failed: %v", err)
	}

	badYAML := `
Feeds:
  - Name: Good1
    URL: https://example.com/1
    Extractor: renamed-away
    Identity: [series]
    Groups:
      - Require:
          series: [X]
`
	if err := os.WriteFile(cfgPath, []byte(badYAML), 0600); err != nil {
		t.Fatalf("failed to overwrite config: %v", err)
	}

	if err := rc.loadConfig(cfgPath); err == nil {
		t.Error("expected reload referencing an unknown Extractor to fail")
	}
	if rc.Config.Feeds[0].Extractor != "demo" {
		t.Errorf("expected previous config to survive a bad reload, got Extractor=%q", rc.Config.Feeds[0].Extractor)
	}
}

// An upload floor is meaningless without an upload measurement: DownloadOnly
// leaves UploadMbps at zero, which would read as "always below the floor" and
// rotate the VPN on every single measurement.
func TestSpeedTestConfig_UploadFloorRequiresUploadTest(t *testing.T) {
	cfg := SpeedTestConfig{
		Enabled: true, Interval: "1h", Cooldown: "2h", Proxy: "http://gluetun:8888",
		MinDownloadMbps: 100, MinUploadMbps: 5, CaptureSeconds: 5, DownloadOnly: true,
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil with MinUploadMbps set and DownloadOnly true, want an error")
	}
	for _, want := range []string{"MinUploadMbps", "DownloadOnly"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	cfg.DownloadOnly = false
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %q with the upload test enabled, want nil", err)
	}
}

// The Transmission web page and its reverse proxy are on by default. A
// deployment that already fronts Transmission with its own proxy turns them
// off with Transmission.WebUI: false.
func TestTransmissionWebUI_Default(t *testing.T) {
	got, ok := ConfigDefaults["Transmission.WebUI"]
	if !ok {
		t.Fatal("ConfigDefaults missing \"Transmission.WebUI\"")
	}
	if got != true {
		t.Errorf("ConfigDefaults[\"Transmission.WebUI\"] = %v (%T), want true", got, got)
	}
}

func TestLoadConfig_TransmissionWebUI(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{"unset falls back to the default", "Transmission:\n  Host: gluetun\n", true},
		{"explicit false wins", "Transmission:\n  Host: gluetun\n  WebUI: false\n", false},
		{"explicit true wins", "Transmission:\n  Host: gluetun\n  WebUI: true\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgFile := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(cfgFile, []byte(tt.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			rc := &RunContext{}
			if err := rc.loadConfig(cfgFile); err != nil {
				t.Fatalf("loadConfig returned error: %v", err)
			}
			if got := rc.Config.Transmission.WebUI; got != tt.want {
				t.Errorf("Transmission.WebUI = %v, want %v", got, tt.want)
			}
		})
	}
}

// The checks below all used to live in lazily-called code that ran
// log.Fatalf on bad input: Feed.compile (Exclude, MinSize, MaxSize),
// ExtractorSet.compile (Regexp, Normalize) and NewGluetun (Rotate). A typo
// saved into the config of a running `watch` therefore killed the daemon
// instead of being rejected. loadConfig must reject each one up front so the
// previously running config stays in effect.
func TestLoadConfig_RejectsUnusableValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "bad Exclude regexp",
			yaml: validExtractorYAML + `
Feeds:
  - Name: F
    URL: https://example.com/f
    Extractor: demo
    Identity: [series]
    Exclude: ['[unterminated']
    Groups:
      - Require:
          series: [X]
`,
		},
		{
			name: "bad MinSize",
			yaml: validExtractorYAML + `
Feeds:
  - Name: F
    URL: https://example.com/f
    Extractor: demo
    Identity: [series]
    MinSize: not-a-size
    Groups:
      - Require:
          series: [X]
`,
		},
		{
			name: "bad MaxSize",
			yaml: validExtractorYAML + `
Feeds:
  - Name: F
    URL: https://example.com/f
    Extractor: demo
    Identity: [series]
    MaxSize: 12 platypuses
    Groups:
      - Require:
          series: [X]
`,
		},
		{
			name: "bad extractor Regexp",
			yaml: `
Extractors:
  demo:
    Labels:
      series:
        Regexp: '([unterminated'
`,
		},
		{
			name: "extractor Regexp without a capture group",
			yaml: `
Extractors:
  demo:
    Labels:
      series:
        Regexp: 'no-captures-here'
`,
		},
		{
			name: "bad extractor Normalize pattern",
			yaml: `
Extractors:
  demo:
    Labels:
      series:
        Regexp: '(.+)'
        Normalize:
          '[unterminated': canonical
`,
		},
		{
			name: "bad Gluetun Rotate",
			yaml: validExtractorYAML + `
Gluetun:
  Host: gluetun
  Port: 8000
  Rotate: every-other-tuesday
`,
		},
		{
			name: "Gluetun Host without Port",
			yaml: validExtractorYAML + `
Gluetun:
  Host: gluetun
`,
		},
		{
			name: "Transmission port above the valid range",
			yaml: validExtractorYAML + `
Transmission:
  Port: 99999
`,
		},
		{
			name: "Transmission port below the valid range",
			yaml: validExtractorYAML + `
Transmission:
  Port: -1
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(tt.yaml), 0600); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}
			rc := &RunContext{}
			if err := rc.loadConfig(cfgPath); err == nil {
				t.Fatal("loadConfig returned nil error, want a rejection")
			}
		})
	}
}

// The reload counterpart of the table above: a bad Exclude arriving on a
// live reload must leave the running config untouched, the same guarantee
// TestLoadConfig_BadReload_KeepsPreviousGoodConfig makes for feed names.
func TestLoadConfig_BadExcludeReload_KeepsPreviousGoodConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	goodYAML := validExtractorYAML + `
Feeds:
  - Name: Good
    URL: https://example.com/1
    Extractor: demo
    Identity: [series]
    Exclude: ['^sample']
    Groups:
      - Require:
          series: [X]
`
	if err := os.WriteFile(cfgPath, []byte(goodYAML), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	rc := &RunContext{}
	if err := rc.loadConfig(cfgPath); err != nil {
		t.Fatalf("initial loadConfig failed: %v", err)
	}

	badYAML := validExtractorYAML + `
Feeds:
  - Name: Good
    URL: https://example.com/1
    Extractor: demo
    Identity: [series]
    Exclude: ['[unterminated']
    Groups:
      - Require:
          series: [X]
`
	if err := os.WriteFile(cfgPath, []byte(badYAML), 0600); err != nil {
		t.Fatalf("failed to rewrite config: %v", err)
	}
	if err := rc.loadConfig(cfgPath); err == nil {
		t.Fatal("reload with a bad Exclude returned nil error, want a rejection")
	}
	if len(rc.Config.Feeds) != 1 || rc.Config.Feeds[0].Exclude[0] != "^sample" {
		t.Errorf("previous config was not preserved: %+v", rc.Config.Feeds)
	}
}
