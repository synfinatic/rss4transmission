<img src="docs/logo.svg" alt="RSS4Transmission" height="64">

[![Tests](https://github.com/synfinatic/rss4transmission/actions/workflows/tests.yml/badge.svg)](https://github.com/synfinatic/rss4transmission/actions/workflows/tests.yml)
[![golangci-lint](https://github.com/synfinatic/rss4transmission/actions/workflows/golangci-lint.yml/badge.svg)](https://github.com/synfinatic/rss4transmission/actions/workflows/golangci-lint.yml)
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
- **History web UI** — the **Torrents** page: a browsable record of every processed feed item with
  outcome and extracted labels; skipped, excluded, and error items can be re-submitted to
  Transmission with a Torrent button. Cross-links the **VPN Speed** and **Rotations** pages when
  speed testing is enabled
- **Embedded Transmission client** — the **Transmission** page shows the Transmission web client
  in a frame, so one nav bar covers both tools. A built-in reverse proxy reaches Transmission on
  your behalf and signs in with the configured credentials, which works even when the browser
  cannot resolve `Transmission.Host`. Turn it off with `Transmission.WebUI: false`
- **Gluetun VPN integration** — automatically restarts the VPN and syncs the peer port into
  Transmission when running behind [Gluetun](https://github.com/qdm12/gluetun); port state is
  polled every 5 minutes and logged/alerted on (also available without Gluetun via
  `PortCheck.Enabled`)
- **VPN speed testing & egress rotation** — periodically measures real speedtest.net throughput
  over the Gluetun tunnel and asks Gluetun to re-pick an egress when the link is slow in either
  direction — a separate `MinUploadMbps` floor catches an exit that downloads fine while uploading
  nothing, which is what silently wrecks a ratio on a private tracker — gated by a cooldown, a
  daily cap, and a never-rotate-while-downloading rule; results are persisted, shown
  on a `/speedtest` page, and exported on a Prometheus-style `/metrics` endpoint. Every rotation
  can send a pair of ntfy alerts — one when it's requested and one naming the new exit IP once the
  tunnel is back up. The `/speedtest` page also has **Run speedtest now** and **Rotate VPN now**
  buttons for acting immediately instead of waiting for the next interval; its header also shows
  the port Gluetun forwards and whether Transmission sees that port as open, and
  `rss4transmission speedtest` runs a single on-demand measurement from the CLI, and
  `--server` targets one speedtest.net server ID for that run
- **Torrent file cache** — avoids re-fetching `.torrent` files on every watch-loop iteration;
  pruned automatically
- **Ordered, stop-after-dispatch processing** — feeds are processed in the order they're listed
  in the config file; as soon as one torrent is dispatched or downloaded, the run stops
  immediately, resuming with the next feed on the following `once`/`watch` tick
- **fail2ban integration** — optional access log with timestamps and client IPs lets fail2ban
  detect and ban brute-force attempts against the cancel endpoint
- **Live config reload** — `watch` re-reads the whole config file when you save it and applies
  every setting to the running process. A bad edit is rejected, and the previous config keeps
  running
- **Config upload page** — an optional `/config` page on the private listener. Drop a new
  `config.yaml` on it. The program checks the file, applies it, and shows any error in the page

## Live config reload

The `watch` command watches the config file. When you save a change, `watch` reads the whole file
again and applies every setting to the running process. A restart is not necessary.

The settings below all take effect on the next save:

- `Feeds` and `Extractors`
- `Transmission`, including a new host or port, new credentials, and `WebUI`
- `Gluetun`, including the rotation policy and the control server address
- `SpeedTest`, `PortCheck.Enabled`, and `TorrentComplete.PollInterval`
- `Ntfy` and `Notifications`, including `HMACSecret`, `TokenTTLH`, and `BaseURL`
- `SeenFile` and `SeenCacheDays`

Two changes cost a little work. A new `SpeedTest` or `Gluetun` block rebuilds the speed monitor,
which abandons a measurement in progress. A new `SeenFile` saves the current cache before it opens
the new path.

If the new file is not valid, `watch` rejects it and keeps the config that is already running. It
logs the error and sends the config-failed ntfy alert. Fix the file and save it again.

The command line flags are read once at start. To change one of the flags below, restart the
process:

- `--private-listen`, `--public-listen`, `--history-file`, and `--access-log`
- `--sleep`, `--torrent-cache-dir`, and `--feed`
- `--download` and `--download-path`
- `--seen-file`, which pins the cache path and overrides `SeenFile` in the config file

## Upload a config file from the web page

The private listener can serve a `/config` page. On this page you drop a new `config.yaml` in the
browser. You do not copy the file to the host with `sftp` or `scp`.

The page is off by default. A config upload can change the Transmission URL and every password, so
the page needs a login. To turn it on, set the `watch` flag `--config-upload` (env
`CONFIG_UPLOAD`). Its value is the login, in the format `user:hash`. The hash is a bcrypt hash of
the password. The cleartext password is not stored anywhere.

To make the value, run this command. `htpasswd` is in the `apache2-utils` or `httpd-tools` package.

```bash
htpasswd -nbB admin 'your-password'
```

The command prints a line like `admin:$2y$05$...`. Use that line as the value.

The flag has these rules:

- `--config-upload` must be `user:hash` with a bcrypt hash. `watch` does not start if the hash is
  cleartext or another hash type.
- Set the value with the environment variable, so that it does not show in the process list.
- `--private-listen` and `--public-listen` are both required. The page is served on the private
  listener only. `watch` does not start if the page would share a port with the public routes.
- The login is not in the config file. A bad upload cannot lock you out.
- bcrypt reads only the first 72 bytes of a password.
- Docker Compose reads `$` as a variable mark. In `docker-compose.yaml`, write each `$` of the
  value as `$$`, and do not put quotes around the value. In the list form (`- KEY=value`), Compose
  keeps the quotes as part of the value. To check the result, run `docker compose config` and look
  at the `CONFIG_UPLOAD` line. It must show single `$` marks.
- If `watch` stops with "needs a bcrypt hash", the cause is usually a `$` that Compose replaced
  or a quote that Compose kept.

When you upload a file, `watch` does these steps:

1. It checks the file with the same checks as a config reload. If a check fails, the page shows
   the error. The running config and the file on disk do not change.
2. It copies the old file to `config.yaml.bak`.
3. It writes the new file next to the old one and renames it into place.
4. It reloads the config. If the reload fails, it puts the old file back and shows the error.

A file larger than 1 MiB is rejected. The page does not show the running config, so no password
leaves the program.

`watch` replaces the file with a rename. Mount the directory that holds the config file in the
container, not the single file. A single-file bind mount fails with "device or resource busy".

The listener does not use TLS, and HTTP Basic sends the password in clear text. Use a trusted
network or a reverse proxy that adds TLS.

You can also upload from a script. `curl` asks for the password:

```bash
curl -u admin -F file=@config.yaml http://localhost:8080/config
```

## Documentation

- [Deployment & Docker Compose](docs/deployment.md) — Docker setup, Transmission config,
  Gluetun integration, seen cache, torrent file cache, environment variables
- [VPN Speed Testing](docs/speedtest.md) — measuring throughput over the Gluetun tunnel,
  automatic egress rotation, bandwidth cost, `/speedtest` page and `/metrics` endpoint
- [Feeds & Labels](docs/feeds.md) — feed configuration, label extractors, identity
  deduplication, preference ranking, full config example
- [Notifications & History](docs/notifications.md) — ntfy push notifications with customizable
  templates and priority, cancel endpoint (Traefik and direct port-forward models), history web
  UI, completed notification via periodic Transmission polling
  (`TorrentComplete.PollInterval`)
- [fail2ban Integration](docs/fail2ban.md) — access log setup, filter and jail configuration,
  Docker volume-mount example, client IP resolution with Cloudflare support

## License

RSS4Transmission is licensed under the [GPLv3](LICENSE).
