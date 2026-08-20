# RSS4Transmission

[![Tests](https://github.com/synfinatic/rss4transmission/actions/workflows/tests.yml/badge.svg)](https://github.com/synfinatic/rss4transmission/actions/workflows/tests.yml)
[![golangci-lint](https://github.com/synfinatic/rss4transmission/actions/workflows/golangci-lint.yml/badge.svg)](https://github.com/synfinatic/rss4transmission/actions/workflows/golangci-lint.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/synfinatic/rss4transmission)](https://goreportcard.com/report/github.com/synfinatic/rss4transmission)
[![License Badge](https://img.shields.io/badge/license-GPLv3-blue.svg)](https://raw.githubusercontent.com/synfinatic/rss4transmission/main/LICENSE)
[![Last Release](https://img.shields.io/github/v/release/synfinatic/rss4transmission)](https://github.com/synfinatic/rss4transmission/releases/)

## About

RSS4Transmission is a tool for fetching torrents over RSS for
[Transmission](https://transmissionbt.com). It watches one or more RSS feeds, applies
configurable label-based filtering and deduplication, and submits matching torrents directly
to Transmission.

## Why?

There are already a few tools that do this — most notably
[rss-transmission](https://github.com/nning/transmission-rss). RSS4Transmission is designed
for users who pull down many different files from the same feed and need them saved to different
directories. It fetches each RSS endpoint only once even when multiple feed configurations share
the same URL, and it selects the highest-quality version of each item automatically using a
configurable preference system.

## Docker Image

Pre-built images are available on [DockerHub](https://hub.docker.com/r/synfinatic/rss4transmission).

## Features

- **Label-based selection** — extract structured metadata (channel/feed, series, round, session,
  resolution, etc.) from torrent titles and file names; deduplicate by identity key; prefer
  higher-quality versions automatically
- **[ntfy](https://ntfy.sh) push notifications** — receive a notification when a torrent starts
  (with a More Info button), when it completes, when `watch` reloads its config file (reporting
  success or failure, with the error text on failure), and when Transmission's peer port
  transitions open/closed or is still closed 60s after startup; notification title, body, and
  priority are user-defined via `text/template` strings in the config file, with full access to
  torrent metadata (labels, size, feed name, GUID, and more)
- **History web UI** — browsable record of every processed feed item with outcome and extracted
  labels; skipped, excluded, and error items can be re-submitted to Transmission with a Torrent
  button
- **Gluetun VPN integration** — automatically restarts the VPN and syncs the peer port into
  Transmission when running behind [Gluetun](https://github.com/qdm12/gluetun); port state is
  polled every 5 minutes and logged/alerted on (also available without Gluetun via
  `PortCheck.Enabled`)
- **VPN speed testing & egress rotation** — periodically measures real speedtest.net throughput
  over the Gluetun tunnel and asks Gluetun to re-pick an egress when the link is slow, gated by a
  cooldown, a daily cap, and a never-rotate-while-downloading rule; results are persisted, shown
  on a `/speedtest` page, and exported on a Prometheus-style `/metrics` endpoint. Every rotation
  can send a pair of ntfy alerts — one when it's requested and one naming the new exit IP once the
  tunnel is back up. Run `rss4transmission speedtest` for a single on-demand measurement
- **Torrent file cache** — avoids re-fetching `.torrent` files on every watch-loop iteration;
  pruned automatically
- **Ordered, stop-after-dispatch processing** — feeds are processed in the order they're listed
  in the config file; as soon as one torrent is dispatched or downloaded, the run stops
  immediately, resuming with the next feed on the following `once`/`watch` tick
- **fail2ban integration** — optional access log with timestamps and client IPs lets fail2ban
  detect and ban brute-force attempts against the cancel endpoint

## Documentation

- [Deployment & Docker Compose](docs/deployment.md) — Docker setup, Transmission config,
  Gluetun integration, seen cache, torrent file cache, environment variables
- [VPN Speed Testing](docs/speedtest.md) — measuring throughput over the Gluetun tunnel,
  automatic egress rotation, bandwidth cost, `/speedtest` page and `/metrics` endpoint
- [Feeds & Labels](docs/feeds.md) — feed configuration, label extractors, identity
  deduplication, preference ranking, full config example
- [Notifications & History](docs/notifications.md) — ntfy push notifications with customizable
  templates and priority, cancel endpoint (Traefik and direct port-forward models), history web
  UI, completed notification via `/notify-complete` endpoint
- [fail2ban Integration](docs/fail2ban.md) — access log setup, filter and jail configuration,
  Docker volume-mount example, client IP resolution with Cloudflare support

## License

RSS4Transmission is licensed under the [GPLv3](LICENSE).
