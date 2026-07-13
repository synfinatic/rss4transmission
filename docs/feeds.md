# Feeds & Labels

This document covers how to configure feeds and the label-based selection system.

## Common Feed Fields

All feeds support these options:

| Field | Description |
|---|---|
| `Name` | Unique feed name (required). Feeds are processed in the order they're listed. |
| `URL` | RSS feed URL (required) |
| `DownloadPath` | Destination directory for torrents added to Transmission |
| `Exclude` | List of regexes — items whose title matches any are skipped before label extraction |
| `MinSize` / `MaxSize` | Accept only items within this size range (e.g. `100MB`, `10GB`) |
| `NoValidateCert` | Skip TLS certificate validation for this feed's URL |
| `NoSubmit` | Dry-run: log matches but do not send to Transmission |
| `NoNotify` | Skip ntfy notifications for this feed (see [Notifications](notifications.md)) |

`Feeds` is a list, so feeds are always processed in the order they appear in the config file. As
soon as one item is actually dispatched (submitted to Transmission or downloaded to disk with
`--download`), the current `once`/`watch` run stops immediately — remaining feeds and candidates
are picked up on the next run.

## Label-Based Feed Configuration

The label system extracts structured metadata from torrent titles and file names, then uses that
metadata to deduplicate and rank candidates automatically.

### Extractors

Define one or more named extractor sets at the top level. Each extractor set maps label names to
a single-capture regex and an optional normalize map:

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

- **Regexp**: must contain exactly one capture group — the value of that group becomes the label
  value.
- **Normalize**: keys are regexes matched against the raw extracted value; the first match wins
  and its value becomes the canonical label value. Useful for normalizing variant spellings.

Labels are extracted from both the RSS item title and the individual file names inside the
`.torrent`. Title labels and file labels are unioned before identity key computation.

### Feeds in Label Mode

A feed enters label mode when `Extractor` is set:

```yaml
Feeds:
  - Name: MotoGP2024
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
   least one group to proceed (each label in `Require` must match one of its listed canonical
   values).
3. Each passing candidate's `.torrent` file is fetched and its file names are extracted. Title
   labels and file labels are unioned.
4. Candidates sharing the same `Identity` key (e.g. `series=MotoGP|round=1|session=Race`) compete.
   The winner is the highest-ranked candidate by the `Prefer` ordering not already bettered in the
   seen cache.
5. A multi-class bundle (one torrent covering MotoGP + Moto2 + Moto3 files) is submitted once but
   recorded against all covered identity keys.

## Full Configuration Example

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
SeenFile:      /config/seen.json
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
  - Name: MotoGP2024
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
