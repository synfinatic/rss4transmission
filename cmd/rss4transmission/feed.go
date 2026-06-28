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
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hekmon/transmissionrpc/v3"
	bytesize "github.com/inhies/go-bytesize"
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
	return "", fmt.Errorf("unable to find Type = application/x-bittorrent for %s", fi.Item.Title)
}

func (fi *FeedItem) getTorrentContents(cacheDir string) ([]byte, error) {
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0755); err != nil { //nolint:gosec
			log.WithError(err).Warnf("Unable to create torrent cache dir: %s", cacheDir)
			return fi.fetchTorrent()
		}
		cachePath := filepath.Join(cacheDir, sanitizeFilename(fi.Item.Title)+".torrent")
		if data, err := os.ReadFile(cachePath); err == nil {
			log.Tracef("Torrent cache hit: %s", cachePath)
			return data, nil
		} else {
			log.Tracef("Torrent cache miss: %s", cachePath)
		}
		data, err := fi.fetchTorrent()
		if err != nil {
			return nil, err
		}
		if werr := os.WriteFile(cachePath, data, 0644); werr != nil { //nolint:gosec
			log.WithError(werr).Warnf("Unable to write torrent cache: %s", cachePath)
		}
		return data, nil
	}
	return fi.fetchTorrent()
}

func (fi *FeedItem) fetchTorrent() ([]byte, error) {
	torrentUrl, err := fi.TorrentURL()
	if err != nil {
		return []byte{}, err
	}
	resp, err := http.Get(torrentUrl) //nolint:gosec
	if err != nil {
		return []byte{}, fmt.Errorf("unable to download %s: %s", torrentUrl, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return io.ReadAll(resp.Body)
}

// Download saves the .torrent file to dir and returns its path. The caller is
// responsible for recording the item in the cache.
func (fi *FeedItem) Download(ctx *RunContext, dir string, cacheDir string) (string, error) {
	filePath := path.Join(dir, fmt.Sprintf("%s.torrent", sanitizeFilename(fi.Item.Title)))
	log.Debugf("Attempting to download torrent file: %s", filePath)

	contents, err := fi.getTorrentContents(cacheDir)
	if err != nil {
		return "", err
	}

	if err = os.WriteFile(filePath, contents, 0644); err != nil { // nolint:gosec
		return "", fmt.Errorf("unable to write %s: %s", filePath, err.Error())
	}

	log.Infof("Downloading: %s", filePath)
	return filePath, nil
}

// TorrentWithBytes submits a torrent to Transmission using pre-fetched bytes
// (MetaInfo upload). The caller is responsible for recording the item in the
// cache.
func (fi *FeedItem) TorrentWithBytes(ctx *RunContext, dir string, data []byte) error {
	log.Debugf("Attempting to torrent: %s", fi.Item.Title)

	if len(data) == 0 {
		return fmt.Errorf("no torrent data available for %s", fi.Item.Title)
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	addPayload := transmissionrpc.TorrentAddPayload{
		DownloadDir: &dir,
		MetaInfo:    &encoded,
	}
	if _, err := ctx.Transmission.TorrentAdd(context.TODO(), addPayload); err != nil {
		if strings.Contains(err.Error(), "duplicate torrent") {
			log.Warnf("Skipping duplicate torrent: %s", fi.Item.Title)
			return nil
		}
		return err
	}

	log.Infof("Torrenting: %s", fi.Item.Title)
	return nil
}

func (fi *FeedItem) IsComplete() bool {
	return fi.Complete
}

func (m *Feed) compile() {
	if m.compiled {
		return
	}

	var err error
	var r *regexp.Regexp

	for _, exclude := range m.Exclude {
		if r, err = regexp.Compile(exclude); err != nil {
			log.WithError(err).Fatalf("Unable to compile Exclude: %s", exclude)
		}
		m.exclude = append(m.exclude, r)
	}

	if m.MaxSize != "" {
		size, err := bytesize.Parse(m.MaxSize)
		if err != nil {
			log.WithError(err).Fatalf("Unable to parse MaxSize: %s", m.MaxSize)
		}
		m.maxSize = uint64(size)
	}

	if m.MinSize != "" {
		size, err := bytesize.Parse(m.MinSize)
		if err != nil {
			log.WithError(err).Fatalf("Unable to parse MinSize: %s", m.MinSize)
		}
		m.minSize = uint64(size)
	}

	m.compiled = true
}
