package main

import (
	"testing"
)

// --- IdentityKey ---

func TestIdentityKey_AllPresent(t *testing.T) {
	labels := map[string]string{
		"series":     "MotoGP",
		"round":      "RD01",
		"session":    "Race",
		"resolution": "1080p",
	}
	key, ok := IdentityKey(labels, []string{"series", "round", "session"})
	if !ok {
		t.Fatal("expected ok=true when all identity labels are present")
	}
	if key == "" {
		t.Error("expected non-empty key")
	}
	// Deterministic
	key2, _ := IdentityKey(labels, []string{"series", "round", "session"})
	if key != key2 {
		t.Errorf("IdentityKey is not deterministic: %q != %q", key, key2)
	}
}

func TestIdentityKey_MissingLabel(t *testing.T) {
	labels := map[string]string{
		"series":  "MotoGP",
		"session": "Race",
		// round is missing
	}
	_, ok := IdentityKey(labels, []string{"series", "round", "session"})
	if ok {
		t.Error("expected ok=false when an identity label is missing")
	}
}

func TestIdentityKey_DifferentValues_DifferentKeys(t *testing.T) {
	a := map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race"}
	b := map[string]string{"series": "Moto2", "round": "RD01", "session": "Race"}
	keyA, _ := IdentityKey(a, []string{"series", "round", "session"})
	keyB, _ := IdentityKey(b, []string{"series", "round", "session"})
	if keyA == keyB {
		t.Errorf("different series should produce different keys: both %q", keyA)
	}
}

func TestIdentityKey_ExtraLabelsIgnored(t *testing.T) {
	a := map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "1080p"}
	b := map[string]string{"series": "MotoGP", "round": "RD01", "session": "Race", "resolution": "720p"}
	keyA, _ := IdentityKey(a, []string{"series", "round", "session"})
	keyB, _ := IdentityKey(b, []string{"series", "round", "session"})
	if keyA != keyB {
		t.Errorf("extra labels should not affect identity key: %q != %q", keyA, keyB)
	}
}

// --- MergeLabels ---

func TestMergeLabels_FileOverridesTitle(t *testing.T) {
	title := map[string]string{"series": "MotoGP", "resolution": "1080p", "round": "RD01"}
	file := map[string]string{"series": "Moto2", "session": "Race"}
	merged := MergeLabels(title, file)
	if merged["series"] != "Moto2" {
		t.Errorf("file label should override title: series = %q, want Moto2", merged["series"])
	}
	if merged["resolution"] != "1080p" {
		t.Errorf("title label should fill in missing: resolution = %q, want 1080p", merged["resolution"])
	}
	if merged["session"] != "Race" {
		t.Errorf("session = %q, want Race", merged["session"])
	}
	if merged["round"] != "RD01" {
		t.Errorf("round = %q, want RD01", merged["round"])
	}
}

func TestMergeLabels_NoMutation(t *testing.T) {
	title := map[string]string{"series": "MotoGP"}
	file := map[string]string{"series": "Moto2"}
	_ = MergeLabels(title, file)
	if title["series"] != "MotoGP" {
		t.Error("MergeLabels must not mutate title map")
	}
}

func TestMergeLabels_EmptyFile(t *testing.T) {
	title := map[string]string{"series": "MotoGP", "resolution": "1080p"}
	merged := MergeLabels(title, map[string]string{})
	if merged["series"] != "MotoGP" || merged["resolution"] != "1080p" {
		t.Errorf("empty file labels should keep title labels: %v", merged)
	}
}

// --- PreferenceRank ---

func TestPreferenceRank_BestChoice(t *testing.T) {
	prefer := []PreferDimension{
		{Label: "network", Order: []string{"TNT", "Global"}},
		{Label: "resolution", Order: []string{"1080p", "720p"}},
	}
	labels := map[string]string{"network": "TNT", "resolution": "1080p"}
	rank := PreferenceRank(labels, prefer)
	if rank[0] != 0 || rank[1] != 0 {
		t.Errorf("TNT+1080p should be [0,0], got %v", rank)
	}
}

