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
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/mmcdole/gofeed"
)

// SimulateCmd replays a local RSS XML file through the configured feed filters,
// reporting which items would be downloaded without taking any action.
type SimulateCmd struct {
	File    string   `kong:"arg,required,help='RSS XML file to simulate'"`
	Feed    []string `kong:"help='Limit simulation to the given feed name(s) from config'"`
	NewItem int      `kong:"short='n',default='0',help='Items to reveal per simulated fetch (0=all at once)'"`
}

func (cmd *SimulateCmd) Run(ctx *RunContext) error {
	f, err := os.Open(cmd.File)
	if err != nil {
		return fmt.Errorf("open %s: %w", cmd.File, err)
	}
	defer f.Close()

	p := gofeed.NewParser()
	parsed, err := p.Parse(f)
	if err != nil {
		return fmt.Errorf("parse %s: %w", cmd.File, err)
	}

	items := sortItemsByDate(parsed.Items)
	batches := splitBatches(items, cmd.NewItem)

	// Determine once whether any in-scope feed uses AI selection.
	needsAI := ctx.Normalizer != nil
	if needsAI {
		needsAI = false
		for name, feed := range ctx.Config.Feeds {
			if cmd.inFeedFilter(name) && feed.AISelection != nil {
				needsAI = true
				break
			}
		}
	}

	for i, batch := range batches {
		if len(batches) > 1 {
			log.Infof("[Batch %d] %d item(s)", i+1, len(batch))
		}

		// Item-first loop: normalize each item exactly once, then evaluate all feeds.
		for _, rawItem := range batch {
			var norm *NormalizedTorrent
			if needsAI {
				n, normErr := ctx.Normalizer.Normalize(context.Background(), rawItem.Title)
				if normErr != nil {
					log.WithError(normErr).Warnf("Normalizer failed for %q, falling back to regexp", rawItem.Title)
				} else {
					norm = n
				}
			}

			for name, feed := range ctx.Config.Feeds {
				if !cmd.inFeedFilter(name) {
					continue
				}

				feedCopy := feed
				item := &FeedItem{Feed: name, Item: rawItem}

				if ctx.Cache.Exists(name, item) {
					log.Debugf("Skipping (cached): %s", rawItem.Title)
					continue
				}

				if feed.AISelection != nil && ctx.Normalizer != nil {
					if norm == nil {
						// normalization failed; fall back to regexp
						if !feedCopy.Check(rawItem) {
							continue
						}
					} else {
						ok, reason := AISelect(norm, feed.AISelection, ctx.Cache, rawItem.Title, &feedCopy)
						if !ok {
							log.Debugf("AI rejected %q for %s: %s", rawItem.Title, name, reason)
							continue
						}
					}
				} else {
					if !feedCopy.Check(rawItem) {
						continue
					}
				}

				log.Infof("simulate %s: %s", name, rawItem.Title)

				// Update in-memory cache so later batches and bundle gating work correctly.
				// Do NOT call SaveCache — simulate is read-only on disk.
				if norm != nil {
					ctx.Cache.AddNormalizedItem(item, norm)
				} else {
					ctx.Cache.AddItem(item)
				}
			}
		}
	}

	return nil
}

// inFeedFilter returns true if name should be processed given cmd.Feed.
func (cmd *SimulateCmd) inFeedFilter(name string) bool {
	if len(cmd.Feed) == 0 {
		return true
	}
	for _, f := range cmd.Feed {
		if f == name {
			return true
		}
	}
	return false
}

// sortItemsByDate returns a copy of items sorted oldest-first by PublishedParsed.
// Items with nil PublishedParsed sort before items with a real date.
func sortItemsByDate(items []*gofeed.Item) []*gofeed.Item {
	sorted := make([]*gofeed.Item, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		return itemPubTime(sorted[i]).Before(itemPubTime(sorted[j]))
	})
	return sorted
}

func itemPubTime(item *gofeed.Item) time.Time {
	if item.PublishedParsed != nil {
		return *item.PublishedParsed
	}
	return time.Time{}
}

// splitBatches partitions items into sub-slices of at most batchSize elements.
// If batchSize <= 0, all items are returned as a single batch.
func splitBatches(items []*gofeed.Item, batchSize int) [][]*gofeed.Item {
	if len(items) == 0 {
		return nil
	}
	if batchSize <= 0 {
		return [][]*gofeed.Item{items}
	}
	var batches [][]*gofeed.Item
	for start := 0; start < len(items); start += batchSize {
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		batches = append(batches, items[start:end])
	}
	return batches
}
