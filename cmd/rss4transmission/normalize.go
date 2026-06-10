package main

/*
 * RSS4Transmission
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
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// NormalizedTorrent is structured metadata extracted from a raw torrent title.
type NormalizedTorrent struct {
	Series     string   `json:"series"`
	Year       int      `json:"year"`
	Round      int      `json:"round"`
	Event      string   `json:"event"`
	Circuit    string   `json:"circuit"`
	Session    string   `json:"session"`
	Feed       string   `json:"feed"`
	Resolution string   `json:"resolution"`
	Language   string   `json:"language"`
	Flags      []string `json:"flags"`
}

// validSessions is the canonical set of session values the LLM may return.
var validSessions = map[string]bool{
	// individual sessions
	"fp1":           true,
	"free_practice": true,
	"fp2":           true,
	"qualifying":    true,
	"sprint_race":   true,
	"gp_race":       true,
	"warm_up":       true,
	// bundles
	"friday_all_day":   true,
	"saturday_all_day": true,
	"sunday_all_day":   true,
	"full_weekend":     true,
	"highlights":       true,
	"race_pack":        true,
	"quali_pack":       true,
	"build_up":         true,
}

// isValidSession returns true if s is a canonical session value.
func isValidSession(s string) bool {
	return validSessions[s]
}

// Normalizer extracts structured metadata from a raw torrent title.
type Normalizer interface {
	Normalize(ctx context.Context, title string) (*NormalizedTorrent, error)
}

// --- MockNormalizer ---

// MockNormalizer is a test double backed by a map. Unknown titles return an error
// unless ErrorOnMiss is false, in which case they return a zero NormalizedTorrent.
type MockNormalizer struct {
	Results     map[string]*NormalizedTorrent
	ErrorOnMiss bool
	CallCount   int
}

func NewMockNormalizer(results map[string]*NormalizedTorrent) *MockNormalizer {
	return &MockNormalizer{Results: results, ErrorOnMiss: true}
}

func (m *MockNormalizer) Normalize(_ context.Context, title string) (*NormalizedTorrent, error) {
	m.CallCount++
	if r, ok := m.Results[title]; ok {
		return r, nil
	}
	if m.ErrorOnMiss {
		return nil, fmt.Errorf("mock: unknown title: %q", title)
	}
	return &NormalizedTorrent{}, nil
}

// --- CachingNormalizer ---

// CachingNormalizer wraps another Normalizer and caches results in CacheFile.NormalizeCache.
type CachingNormalizer struct {
	inner Normalizer
	cache *CacheFile
}

func NewCachingNormalizer(inner Normalizer, cache *CacheFile) *CachingNormalizer {
	if cache.NormalizeCache == nil {
		cache.NormalizeCache = map[string]NormalizedTorrent{}
	}
	return &CachingNormalizer{inner: inner, cache: cache}
}

func (c *CachingNormalizer) Normalize(ctx context.Context, title string) (*NormalizedTorrent, error) {
	if r, ok := c.cache.NormalizeCache[title]; ok {
		return &r, nil
	}
	r, err := c.inner.Normalize(ctx, title)
	if err != nil {
		return nil, err
	}
	c.cache.NormalizeCache[title] = *r
	c.cache.needSave = true
	return r, nil
}

// --- AnthropicNormalizer ---

const normalizerModel = "claude-haiku-4-5-20251001"

const normalizerSystemPrompt = `You extract structured metadata from motorsport torrent release titles.
Return a JSON object only — no markdown code fences, no explanation, no preamble.

Output schema:
{
  "series":     string,
  "year":       number,
  "round":      number,
  "event":      string,
  "circuit":    string,
  "session":    string,
  "feed":       string,
  "resolution": string,
  "language":   string,
  "flags":      []string
}

Canonical session values:
  Individual:  fp1, free_practice, fp2, qualifying, sprint_race, gp_race, warm_up
  Bundles:     friday_all_day, saturday_all_day, sunday_all_day, full_weekend,
               highlights, race_pack, quali_pack, build_up

Session mapping rules:
  "Friday.All.Day" or "Friday.Pack"       -> friday_all_day
  "Saturday.All.Day"                       -> saturday_all_day
  "Race.Pack"                              -> race_pack
  "Quali.Pack" or "Qualifying.Pack"        -> quali_pack
  "Full.Weekend"                           -> full_weekend
  "Full.Event" or "Full.Event.Highlights"  -> full_weekend
  "Highlights"                             -> highlights
  "Build.Up"                               -> build_up
  "PRAC.Quali.SPRINT.RACE" (4 sessions)    -> full_weekend
  "FP1" or "Practic*" (single)             -> fp1 or free_practice respectively
  "Warm.Up"                                -> warm_up
  "Sprint" or "Sprint.Race"                -> sprint_race
  "Race" (alone, not Race.Pack)            -> gp_race
  "Qualifying" or "Quali" (alone)          -> qualifying

Canonical feed values (normalize regardless of case):
  "TNT Sports"      <- TNT, TNT2, TNTSports, TNTSport
  "DAZN"            <- DAZN
  "FOX"             <- FOX, FoxSports
  "Servus"          <- Servus, ServusTV
  "MotoGP Official" <- Web-Rip or WEB-DL with no known broadcaster suffix
  "SamsungTV"       <- SamsungTV

Additional parsing rules:
  - Double dots (e.g. "Italy..Mugello") are encoding artifacts; treat as single dot
  - "FREELEECH" is a site tag — omit from all fields
  - "NaN" appearing as a trailing group name — omit
  - "2160p.Upscaled" -> resolution "2160p", add "upscaled" to flags
  - "Missing.MGP.PR" or similar -> add "missing_content" to flags
  - Multi-series prefix like "Moto3.Moto2.MotoGP" -> series is the last (highest-profile) one: "MotoGP"
  - If broadcaster cannot be matched to a canonical value, use the raw broadcaster token from the title
  - WSBK "Build.Up" is a pre-event show -> session "build_up"
  - flags contains zero or more of: "upscaled", "missing_content", "live", "highlights"

Few-shot examples:

Input: MotoGP.2026.Round08.Hungary.Race.TNT.720p.X264.English-VNL
Output: {"series":"MotoGP","year":2026,"round":8,"event":"Hungarian GP","circuit":"","session":"gp_race","feed":"TNT Sports","resolution":"720p","language":"English","flags":[]}

Input: MotoGP.2026.Round08.Hungary.Race.Pack.TNT.2160p.Upscaled.X264.English-smcgill1969
Output: {"series":"MotoGP","year":2026,"round":8,"event":"Hungarian GP","circuit":"","session":"race_pack","feed":"TNT Sports","resolution":"2160p","language":"English","flags":["upscaled"]}

Input: MotoGP.2026.Round08.Hungary.Friday.All.Day.TNT2.Live.1080p.WEB-DL.H264.English-xlab888.Missing.MGP.PR
Output: {"series":"MotoGP","year":2026,"round":8,"event":"Hungarian GP","circuit":"","session":"friday_all_day","feed":"TNT Sports","resolution":"1080p","language":"English","flags":["missing_content"]}

Input: Moto3.2026.Round07.Italy..Mugello.Quali.Servus.1080p.X264.German
Output: {"series":"Moto3","year":2026,"round":7,"event":"Italian GP","circuit":"Mugello","session":"qualifying","feed":"Servus","resolution":"1080p","language":"German","flags":[]}

Input: Moto3.Moto2.MotoGP.2026.Round08.Hungarian.Full.Event.Highlights.TNT2.1080p.DDP5.1.Web-DL.H264.English-xlab888
Output: {"series":"MotoGP","year":2026,"round":8,"event":"Hungarian GP","circuit":"","session":"full_weekend","feed":"TNT Sports","resolution":"1080p","language":"English","flags":["highlights"]}`

// AnthropicNormalizer calls the Anthropic API to normalize torrent titles.
type AnthropicNormalizer struct {
	client anthropic.Client
	model  string
}

func NewAnthropicNormalizer(apiKey, model string) *AnthropicNormalizer {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if model == "" {
		model = normalizerModel
	}
	return &AnthropicNormalizer{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:  model,
	}
}

func (a *AnthropicNormalizer) Normalize(ctx context.Context, title string) (*NormalizedTorrent, error) {
	msg, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: 512,
		System: []anthropic.TextBlockParam{
			{
				Text:         normalizerSystemPrompt,
				CacheControl: anthropic.NewCacheControlEphemeralParam(),
			},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(title)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic API: %w", err)
	}

	if len(msg.Content) == 0 {
		return nil, fmt.Errorf("anthropic API: empty response")
	}

	block := msg.Content[0]
	if block.Type != "text" {
		return nil, fmt.Errorf("anthropic API: unexpected content type %q", block.Type)
	}

	var norm NormalizedTorrent
	if err := json.Unmarshal([]byte(block.Text), &norm); err != nil {
		return nil, fmt.Errorf("anthropic API: parse JSON %q: %w", block.Text, err)
	}
	return &norm, nil
}
