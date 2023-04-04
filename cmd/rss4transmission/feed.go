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
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"

	"github.com/hekmon/transmissionrpc/v2"
	"github.com/mmcdole/gofeed"
)

const (
	RSS_PARAM_TAG = "param"
)

type FeedItem struct {
	Item *gofeed.Item
}

func (fi *FeedItem) getTorrentContents() ([]byte, error) {
	torrentUrl := ""

	for _, enclosure := range fi.Item.Enclosures {
		if enclosure.Type == "application/x-bittorrent" {
			torrentUrl = enclosure.URL
			break
		}
	}
	if torrentUrl == "" {
		return []byte{}, fmt.Errorf("Unable to find torrent link for %s", fi.Item.Title)
	}

	resp, err := http.Get(torrentUrl)
	if err != nil {
		return []byte{}, fmt.Errorf("Unable to download %s: %s", torrentUrl, err)
	}
	return io.ReadAll(resp.Body)

}

func (fi *FeedItem) Download(dir string) (string, error) {
	var contents []byte
	var err error

	if contents, err = fi.getTorrentContents(); err != nil {
		return "", err
	}

	filePath := path.Join(dir, fmt.Sprintf("%s.torrent", fi.Item.Title))
	if err = os.WriteFile(filePath, contents, 0644); err != nil {
		return "", fmt.Errorf("Unable to write %s: %s", filePath, err.Error())
	}
	return filePath, nil
}

func (fi *FeedItem) Torrent(t *transmissionrpc.Client, dir string) (string, error) {
	var err error
	var filePath string

	if filePath, err = fi.Download("/tmp"); err != nil {
		return filePath, err
	}

	ctx := context.TODO()
	if _, err = t.TorrentAddFileDownloadDir(ctx, filePath, dir); err != nil {
		return filePath, err
	}

	return filePath, nil
}

func (f *Feed) NewItems(feed *gofeed.Feed) []*FeedItem {
	items := []*FeedItem{}

	for _, item := range feed.Items {
		if f.Check(item.Title) {
			items = append(items, &FeedItem{Item: item})
		}
	}

	return items
}

func (m *Feed) compile() {
	if m.compiled {
		return
	}

	var err error
	var r *regexp.Regexp

	for _, match := range m.Regexp {
		if r, err = regexp.Compile(match); err != nil {
			log.WithError(err).Errorf("Unable to compile Regexp: %s", match)
		}
		m.regexp = append(m.regexp, r)
	}

	for _, exclude := range m.Exclude {
		if r, err = regexp.Compile(exclude); err != nil {
			log.WithError(err).Errorf("Unable to compile Exclude: %s", exclude)
		}
		m.exclude = append(m.exclude, r)
	}

	m.compiled = true
}

// Check if a given string matches this feed
func (m *Feed) Check(check string) bool {
	m.compile()

	// first see if we exclude it
	for i, r := range m.exclude {
		// use compiled
		match := r.Find([]byte(check))
		if match != nil {
			log.Debugf("Exclude %s => %s", check, m.Exclude[i])
			return false
		}
	}

	// then see if we match
	for i, r := range m.regexp {
		// use compiled
		match := r.Find([]byte(check))
		if match != nil {
			log.Infof("Matched %s => %s", check, m.Regexp[i])
			return true
		}
	}

	// no match
	log.Debugf("Skipped %s", check)
	return false
}

// Does the RssFilter have the given category?
func (m *Feed) HasCategory(category string) bool {
	for _, c := range m.Categories {
		if c == category {
			return true
		}
	}
	return false
}

