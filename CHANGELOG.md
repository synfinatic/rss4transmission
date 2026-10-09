# Changelog

## v2.1.0

### Changes in behavior

- **Cancel now pauses the torrent.** The `/cancel` confirmation page used to remove the torrent
  from Transmission. It now pauses the torrent. The torrent and its data stay in Transmission, and
  a user can resume the download later. The page text and the access log result (`paused`, not
  `cancelled`) changed to match. If a script or log filter depends on the old text, update it.

### New features

**Config upload page**

- Added the `--config-upload` flag to the `watch` command (env `CONFIG_UPLOAD`). Its value is the
  login as `user:hash`, the output of `htpasswd -nbB`. It turns on a `/config` page on the private
  listener. The page takes a dropped `config.yaml`, validates it, applies it, and shows any error.
- The flag needs both `--private-listen` and `--public-listen`. The `watch` command refuses to
  start without them, so the page is never on the public port.
- HTTP Basic auth guards the page. The cleartext password is not stored. A `POST` from another
  origin is refused, and the upload size is limited.
- The program writes the new file with a rename and keeps the old file as `<config>.bak`. If the
  reload fails, the program restores the old file and reloads it again.
- In Docker Compose, write each `$` in the hash as `$$`. Do not quote the value. Both compose
  files show an example.

**Notifications and Alerts pages**

- Added the `/notifications` and `/alerts` pages. Each page frames the ntfy web page for one topic
  (`Ntfy.Topic` and `Ntfy.AlertTopic`). A page is on only when `Ntfy.BaseURL` and its own topic
  are set. The nav bar shows the page links.
- The browser loads the frame directly from the ntfy server. The ntfy server must not send
  `X-Frame-Options` or a CSP `frame-ancestors` header that blocks framing.

**Config warning**

- A feed that lists the same label in both `Identity` and `Prefer` now logs a warning at startup.
  The `Prefer` entry has no effect on that label, because all candidates for one identity key
  share its value.

### Bug fixes

- Fixed: a `.torrent` file that bundles files for a sibling class (for example a support race in a
  full-weekend pack) could overwrite the `class` label that won selection. The recorded and
  displayed labels were then wrong. A file now changes the labels only if it matches a feed
  `Groups` entry.

### Other changes

- The nav bar now shows above the page title, not below it.
- The config checks now live in one function that `loadConfig` and the upload page both use, so
  the two cannot differ.
- Docs: described the `MinSize` and `MaxSize` format, and linked the history file pruning to
  `SeenCacheDays`.
- Added the project logo to the README.
- Docker: the builder image is now `golang:1.27-alpine`.
- Updated dependencies: `koanf/v2` 2.3.7, `gofeed` 1.5.0, `logrus` 1.10.2,
  `speedtest-go` 1.8.3, and `testify` 1.12.1.

## v2.0.0

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

- Added `--history-file` flag to the `watch` command (env `HISTORY_FILE` in Docker). When set, every
  feed item outcome (dispatched, downloaded, skipped, excluded, error) is recorded with its feed name,
  title, labels, and timestamps. `HistoryFile` is no longer a config-file key.
- Added `--private-listen` flag to the `watch` command (env `PRIVATE_LISTEN` in Docker). Accepts a
  bare port number (binds to `127.0.0.1`) or a full `host:port` / `[ipv6]:port` address. When set,
  starts a private HTTP server serving a browsable, reverse-chronological history page. Records are
  pruned on the same schedule as the seen cache. Without `--history-file`, the history page
  answers 404.
- Added `--public-listen` flag to the `watch` command (env `PUBLIC_LISTEN` in Docker). It starts a
  separate public HTTP server for `/cancel`, `/start`, and `/healthz` only, so the history page
  stays on the private listener. Without it, the private listener also serves `/cancel` and
  `/start`.

**`simulate` command**

- New `simulate` subcommand runs the full feed-processing pipeline without submitting anything to
  Transmission. Useful for validating extractor and feed config against a live feed.

**Inline torrent parser**

- Added a pure-Go bencode decoder (`torrent.go`) to extract file names from `.torrent` files without
  any external dependency. Required by label extraction from file names.

**Live config reload**

- `watch` now applies the whole config file on save. A change to `Feeds`, `Extractors`,
  `Transmission`, `Gluetun`, `SpeedTest`, `PortCheck`, `Ntfy`, `Notifications`, `SeenFile`, or
  `SeenCacheDays` takes effect without a restart. Only the command line flags still need one.
- A moved Transmission origin rebuilds the RPC client. A moved `SeenFile` saves the current cache
  and opens the new path. A changed `SpeedTest`, `Ntfy`, or `Gluetun` block rebuilds the speed
  monitor.
- Web routes are registered once and read the live config per request, so `Transmission.WebUI`,
  the cancel and start endpoints, and `/notify-complete` turn on and off without a restart. A route
  that is turned off answers 404.
- Fixed: the cancel and start buttons stopped working after a change to `Notifications.HMACSecret`.
  The handlers verified with the secret read at startup, while the notifications were signed with
  the live secret. Both sides now read the live secret.
- Fixed: a config file with a bad `Exclude` regex, a bad `MinSize` or `MaxSize`, a bad extractor
  pattern, a bad `Gluetun.Rotate`, or an out-of-range `Transmission.Port` exited the running
  process. `loadConfig` now rejects the file and keeps the running config.

### Other changes

- Seen cache now tracks per-GUID error hold-downs to avoid spamming retries on transient failures.
- Docker: `PRIVATE_LISTEN` and `PUBLIC_LISTEN` env vars added to `Dockerfile` and both compose
  files (empty = disabled). The gluetun compose file includes a commented `ports:` block to expose
  the listener ports.
- Makefile: added `make coverage` (atomic coverage report) and `make vulncheck` (`govulncheck`);
  `vulncheck` is now part of `make precheck`.
