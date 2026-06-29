package main

import (
	"regexp"
	"testing"
)

func TestExtractLabels_SingleLabel(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"series": {Regexp: `(MotoGP|Moto2|Moto3)`},
		},
	}
	got := es.ExtractLabels("MotoGP.2024.RD01.Qatar.Qualifying.TNT.1080p")
	if got["series"] != "MotoGP" {
		t.Errorf("series = %q, want MotoGP", got["series"])
	}
}

func TestExtractLabels_MultipleLabels(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"series":     {Regexp: `(MotoGP|Moto2|Moto3)`},
			"resolution": {Regexp: `(1080p|720p)`},
			"session":    {Regexp: `(Qualifying|Sprint|Race)`},
		},
	}
	got := es.ExtractLabels("MotoGP.2024.RD01.Qatar.Qualifying.TNT.1080p")
	if got["series"] != "MotoGP" {
		t.Errorf("series = %q, want MotoGP", got["series"])
	}
	if got["resolution"] != "1080p" {
		t.Errorf("resolution = %q, want 1080p", got["resolution"])
	}
	if got["session"] != "Qualifying" {
		t.Errorf("session = %q, want Qualifying", got["session"])
	}
}

func TestExtractLabels_NoMatch(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"series": {Regexp: `(MotoGP|Moto2|Moto3)`},
		},
	}
	got := es.ExtractLabels("SomeRandomContent.1080p")
	if _, ok := got["series"]; ok {
		t.Error("series should not be present when regex does not match")
	}
}

func TestExtractLabels_NormalizeVariant(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"session": {
				Regexp: `(Qual\w*)`,
				Normalize: map[string]string{
					`Qual\w+`: "Qualifying",
				},
			},
		},
	}
	for _, title := range []string{
		"MotoGP.RD01.Qualifying.1080p",
		"MotoGP.RD01.Quali.1080p",
		"MotoGP.RD01.Qualification.1080p",
	} {
		got := es.ExtractLabels(title)
		if got["session"] != "Qualifying" {
			t.Errorf("title %q: session = %q, want Qualifying", title, got["session"])
		}
	}
}

func TestExtractLabels_NormalizeNoRuleMatch(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"session": {
				Regexp: `(Race|Qualifying|Sprint)`,
				Normalize: map[string]string{
					`Qual\w+`: "Qualifying",
				},
			},
		},
	}
	got := es.ExtractLabels("MotoGP.RD01.Race.1080p")
	if got["session"] != "Race" {
		t.Errorf("session = %q, want Race (no normalize rule matches)", got["session"])
	}
}

func TestExtractLabels_NormalizeFPPattern(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"session": {
				Regexp: `(FP\d+)`,
				Normalize: map[string]string{
					`FP\d+`: "Practice",
				},
			},
		},
	}
	for _, title := range []string{"MotoGP.RD01.FP1.1080p", "MotoGP.RD01.FP2.1080p", "MotoGP.RD01.FP3.1080p"} {
		got := es.ExtractLabels(title)
		if got["session"] != "Practice" {
			t.Errorf("title %q: session = %q, want Practice", title, got["session"])
		}
	}
}

func TestExtractLabels_EmptyExtractorSet(t *testing.T) {
	es := &ExtractorSet{Labels: map[string]LabelDef{}}
	got := es.ExtractLabels("MotoGP.2024.RD01.Qatar.Qualifying.1080p")
	if len(got) != 0 {
		t.Errorf("expected empty label map, got %v", got)
	}
}

func TestExtractFromFiles_Basic(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"series":  {Regexp: `(MotoGP|Moto2|Moto3)`},
			"session": {Regexp: `(Race|Qualifying|Sprint)`},
		},
	}
	files := []string{
		"MotoGP.2024.RD01.Qatar.Race.mkv",
		"Moto2.2024.RD01.Qatar.Race.mkv",
		"Moto3.2024.RD01.Qatar.Race.mkv",
	}
	got := es.ExtractFromFiles(files)
	if len(got) != 3 {
		t.Fatalf("expected 3 label sets, got %d", len(got))
	}
	series := map[string]bool{}
	for _, labels := range got {
		if s, ok := labels["series"]; ok {
			series[s] = true
		}
	}
	if !series["MotoGP"] || !series["Moto2"] || !series["Moto3"] {
		t.Errorf("expected MotoGP/Moto2/Moto3 in results, got %v", series)
	}
}

