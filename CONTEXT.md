# rss4transmission Domain Glossary

## Extractor Set
A named, reusable collection of **Label** definitions. Referenced by feeds via the `Extractor` field.
Multiple feeds sharing the same RSS source format reference the same extractor set.

## Label
A named piece of structured metadata extracted from a torrent title or torrent file name by a
single-capture regex. Examples: `series`, `session`, `resolution`, `network`, `round`. Each label has:
- **Regexp**: a single-capture regex applied to the raw string
- **Normalize**: a map of `regex → canonical value` applied to the raw match after extraction

## Normalize Map
Post-extraction alias table under a label. Keys are regexes matched against the raw extracted value;
values are the canonical string to substitute. Allows variant spellings (`Qual[^.]+` → `Qualifying`)
without enumerating every variant in the extraction regex.

## Identity Key
The tuple of label values that uniquely identifies a sporting event within a feed. Declared per-feed
via the `Identity` field (e.g., `[series, round, session]`). Two torrents with the same identity key
represent the same event at different quality levels. A torrent that is missing any identity label is
skipped for that group.

## Preference Dimension
A label used to rank candidates for the same identity key. Declared per-feed via the `Prefer` list.
Comparison is lexicographic: the first dimension in the list has highest priority; later dimensions are
tiebreakers. A missing preference label ranks below all explicit values (treated as lowest preference).

## Group
A named set of `Require` constraints within a single feed config entry. A feed may have multiple groups
(e.g., one for MotoGP, one for Moto2/Moto3) sharing the same URL, extractor, identity key definition,
and preference ordering. Groups are evaluated independently — the group ordering in config has no
semantic significance.

## Require
A per-group filter declaring which canonical label values are acceptable. A candidate torrent must
produce a matching value for every `Require` label (after extraction and normalization) to be considered
for that group. Acts as a hard filter before preference comparison.

## Candidate
A feed item that has passed `Require` filtering on its title-extracted labels. Candidates have their
`.torrent` file fetched, file names extracted and labeled, and title + file labels unioned before full
identity key computation.

## Covered Identity Keys
The set of identity keys derivable from a torrent's title labels unioned with its per-file labels. A
multi-class bundle torrent (e.g., containing MotoGP, Moto2, and Moto3 races) covers multiple identity
keys simultaneously.

## Winner
The highest-preference candidate for a given identity key in a run, after comparing all candidates
against each other and against the cache. A torrent that is a winner for multiple identity keys is
submitted to Transmission once and recorded in the cache against all covered identity keys.

## Seen Cache
A JSON file recording every torrent submitted to Transmission. Each record stores the raw extracted
label map and the list of covered identity keys. On load, an in-memory **Identity Index** is rebuilt
from the seen list for O(1) preference lookups.

## Identity Index
An in-memory map from identity key → best preference labels downloaded so far. Rebuilt on cache load
from the seen list. Used to answer: "have I already downloaded a 1080p TNT version of
MotoGP/RD01/Qualifying?" Never persisted directly.
