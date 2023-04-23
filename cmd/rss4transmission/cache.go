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
	"time"
)

const (
	ERROR_HOLD_DOWN = 1 // hours
	CACHE_VERSION   = 1
)

type CacheFile struct {
	Version  int              `json:"Version"`
	Errors   map[string]int64 `json:"Errors"`
	Seen     []CacheRecord    `json:"Seen"`
	filename string
	needSave bool
}

type CacheRecord struct {
	Feed      string    `json:"Feed"`
	Published time.Time `json:"Published"`
	AddTime   time.Time `json:"AddTime"`
	GUID      string    `json:"GUID"`
	Complete  bool      `json:"Complete"`
}

func OpenCache(path string) (*CacheFile, error) {
	cache := CacheFile{
		Version:  CACHE_VERSION,
		Errors:   map[string]int64{},
		Seen:     []CacheRecord{},
		needSave: false,
	}
	cacheFile := GetPath(path)
	cacheBytes, err := os.ReadFile(cacheFile)
	if err != nil {
		log.Warnf("Creating new cache file: %s", cacheFile)
	} else {
		if err = json.Unmarshal(cacheBytes, &cache); err != nil {
			return &cache, err
		}
	}
	cache.filename = cacheFile
	return &cache, nil
}

// SaveCache updates the cache and removes any entries older than the specified
// duration
func (c *CacheFile) SaveCache(d time.Duration) error {
	deletedRecord := false
	NewSeen := []CacheRecord{}

	for _, s := range c.Seen {
		if time.Since(s.Published).Hours() < d.Hours() {
			NewSeen = append(NewSeen, s)
		} else {
			deletedRecord = true
			log.Infof("Removing %s from cache", s.GUID)
		}
	}

	if !deletedRecord && !c.needSave {
		log.Debugf("no changes, so skipping cache saving")
		return nil
	}

	// move seen records over
	c.Seen = NewSeen

	log.Infof("saving cache with %d entries less than %d days old", len(c.Seen), int(d.Hours()/24))
	cacheBytes, _ := json.MarshalIndent(*c, "", "  ")
	err := os.WriteFile(c.filename, cacheBytes, 0644)
	if err != nil {
		c.needSave = true // force save again, incase we deleted a record
		return err
	}
	c.needSave = false
	return nil
}

// AddItem adds the given FeedItem to our seen cach
func (c *CacheFile) AddItem(item *FeedItem) {
	now := time.Now()
	cr := CacheRecord{
		Feed:     item.Feed,
		AddTime:  now,
		GUID:     item.Item.GUID,
		Complete: item.Complete,
	}

	if item.Item.PublishedParsed != nil {
		cr.Published = *item.Item.PublishedParsed
	}
	c.Seen = append(c.Seen, cr)
	c.needSave = true
}

/*
func (c *CacheFile) MarkComplete(item *FeedItem) {
	for _, s := range c.Seen {
		if s.GUID == item.Item.GUID && s.Feed == XXX {
			s.Complete = true
			break
		}
	}
}
*/

// Exists checks to see if the given FeedItem already exists in the Seen cache
func (c *CacheFile) Exists(feedName string, item *FeedItem) bool {
	for _, s := range c.Seen {
		if s.GUID == item.Item.GUID && s.Feed == feedName {
			return true
		}
	}
	return false
}

// CheckError determines if the given error entry is new or not
func (c *CacheFile) CheckError(item FeedItem) bool {
	expire, ok := c.Errors[item.Item.GUID]
	if ok {
		return expire < time.Now().Unix()
	}
	return true
}

// AddError adds the given entry to or error cache and returns true
// if the error entry is new or false if the error entry was cached
func (c *CacheFile) AddError(item FeedItem) bool {
	if c.CheckError(item) {
		c.Errors[item.Item.GUID] = time.Now().Add(time.Hour * ERROR_HOLD_DOWN).Unix()
		return true
	}
	return false
}
