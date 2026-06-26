package main

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

// candidate is a feed item that has passed pre-filtering, with its extracted labels.
type candidate struct {
	item         *FeedItem
	titleLabels  map[string]string
	fileLabels   []map[string]string // one set per file in the .torrent
	torrentBytes []byte              // raw .torrent content for MetaInfo upload
}

// coverages returns the set of {identityKey, mergedLabels} pairs this candidate
// covers, given the feed's identity label names.
func (c *candidate) coverages(identityLabels []string) []coverage {
	var labelSets []map[string]string
	if len(c.fileLabels) == 0 {
		labelSets = []map[string]string{c.titleLabels}
	} else {
		for _, fl := range c.fileLabels {
			labelSets = append(labelSets, MergeLabels(c.titleLabels, fl))
		}
	}

	seen := map[string]bool{}
	var result []coverage
	for _, labels := range labelSets {
		key, ok := IdentityKey(labels, identityLabels)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, coverage{identityKey: key, labels: labels})
	}
	return result
}

type coverage struct {
	identityKey string
	labels      map[string]string
}

func (cmd *OnceCmd) Run(ctx *RunContext) error {
	var err error

	log.Debugf("Starting our run...")
	if ctx.Cli.Once.DownloadPath == "" {
		ctx.Cli.Once.DownloadPath = os.Getenv("PWD")
	}

	// Cache gofeed results per URL so each RSS endpoint is fetched only once.
	feeds := Feeds{}

	for feedName, feedCfg := range ctx.Config.Feeds {
		// Apply --feed filter if specified.
		if len(ctx.Cli.Once.Feed) > 0 {
			found := false
			for _, f := range ctx.Cli.Once.Feed {
				if f == feedName {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		if feedCfg.Extractor == "" {
			log.Warnf("Feed %q has no Extractor configured, skipping", feedName)
			continue
		}

		extractor, ok := ctx.Config.Extractors[feedCfg.Extractor]
		if !ok {
			log.Errorf("Feed %q references unknown Extractor %q, skipping", feedName, feedCfg.Extractor)
			continue
		}

		// Fetch RSS (cached by URL).
		if _, ok := feeds[feedCfg.URL]; !ok {
			p := gofeed.NewParser()
			if feeds[feedCfg.URL], err = p.ParseURL(feedCfg.URL); err != nil {
				log.WithError(err).Warnf("Unable to process URL: %s", feedCfg.URL)
				feeds[feedCfg.URL] = nil
			}
		}
		rss := feeds[feedCfg.URL]
		if rss == nil {
			continue
		}

		// Phase 1: Pre-filter candidates via Exclude + size, then extract title labels.
		var candidates []*candidate
		for _, item := range rss.Items {
			fi := &FeedItem{
				Feed:     feedName,
				Item:     item,
				Complete: false,
			}
			if ctx.Cache.Exists(feedName, fi) {
				continue // already dispatched; skip history recording
			}
			ok, reason := feedCfg.Check(item)
			if !ok {
				if ctx.History != nil {
					ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, item, "excluded", reason, nil))
				}
				continue
			}
			titleLabels := extractor.ExtractLabels(item.Title)
			candidates = append(candidates, &candidate{
				item:        fi,
				titleLabels: titleLabels,
			})
		}

		// Phase 2: Fetch .torrent for each candidate; extract file-level labels.
		for _, c := range candidates {
			torrentBytes, err := c.item.getTorrentContents()
			if err != nil {
				log.WithError(err).Debugf("Unable to fetch torrent for %s, using title labels only", c.item.Item.Title)
				continue
			}
			c.torrentBytes = torrentBytes
			fileNames, err := TorrentFileNames(torrentBytes)
			if err != nil {
				log.WithError(err).Debugf("Unable to parse torrent files for %s", c.item.Item.Title)
				continue
			}
			c.fileLabels = extractor.ExtractFromFiles(fileNames)
		}

		// Phase 3: For each group, select the highest-preference winner per identity key.
		winners, skipped := selectWinners(candidates, feedCfg, ctx.Cache)
		if ctx.History != nil {
			for _, s := range skipped {
				ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, s.cand.item.Item, "skipped", s.reason, s.cand.titleLabels))
			}
		}

		// Phase 4: Dispatch winners.
		quit := false
		for _, w := range winners {
			if quit {
				break
			}
			covs := w.coverages(feedCfg.Identity)
			keys := make([]string, len(covs))
			for i, cov := range covs {
				keys[i] = cov.identityKey
			}
			quit = cmd.dispatch(ctx, feedCfg, feedName, w, keys)
		}
		if quit {
			break
		}
	}

	cacheTime := time.Duration(ctx.Konf.Int("SeenCacheDays")) * time.Duration(24) * time.Hour
	if err = ctx.Cache.SaveCache(cacheTime); err != nil {
		return fmt.Errorf("unable to save seen cache: %s", err.Error())
	}
	if ctx.History != nil {
		if err = ctx.History.SaveHistory(cacheTime); err != nil {
			log.WithError(err).Warn("Unable to save history file")
		}
	}
	return nil
}

