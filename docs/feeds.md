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
  talkshow:
    Labels:
      edition:
        Regexp: '(?i)(US|UK|AU)\.Edition'
        Normalize:
          '(?i)us': 'US'
          '(?i)uk': 'UK'
          '(?i)au': 'AU'
      episode:
        Regexp: 'E(\d+)'
      segment:
        Regexp: '(?i)(FullEpisode|Recap|Preview)'
        Normalize:
          'Rec[^.]*': 'Recap'
      resolution:
        Regexp: '(\d{3,4}p)'
      network:
        Regexp: '(?i)\.(NBC|Sky|BT|AMZN)\.'
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
  - Name: TalkShowUS
    URL: https://rss.example.com/feed
    DownloadPath: /torrents/talkshow
    Exclude:
      - '.*Bloopers.*'
    Extractor: talkshow          # references an Extractor defined above
    Identity: [edition, episode, segment]   # uniquely identifies one broadcast
    Prefer:
      - label: resolution
        order: [1080p, 720p]   # 1080p wins over 720p; unlisted values rank lowest
      - label: network
        order: [NBC, Sky]      # tiebreaker if resolution is equal
    Groups:
      - Require:
          edition: [US]
      - Require:
          edition: [UK, AU]
```

**How it works:**

1. `Exclude` is applied to the raw title first.
2. `Groups` are evaluated independently. A candidate must satisfy all `Require` constraints in at
   least one group to proceed (each label in `Require` must match one of its listed canonical
   values).
3. Each passing candidate's `.torrent` file is fetched and its file names are extracted. Title
   labels and file labels are unioned.
4. Candidates sharing the same `Identity` key (e.g. `edition=US|episode=1|segment=FullEpisode`)
   compete. The winner is the highest-ranked candidate by the `Prefer` ordering not already
   bettered in the seen cache.
5. A multi-edition bundle (one torrent covering the US + UK + AU files together) is submitted once
   but recorded against all covered identity keys.

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
  talkshow:
    Labels:
      edition:
        Regexp: '(?i)(US|UK|AU)\.Edition'
        Normalize:
          '(?i)us': 'US'
          '(?i)uk': 'UK'
          '(?i)au': 'AU'
      episode:
        Regexp: 'E(\d+)'
      segment:
        Regexp: '(?i)(FullEpisode|Recap)'
        Normalize:
          'Rec[^.]*': 'Recap'
      resolution:
        Regexp: '(\d{3,4}p)'

Feeds:
  - Name: TalkShow2024
    URL: https://rss.example.com/feed
    DownloadPath: /torrents/talkshow
    Extractor: talkshow
    Identity: [edition, episode, segment]
    Prefer:
      - label: resolution
        order: [1080p, 720p]
    Groups:
      - Require:
          edition: [US, UK, AU]
```
