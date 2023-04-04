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
	"io/ioutil"
	"time"
)

const (
	ERROR_HOLD_DOWN = 4 // hours
)

type CacheFile struct {
	filename string
	Errors   map[string]int64 `json:"Errors"`
	Items    []*FeedItem      `json:"Items"`
}

func OpenCache(path string) (*CacheFile, error) {
	cache := CacheFile{
		Errors: map[string]int64{},
		Items:  []*FeedItem{},
	}
	cacheFile := GetPath(path)
	cacheBytes, err := ioutil.ReadFile(cacheFile)
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

func (c *CacheFile) SaveCache() error {
	cacheBytes, _ := json.MarshalIndent(*c, "", "  ")
	return ioutil.WriteFile(c.filename, cacheBytes, 0644)
}

// returns true if the error for the given entry is 'new'
func (c *CacheFile) CheckNewError(entry string) bool {
	expire, ok := c.Errors[entry]
	if ok {
		return expire < time.Now().Unix()
	}
	return true
}

func (c *CacheFile) AddError(entry string) {
	c.Errors[entry] = time.Now().Add(time.Hour * ERROR_HOLD_DOWN).Unix()
}

func (c *CacheFile) AddItem(item *FeedItem) {
	c.Items = append(c.Items, item)
}
