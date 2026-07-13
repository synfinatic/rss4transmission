package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/mmcdole/gofeed"
)

// extractSize returns the byte length of the bittorrent enclosure, or 0 if
// no parseable length is found.
func extractSize(item *gofeed.Item) int64 {
	for _, enc := range item.Enclosures {
		if enc.Type == "application/x-bittorrent" && enc.Length != "" {
			if n, err := strconv.ParseInt(enc.Length, 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// sendNtfyStarted sends a "torrent started" notification to ntfy. The cancel
// action button is only included when cancel routes are registered (either via
// --private-listen alone or --public-listen) and all cancel config fields are
// set; otherwise a plain notification is sent.
func sendNtfyStarted(ctx *RunContext, feedCfg Feed, torrentID int64, meta CancelMetadata, item *gofeed.Item) {
	if feedCfg.NoNotify {
		return
	}
	if ctx.Config.Ntfy.BaseURL == "" || ctx.Config.Ntfy.Topic == "" {
		return
	}

	var cancelURL, cancelID string
	if ctx.CancelRoutesEnabled &&
		ctx.Config.Cancel.HMACSecret != "" &&
		ctx.Config.Cancel.BaseURL != "" &&
		ctx.CancelStore != nil &&
		torrentID != 0 {
		cancelID = newUUID()
		ttl := time.Duration(ctx.Config.Cancel.TokenTTLH) * time.Hour
		expires, sig := GenerateToken([]byte(ctx.Config.Cancel.HMACSecret), cancelID, ttl)
		cancelURL = fmt.Sprintf("%s/cancel?id=%s&expires=%d&sig=%s",
			strings.TrimRight(ctx.Config.Cancel.BaseURL, "/"), cancelID, expires, sig)
	}

	var guid, link string
	var published *time.Time
	if item != nil {
		guid = item.GUID
		link = item.Link
		published = item.PublishedParsed
	}
	ntfyCtx := &NtfyTemplateContext{
		Title:     meta.Title,
		FeedName:  meta.FeedName,
		Files:     meta.Files,
		Labels:    meta.Labels,
		SizeBytes: meta.SizeBytes,
		Size:      formatGB(meta.SizeBytes),
		GUID:      guid,
		Link:      link,
		Published: published,
		TorrentID: torrentID,
		CancelURL: cancelURL,
	}

	client := NewNtfyClient(ctx.Config.Ntfy)
	if err := client.SendTorrentStarted(ntfyCtx); err != nil {
		log.WithError(err).Warn("Failed to send ntfy notification")
		return
	}
	// Register only after the notification was delivered; if Send failed the user
	// never saw the cancel link and the store entry would be unreachable.
	if cancelID != "" {
		ctx.CancelStore.Register(cancelID, torrentID, meta)
	}
}

type Feeds map[string]*gofeed.Feed

type OnceCmd struct {
	Feed            []string `kong:"help='Limit scraping to the given feed(s)'"`
	Download        bool     `kong:"short='d',help='Download torrent file instead of torrenting',xor='action'"`
	DownloadPath    string   `kong:"short='p',help='Path to download torrent files to ($PWD)'"`
	Interactive     bool     `kong:"short='i',help='Interactive mode',xor='action'"`
	NoAction        bool     `kong:"short='n',help='Just print results and take no action',xor='action'"`
	Skip            bool     `kong:"short='s',help='Just skip any matching torrents',xor='action'"`
	TorrentCacheDir string   `kong:"help='Directory to cache fetched .torrent files across runs'"`
}

// candidate is a feed item that has passed pre-filtering, with its extracted labels.
type candidate struct {
	item         *FeedItem
	titleLabels  map[string]string
	fileLabels   []map[string]string // one set per file in the .torrent
	fileNames    []string            // raw file names from the .torrent, for metadata display
	torrentBytes []byte              // raw .torrent content for MetaInfo upload
	defaults     map[string]string   // label defaults from the extractor config
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
		labels = withDefaultLabels(labels, c.defaults)
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

// allLabels returns titleLabels merged with the labels of every file that
// forms a valid coverage for identityLabels (same scoping as coverages()),
// with extractor defaults filled in for anything still missing. A file that
// matches only a Prefer-dimension regex but not the identity regexes (a
// sample clip, an NFO, a stray extra) never wins selection and must not be
// allowed to overwrite a value that came from the file that did. This is the
// full label set used when recording a dispatched candidate in the seen
// cache, so Prefer dimensions that only appear in file names (e.g.
// resolution) aren't lost on future preference-rank comparisons.
func (c *candidate) allLabels(identityLabels []string) map[string]string {
	merged := make(map[string]string, len(c.titleLabels)+len(c.fileLabels))
	maps.Copy(merged, c.titleLabels)
	for _, fl := range c.fileLabels {
		combined := withDefaultLabels(MergeLabels(c.titleLabels, fl), c.defaults)
		if _, ok := IdentityKey(combined, identityLabels); !ok {
			continue
		}
		maps.Copy(merged, fl)
	}
	return withDefaultLabels(merged, c.defaults)
}

// feedAllowed reports whether feedName should be processed given the --feed filter.
func (cmd *OnceCmd) feedAllowed(feedName string) bool {
	return len(cmd.Feed) == 0 || slices.Contains(cmd.Feed, feedName)
}

// processFeed runs phases 1–4 for a single feed: build candidates, fetch
// torrent data, select winners, and dispatch. Returns true if the caller
// should stop processing further feeds this run — either a torrent was
// dispatched/downloaded, or the user selected Quit interactively.
func (cmd *OnceCmd) processFeed(ctx *RunContext, feedName string, feedCfg Feed, rss *gofeed.Feed, extractor *ExtractorSet) bool {
	// Phase 1: Pre-filter candidates via Exclude + size, then extract title labels.
	var candidates []*candidate
	for _, item := range rss.Items {
		fi := &FeedItem{Feed: feedName, Item: item}
		if ctx.Cache.Exists(feedName, fi) {
			continue
		}
		ok, reason := feedCfg.Check(item)
		if !ok {
			ctx.recordHistory(feedName, item, "excluded", reason, nil)
			continue
		}
		candidates = append(candidates, &candidate{
			item:        fi,
			titleLabels: extractor.ExtractLabels(item.Title),
			defaults:    extractor.Defaults(),
		})
	}

	// Phase 2: Fetch .torrent for each candidate; extract file-level labels.
	for _, c := range candidates {
		start := time.Now()
		torrentBytes, err := c.item.getTorrentContents(cmd.TorrentCacheDir)
		if err != nil {
			log.WithError(err).Debugf("Unable to fetch torrent for %s, using title labels only", c.item.Item.Title)
			continue
		}
		log.Tracef("Fetched torrent for %s in %s", c.item.Item.Title, time.Since(start))
		c.torrentBytes = torrentBytes
		fileNames, err := TorrentFileNames(torrentBytes)
		if err != nil {
			log.WithError(err).Debugf("Unable to parse torrent files for %s", c.item.Item.Title)
			continue
		}
		c.fileNames = fileNames
		c.fileLabels = extractor.ExtractFromFiles(fileNames)
	}

	// Phase 3: Select highest-preference winner per identity key.
	winners, skipped := selectWinners(candidates, feedCfg, ctx.Cache)
	markCacheRejectedSeen(skipped, ctx.Cache)
	for _, s := range skipped {
		ctx.recordHistory(feedName, s.cand.item.Item, "skipped", s.reason, s.cand.titleLabels)
	}

	// Phase 4: Dispatch winners.
	for _, w := range winners {
		covs := w.coverages(feedCfg.Identity)
		keys := make([]string, len(covs))
		for i, cov := range covs {
			keys[i] = cov.identityKey
		}
		if cmd.dispatch(ctx, feedCfg, feedName, w, keys) {
			return true
		}
	}
	return false
}

// collectActiveGUIDs builds a map of feed name → set of GUIDs currently
// present in the fetched RSS feeds. Every configured feed gets an entry: a
// populated set if its RSS was fetched this run, or an explicit nil if it
// wasn't (early stop, --feed filter, or a fetch error) — the nil lets
// SaveCache tell "not checked this run" apart from "no longer configured".
func collectActiveGUIDs(feeds Feeds, cfgs []Feed) map[string]map[string]bool {
	active := make(map[string]map[string]bool, len(cfgs))
	for _, feedCfg := range cfgs {
		rss := feeds[feedCfg.URL]
		if rss == nil {
			active[feedCfg.Name] = nil
			continue
		}
		m := make(map[string]bool, len(rss.Items))
		for _, item := range rss.Items {
			m[item.GUID] = true
		}
		active[feedCfg.Name] = m
	}
	return active
}

func (cmd *OnceCmd) Run(ctx *RunContext) error {
	var err error

	log.Debugf("Starting.  Download: %v, DownloadPath: %s, Interactive: %v, NoAction: %v, Skip: %v, TorrentCacheDir: %s",
		cmd.Download, cmd.DownloadPath, cmd.Interactive, cmd.NoAction, cmd.Skip, cmd.TorrentCacheDir,
	)
	if cmd.DownloadPath == "" {
		cmd.DownloadPath = os.Getenv("PWD")
	}

	// Cache gofeed results per URL so each RSS endpoint is fetched only once.
	feeds := Feeds{}

	for _, feedCfg := range ctx.Config.Feeds {
		if !cmd.feedAllowed(feedCfg.Name) {
			continue
		}
		if feedCfg.Extractor == "" {
			log.Warnf("Feed %q has no Extractor configured, skipping", feedCfg.Name)
			continue
		}
		extractor, ok := ctx.Config.Extractors[feedCfg.Extractor]
		if !ok {
			log.Errorf("Feed %q references unknown Extractor %q, skipping", feedCfg.Name, feedCfg.Extractor)
			continue
		}

		// Fetch RSS (cached by URL).
		if _, ok := feeds[feedCfg.URL]; !ok {
			p := gofeed.NewParser()
			start := time.Now()
			if feeds[feedCfg.URL], err = p.ParseURL(feedCfg.URL); err != nil {
				log.WithError(err).Warnf("Unable to process URL: %s", feedCfg.URL)
				feeds[feedCfg.URL] = nil
			}
			log.Tracef("Fetched RSS %s in %s", feedCfg.URL, time.Since(start))
		}
		if feeds[feedCfg.URL] == nil {
			continue
		}

		if cmd.processFeed(ctx, feedCfg.Name, feedCfg, feeds[feedCfg.URL], extractor) {
			break
		}
	}

	activeGUIDs := collectActiveGUIDs(feeds, ctx.Config.Feeds)

	cacheTime := time.Duration(ctx.Konf.Int("SeenCacheDays")) * time.Duration(24) * time.Hour
	if err = ctx.Cache.SaveCache(cacheTime, activeGUIDs); err != nil {
		return fmt.Errorf("unable to save seen cache: %s", err.Error())
	}
	if cmd.TorrentCacheDir != "" {
		pruneTorrentCache(cmd.TorrentCacheDir, cacheTime)
	}
	if ctx.History != nil {
		if err = ctx.History.SaveHistory(cacheTime); err != nil {
			log.WithError(err).Warn("Unable to save history file")
		}
	}
	return nil
}

// dispatch handles a single winner: submits it and records it in the cache.
// Returns true if processing should stop for the rest of this run — either
// because the item was actually dispatched (torrented or downloaded), or the
// user selected Quit in interactive mode. NoAction, Skip, and error paths
// never stop processing, since nothing was produced.
func (cmd *OnceCmd) dispatch(ctx *RunContext, feedCfg Feed, feedName string, w *candidate, keys []string) bool {
	var err error
	labels := w.allLabels(feedCfg.Identity)

	if cmd.NoAction {
		log.Infof("%s match: %s", feedName, w.item.Item.Title)
		ctx.recordHistory(feedName, w.item.Item, "skipped", "no-action mode", labels)
		return false
	}
	if cmd.Skip {
		ctx.Cache.AddItem(w.item, labels, keys)
		ctx.recordHistory(feedName, w.item.Item, "skipped", "user skip", labels)
		return false
	}
	if cmd.Interactive {
		return cmd.dispatchInteractive(ctx, feedCfg, feedName, w, keys)
	}
	// Default: torrent or download.
	if cmd.Download {
		if _, err = w.item.Download(ctx, cmd.DownloadPath, cmd.TorrentCacheDir); err != nil {
			log.WithError(err).Errorf("Unable to download: %s", w.item.Item.Title)
			ctx.recordHistory(feedName, w.item.Item, "error", err.Error(), labels)
			return false
		}
		ctx.recordHistory(feedName, w.item.Item, "downloaded", "", labels)
	} else {
		torrentBytes, err := ensureTorrentBytes(w.item, cmd.TorrentCacheDir, w.torrentBytes)
		if err != nil {
			log.WithError(err).Errorf("Unable to fetch torrent data for %s", w.item.Item.Title)
			ctx.recordHistory(feedName, w.item.Item, "error", err.Error(), labels)
			return false
		}
		torrentID, err := w.item.TorrentWithBytes(ctx, feedCfg.DownloadPath, torrentBytes)
		if err != nil {
			log.WithError(err).Errorf("Unable to torrent: %s", feedName)
			ctx.recordHistory(feedName, w.item.Item, "error", err.Error(), labels)
			return false
		}
		meta := CancelMetadata{
			Title:     w.item.Item.Title,
			FeedName:  feedName,
			Labels:    labels,
			Files:     w.fileNames,
			SizeBytes: extractSize(w.item.Item),
		}
		sendNtfyStarted(ctx, feedCfg, torrentID, meta, w.item.Item)
		ctx.recordHistory(feedName, w.item.Item, "dispatched", "", labels)
	}
	ctx.Cache.AddItem(w.item, labels, keys)
	return true
}

// dispatchInteractive prompts the user for what to do with a winner. Returns
// true if processing should stop for the rest of this run — either because
// the item was actually dispatched (torrented or downloaded), or the user
// selected Quit.
func (cmd *OnceCmd) dispatchInteractive(ctx *RunContext, feedCfg Feed, feedName string, w *candidate, keys []string) bool {
	var err error
	labels := w.allLabels(feedCfg.Identity)

	switch prompt(feedName, w.item.Item.Title) {
	case Download:
		if _, err = w.item.Download(ctx, cmd.DownloadPath, cmd.TorrentCacheDir); err != nil {
			log.WithError(err).Errorf("Unable to download: %s", w.item.Item.Title)
			ctx.recordHistory(feedName, w.item.Item, "error", err.Error(), labels)
			return false
		}
		ctx.Cache.AddItem(w.item, labels, keys)
		ctx.recordHistory(feedName, w.item.Item, "downloaded", "", labels)
		return true
	case Torrent:
		torrentID, err := w.item.TorrentWithBytes(ctx, feedCfg.DownloadPath, w.torrentBytes)
		if err != nil {
			log.WithError(err).Errorf("Unable to torrent: %s", feedName)
			ctx.recordHistory(feedName, w.item.Item, "error", err.Error(), labels)
			return false
		}
		meta := CancelMetadata{
			Title:     w.item.Item.Title,
			FeedName:  feedName,
			Labels:    labels,
			Files:     w.fileNames,
			SizeBytes: extractSize(w.item.Item),
		}
		sendNtfyStarted(ctx, feedCfg, torrentID, meta, w.item.Item)
		ctx.Cache.AddItem(w.item, labels, keys)
		ctx.recordHistory(feedName, w.item.Item, "dispatched", "", labels)
		return true
	case Skip:
		ctx.Cache.AddItem(w.item, labels, keys)
		ctx.recordHistory(feedName, w.item.Item, "skipped", "user skip", labels)
	case SkipOnce:
		// don't add to cache
	case Quit:
		return true
	default:
		log.Errorf("Unknown reply")
	}
	return false
}

// ensureTorrentBytes returns existing if non-empty; otherwise fetches via getTorrentContents.
// This prevents dispatch failures when Phase 2 couldn't fetch the torrent at selection time.
func ensureTorrentBytes(item *FeedItem, cacheDir string, existing []byte) ([]byte, error) {
	if len(existing) > 0 {
		return existing, nil
	}
	return item.getTorrentContents(cacheDir)
}

// skipReasonCacheBetter is the reason string used when a candidate is rejected
// because the cache already has an equal or better version for all its identity
// keys. Referenced in markCacheRejectedSeen to avoid string duplication.
const skipReasonCacheBetter = "better version already in cache"

// markCacheRejectedSeen adds the GUID of each cache-rejected candidate to the
// seen cache. On the next run, Exists() catches these GUIDs in Phase 1, before
// the torrent disk read, eliminating redundant I/O for items that will never
// be dispatched again.
func markCacheRejectedSeen(skipped []skippedCandidate, cache *CacheFile) {
	for _, s := range skipped {
		if s.reason == skipReasonCacheBetter {
			cache.AddSkippedItem(s.cand.item)
		}
	}
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
		covs := c.coverages(feedCfg.Identity)
		for _, g := range feedCfg.Groups {
			for _, cov := range covs {
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
				skipReasons[e.cand] = skipReasonCacheBetter
			}
			continue
		}
		if !seen[e.cand] {
			winners = append(winners, e.cand)
			seen[e.cand] = true
			delete(skipReasons, e.cand)
		}
	}

	// Refine "no group matched labels": if the candidate's title-only identity key
	// matches a dispatched winner's, the real reason is that the same content is
	// already covered by that winner.
	titleOnlyKey := func(c *candidate) (string, bool) {
		return IdentityKey(withDefaultLabels(c.titleLabels, c.defaults), feedCfg.Identity)
	}
	titleKeyWinner := make(map[string]string)
	for _, w := range winners {
		if key, ok := titleOnlyKey(w); ok {
			titleKeyWinner[key] = w.item.Item.Title
		}
	}
	for c, reason := range skipReasons {
		if reason != "no group matched labels" {
			continue
		}
		if key, ok := titleOnlyKey(c); ok {
			if winnerTitle, found := titleKeyWinner[key]; found {
				skipReasons[c] = "covered by winner: " + winnerTitle
			}
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

// pruneTorrentCache removes .torrent files in cacheDir that are older than maxAge.
func pruneTorrentCache(cacheDir string, maxAge time.Duration) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		log.WithError(err).Warnf("Unable to read torrent cache dir: %s", cacheDir)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".torrent" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > maxAge {
			path := filepath.Join(cacheDir, entry.Name())
			if err := os.Remove(path); err != nil {
				log.WithError(err).Warnf("Unable to remove stale torrent cache file: %s", path)
			} else {
				log.Debugf("Pruned stale torrent cache file: %s", path)
			}
		}
	}
}
