# Notifications & History

## Overview

RSS4Transmission supports two kinds of push notifications via [ntfy](https://ntfy.sh):

- **Torrent started** — sent by rss4transmission immediately after submitting a torrent to
  Transmission. Includes a **Cancel Download** action button that opens a browser confirmation
  page showing torrent details and live download progress. Confirming removes the torrent from
  Transmission.
- **Torrent completed** — sent via the `POST /notify-complete` endpoint, which is called by
  `bin/torrent-complete.sh` running as Transmission's "torrent done" hook. The endpoint renders
  your configured templates and sends the notification to ntfy.

## ntfy and Cancel Configuration

Add `Ntfy` and `Cancel` blocks to your config file:

```yaml
Ntfy:
  BaseURL: https://ntfy.sh              # your ntfy server
  Topic:   <your-topic-name>            # ntfy topic to publish to
  Token:   tk_<your-access-token>       # ntfy access token

Cancel:
  HMACSecret: <random-32-byte-hex>                   # generate: openssl rand -hex 32
  BaseURL:    https://rss4transmission.yourdomain.com # externally reachable URL
  TokenTTLH:  24                                     # cancel link TTL in hours (default: 24)
```

| Field | Default | Description |
|---|---|---|
| `Ntfy.BaseURL` | — | Base URL of the ntfy server |
| `Ntfy.Topic` | — | ntfy topic to publish to |
| `Ntfy.Token` | — | ntfy access token (`Authorization: Bearer`) |
| `Ntfy.StartedTitle` | `"Torrent Started"` | `text/template` string for the started notification title |
| `Ntfy.StartedBody` | `"{{.Title}}\n{{.Size}}"` | `text/template` string for the started notification body |
| `Ntfy.StartedPriority` | `default` | ntfy priority for started notifications |
| `Ntfy.CompletedTitle` | `"Torrent Complete"` | `text/template` string for the completed notification title |
| `Ntfy.CompletedBody` | `"{{.Title}}\n{{.Dir}}"` | `text/template` string for the completed notification body |
| `Ntfy.CompletedPriority` | `default` | ntfy priority for completed notifications |
| `Cancel.HMACSecret` | — | Secret key for signing cancel URLs (HMAC-SHA256) |
| `Cancel.BaseURL` | — | Public base URL of rss4transmission (used in cancel links) |
| `Cancel.TokenTTLH` | `24` | Hours before a cancel link expires |

Cancel links are omitted from notifications when `Cancel.HMACSecret` or `Cancel.BaseURL` is not
configured — the torrent started notification is still sent, just without the cancel action.
Ntfy notifications are entirely disabled when `Ntfy.BaseURL` is not set.

## Notification Templates

The `StartedTitle`, `StartedBody`, `CompletedTitle`, and `CompletedBody` fields accept
[Go `text/template`](https://pkg.go.dev/text/template) strings. The following context variables
are available:

| Field | Type | Description | Notes |
|---|---|---|---|
| `{{.Title}}` | `string` | Torrent/RSS item title | |
| `{{.FeedName}}` | `string` | Name of the feed | Empty for completions |
| `{{.Dir}}` | `string` | Download directory | Populated for completions (`TR_TORRENT_DIR`) |
| `{{.Files}}` | `[]string` | List of file names in the torrent | Empty for completions |
| `{{.Labels}}` | `map[string]string` | Extracted labels (e.g. resolution, language) | Empty for completions |
| `{{.SizeBytes}}` | `int64` | Raw size in bytes | `0` when unknown |
| `{{.Size}}` | `string` | Human-readable size (e.g. `"4.32 GB"`) | `"Unknown"` when size is 0 |
| `{{.GUID}}` | `string` | RSS item GUID | Empty for completions |
| `{{.Link}}` | `string` | RSS item URL (web page) | Empty for completions |
| `{{.Published}}` | `*time.Time` | RSS item publication time | May be `nil`; guard with `{{if .Published}}` |
| `{{.TorrentID}}` | `int64` | Transmission torrent ID | |
| `{{.CancelURL}}` | `string` | Signed cancel link | Empty when cancel is not configured |

Valid `Priority` values: `min`, `low`, `default`, `high`, `max`.

### Template examples

Show feed name in the started notification title:

```yaml
Ntfy:
  StartedTitle: "{{.FeedName}}: {{.Title}}"
```

Show labels in the body:

```yaml
Ntfy:
  StartedBody: |
    {{.Title}}
    {{.Size}} — {{index .Labels "resolution"}} {{index .Labels "language"}}
```

Conditionally show publication date:

```yaml
Ntfy:
  StartedBody: |
    {{.Title}}
    {{- if .Published}} ({{.Published.Format "2006-01-02"}}){{end}}
    {{.Size}}
```

Use high priority for started notifications and low for completed:

```yaml
Ntfy:
  StartedPriority:   high
  CompletedPriority: low
```

> **Note:** Multiline template bodies in YAML require a block scalar (`|`). In a
> double-quoted YAML string, use `\n` for a literal newline:
> `StartedBody: "{{.Title}}\n{{.Size}}"`.

## Cancel Endpoint

The `/cancel` endpoint serves a confirmation page where the user can review torrent details and
live download progress before removing the torrent from Transmission. It must be reachable from
the internet so ntfy can open it when the user taps Cancel.

There are two deployment models.

**Model 1 — Traefik (or other reverse proxy)**

Use `--history-listen` to start a single web server and let Traefik route only `/cancel` and
`/healthz` externally:

```bash
rss4transmission watch --config config.yaml --history-listen :8080
```

The [docker-compose.yaml](../docker-compose.yaml) example defaults to this model. Its Traefik
labels route only those two paths externally while keeping the history page (`/`) internal.

**Model 2 — Direct port-forward (no reverse proxy)**

Use `--cancel-listen` to start a separate public-facing listener that serves only `/cancel`,
`/notify-complete`, and `/healthz`, keeping the history page on `--history-listen`
(internal only):

```bash
rss4transmission watch --config config.yaml \
  --cancel-listen 0.0.0.0:8080 \
  --history-listen 127.0.0.1:9090
```

Port-forward from your firewall directly to the `--cancel-listen` port. The history page is
never reachable on that port (requests to `/` return 404). `--history-listen` is optional; omit
it if you don't need the history UI.

In Docker:

```yaml
environment:
  - CANCEL_LISTEN=0.0.0.0:8080
  - HISTORY_LISTEN=127.0.0.1:9090  # optional
ports:
  - "8080:8080"
```

When using [docker-compose-gluetun.yaml](../docker-compose-gluetun.yaml), set `CANCEL_LISTEN`
and uncomment the `ports` block to forward the cancel port from your firewall or NAS.

## History Web UI

Pass `--history-file` to enable history recording. rss4transmission records the outcome of
every feed item it processes (dispatched, downloaded, skipped, excluded, error).

Pass `--history-listen` to start the web UI. That flag accepts a bare port number (binds to
`127.0.0.1`) or a full `host:port` address (including IPv6 `[::1]:port`).

```bash
# Single listener (Traefik routes /cancel externally)
rss4transmission watch --config config.yaml \
    --history-file /data/history.json \
    --history-listen 8080

# Split listeners (firewall port-forwards to --cancel-listen)
rss4transmission watch --config config.yaml \
    --history-file /data/history.json \
    --history-listen 127.0.0.1:9090 \
    --cancel-listen 0.0.0.0:8080
```

The history page shows each item's feed name, title, publication date, outcome, and extracted
labels. Records are pruned on the same schedule as the seen cache (`SeenCacheDays`).

In Docker:

```yaml
environment:
  - HISTORY_FILE=/config/history.json
  - HISTORY_LISTEN=8080             # binds to 127.0.0.1:8080
  - CANCEL_LISTEN=0.0.0.0:8080     # optional; enables split-listener mode
```

## Routes Overview

| Route | `--history-listen` (single) | `--history-listen` (split) | `--cancel-listen` (split) |
|---|---|---|---|
| `/` (history page) | ✓ (requires `--history-file`) | ✓ (requires `--history-file`) | — |
| `/cancel` | ✓ | — | ✓ |
| `/notify-complete` | ✓ | — | ✓ |
| `/healthz` | ✓ | ✓ | ✓ |

## Completed Notification (POST /notify-complete)

`bin/torrent-complete.sh` is configured as Transmission's "torrent done" hook. It posts torrent
details to the `/notify-complete` endpoint, which renders your configured `CompletedTitle`,
`CompletedBody`, and `CompletedPriority` templates before sending to ntfy.

Set `RSS4TRANSMISSION_URL` to the base URL of your rss4transmission server (same host:port as
`--cancel-listen` or `--history-listen`). If `Cancel.HMACSecret` is configured, also set
`CANCEL_HMAC_SECRET` to the same value — the endpoint will then require
`Authorization: Bearer <secret>` and reject unauthenticated requests with `401`.

```yaml
# Transmission container environment
environment:
  - RSS4TRANSMISSION_URL=http://rss4transmission:8080
  - CANCEL_HMAC_SECRET=<same value as Cancel.HMACSecret in config.yaml>
```

The endpoint accepts `POST /notify-complete` with a JSON body:

```json
{"name": "My.Show.S01E01", "dir": "/downloads", "id": 42}
```

| Field | Source | Description |
|---|---|---|
| `name` | `TR_TORRENT_NAME` | Torrent name (maps to `{{.Title}}`) |
| `dir` | `TR_TORRENT_DIR` | Download directory (maps to `{{.Dir}}`) |
| `id` | `TR_TORRENT_ID` | Transmission torrent ID (maps to `{{.TorrentID}}`) |

Copy `bin/torrent-complete.sh` into your Transmission data volume and configure Transmission to
run it via its "torrent done" script hook. The [docker-compose.yaml](../docker-compose.yaml)
example mounts `./bin:/scripts` to make the script available inside the Transmission container
at `/scripts/torrent-complete.sh`.

## Per-Feed Opt-Out

Set `NoNotify: true` on any feed to suppress ntfy notifications for that feed only. This is
useful when you want global ntfy enabled but need to silence a high-volume or low-priority feed.
