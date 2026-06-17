package main

import (
	"context"
	"testing"
	"time"
)

// --- Phase A: enum validation ---

func TestSessionCanonical_ValidValues(t *testing.T) {
	valid := []string{
		"fp1", "free_practice", "fp2", "qualifying", "sprint_race", "gp_race", "warm_up",
		"friday_all_day", "saturday_all_day", "sunday_all_day", "full_weekend",
		"highlights", "race_pack", "quali_pack", "build_up",
	}
	for _, s := range valid {
		if !isValidSession(s) {
			t.Errorf("isValidSession(%q) = false, want true", s)
		}
	}
}

func TestSessionCanonical_InvalidRejects(t *testing.T) {
	invalid := []string{"race", "sprint", "practice", "RACE", "GP_RACE", "", "unknown"}
	for _, s := range invalid {
		if isValidSession(s) {
			t.Errorf("isValidSession(%q) = true, want false", s)
		}
	}
}

// --- Phase B: MockNormalizer ---

func makeNorm(series, session, feed, resolution, language string, year, round int, flags ...string) *NormalizedTorrent {
	if flags == nil {
		flags = []string{}
	}
	return &NormalizedTorrent{
		Series:     series,
		Year:       year,
		Round:      round,
		Session:    session,
		Feed:       feed,
		Resolution: resolution,
		Language:   language,
		Flags:      flags,
	}
}

func TestNormalize_BasicTitle(t *testing.T) {
	title := "MotoGP.2026.Round08.Hungary.Race.TNT.720p.X264.English-VNL"
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		title: makeNorm("MotoGP", "gp_race", "TNT Sports", "720p", "English", 2026, 8),
	})
	norm, err := mock.Normalize(context.Background(), title)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if norm.Series != "MotoGP" {
		t.Errorf("Series = %q, want MotoGP", norm.Series)
	}
	if norm.Year != 2026 {
		t.Errorf("Year = %d, want 2026", norm.Year)
	}
	if norm.Round != 8 {
		t.Errorf("Round = %d, want 8", norm.Round)
	}
	if norm.Session != "gp_race" {
		t.Errorf("Session = %q, want gp_race", norm.Session)
	}
	if norm.Feed != "TNT Sports" {
		t.Errorf("Feed = %q, want TNT Sports", norm.Feed)
	}
	if norm.Resolution != "720p" {
		t.Errorf("Resolution = %q, want 720p", norm.Resolution)
	}
	if norm.Language != "English" {
		t.Errorf("Language = %q, want English", norm.Language)
	}
}

func TestNormalize_TNTVariants(t *testing.T) {
	titles := map[string]*NormalizedTorrent{
		"MotoGP.2026.Round08.Hungary.Race.TNT.720p.X264.English-VNL":          makeNorm("MotoGP", "gp_race", "TNT Sports", "720p", "English", 2026, 8),
		"MotoGP.2026.Round07.Italy.Race.Pack.TNT2.Live.1080p.WEB-DL.English":  makeNorm("MotoGP", "race_pack", "TNT Sports", "1080p", "English", 2026, 7),
		"MotoGP.2026.Round07.Italy.Mugello.Race.Pack.TNTSports.1080p.English": makeNorm("MotoGP", "race_pack", "TNT Sports", "1080p", "English", 2026, 7),
	}
	mock := NewMockNormalizer(titles)
	for title, want := range titles {
		norm, err := mock.Normalize(context.Background(), title)
		if err != nil {
			t.Fatalf("title %q: unexpected error: %v", title, err)
		}
		if norm.Feed != "TNT Sports" {
			t.Errorf("title %q: Feed = %q, want TNT Sports", title, norm.Feed)
		}
		if norm.Feed != want.Feed {
			t.Errorf("title %q: Feed = %q, want %q", title, norm.Feed, want.Feed)
		}
	}
}

func TestNormalize_DoubleDot(t *testing.T) {
	title := "MotoGP.2026.Round07.Italy..Mugello.Race.TNT.720p.X264.English-VNL"
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		title: {Series: "MotoGP", Year: 2026, Round: 7, Circuit: "Mugello", Session: "gp_race",
			Feed: "TNT Sports", Resolution: "720p", Language: "English", Flags: []string{}},
	})
	norm, err := mock.Normalize(context.Background(), title)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if norm.Circuit != "Mugello" {
		t.Errorf("Circuit = %q, want Mugello", norm.Circuit)
	}
}

func TestNormalize_Upscaled(t *testing.T) {
	title := "MotoGP.2026.Round08.Hungary.Race.Pack.TNT.2160p.Upscaled.X264.English-smcgill1969"
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		title: makeNorm("MotoGP", "race_pack", "TNT Sports", "2160p", "English", 2026, 8, "upscaled"),
	})
	norm, err := mock.Normalize(context.Background(), title)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if norm.Resolution != "2160p" {
		t.Errorf("Resolution = %q, want 2160p", norm.Resolution)
	}
	if len(norm.Flags) == 0 || norm.Flags[0] != "upscaled" {
		t.Errorf("Flags = %v, want [upscaled]", norm.Flags)
	}
}

func TestNormalize_MissingContent(t *testing.T) {
	title := "MotoGP.2026.Round08.Hungary.Friday.All.Day.TNT2.Live.1080p.WEB-DL.H264.English-xlab888.Missing.MGP.PR"
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		title: makeNorm("MotoGP", "friday_all_day", "TNT Sports", "1080p", "English", 2026, 8, "missing_content"),
	})
	norm, err := mock.Normalize(context.Background(), title)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, f := range norm.Flags {
		if f == "missing_content" {
			found = true
		}
	}
	if !found {
		t.Errorf("Flags = %v, want missing_content in flags", norm.Flags)
	}
}