func TestExtractFromFiles_SkipsNoMatch(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"series": {Regexp: `(MotoGP|Moto2|Moto3)`},
		},
	}
	files := []string{
		"MotoGP.Race.mkv",
		"SomeSubtitleFile.srt",
		"Moto2.Race.mkv",
	}
	got := es.ExtractFromFiles(files)
	if len(got) != 2 {
		t.Errorf("expected 2 label sets (skipping non-matching file), got %d", len(got))
	}
}

func TestExtractorSetDefaults_ReturnsConfiguredDefaults(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"language": {Regexp: `(English|German)`, Default: "English"},
			"series":   {Regexp: `(MotoGP|Moto2)`},
		},
	}
	defaults := es.Defaults()
	if defaults["language"] != "English" {
		t.Errorf("language default = %q, want English", defaults["language"])
	}
	if _, ok := defaults["series"]; ok {
		t.Error("series should not appear in defaults (no Default set)")
	}
}

func TestExtractFromFiles_Empty(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"series": {Regexp: `(MotoGP|Moto2|Moto3)`},
		},
	}
	got := es.ExtractFromFiles([]string{})
	if len(got) != 0 {
		t.Errorf("expected 0 label sets for empty file list, got %d", len(got))
	}
}

func TestExtractLabels_DefaultNotApplied_LabelAbsentWhenNoMatch(t *testing.T) {
	// Defaults are intentionally NOT applied in ExtractLabels; they are applied
	// at coverage time via Defaults() so a file's absent label never silently
	// overrides an explicit value from the torrent title.
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"language": {Regexp: `(?i)(FRENCH|GERMAN|ENGLISH)`, Default: "English"},
		},
	}
	got := es.ExtractLabels("Show.S01E01.1080p.mkv")
	if _, ok := got["language"]; ok {
		t.Errorf("ExtractLabels should NOT apply defaults; language = %q, want absent", got["language"])
	}
}

func TestExtractLabels_DefaultNotUsedWhenMatched(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"language": {
				Regexp:  `(?i)(FRENCH|GERMAN|ENGLISH)`,
				Default: "English",
				Normalize: map[string]string{
					`(?i)french`:  "French",
					`(?i)german`:  "German",
					`(?i)english`: "English",
				},
			},
		},
	}
	got := es.ExtractLabels("Show.S01E01.1080p.FRENCH.mkv")
	if got["language"] != "French" {
		t.Errorf("language = %q, want French (matched, not default)", got["language"])
	}
}

func TestExtractLabels_DefaultEmptyMeansOmit(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"language": {Regexp: `(?i)(FRENCH|GERMAN|ENGLISH)`},
		},
	}
	got := es.ExtractLabels("Show.S01E01.1080p.mkv")
	if _, ok := got["language"]; ok {
		t.Error("language should be absent when no match and no default set")
	}
}

// --- validateLabelRegexp ---

func TestValidateLabelRegexp_OneGroup(t *testing.T) {
	re := regexp.MustCompile(`(MotoGP|Moto2)`)
	if err := validateLabelRegexp("series", `(MotoGP|Moto2)`, re); err != nil {
		t.Errorf("expected nil error for exactly one capture group, got %v", err)
	}
}

func TestValidateLabelRegexp_ZeroGroups(t *testing.T) {
	re := regexp.MustCompile(`MotoGP|Moto2`)
	if err := validateLabelRegexp("series", `MotoGP|Moto2`, re); err == nil {
		t.Error("expected error for zero capture groups, got nil")
	}
}

func TestValidateLabelRegexp_TwoGroups(t *testing.T) {
	re := regexp.MustCompile(`(MotoGP).(Moto2)`)
	if err := validateLabelRegexp("series", `(MotoGP).(Moto2)`, re); err == nil {
		t.Error("expected error for two capture groups, got nil")
	}
}

func TestExtractFromFiles_DefaultDoesNotAffectFileSkip(t *testing.T) {
	es := &ExtractorSet{
		Labels: map[string]LabelDef{
			"series":   {Regexp: `(MotoGP|Moto2|Moto3)`},
			"language": {Regexp: `(?i)(FRENCH|GERMAN|ENGLISH)`, Default: "English"},
		},
	}
	files := []string{
		"MotoGP.Race.mkv",
		"subtitles.srt", // no series match → should still be skipped
	}
	got := es.ExtractFromFiles(files)
	if len(got) != 1 {
		t.Errorf("expected 1 label set (subtitle skipped), got %d", len(got))
	}
}