/*
// Define the interface for the RSS Feed Filter
type RssFeed interface {
	Reset()
	GetFeedType() string
	GetOrder() int
	GetAutoDownload() bool
	GetDownloadPath() string
	DownloadFilename(string, RssFeedEntry) string
	GetParam(string) (string, error)
	GenerateUrl() string
	GetPublishFormat() string
	UrlRewriter(string) string
	Match(RssFeedEntry) (bool, string)
	GetFilters() map[string]RssFilter
}

func GetParamTag(v reflect.Value, fieldName string) (string, error) {
	field, ok := v.Type().FieldByName(fieldName)
	if !ok {
		return "", fmt.Errorf("Invalid field '%s' in %s", fieldName, v.Type().Name())
	}
	tag := string(field.Tag.Get(RSS_PARAM_TAG))
	return tag, nil
}

// Represents a single RSS Feed Entry
type RssFeedEntry struct {
	FeedName          string    `json:"FeedName"`
	Title             string    `json:"Title"`
	Published         time.Time `json:"Published"`
	Categories        []string  `json:"Categories"`
	Description       string    `json:"Description"`
	Url               string    `json:"Url"`
	TorrentUrl        string    `json:"TorrentUrl"`
	TorrentBytes      uint64    `json:"TorrentBytes"`
	TorrentSize       string    `json:"TorrentSize"`
	TorrentCategories []string  `json:"TorrentCategories"`
	AutoDownload      bool
}

// returns an entry as a pretty string
func (rfe *RssFeedEntry) Sprint() string {
	ret := fmt.Sprintf("Title: %s", rfe.Title)
	ret = fmt.Sprintf("%s\n\tPublished: %s", ret, rfe.Published.Local().Format("2006-01-02 15:04 MST"))
	ret = fmt.Sprintf("%s\n\tCategories: %s", ret, rfe.Categories)
	ret = fmt.Sprintf("%s\n\tDescription: %s", ret, rfe.Description)
	ret = fmt.Sprintf("%s\n\tUrl: %s", ret, rfe.Url)
	ret = fmt.Sprintf("%s\n\tTorrent: %s [%d]", ret, rfe.TorrentUrl, rfe.TorrentBytes)
	ret = fmt.Sprintf("%s\n\tTorrent Categories: %s", ret, strings.Join(rfe.TorrentCategories, ", "))
	ret = fmt.Sprintf("%s\n\tTorrent Size: %s\n", ret, rfe.TorrentSize)
	return ret
}

func DownloadFeed(feedname string, rssFeed RssFeed) ([]RssFeedEntry, error) {
	ret := []RssFeedEntry{}
	url := rssFeed.GenerateUrl()
	log.Debugf("RSS Feed URL = %s", url)
	fp := gofeed.NewParser()

	feed, err := fp.ParseURL(url)
	if err != nil {
		return ret, fmt.Errorf("Unable to load %s", url)
	}

	for _, item := range feed.Items {
		t, err := time.Parse(rssFeed.GetPublishFormat(), item.Published)
		if err != nil {
			return ret, fmt.Errorf("Unable to parse Published time `%s` with format `%s`: %s",
				item.Published, rssFeed.GetPublishFormat(), err)
		}

		// figure out torrent info
		torrentUrl := ""
		torrentBytes := uint64(0)

		for _, enclosure := range item.Enclosures {
			if enclosure.Type == "application/x-bittorrent" {
				torrentUrl = enclosure.URL
				torrentBytes, err = strconv.ParseUint(enclosure.Length, 10, 64)
				if err != nil {
					return ret, fmt.Errorf("Unable to parse Torrent Bytes `%s`: %s",
						enclosure.Length, err)
				}
				break
			}
		}

		// torrent extension fields
		torrentCategories := []string{}
		torrentSize := ""
		for _, val1 := range item.Extensions {
			for _, val2 := range val1 {
				for _, ext := range val2 {
					switch name := ext.Attrs["name"]; name {
					case "category":
						torrentCategories = strings.Split(ext.Attrs["value"], ", ")
					case "size":
						torrentSize = ext.Attrs["value"]
					}
				}
			}
		}
		ret = append(ret,
			RssFeedEntry{
				FeedName:          feedname,
				Title:             item.Title,
				Published:         t,
				Categories:        item.Categories,
				Description:       item.Description,
				Url:               item.Link,
				TorrentUrl:        torrentUrl,
				TorrentBytes:      torrentBytes,
				TorrentSize:       torrentSize,
				TorrentCategories: torrentCategories,
			})
	}
	return ret, nil
}

// filters the given entries and returns those that match our filters
func FilterEntries(entries []RssFeedEntry, feed RssFeed, filters []string) ([]RssFeedEntry, error) {
	retEntries := []RssFeedEntry{}
	for _, entry := range entries {
		// check to see if anything matches
		match, filter := feed.Match(entry)
		if match {
			for _, filterName := range filters {
				// does the hit match one of our specified filters?
				if filter == filterName {
					// set if this entry should be auto downloaded
					filters := feed.GetFilters()
					entry.AutoDownload = filters[filter].AutoDownload
					retEntries = append(retEntries, entry)
				}
			}
		}
	}
	return retEntries, nil
}

// returns true or false if the entry is already in the entries
func RssFeedEntryExits(entries []RssFeedEntry, entry RssFeedEntry) bool {
	for _, e := range entries {
		if e.Title == entry.Title {
			return true
		}
	}
	return false
}
*/