func TestNormalize_FreeleechIgnored(t *testing.T) {
	title := "MotoGP.2026.Round07.Italy..Mugello.Sprint.Race.Web-Rip.1080p.X264.English.VERUM.FREELEECH"
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		title: makeNorm("MotoGP", "sprint_race", "MotoGP Official", "1080p", "English", 2026, 7),
	})
	norm, err := mock.Normalize(context.Background(), title)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// FREELEECH must not appear in any field
	fields := []string{norm.Series, norm.Session, norm.Feed, norm.Resolution, norm.Language, norm.Event, norm.Circuit}
	for _, f := range fields {
		if f == "FREELEECH" || f == "freeleech" {
			t.Errorf("FREELEECH appeared in field value %q", f)
		}
	}
	for _, flag := range norm.Flags {
		if flag == "FREELEECH" || flag == "freeleech" {
			t.Errorf("FREELEECH appeared in flags")
		}
	}
}

func TestNormalize_UnknownSeries(t *testing.T) {
	title := "WSBK.2026.Round06.Spain.Aragon.Build.Up.WEB-DL.1080p.H264.English-dtb"
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		title: makeNorm("WSBK", "build_up", "MotoGP Official", "1080p", "English", 2026, 6),
	})
	norm, err := mock.Normalize(context.Background(), title)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if norm.Series != "WSBK" {
		t.Errorf("Series = %q, want WSBK", norm.Series)
	}
}

func TestNormalize_MultiSeriesPrefix(t *testing.T) {
	title := "Moto3.Moto2.MotoGP.2026.Round08.Hungarian.Full.Event.Highlights.TNT2.1080p.DDP5.1.Web-DL.H264.English-xlab888"
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		title: makeNorm("MotoGP", "full_weekend", "TNT Sports", "1080p", "English", 2026, 8, "highlights"),
	})
	norm, err := mock.Normalize(context.Background(), title)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if norm.Series != "MotoGP" {
		t.Errorf("Series = %q, want MotoGP", norm.Series)
	}
}

func TestNormalize_BuildUp(t *testing.T) {
	title := "WSBK.2026.Round06.Spain.Aragon.Build.Up.WEB-DL.1080p.H264.English-dtb"
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{
		title: makeNorm("WSBK", "build_up", "MotoGP Official", "1080p", "English", 2026, 6),
	})
	norm, err := mock.Normalize(context.Background(), title)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if norm.Session != "build_up" {
		t.Errorf("Session = %q, want build_up", norm.Session)
	}
}

func TestNormalize_ErrorOnUnknownTitle(t *testing.T) {
	mock := NewMockNormalizer(map[string]*NormalizedTorrent{})
	_, err := mock.Normalize(context.Background(), "unknown title")
	if err == nil {
		t.Error("expected error for unknown title, got nil")
	}
}

// --- Phase C: CachingNormalizer ---

func TestCache_HitAvoidsDuplicateAPICall(t *testing.T) {
	title := "MotoGP.2026.Round08.Hungary.Race.TNT.720p.X264.English-VNL"
	want := makeNorm("MotoGP", "gp_race", "TNT Sports", "720p", "English", 2026, 8)
	inner := NewMockNormalizer(map[string]*NormalizedTorrent{title: want})

	cache := &CacheFile{
		Version:        CACHE_VERSION,
		Errors:         map[string]int64{},
		Seen:           []CacheRecord{},
		NormalizeCache: map[string]NormalizedTorrent{},
	}
	cn := NewCachingNormalizer(inner, cache)

	ctx := context.Background()
	if _, err := cn.Normalize(ctx, title); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := cn.Normalize(ctx, title); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if inner.CallCount != 1 {
		t.Errorf("inner called %d times, want 1", inner.CallCount)
	}
}

func TestCache_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/seen.json"

	title := "MotoGP.2026.Round08.Hungary.Race.TNT.720p.X264.English-VNL"
	norm := makeNorm("MotoGP", "gp_race", "TNT Sports", "720p", "English", 2026, 8)

	cache := &CacheFile{
		Version:        CACHE_VERSION,
		Errors:         map[string]int64{},
		Seen:           []CacheRecord{},
		NormalizeCache: map[string]NormalizedTorrent{title: *norm},
		filename:       path,
		needSave:       true,
	}
	if err := cache.SaveCache(30 * 24 * time.Hour); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	reloaded, err := OpenCache(path)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	if _, ok := reloaded.NormalizeCache[title]; !ok {
		t.Error("NormalizeCache entry not found after reload")
	}
	if reloaded.NormalizeCache[title].Series != "MotoGP" {
		t.Errorf("reloaded Series = %q, want MotoGP", reloaded.NormalizeCache[title].Series)
	}
}

func TestNewGeminiNormalizer_EnvFallback(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "test-key")
	_, err := NewGeminiNormalizer("", "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("NewGeminiNormalizer with env key: %v", err)
	}
}

func TestNewGeminiNormalizer_ExplicitKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "") // ensure env var doesn't interfere; t.Setenv restores original value
	_, err := NewGeminiNormalizer("explicit-key", "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("NewGeminiNormalizer with explicit key: %v", err)
	}
}
