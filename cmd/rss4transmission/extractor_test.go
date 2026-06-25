package main

import (
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