// dispatch handles a single winner: submits it and records it in the cache.
// Returns true if the user selected Quit in interactive mode.
func (cmd *OnceCmd) dispatch(ctx *RunContext, feedCfg Feed, feedName string, w *candidate, keys []string) bool {
	var err error

	if ctx.Cli.Once.NoAction {
		log.Infof("%s match: %s", feedName, w.item.Item.Title)
		if ctx.History != nil {
			ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "skipped", "no-action mode", w.titleLabels))
		}
		return false
	}
	if ctx.Cli.Once.Skip {
		ctx.Cache.AddItem(w.item, w.titleLabels, keys)
		if ctx.History != nil {
			ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "skipped", "user skip", w.titleLabels))
		}
		return false
	}
	if ctx.Cli.Once.Interactive {
		return cmd.dispatchInteractive(ctx, feedCfg, feedName, w, keys)
	}
	// Default: torrent or download.
	if ctx.Cli.Once.Download {
		if _, err = w.item.Download(ctx, ctx.Cli.Once.DownloadPath); err != nil {
			log.WithError(err).Errorf("Unable to download: %s", w.item.Item.Title)
			if ctx.History != nil {
				ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "error", err.Error(), w.titleLabels))
			}
			return false
		}
		if ctx.History != nil {
			ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "downloaded", "", w.titleLabels))
		}
	} else {
		if err = w.item.TorrentWithBytes(ctx, feedCfg.DownloadPath, w.torrentBytes); err != nil {
			log.WithError(err).Errorf("Unable to torrent: %s", feedName)
			if ctx.History != nil {
				ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "error", err.Error(), w.titleLabels))
			}
			return false
		}
		if ctx.History != nil {
			ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "dispatched", "", w.titleLabels))
		}
	}
	ctx.Cache.AddItem(w.item, w.titleLabels, keys)
	return false
}

// dispatchInteractive prompts the user for what to do with a winner.
// Returns true if the user selected Quit.
func (cmd *OnceCmd) dispatchInteractive(ctx *RunContext, feedCfg Feed, feedName string, w *candidate, keys []string) bool {
	var err error

	switch prompt(feedName, w.item.Item.Title) {
	case Download:
		if _, err = w.item.Download(ctx, ctx.Cli.Once.DownloadPath); err != nil {
			log.WithError(err).Errorf("Unable to download: %s", w.item.Item.Title)
			if ctx.History != nil {
				ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "error", err.Error(), w.titleLabels))
			}
			return false
		}
		ctx.Cache.AddItem(w.item, w.titleLabels, keys)
		if ctx.History != nil {
			ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "downloaded", "", w.titleLabels))
		}
	case Torrent:
		if err = w.item.TorrentWithBytes(ctx, feedCfg.DownloadPath, w.torrentBytes); err != nil {
			log.WithError(err).Errorf("Unable to torrent: %s", feedName)
			if ctx.History != nil {
				ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "error", err.Error(), w.titleLabels))
			}
			return false
		}
		ctx.Cache.AddItem(w.item, w.titleLabels, keys)
		if ctx.History != nil {
			ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "dispatched", "", w.titleLabels))
		}
	case Skip:
		ctx.Cache.AddItem(w.item, w.titleLabels, keys)
		if ctx.History != nil {
			ctx.History.AddOrUpdateRecord(NewHistoryRecord(feedName, w.item.Item, "skipped", "user skip", w.titleLabels))
		}
	case SkipOnce:
		// don't add to cache
	case Quit:
		return true
	default:
		log.Errorf("Unknown reply")
	}
	return false
}

type skippedCandidate struct {
	cand   *candidate
	reason string
}

// selectWinners returns winners and skipped candidates with reasons. For each
// identity key, the highest-preference candidate that beats the cache wins.
// A candidate winning on multiple keys appears once in winners.
func selectWinners(candidates []*candidate, feedCfg Feed, cache *CacheFile) ([]*candidate, []skippedCandidate) {
	type entry struct {
		cand *candidate
		rank []int
	}
	best := map[string]*entry{}
	matchedCands := map[*candidate]bool{}

	for _, c := range candidates {
		for _, g := range feedCfg.Groups {
			for _, cov := range c.coverages(feedCfg.Identity) {
				if !g.Matches(cov.labels) {
					continue
				}
				matchedCands[c] = true
				rank := PreferenceRank(cov.labels, feedCfg.Prefer)
				if e, ok := best[cov.identityKey]; !ok || IsBetter(rank, e.rank) {
					best[cov.identityKey] = &entry{cand: c, rank: rank}
				}
			}
		}
	}

	inBest := map[*candidate]bool{}
	for _, e := range best {
		inBest[e.cand] = true
	}

	skipReasons := map[*candidate]string{}
	for _, c := range candidates {
		if !matchedCands[c] {
			skipReasons[c] = "no group matched labels"
		} else if !inBest[c] {
			skipReasons[c] = "outranked by better candidate in this run"
		}
	}

	seen := map[*candidate]bool{}
	var winners []*candidate
	for key, e := range best {
		cachedRank, cached := cache.BestRankForKey(key, feedCfg.Prefer)
		if cached && !IsBetter(e.rank, cachedRank) {
			log.Debugf("Skipping %s for key %s: cache has equal or better preference", e.cand.item.Item.Title, key)
			if !seen[e.cand] {
				skipReasons[e.cand] = "better version already in cache"
			}
			continue
		}
		if !seen[e.cand] {
			winners = append(winners, e.cand)
			seen[e.cand] = true
			delete(skipReasons, e.cand)
		}
	}

	var skipped []skippedCandidate
	for _, c := range candidates {
		if reason, ok := skipReasons[c]; ok {
			skipped = append(skipped, skippedCandidate{cand: c, reason: reason})
		}
	}

	return winners, skipped
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
	{Name: "Torrent", Value: Torrent},
	{Name: "Download", Value: Download},
	{Name: "Skip", Value: Skip},
	{Name: "Skip Once", Value: SkipOnce},
	{Name: "Quit", Value: Quit},
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

func (bs *BellSkipper) Write(b []byte) (int, error) {
	const charBell = 7
	if len(b) == 1 && b[0] == charBell {
		return 0, nil
	}
	return os.Stderr.Write(b)
}

func (bs *BellSkipper) Close() error {
	return os.Stderr.Close()
}
