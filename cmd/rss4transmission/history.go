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
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
)

const HISTORY_VERSION = 1

type HistoryFile struct {
	Version  int             `json:"Version"`
	Records  []HistoryRecord `json:"Records"`
	filename string
	mu       sync.RWMutex
	// guidIndex maps "feedName|GUID" → index in Records; rebuilt on load.
	guidIndex map[string]int
}

type HistoryRecord struct {
	Feed        string            `json:"Feed"`
	Title       string            `json:"Title"`
	GUID        string            `json:"GUID"`
	Published   time.Time         `json:"Published"`
	ProcessedAt time.Time         `json:"ProcessedAt"`
	Outcome     string            `json:"Outcome"`
	Reason      string            `json:"Reason,omitempty"`
	Labels      map[string]string `json:"Labels,omitempty"`
}

// outcomeRank returns a rank for dedup: lower is more interesting.
// "dispatched"/"downloaded" beat "skipped" beat "excluded" beat "error".
func outcomeRank(outcome string) int {
	switch outcome {
	case "dispatched", "downloaded":
		return 0
	case "skipped":
		return 1
	case "excluded":
		return 2
	default:
		return 3
	}
}

func historyKey(feedName, guid string) string {
	return feedName + "|" + guid
}

// NewHistoryRecord builds a HistoryRecord from a gofeed.Item.
func NewHistoryRecord(feedName string, item *gofeed.Item, outcome, reason string, labels map[string]string) HistoryRecord {
	rec := HistoryRecord{
		Feed:    feedName,
		Title:   item.Title,
		GUID:    item.GUID,
		Outcome: outcome,
		Reason:  reason,
		Labels:  labels,
	}
	if item.PublishedParsed != nil {
		rec.Published = *item.PublishedParsed
	}
	return rec
}

func OpenHistory(path string) (*HistoryFile, error) {
	h := &HistoryFile{
		Version: HISTORY_VERSION,
		Records: []HistoryRecord{},
	}
	histFile := GetPath(path)
	data, err := os.ReadFile(histFile)
	if os.IsNotExist(err) {
		log.Infof("Creating new history file: %s", histFile)
	} else if err != nil {
		return h, err
	} else {
		if err = json.Unmarshal(data, h); err != nil {
			return h, err
		}
	}
	h.filename = histFile
	h.rebuildGUIDIndex()
	return h, nil
}

func (h *HistoryFile) rebuildGUIDIndex() {
	h.guidIndex = make(map[string]int, len(h.Records))
	for i, r := range h.Records {
		h.guidIndex[historyKey(r.Feed, r.GUID)] = i
	}
}

// AddOrUpdateRecord records a feed item outcome. If the GUID is already in history
// it is only updated when the new outcome is more interesting (dispatched > skipped > excluded).
func (h *HistoryFile) AddOrUpdateRecord(rec HistoryRecord) {
	rec.ProcessedAt = time.Now()
	key := historyKey(rec.Feed, rec.GUID)

	h.mu.Lock()
	defer h.mu.Unlock()

	if idx, ok := h.guidIndex[key]; ok {
		if outcomeRank(rec.Outcome) < outcomeRank(h.Records[idx].Outcome) {
			h.Records[idx] = rec
		}
		return
	}
	h.guidIndex[key] = len(h.Records)
	h.Records = append(h.Records, rec)
}

// SaveHistory prunes records older than d and writes the file.
func (h *HistoryFile) SaveHistory(d time.Duration) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var newRecords []HistoryRecord
	for _, r := range h.Records {
		if time.Since(r.ProcessedAt) < d {
			newRecords = append(newRecords, r)
		} else {
			log.Debugf("Removing history record for %s", r.GUID)
		}
	}
	h.Records = newRecords
	h.rebuildGUIDIndex()

	type serialized struct {
		Version int             `json:"Version"`
		Records []HistoryRecord `json:"Records"`
	}
	data, _ := json.MarshalIndent(serialized{Version: h.Version, Records: h.Records}, "", "  ")
	return os.WriteFile(h.filename, data, 0644) //nolint:gosec
}

// recordHistory is a convenience method on RunContext that creates a HistoryRecord
// and adds it to the history file. It is a no-op when ctx.History is nil.
func (ctx *RunContext) recordHistory(feedName string, item *gofeed.Item, outcome, reason string, labels map[string]string) {
	if ctx.History != nil {
		ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, item, outcome, reason, labels))
	}
}

// GetRecords returns a copy of all records for safe concurrent reads.
func (h *HistoryFile) GetRecords() []HistoryRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]HistoryRecord, len(h.Records))
	copy(result, h.Records)
	return result
}
