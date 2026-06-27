package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

type SimulateCmd struct {
	Feed     string `kong:"required,help='Feed name from config whose settings to apply'"`
	FeedFile string `kong:"required,name='feed-file',help='Local RSS/Atom file to use instead of the feed URL',type='existingfile'"`
	Batch    int    `kong:"default=10,help='Number of feed items to process per simulated run'"`
	Dir      string `kong:"default='.',name='torrent-dir',help='Directory to write .torrent files'"`
}

func (cmd *SimulateCmd) Run(ctx *RunContext) error {
	feedCfg, ok := ctx.Config.Feeds[cmd.Feed]
	if !ok {
		return fmt.Errorf("feed %q not found in config", cmd.Feed)
	}
	if feedCfg.Extractor == "" {
		return fmt.Errorf("feed %q has no Extractor configured", cmd.Feed)
	}
	extractor, ok := ctx.Config.Extractors[feedCfg.Extractor]
	if !ok {
		return fmt.Errorf("feed %q references unknown Extractor %q", cmd.Feed, feedCfg.Extractor)
	}

	f, err := os.Open(cmd.FeedFile)
	if err != nil {
		return fmt.Errorf("unable to open feed file: %w", err)
	}
	defer f.Close() //nolint:errcheck

	p := gofeed.NewParser()
	rss, err := p.Parse(f)
	if err != nil {
		return fmt.Errorf("unable to parse feed file: %w", err)
	}

	batches := splitBatches(rss.Items, cmd.Batch)
	totalBatches := len(batches)

	for i, batch := range batches {
		log.Infof("Batch %d/%d: processing %d items", i+1, totalBatches, len(batch))

		// Phase 1: pre-filter + title label extraction.
		var candidates []*candidate
		for _, item := range batch {
			fi := &FeedItem{Feed: cmd.Feed, Item: item}
			if ok, _ := feedCfg.Check(item); !ok {
				continue
			}
			candidates = append(candidates, &candidate{
				item:        fi,
				titleLabels: extractor.ExtractLabels(item.Title),
				defaults:    extractor.Defaults(),
			})
		}

		// Phase 2: fetch .torrent bytes + file-level labels (best-effort).
		for _, c := range candidates {
			torrentBytes, fetchErr := c.item.getTorrentContents()
			if fetchErr != nil {
				log.WithError(fetchErr).Debugf("Unable to fetch torrent for %s", c.item.Item.Title)
				continue
			}
			c.torrentBytes = torrentBytes
			fileNames, parseErr := TorrentFileNames(torrentBytes)
			if parseErr != nil {
				log.WithError(parseErr).Debugf("Unable to parse torrent for %s", c.item.Item.Title)
				continue
			}
			c.fileLabels = extractor.ExtractFromFiles(fileNames)
		}

		// Phases 3+4: select winners, write all torrent files, log and cache winners.
		won := cmd.dispatchBatch(ctx, feedCfg, candidates)
		log.Infof("Batch %d/%d: %d winner(s)", i+1, totalBatches, won)
	}

	cacheTime := time.Duration(ctx.Konf.Int("SeenCacheDays")) * 24 * time.Hour
	if err = ctx.Cache.SaveCache(cacheTime); err != nil {
		return fmt.Errorf("unable to save seen cache: %w", err)
	}
	return nil
}

// dispatchBatch runs selectWinners over candidates, writes every candidate's
// .torrent file to cmd.Dir (if bytes are available and the file doesn't
// already exist), and caches winners. Returns the number of winners.
func (cmd *SimulateCmd) dispatchBatch(ctx *RunContext, feedCfg Feed, candidates []*candidate) int {
	winners, _ := selectWinners(candidates, feedCfg, ctx.Cache)
	winnerSet := make(map[*candidate]bool, len(winners))
	for _, w := range winners {
		winnerSet[w] = true
	}

	count := 0
	for _, c := range candidates {
		if len(c.torrentBytes) > 0 {
			dest := filepath.Join(cmd.Dir, sanitizeFilename(c.item.Item.Title)+".torrent")
			if _, err := os.Stat(dest); os.IsNotExist(err) {
				if err = os.WriteFile(dest, c.torrentBytes, 0644); err != nil { //nolint:gosec
					log.WithError(err).Warnf("Unable to write torrent file: %s", dest)
				}
			}
		}

		if winnerSet[c] {
			covs := c.coverages(feedCfg.Identity)
			keys := make([]string, len(covs))
			for j, cov := range covs {
				keys[j] = cov.identityKey
			}
			log.Infof("WINNER: %s labels=%v", c.item.Item.Title, c.titleLabels)
			ctx.Cache.AddItem(c.item, c.titleLabels, keys)
			count++
		} else {
			log.Debugf("skipped: %s", c.item.Item.Title)
		}
	}
	return count
}

// sanitizeFilename replaces characters that are illegal in filenames on common
// operating systems with underscores.
func sanitizeFilename(s string) string {
	return strings.NewReplacer(
		"/", "_", `\`, "_", ":", "_", "*", "_",
		"?", "_", `"`, "_", "<", "_", ">", "_", "|", "_",
	).Replace(s)
}

// splitBatches divides items into consecutive sub-slices of at most size
// elements. If size <= 0 it is treated as 1.
func splitBatches[T any](items []T, size int) [][]T {
	if size <= 0 {
		size = 1
	}
	var batches [][]T
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		batches = append(batches, items[i:end])
	}
	return batches
}
