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

// resolutionRanks maps canonical resolution strings to comparable integers (higher = better).
var resolutionRanks = map[string]int{
	"540p":  1,
	"720p":  2,
	"1080p": 3,
	"2160p": 4,
}

// resolutionRank returns the rank of a resolution string (0 = unknown).
func resolutionRank(res string) int {
	return resolutionRanks[res]
}

// bundleSessions returns the set of individual sessions that a bundle session covers,
// taking the series into account (sprint_race only exists in MotoGP).
func bundleSessions(session, series string) []string {
	isMotoGP := series == "MotoGP"
	switch session {
	case "friday_all_day":
		return []string{"fp1", "free_practice"}
	case "saturday_all_day":
		if isMotoGP {
			return []string{"fp2", "qualifying", "sprint_race"}
		}
		return []string{"fp2", "qualifying"}
	case "sunday_all_day":
		return []string{"gp_race"}
	case "full_weekend":
		if isMotoGP {
			return []string{"fp1", "free_practice", "fp2", "qualifying", "sprint_race", "gp_race", "warm_up"}
		}
		return []string{"fp1", "free_practice", "fp2", "qualifying", "gp_race"}
	case "race_pack":
		return []string{"gp_race"}
	case "quali_pack":
		return []string{"qualifying"}
	// highlights and build_up have no individual sessions to gate on
	default:
		return nil
	}
}

// AISelect evaluates a normalized torrent against an AISelection config.
// Returns (true, "") if the item should be downloaded, or (false, reason) if rejected.
// The rawTitle and excludePatterns implement the Exclude escape hatch.
func AISelect(norm *NormalizedTorrent, cfg *AISelection, cache *CacheFile, rawTitle string, feed *Feed) (bool, string) {
	// 1. Series filter
	if len(cfg.Series) > 0 {
		found := false
		for _, s := range cfg.Series {
			if s == norm.Series {
				found = true
				break
			}
		}
		if !found {
			return false, "series not in wanted list"
		}
	}

	// 2. Session filter
	if len(cfg.Sessions) > 0 {
		found := false
		for _, s := range cfg.Sessions {
			if s == norm.Session {
				found = true
				break
			}
		}
		if !found {
			return false, "session not in wanted list"
		}
	}

	// 3. Language filter
	if len(cfg.Languages) > 0 {
		found := false
		for _, l := range cfg.Languages {
			if l == norm.Language {
				found = true
				break
			}
		}
		if !found {
			return false, "language not in wanted list"
		}
	}

	// 4. Resolution filter
	if cfg.MinResolution != "" {
		if resolutionRank(norm.Resolution) < resolutionRank(cfg.MinResolution) {
			return false, "resolution below minimum"
		}
	}

	// 5. Flag exclusion
	for _, ef := range cfg.ExcludeFlags {
		for _, f := range norm.Flags {
			if f == ef {
				return false, "excluded flag: " + ef
			}
		}
	}

	// 6. Exclude regexp escape hatch (applied after AI selection rules)
	if feed != nil {
		feed.compile()
		for _, r := range feed.exclude {
			if r.Find([]byte(rawTitle)) != nil {
				return false, "matched exclude pattern"
			}
		}
	}

	// 7. Already downloaded check + feed priority
	existing := cache.FindByKey(norm.Series, norm.Year, norm.Round, norm.Session)
	if existing != nil {
		// Something was already downloaded for this (series, year, round, session).
		// Skip if we already have this session at the same or higher priority.
		existingPriority := feedPriorityIndex(existing.SourceFeed, cfg.FeedPriority)
		thisPriority := feedPriorityIndex(norm.Feed, cfg.FeedPriority)
		if thisPriority >= existingPriority {
			// existing is same priority or better — skip
			return false, "already downloaded at same or higher priority"
		}
		// existing is lower priority — but we don't upgrade, so also skip
		return false, "already downloaded (no upgrades)"
	}

	// 8. Bundle gating
	covered := bundleSessions(norm.Session, norm.Series)
	for _, s := range covered {
		if cache.ExistsByKey(norm.Series, norm.Year, norm.Round, s) {
			return false, "bundle session already downloaded: " + s
		}
	}

	return true, ""
}

// feedPriorityIndex returns the index of feed in the priority list (0 = highest priority).
// Returns len(list) if the feed is not found (treats unknown feeds as lowest priority).
func feedPriorityIndex(feed string, priority []string) int {
	for i, f := range priority {
		if f == feed {
			return i
		}
	}
	return len(priority)
}
