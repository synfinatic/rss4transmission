# RSS4Transmission

[![Tests](https://github.com/synfinatic/rss4transmission/actions/workflows/tests.yml/badge.svg)](https://github.com/synfinatic/rss4transmission/actions/workflows/tests.yml)
[![golangci-lint](https://github.com/synfinatic/rss4transmission/actions/workflows/golangci-lint.yml/badge.svg)](https://github.com/synfinatic/rss4transmission/actions/workflows/golangci-lint.yml)

## About

RSS4Transmission is a tool for fetching torrents over RSS for [Transmission](
https://transmissionbt.com).

## Why?

There are already a few tools that do this... most notably [rss-transmission](
https://github.com/nning/transmission-rss) is the closest, and I frankly stole a lot of concepts
from it.

The biggest difference is that RSS4Transmission is designed for OCD people who pull down a lot of
different files from the same feed and want them saved to different directories. I wanted something
that would be "nice" and only read the RSS feed once, even though I've got 10 different categories.

RSS4Transmission also supports a **label-based selection system** that can extract structured metadata
(series, round, session, resolution, etc.) from torrent titles and file names, deduplicate items by
identity key, and prefer higher-quality versions automatically.

## Images

Pre-built images are available on [DockerHub](https://hub.docker.com/r/synfinatic/rss4transmission).

## Gluetun Compatibility

RSS4Transmission supports integrating with [Gluetun](https://github.com/qdm12/gluetun).

When you launch Transmission, RSS4Transmission and Gluetun under
[docker-compose](docker-compose-gluetun.yaml), RSS4Transmission will take care of restarting the VPN
and updating Transmission with the appropriate [peer port](
https://github.com/transmission/transmission/blob/main/docs/Port-Forwarding-Guide.md)
information — assuming [Gluetun supports your VPN provider](
https://github.com/qdm12/gluetun-wiki/blob/main/setup/advanced/vpn-port-forwarding.md) for that.

Note that this functionality is currently experimental.

## Configuration

### Common Fields

All feeds support these optional pre-filters:

| Field | Description |
|---|---|
| `URL` | RSS feed URL (required) |
| `DownloadPath` | Destination directory for added torrents |
| `Exclude` | List of regexes — items whose title matches any are skipped |
| `MinSize` / `MaxSize` | Accept only items whose enclosure size is within this range (e.g. `100MB`, `10GB`) |
| `NoValidateCert` | Skip TLS certificate validation for this feed's URL |
| `NoSubmit` | Dry-run: log matches but do not send to Transmission |

### Label-Based Feed Configuration

The label system extracts structured metadata from torrent titles and file names, then uses that
metadata to deduplicate and rank candidates automatically.

#### Extractors

Define one or more named extractor sets at the top level. Each extractor set maps label names to a
single-capture regex and an optional normalize map:

```yaml
Extractors:
  motogp:
    Labels:
      series:
        Regexp: '(?i)(MotoGP|Moto2|Moto3)'
        Normalize:
          '(?i)motogp': 'MotoGP'
          '(?i)moto2': 'Moto2'
          '(?i)moto3': 'Moto3'
      round:
        Regexp: 'RD(\d+)'
      session:
        Regexp: '(?i)(Race|Qualifying|Sprint|Practice\d*)'
        Normalize:
          'Qual[^.]*': 'Qualifying'
      resolution:
        Regexp: '(\d{3,4}p)'
      network:
        Regexp: '(?i)\.(TNT|NBC|Sky|BT)\.'
```

- **Regexp**: must contain exactly one capture group — the value of that group becomes the label value.
- **Normalize**: keys are regexes matched against the raw extracted value; the first match wins and
  its value becomes the canonical label value. Useful for normalizing variant spellings.

Labels are extracted from both the RSS item title and the individual file names inside the `.torrent`.
Title labels and file labels are unioned before identity key computation.

#### Feeds in Label Mode

A feed enters label mode when `Extractor` is set:

```yaml
Feeds:
  MotoGP2024:
    URL: https://rss.example.com/feed
    DownloadPath: /torrents/motogp
    Exclude:
      - '.*Highlights.*'
    Extractor: motogp          # references an Extractor defined above
    Identity: [series, round, session]   # uniquely identifies one event
    Prefer:
      - label: resolution
        order: [1080p, 720p]   # 1080p wins over 720p; unlisted values rank lowest
      - label: network
        order: [TNT, NBC]      # tiebreaker if resolution is equal
    Groups:
      - Require:
          series: [MotoGP]
      - Require:
          series: [Moto2, Moto3]
```

**How it works:**

1. `Exclude` is applied to the raw title first.
2. `Groups` are evaluated independently. A candidate must satisfy all `Require` constraints in at
   least one group to proceed (each label in `Require` must match one of its listed canonical values).
3. Each passing candidate's `.torrent` file is fetched and its file names are extracted. Title labels
   and file labels are unioned.
4. Candidates sharing the same `Identity` key (e.g. `MotoGP|RD01|Race`) compete. The winner is the
   highest-ranked candidate by the `Prefer` ordering not already bettered in the seen cache.
5. A multi-class bundle (one torrent covering MotoGP + Moto2 + Moto3 files) is submitted once but
   recorded against all covered identity keys.

### Basic Configuration Example

```yaml
# Transmission connection — defaults shown
Transmission:
  Host:     localhost
  Port:     9091
  Username: admin
  Password: admin
  HTTPS:    false
  Path:     /transmission/rpc

# Seen-cache: tracks what has already been downloaded
SeenFile:      /path/to/seen.json
SeenCacheDays: 30  # prune records older than this many days

Extractors:
  motogp:
    Labels:
      series:
        Regexp: '(?i)(MotoGP|Moto2|Moto3)'
        Normalize:
          '(?i)motogp': 'MotoGP'
          '(?i)moto2': 'Moto2'
          '(?i)moto3': 'Moto3'
      round:
        Regexp: 'RD(\d+)'
      session:
        Regexp: '(?i)(Race|Qualifying|Sprint)'
        Normalize:
          'Qual[^.]*': 'Qualifying'
      resolution:
        Regexp: '(\d{3,4}p)'

Feeds:
  MotoGP2024:
    URL: https://rss.example.com/feed
    DownloadPath: /torrents/motogp
    Extractor: motogp
    Identity: [series, round, session]
    Prefer:
      - label: resolution
        order: [1080p, 720p]
    Groups:
      - Require:
          series: [MotoGP, Moto2, Moto3]
```

### Gluetun Configuration

Note that this config file works with [docker-compose-gluetun.yaml](docker-compose-gluetun.yaml).

```yaml
# When using Gluetun, Docker service networking changes the hostname
Transmission:
  Host:     gluetun
  Port:     9091
  Username: admin
  Password: admin

Gluetun:
  Host:             gluetun
  Port:             8000
  RotateTime:       12h
  ClosedPortChecks: 5
  # set AuthUsername + AuthPassword OR AuthAPIKey
  AuthUsername: Basic Auth Username
  AuthPassword: Basic Auth Password
  AuthAPIKey:   API Key

SeenFile: /path/to/seen.json

# your feeds go here...
```

## History Web UI

Use `--history-file` on the `watch` command to enable history recording. RSS4Transmission will record
the outcome of every feed item it processes (dispatched, downloaded, skipped, excluded, error).

Optionally pass `--history-listen` to serve a browsable history page. That flag accepts a bare port
number (binds to `127.0.0.1`) or a full `host:port` address (including IPv6 `[::1]:port`).

```bash
rss4transmission watch --config config.yaml --history-file /data/history.json --history-listen 8080
rss4transmission watch --config config.yaml --history-file /data/history.json \
    --history-listen 0.0.0.0:8080
```

In Docker, set the `HISTORY_FILE` and `HISTORY_LISTEN` environment variables:

```yaml
environment:
  - HISTORY_FILE=/config/history.json
  - HISTORY_LISTEN=8080          # binds to 127.0.0.1:8080
  # - HISTORY_LISTEN=0.0.0.0:8080  # bind to all interfaces
```

When using the plain `docker-compose.yaml` (`network_mode: host`) no additional port mapping is
needed. When using `docker-compose-gluetun.yaml`, add an explicit port mapping for the chosen port:

```yaml
ports:
  - "8080:8080"
```

The history page shows each item's feed name, title, publication date, outcome, and extracted labels.
Records are pruned on the same schedule as the seen cache (`SeenCacheDays`).

## Torrent File Cache

In watch mode, RSS4Transmission re-fetches every candidate's `.torrent` file on each run in order to
extract per-file labels from the torrent's file list. For pack torrents (one torrent containing
multiple sessions or classes), these downloads happen every few minutes even though the content never
changes.

Pass `--torrent-cache-dir` to both `once` and `watch` to cache `.torrent` files on disk:

```bash
rss4transmission watch --config config.yaml --torrent-cache-dir /data/torrent-cache
rss4transmission once  --config config.yaml --torrent-cache-dir /data/torrent-cache --no-action
```

On a cache hit the file is read from disk instead of re-fetched; on a miss the file is fetched and
then written to the cache. Cache files are named after the sanitized torrent title
(`<title>.torrent`) so the directory is human-inspectable.

Files older than `SeenCacheDays` are pruned automatically at the end of each run, keeping the cache
directory from growing unbounded.

In Docker, set the `TORRENT_CACHE_DIR` environment variable:

```yaml
environment:
  - TORRENT_CACHE_DIR=/config/torrent-cache
```

## License

RSS4Transmission is licensed under the [GPLv3](LICENSE).