func TestPreferenceRank_SecondChoice(t *testing.T) {
	prefer := []PreferDimension{
		{Label: "network", Order: []string{"TNT", "Global"}},
		{Label: "resolution", Order: []string{"1080p", "720p"}},
	}
	labels := map[string]string{"network": "Global", "resolution": "720p"}
	rank := PreferenceRank(labels, prefer)
	if rank[0] != 1 || rank[1] != 1 {
		t.Errorf("Global+720p should be [1,1], got %v", rank)
	}
}

func TestPreferenceRank_MissingLabel(t *testing.T) {
	prefer := []PreferDimension{
		{Label: "network", Order: []string{"TNT", "Global"}},
		{Label: "resolution", Order: []string{"1080p", "720p"}},
	}
	labels := map[string]string{"network": "TNT"} // resolution missing
	rank := PreferenceRank(labels, prefer)
	if rank[0] != 0 {
		t.Errorf("network=TNT should be rank 0, got %d", rank[0])
	}
	if rank[1] != 2 { // len(order)
		t.Errorf("missing resolution should rank as worst (2), got %d", rank[1])
	}
}

func TestPreferenceRank_UnknownValue(t *testing.T) {
	prefer := []PreferDimension{
		{Label: "resolution", Order: []string{"1080p", "720p"}},
	}
	labels := map[string]string{"resolution": "480p"}
	rank := PreferenceRank(labels, prefer)
	if rank[0] != 2 {
		t.Errorf("unknown value should rank as worst (2), got %d", rank[0])
	}
}

// --- IsBetter ---

func TestIsBetter_FirstDimWins(t *testing.T) {
	a := []int{0, 1}
	b := []int{1, 0}
	if !IsBetter(a, b) {
		t.Error("a=[0,1] should be better than b=[1,0]")
	}
	if IsBetter(b, a) {
		t.Error("b=[1,0] should NOT be better than a=[0,1]")
	}
}

func TestIsBetter_Equal(t *testing.T) {
	a := []int{0, 1}
	b := []int{0, 1}
	if IsBetter(a, b) {
		t.Error("equal ranks should not be 'better'")
	}
}

func TestIsBetter_Tiebreak(t *testing.T) {
	a := []int{0, 0}
	b := []int{0, 1}
	if !IsBetter(a, b) {
		t.Error("a=[0,0] should be better than b=[0,1] via tiebreak")
	}
}

// --- Group.Matches ---

func TestGroupMatches_AllRequired(t *testing.T) {
	g := Group{Require: map[string][]string{
		"series":  {"MotoGP"},
		"session": {"Race", "Qualifying"},
	}}
	labels := map[string]string{"series": "MotoGP", "session": "Qualifying", "resolution": "1080p"}
	if !g.Matches(labels) {
		t.Error("expected match when all required labels are satisfied")
	}
}

func TestGroupMatches_WrongValue(t *testing.T) {
	g := Group{Require: map[string][]string{
		"series": {"MotoGP"},
	}}
	labels := map[string]string{"series": "Moto2"}
	if g.Matches(labels) {
		t.Error("expected no match when series value not in required list")
	}
}

func TestGroupMatches_MissingLabel(t *testing.T) {
	g := Group{Require: map[string][]string{
		"series":  {"MotoGP"},
		"session": {"Race"},
	}}
	labels := map[string]string{"series": "MotoGP"} // session missing
	if g.Matches(labels) {
		t.Error("expected no match when a required label is missing")
	}
}

func TestGroupMatches_EmptyRequire(t *testing.T) {
	g := Group{Require: map[string][]string{}}
	labels := map[string]string{"series": "MotoGP"}
	if !g.Matches(labels) {
		t.Error("empty Require should match anything")
	}
}

func TestGroupMatches_MultipleAcceptableValues(t *testing.T) {
	g := Group{Require: map[string][]string{
		"series": {"Moto2", "Moto3"},
	}}
	for _, series := range []string{"Moto2", "Moto3"} {
		labels := map[string]string{"series": series}
		if !g.Matches(labels) {
			t.Errorf("expected match for series=%q", series)
		}
	}
	labels := map[string]string{"series": "MotoGP"}
	if g.Matches(labels) {
		t.Error("expected no match for series=MotoGP")
	}
}
