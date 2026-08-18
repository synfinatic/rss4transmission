# Notifications & History

## Overview

RSS4Transmission supports four kinds of push notifications via [ntfy](https://ntfy.sh):

- **Torrent started** — sent by rss4transmission immediately after submitting a torrent to
  Transmission. Includes a **More Info** action button that opens a browser confirmation
  page showing torrent details and live download progress. Confirming removes the torrent from
  Transmission.
- **Torrent completed** — sent via the `POST /notify-complete` endpoint, which is called by
  `bin/torrent-complete.sh` running as Transmission's "torrent done" hook. The endpoint renders
  your configured templates and sends the notification to ntfy.
- **Config reloaded** — sent by `watch` whenever it picks up a change to the config file, to its
  own `Ntfy.AlertTopic` (separate from the torrent notifications' `Ntfy.Topic`). Reports whether
  the reload succeeded or failed; on failure, the notification body includes the actual error
  (e.g. a YAML parse error), so a bad edit is visible immediately without tailing logs.
- **Port closed / reopened** — see [Port Notifications](#port-notifications) below.

## ntfy and Cancel Configuration

Add `Ntfy` and `Cancel` blocks to your config file:

```yaml
Ntfy:
  BaseURL:    https://ntfy.sh              # your ntfy server
  Topic:      <your-topic-name>            # ntfy topic to publish to (torrent notifications)
  AlertTopic: <your-alert-topic-name>      # ntfy topic to publish to (config-reload / port alerts)
  Token:      tk_<your-access-token>       # ntfy access token

Cancel:
  HMACSecret: <random-32-byte-hex>                   # generate: openssl rand -hex 32
  BaseURL:    https://rss4transmission.yourdomain.com # externally reachable URL
  TokenTTLH:  24                                     # cancel link TTL in hours (default: 24)

# Only needed to enable the port-open check/alerts when Gluetun is NOT configured
# (with Gluetun configured, the check runs automatically). See Port Notifications below.
PortCheck:
  Enabled: true
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
| `Ntfy.AlertTopic` | — | ntfy topic to publish config-reload and port-state notifications to |
| `Ntfy.ConfigReloadedTitle` | `"Config Reloaded"` | `text/template` string for the reload-success notification title |
| `Ntfy.ConfigReloadedBody` | `"{{.ConfigFile}}"` | `text/template` string for the reload-success notification body |
| `Ntfy.ConfigReloadedPriority` | `low` | ntfy priority for reload-success notifications |
| `Ntfy.ConfigFailedTitle` | `"Config Reload FAILED"` | `text/template` string for the reload-failure notification title |
| `Ntfy.ConfigFailedBody` | `"{{.ConfigFile}}\n{{.Error}}"` | `text/template` string for the reload-failure notification body |
| `Ntfy.ConfigFailedPriority` | `high` | ntfy priority for reload-failure notifications |
| `Ntfy.PortClosedTitle` | `"Transmission Port Closed"` | `text/template` string for the port-closed notification title |
| `Ntfy.PortClosedBody` | `"{{.Reason}}"` | `text/template` string for the port-closed notification body |
| `Ntfy.PortClosedPriority` | `high` | ntfy priority for port-closed notifications |
| `Ntfy.PortOpenedTitle` | `"Transmission Port Open"` | `text/template` string for the port-reopened notification title |
| `Ntfy.PortOpenedBody` | `"{{.Reason}}"` | `text/template` string for the port-reopened notification body |
| `Ntfy.PortOpenedPriority` | `default` | ntfy priority for port-reopened notifications |
| `PortCheck.Enabled` | `false` | Enables the periodic port-open check when Gluetun is **not** configured (see [Port Notifications](#port-notifications)) |
| `Cancel.HMACSecret` | — | Secret key for signing cancel URLs (HMAC-SHA256) |
| `Cancel.BaseURL` | — | Public base URL of rss4transmission (used in cancel links) |
| `Cancel.TokenTTLH` | `24` | Hours before a cancel link expires |

Cancel links are omitted from notifications when `Cancel.HMACSecret` or `Cancel.BaseURL` is not
configured — the torrent started notification is still sent, just without the cancel action.
Ntfy notifications are entirely disabled when `Ntfy.BaseURL` is not set. `Ntfy.Topic` and
`Ntfy.AlertTopic` gate their respective notification groups independently: torrent
started/completed notifications require `Topic`, config-reload and port-state notifications
require `AlertTopic`, and either can be configured without the other.

> **Breaking rename:** `Ntfy.ConfigTopic` was renamed to `Ntfy.AlertTopic`. If your config sets
> `ConfigTopic`, rename it to `AlertTopic` — it now gates both config-reload and port-state
> notifications together.

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
| `{{.Size}}` | `string` | Human-readable size (e.g. `"4.32 GB"`) | `"Unknown"` when size is 0 or unavailable (always `"Unknown"` for completions) |
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

## Config Reload Notification Context

The `ConfigReloadedTitle`, `ConfigReloadedBody`, `ConfigFailedTitle`, and `ConfigFailedBody`
fields also accept `text/template` strings, but with their own, smaller context — they do **not**
have access to `{{.Title}}`, `{{.Size}}`, or any other torrent-notification field:

| Field | Type | Description |
|---|---|---|
| `{{.ConfigFile}}` | `string` | Path to the config file that was (re)loaded |
| `{{.Error}}` | `string` | Reload error text (e.g. a YAML parse error); empty on success |

Include the parse error in the failure body:

```yaml
Ntfy:
  ConfigFailedBody: |
    Failed to reload {{.ConfigFile}}:
    {{.Error}}
```

## Port Notifications

`watch` periodically checks whether Transmission's peer port is reachable, on a fixed 5-minute
interval (not configurable). Every check is logged — `debug` when open, `warn` when closed. Two
ntfy alerts can fire, both to `Ntfy.AlertTopic`:

- **Transition alerts** — sent the moment the port's state changes: open → closed sends
  `PortClosedTitle`/`PortClosedBody`, closed → open sends `PortOpenedTitle`/`PortOpenedBody`.
- **Startup alert** — if the port is still closed 60 seconds (fixed, not configurable) after
  `watch` starts, `PortClosedTitle`/`PortClosedBody` is sent once, independent of the transition
  alert above.

The check runs automatically whenever `Gluetun.Host`/`Gluetun.Port` are configured — it's the
same check Gluetun already performs for VPN rotation and peer-port sync, now also logged and
alerted on. Without Gluetun, set `PortCheck.Enabled: true` to opt in to the periodic check on
its own (no rotation/sync, just the open/closed poll, logging, and alerts).

### Port Notification Context

The `PortClosedTitle`, `PortClosedBody`, `PortOpenedTitle`, and `PortOpenedBody` fields also
accept `text/template` strings, with their own small context:

| Field | Type | Description |
|---|---|---|
| `{{.Reason}}` | `string` | Why the alert fired, e.g. `"port closed"`, `"port reopened"`, or `"port not open 60s after startup"` |

## Cancel Endpoint

The `/cancel` endpoint serves a confirmation page where the user can review torrent details and
live download progress before removing the torrent from Transmission. It must be reachable from
the internet so ntfy can open it when the user taps Cancel.

There are two deployment models.

**Model 1 — Traefik (or other reverse proxy)**

Use `--private-listen` to start a single web server and let Traefik route only `/cancel` and
`/healthz` externally:

```bash
rss4transmission watch --config config.yaml --private-listen :8080
```

The [docker-compose.yaml](../docker-compose.yaml) example defaults to this model. Its Traefik
labels route only those two paths externally while keeping the history page (`/`) internal.

**Model 2 — Direct port-forward (no reverse proxy)**

Use `--public-listen` to start a separate public-facing listener that serves only `/cancel`,
`/notify-complete`, and `/healthz`, keeping the history page on `--private-listen`
(internal only):

```bash
rss4transmission watch --config config.yaml \
  --public-listen 0.0.0.0:8080 \
  --private-listen 127.0.0.1:9090
```

Port-forward from your firewall directly to the `--public-listen` port. The history page is
never reachable on that port (requests to `/` return 404). `--private-listen` is optional; omit
it if you don't need the history UI.

In Docker:

```yaml
environment:
  - PUBLIC_LISTEN=0.0.0.0:8080
  - PRIVATE_LISTEN=127.0.0.1:9090  # optional
ports:
  - "8080:8080"
```

When using [docker-compose-gluetun.yaml](../docker-compose-gluetun.yaml), set `PUBLIC_LISTEN`
and uncomment the `ports` block to forward the cancel port from your firewall or NAS.

## History Web UI

Pass `--history-file` to enable history recording. rss4transmission records the outcome of
every feed item it processes (dispatched, downloaded, skipped, excluded, error).

Pass `--private-listen` to start the web UI. That flag accepts a bare port number (binds to
`127.0.0.1`) or a full `host:port` address (including IPv6 `[::1]:port`).

```bash
# Single private listener (Traefik routes /cancel externally)
rss4transmission watch --config config.yaml \
    --history-file /data/history.json \
    --private-listen 8080

# Split listeners (firewall port-forwards to --public-listen)
rss4transmission watch --config config.yaml \
    --history-file /data/history.json \
    --private-listen 127.0.0.1:9090 \
    --public-listen 0.0.0.0:8080
```

The history page shows each item's feed name, title, publication date, outcome, and extracted
labels. Records are pruned on the same schedule as the seen cache (`SeenCacheDays`).

When multiple sibling feeds share one RSS URL — for example, separate feeds for different
categories of content that all happen to be published through the same feed URL — the same item
produces one history record per sibling. These are collapsed into a single expandable group
instead of a wall of near-duplicate rows. The visible parent row is chosen by, in order: a record
that actually engaged with the item beats one whose `Groups` never matched at all; among those,
the more informative outcome wins; remaining ties prefer whichever sibling's own extracted labels
actually satisfy its own `Groups.Require` constraints (see
[Feeds & Labels](feeds.md#label-based-feed-configuration)) — this matters because siblings that
share an `Extractor` can extract identical-looking labels for an item that isn't actually theirs,
so only a label value that satisfies a feed's own `Require` counts as evidence. Any remaining tie
breaks alphabetically by feed name.

Rows with outcome `skipped`, `excluded`, or `error` show a **Torrent** button when a `.torrent`
URL was captured for that item, letting you manually re-submit it to Transmission without waiting
for the feed to re-offer it. Clicking it re-fetches the `.torrent` fresh, submits it exactly like
an automatic dispatch, and sends the normal "torrent started" ntfy notification (including a
working Cancel link) on success. The button requires the item's feed to still be present in the
config — it's hidden or fails with an error otherwise — and disappears once an item is
successfully torrented. The action lives at `POST /torrent`, served only alongside the history
page (`--private-listen`); it is never reachable on `--public-listen`.

Every row also shows a **Forget** button, which removes that item's `(feed, guid)` pair from
both the seen cache and the history page. Use it to retest a config change — for example after
loosening a `Group.Require` or fixing an `Exclude` regex — without hand-editing `cache.json`.
Unlike Torrent, Forget is available for every outcome, including `dispatched`, since you may
deliberately want to allow a re-download. On success the row disappears from the history page;
the change is persisted to `cache.json` and `history.json` on the next scheduled run. The action
lives at `POST /forget`, served only alongside the history page (`--private-listen`); it is never
reachable on `--public-listen`.

In Docker:

```yaml
environment:
  - HISTORY_FILE=/config/history.json
  - PRIVATE_LISTEN=8080             # binds to 127.0.0.1:8080
  - PUBLIC_LISTEN=0.0.0.0:8080     # optional; enables split-listener mode
```

## Routes Overview

| Route | `--private-listen` (single) | `--private-listen` (split) | `--public-listen` (split) |
|---|---|---|---|
| `/` (history page) | ✓ (requires `--history-file`) | ✓ (requires `--history-file`) | — |
| `/torrent` | ✓ (requires `--history-file`) | ✓ (requires `--history-file`) | — |
| `/forget` | ✓ (requires `--history-file`) | ✓ (requires `--history-file`) | — |
| `/cancel` | ✓ | — | ✓ |
| `/notify-complete` | ✓ | — | ✓ |
| `/healthz` | ✓ | ✓ | ✓ |

## Completed Notification (POST /notify-complete)

`bin/torrent-complete.sh` is configured as Transmission's "torrent done" hook. It posts torrent
details to the `/notify-complete` endpoint, which renders your configured `CompletedTitle`,
`CompletedBody`, and `CompletedPriority` templates before sending to ntfy.

Set `RSS4TRANSMISSION_URL` to the base URL of your rss4transmission server (same host:port as
`--public-listen` or `--private-listen`). If `Cancel.HMACSecret` is configured, also set
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
