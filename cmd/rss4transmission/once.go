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
	"fmt"
	"os"

	// "github.com/hekmon/transmissionrpc/v2"
	"github.com/manifoldco/promptui"
	"github.com/mmcdole/gofeed"
)

type FeedCache map[string]*gofeed.Feed

type OnceCmd struct {
	Feed         []string `kong:"help='Limit scraping to the given feed(s)'"`
	Download     bool     `kong:"short='d',help='Download torrent file instead of torrenting',xor='action'"`
	DownloadPath string   `kong:"short='p',help='Path to download torrent files to ($PWD)'"`
	Interactive  bool     `kong:"short='i',help='Interactive mode',xor='action'"`
	NoAction     bool     `kong:"short='n',help='Just print results and take no action',xor='action'"`
}

func (cmd *OnceCmd) Run(ctx *RunContext) error {
	var err error
	var filePath string

	log.Debugf("Starting our run...")
	if ctx.Cli.Once.DownloadPath == "" {
		ctx.Cli.Once.DownloadPath = os.Getenv("PWD")
	}

	cache := FeedCache{}
	for name, feed := range ctx.Config.Feeds {
		log.Debugf("Processing %s: %v", name, feed)
		if _, ok := cache[feed.URL]; !ok {
			p := gofeed.NewParser()
			if cache[feed.URL], err = p.ParseURL(feed.URL); err != nil {
				log.WithError(err).Warnf("Unable to process URL: %s", feed.URL)
				continue
			}
		}

		for _, item := range feed.NewItems(cache[feed.URL]) {
			if ctx.Cli.Once.NoAction {
				log.Infof("%s match: %s", name, item.Item.Title)
				continue
			} else if ctx.Cli.Once.Download {
				if filePath, err = item.Download(ctx.Cli.Once.DownloadPath); err != nil {
					log.WithError(err).Errorf("Unable to download: %s", name)
					continue
				}
				log.Infof("Downloaded: %s", filePath)
			} else if ctx.Cli.Once.Interactive {
				switch prompt(name, item.Item.Title) {
				case Download:
					if filePath, err = item.Download(ctx.Cli.Once.DownloadPath); err != nil {
						log.WithError(err).Errorf("Unable to download: %s", name)
						continue
					}
					log.Infof("Downloading: %s", filePath)

				case Torrent:
					if err = item.Torrent(ctx.Transmission, feed.DownloadPath); err != nil {
						log.WithError(err).Errorf("Unable to torrent: %s", name)
						continue
					}
					log.Infof("Torrenting: %s", item.Item.Title)

				case Skip:
					ctx.Cache.AddItem(item)
					continue
				case SkipOnce:
					continue // don't add to the cache
				default:
					log.Errorf("Unknown reply")
				}
			} else {
				if err = item.Torrent(ctx.Transmission, feed.DownloadPath); err != nil {
					log.WithError(err).Errorf("Unable to torrent: %s", name)
					continue
				}
				log.Infof("Torrenting: %s", item.Item.Title)
			}

			// add to the cache
			ctx.Cache.AddItem(item)
		}
	}

	return nil
}

func download(ctx *RunContext, item *gofeed.Item) error {

	return nil
}

func torrent(ctx *RunContext, item *gofeed.Item) error {

	return nil
}

func yesNoPos(val bool) int {
	if val {
		return 1
	}
	return 0
}

type selectOptions struct {
	Name  string
	Value SelectType
}

type SelectType int

const (
	Skip SelectType = iota
	SkipOnce
	Download
	Torrent
)

var selectItems = []selectOptions{
	{
		Name:  "Skip",
		Value: Skip,
	},
	{
		Name:  "Skip Once",
		Value: SkipOnce,
	},
	{
		Name:  "Download",
		Value: Download,
	},
	{
		Name:  "Torrent",
		Value: Torrent,
	},
}

func makeSelectTemplate(label string) *promptui.SelectTemplates {
	return &promptui.SelectTemplates{
		Label:    "{{ . }}",
		Active:   promptui.IconSelect + " {{ .Name | cyan }}",
		Inactive: "  {{ .Name }}",
		Selected: promptui.IconGood + fmt.Sprintf(" %s {{ .Name }}", label),
	}
}

func prompt(feed, name string) SelectType {
	var i int
	var err error

	label := fmt.Sprintf("[%s] download %s?", feed, name)
	sel := promptui.Select{
		Label:        label,
		Items:        selectItems,
		HideSelected: false,
		Templates:    makeSelectTemplate(label),
	}

	if i, _, err = sel.Run(); err != nil {
		log.WithError(err).Fatalf("Unable to select option")
	}
	return selectItems[i].Value
}
