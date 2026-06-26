# Changelog

## v2.0.0 (unreleased)

### Breaking changes

- **`Regexp` and `Categories` feed fields removed.** Replace them with the new label-based system
  (`Extractor`, `Identity`, `Groups`, `Prefer`) described in the README.

### New features

**Label-based feed selection**

- Added top-level `Extractors` config block. Each extractor set maps label names to a single-capture
  regex and an optional `Normalize` map (regex → canonical value) for normalising variant spellings.
- Feeds now support `Extractor`, `Identity`, `Groups`, and `Prefer` fields.
  - `Identity` declares the tuple of label values that uniquely identifies one event (dedup key).
  - `Groups` declares per-group `Require` filters; a candidate must satisfy all constraints in at
    least one group.
  - `Prefer` declares ordered preference dimensions for ranking candidates with the same identity key.
- Labels are extracted from both the RSS item title and individual file names inside the `.torrent`
  file; title and file labels are unioned before identity key computation.
- Multi-class bundle torrents (one `.torrent` covering multiple identity keys) are submitted once and
  recorded against all covered keys in the seen cache.

**History file and web UI**

- Added `HistoryFile` config key. When set, every feed item outcome (dispatched, downloaded, skipped,
  excluded, error) is recorded with its feed name, title, labels, and timestamps.
- Added `--history-port` flag to the `watch` command (env `HISTORY_PORT` in Docker). When non-zero,
  starts an HTTP server on `127.0.0.1:<port>` serving a browsable, reverse-chronological history
  page. Records are pruned on the same schedule as the seen cache.

**`simulate` command**

- New `simulate` subcommand runs the full feed-processing pipeline without submitting anything to
  Transmission. Useful for validating extractor and feed config against a live feed.

**Inline torrent parser**

- Added a pure-Go bencode decoder (`torrent.go`) to extract file names from `.torrent` files without
  any external dependency. Required by label extraction from file names.

### Other changes

- Seen cache now tracks per-GUID error hold-downs to avoid spamming retries on transient failures.
- Docker: `HISTORY_PORT` env var (default `0`) added to `Dockerfile` and both compose files.
  The gluetun compose file includes a commented `ports:` block to expose the history UI.
- Makefile: added `make coverage` (atomic coverage report) and `make vulncheck` (`govulncheck`);
  `vulncheck` is now part of `make precheck`.
