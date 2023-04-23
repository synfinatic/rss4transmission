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
	"time"

	"github.com/manifoldco/promptui"
	"github.com/mmcdole/gofeed"
)

type Feeds map[string]*gofeed.Feed

type OnceCmd struct {
	Feed         []string `kong:"help='Limit scraping to the given feed(s)'"`
	Download     bool     `kong:"short='d',help='Download torrent file instead of torrenting',xor='action'"`
	DownloadPath string   `kong:"short='p',help='Path to download torrent files to ($PWD)'"`
	Interactive  bool     `kong:"short='i',help='Interactive mode',xor='action'"`
	NoAction     bool     `kong:"short='n',help='Just print results and take no action',xor='action'"`
	Skip         bool     `kong:"short='s',help='Just skip any matching torrents',xor='action'"`
}

func (cmd *OnceCmd) Run(ctx *RunContext) error {
	var err error
	var filePath string

	log.Debugf("Starting our run...")
	if ctx.Cli.Once.DownloadPath == "" {
		ctx.Cli.Once.DownloadPath = os.Getenv("PWD")
	}

	// we cache gofeed results for each URL so we can re-use the feed results without hitting
	// the RSS multiple times
	feeds := Feeds{}

	quit := false
	for name, feed := range ctx.Config.Feeds {
		if quit {
			break
		}

		log.Debugf("Processing %s: %v", name, feed)
		// have we already fetched this RSS feed?
		if _, ok := feeds[feed.URL]; !ok {
			p := gofeed.NewParser()
			if feeds[feed.URL], err = p.ParseURL(feed.URL); err != nil {
				log.WithError(err).Warnf("Unable to process URL: %s", feed.URL)
				continue
			}
		}

		for _, item := range feed.NewItems(name, feeds[feed.URL]) {
			if quit {
				break
			}
			if ctx.Cache.Exists(name, item) {
				log.Debugf("Skipping due to cache hit: %s", item.Item.Title)
				continue
			}

			if ctx.Cli.Once.NoAction {
				log.Infof("%s match: %s", name, item.Item.Title)
			} else if ctx.Cli.Once.Skip {
				// add to cache and do nothing
				ctx.Cache.AddItem(item)
			} else if ctx.Cli.Once.Download {
				if filePath, err = item.Download(ctx, ctx.Cli.Once.DownloadPath); err != nil {
					log.WithError(err).Errorf("Unable to download: %s", filePath)
					continue
				}
			} else if ctx.Cli.Once.Interactive {
				switch prompt(name, item.Item.Title) {
				case Download:
					if filePath, err = item.Download(ctx, ctx.Cli.Once.DownloadPath); err != nil {
						log.WithError(err).Errorf("Unable to download: %s", filePath)
						continue
					}

				case Torrent:
					if err = item.Torrent(ctx, feed.DownloadPath); err != nil {
						log.WithError(err).Errorf("Unable to torrent: %s", name)
						continue
					}

				case Skip:
					// add to cache and do nothing
					ctx.Cache.AddItem(item)
					continue

				case SkipOnce:
					continue // don't add to the cache

				case Quit:
					quit = true

				default:
					log.Errorf("Unknown reply")
				}
			} else {
				if err = item.Torrent(ctx, feed.DownloadPath); err != nil {
					log.WithError(err).Errorf("Unable to torrent: %s", name)
					continue
				}
			}
		}
	}

	cacheTime := time.Duration(ctx.Konf.Int("SeenCacheDays")) * time.Duration(24) * time.Hour
	if err = ctx.Cache.SaveCache(cacheTime); err != nil {
		return fmt.Errorf("Unable to save seen cache: %s", err.Error())
	}
	return nil
}

type selectOptions struct {
	Name  string
	Value SelectType
}

type SelectType int

const (
	Torrent SelectType = iota
	Download
	Skip
	SkipOnce
	Quit
)

var selectItems = []selectOptions{
	{
		Name:  "Torrent",
		Value: Torrent,
	},
	{
		Name:  "Download",
		Value: Download,
	},
	{
		Name:  "Skip",
		Value: Skip,
	},
	{
		Name:  "Skip Once",
		Value: SkipOnce,
	},
	{
		Name:  "Quit",
		Value: Quit,
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
		Stdout:       &BellSkipper{},
		HideSelected: false,
		Templates:    makeSelectTemplate(label),
	}

	if i, _, err = sel.Run(); err != nil {
		log.WithError(err).Fatalf("Unable to select option")
	}
	return selectItems[i].Value
}

/*
 * BellSkipper implements an io.WriteCloser that skips the terminal bell
 * character (ASCII code 7), and writes the rest to os.Stderr. It is used to
 * replace readline.Stdout, that is the package used by promptui to display the
 * prompts.
 *
 * This is a workaround for the bell issue documented in
 * https://github.com/manifoldco/promptui/issues/49#issuecomment-573814976
 */
type BellSkipper struct{}

// Write implements an io.WriterCloser over os.Stderr, but it skips the terminal
// bell character.
func (bs *BellSkipper) Write(b []byte) (int, error) {
	const charBell = 7 // c.f. readline.CharBell
	if len(b) == 1 && b[0] == charBell {
		return 0, nil
	}
	return os.Stderr.Write(b)
}

// Close implements an io.WriterCloser over os.Stderr.
func (bs *BellSkipper) Close() error {
	return os.Stderr.Close()
}
