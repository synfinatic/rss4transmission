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
	Feed     string
	Complete bool
	Location string
	Item     *gofeed.Item
}

func (fi *FeedItem) TorrentURL() (string, error) {
	for _, enclosure := range fi.Item.Enclosures {
		if enclosure.Type == "application/x-bittorrent" {
			return enclosure.URL, nil
		}
	}

	return "", fmt.Errorf("Unable to find Type = application/x-bittorrent for %s", fi.Item.Title)
}

func (fi *FeedItem) getTorrentContents() ([]byte, error) {
	torrentUrl, err := fi.TorrentURL()
	if err != nil {
		return []byte{}, err
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

func (fi *FeedItem) Torrent(t *transmissionrpc.Client, dir string) error {
	var err error
	var torrentURL string

	if torrentURL, err = fi.TorrentURL(); err != nil {
		return err
	}

	addPayload := transmissionrpc.TorrentAddPayload{
		DownloadDir: &dir,
		Filename:    &torrentURL,
	}
	if _, err = t.TorrentAdd(context.TODO(), addPayload); err != nil {
		return err
	}

	return nil
}

func (fi *FeedItem) IsComplete() bool {
	if fi.Complete == true {
		return true
	}

	// XXX: ask transmission for an update
	return false
}

func (f *Feed) NewItems(feedName string, feed *gofeed.Feed) []*FeedItem {
	items := []*FeedItem{}

	for _, item := range feed.Items {
		if f.Check(item) {
			fi := FeedItem{
				Feed:     feedName,
				Item:     item,
				Complete: false,
				Location: path.Join(f.DownloadPath, item.Title),
			}
			items = append(items, &fi)
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
