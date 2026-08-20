# Deployment & Docker Compose

## Docker Images

Pre-built images are available on [DockerHub](https://hub.docker.com/r/synfinatic/rss4transmission).

## Basic Docker Setup

The [docker-compose.yaml](../docker-compose.yaml) example sets up rss4transmission alongside
Transmission. It defaults to the Traefik reverse-proxy model for exposing the `/cancel` endpoint
externally, but comments show how to switch to a direct port-forward if you don't have Traefik.

```yaml
# example, edit to taste
version: "3"

services:
  rss4transmission:
    container_name: rss4transmission
    restart: unless-stopped
    image: synfinatic/rss4transmission:latest
    environment:
      - POLL_SECONDS=120
      - LOG_LEVEL=info
      - HISTORY_FILE=       # path to history JSON file (e.g. /config/history.json)
      - PRIVATE_LISTEN=     # host:port or bare port — enables history UI (private/internal)
      - PUBLIC_LISTEN=      # public-facing /cancel, /notify-complete, and /healthz only
      - TORRENT_CACHE_DIR=  # directory to cache .torrent files (e.g. /config/torrent-cache)
    volumes:
      - /volume1/docker/transmission/rss4transmission:/config
    # Option A — Traefik routes /cancel and /healthz externally (PUBLIC_LISTEN not needed):
    networks:
      - internal
      - traefik
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.rss4tx.rule=Host(`rss4transmission.yourdomain.com`) && (PathPrefix(`/cancel`) || PathPrefix(`/healthz`))"
      - "traefik.http.routers.rss4tx.entrypoints=websecure"
      - "traefik.http.routers.rss4tx.tls.certresolver=letsencrypt"
      - "traefik.http.services.rss4tx.loadbalancer.server.port=8080"
    # Option B — no Traefik; firewall port-forwards directly to PUBLIC_LISTEN port
    # (/cancel, /notify-complete, /healthz). Set PUBLIC_LISTEN=0.0.0.0:8080 and add:
    # ports:
    #   - "8080:8080"
    # Remove the networks/labels above; use network_mode: host or a single internal network.

  transmission:
    container_name: transmission
    restart: unless-stopped
    image: lscr.io/linuxserver/transmission:latest
    environment:
      - PUID=1026
      - GUID=100
      - TZ=US/Pacific
      - USER=admin
      - PASS=admin
      # ntfy credentials used by torrent-complete.sh
      - NTFY_BASE_URL=https://ntfy.sh
      - NTFY_TOPIC=<your-topic-name>
      - NTFY_TOKEN=
    volumes:
      - /volume1/docker/transmission/data:/config
      - /volume1/video/torrents:/torrents
      - ./bin:/scripts   # makes torrent-complete.sh available inside the container
    networks:
      - internal

networks:
  internal:
  traefik:
    external: true
```

See [Notifications & History](notifications.md) for details on Option A vs Option B and how to
configure the `/cancel` endpoint.

## Gluetun Docker Setup

[docker-compose-gluetun.yaml](../docker-compose-gluetun.yaml) adds a Gluetun VPN sidecar.
Transmission runs with `network_mode: "service:gluetun"` so all its traffic goes over the VPN.
rss4transmission is intentionally **not** on the Gluetun network — this keeps RSS feed fetching
off the VPN while still allowing rss4transmission to reach Transmission via its published port.

Note: Gluetun VPN integration is experimental.

```yaml
# example for using with Gluetun, edit to taste
version: "3"

services:
  rss4transmission:
    image: synfinatic/rss4transmission:latest
    container_name: rss4transmission
    restart: unless-stopped
    depends_on:
      - gluetun
      - transmission
    user: 1026:100
    environment:
      - POLL_SECONDS=120
      - LOG_LEVEL=info
      - HISTORY_FILE=
      - PRIVATE_LISTEN=
      - PUBLIC_LISTEN=      # /cancel, /notify-complete, /healthz (e.g. 0.0.0.0:8080); port-forward from your firewall to this port
      - TORRENT_CACHE_DIR=
    # Uncomment to expose PUBLIC_LISTEN externally:
    # ports:
    #   - "8080:8080"
    volumes:
      - /volume1/docker/transmission/rss4transmission:/config

  transmission:
    image: lscr.io/linuxserver/transmission:latest
    container_name: transmission
    restart: unless-stopped
    network_mode: "service:gluetun"
    depends_on:
      - gluetun
    environment:
      - PUID=1026
      - GUID=100
      - TZ=US/Pacific
      - USER=XXXX
      - PASS=XXXX
    volumes:
      - /volume1/docker/transmission/data:/config
      - /volume1/video/torrents:/torrents

  gluetun:
    image: qmcgaw/gluetun:latest
    container_name: gluetun
    restart: unless-stopped
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    volumes:
      - /volume1/docker/transmission/gluetun:/gluetun
    environment:
      - VPN_SERVICE_PROVIDER=protonvpn
      - VPN_TYPE=openvpn
      - OPENVPN_USER=XXXXX
      - OPENVPN_PASSWORD=XXXX
      - OPENVPN_VERSION=2.6
      - VPN_PORT_FORWARDING=on
      - SERVER_HOSTNAMES=XXXXX,YYYYYY,ZZZZZ
      - HTTP_CONTROL_SERVER_LOG=off
    ports:
      - "0.0.0.0:9091:9091/tcp"  # expose Transmission RPC/WebUI to local network
      - 51413:51413/tcp
      - 51413:51413/udp
      - 9092:8000/tcp   # Gluetun control server on local port 9092
```

## Transmission Config

Add a `Transmission` block to your config file. When using the Gluetun compose, set `Host` to
`"gluetun"` since Transmission's port is published via the Gluetun service.

```yaml
Transmission:
  Host:     localhost   # use "gluetun" in the Gluetun compose
  Port:     9091
  Username: admin
  Password: admin
  HTTPS:    false
  Path:     /transmission/rpc
```

## Gluetun Config

When using Gluetun, add a `Gluetun` block to enable automatic VPN rotation and peer-port
forwarding. rss4transmission will restart the VPN when the peer port closes or after
`RotateTime` elapses, then update Transmission with the new peer port.

This check runs on its own fixed 5-minute cadence, independent of `--sleep` (the feed-scrape
interval). Previously it ran once per feed-scrape iteration, so `RotateTime` and
`ClosedPortChecks` were silently throttled by whatever `--sleep` value was configured; that
coupling is now removed. For the default `--sleep 300` this is a no-op, but if you run a
non-default `--sleep`, rotation/port-sync timing will now follow the 5-minute cadence instead.
See [Port Notifications](notifications.md#port-notifications) for the accompanying ntfy alerts.

```yaml
# When using Gluetun, Docker service networking changes the hostname
Transmission:
  Host: gluetun
  Port: 9091
  Username: admin
  Password: admin

Gluetun:
  Host:             gluetun
  Port:             8000
  RotateTime:       12h        # rotate the VPN connection every 12 hours
  ClosedPortChecks: 5          # also rotate after 5 consecutive closed-port checks
  # Set AuthUsername + AuthPassword OR AuthAPIKey (not both)
  AuthUsername: Basic Auth Username
  AuthPassword: Basic Auth Password
  AuthAPIKey:   API Key
```

[Gluetun must be configured with VPN port forwarding support](
https://github.com/qdm12/gluetun-wiki/blob/main/setup/advanced/vpn-port-forwarding.md) for
this integration to work.

## VPN Speed Testing

Gluetun picks a VPN server from whatever filter you configured (`SERVER_CITIES`,
`SERVER_HOSTNAMES`, and friends), and some of those servers are much slower than others.
`RotateTime` and `ClosedPortChecks` react to *time* and to *port state* — neither one notices
that the exit you landed on is only doing 20 Mbps.

The `SpeedTest` block runs a real speedtest.net measurement over the tunnel on a fixed interval
and asks Gluetun to re-pick an egress when throughput sits below a threshold.

### Enabling the Gluetun HTTP proxy

Measurement traffic is routed through Gluetun's built-in HTTP proxy, so rss4transmission itself
can stay off the VPN network and keep fetching RSS feeds directly. Add `HTTPPROXY=on` to the
gluetun service:

```yaml
  gluetun:
    environment:
      - HTTPPROXY=on
```

No `ports:` entry is needed. rss4transmission and gluetun share the default compose network, so
`http://gluetun:8888` resolves by service name — exactly like the `Gluetun.Host` control-server
setting already does.

### Configuration

```yaml
SpeedTest:
  Enabled:            true
  Interval:           1h                     # how often to measure
  Proxy:              http://gluetun:8888    # gluetun's HTTP proxy
  MinDownloadMbps:    100                    # rotate below this
  Cooldown:           2h                     # minimum time between rotations
  MaxRotationsPerDay: 6                      # 0 means unlimited
  CaptureSeconds:     5                      # bounds bandwidth used per test
  Threads:            2                      # parallel connections per test
  DownloadOnly:       true                   # skip the upload leg
  SkipWhenActive:     true                   # don't measure while torrents download
  ServerID:           ""                     # optional speedtest.net server pin
  ResultsFile:        /config/speedtest.json # required when Enabled
  RetentionDays:      30
```

| Setting | Meaning |
|---|---|
| `Enabled` | Master switch. Defaults to `false`. |
| `Interval` | Measurement cadence. Must be longer than `CaptureSeconds`. |
| `Proxy` | Full URL of Gluetun's HTTP proxy. Required when enabled. |
| `MinDownloadMbps` | Measurements below this make a rotation eligible. |
| `Cooldown` | Suppresses a rotation this soon after the previous one. |
| `MaxRotationsPerDay` | Cap on rotations in the trailing 24 hours; `0` disables the cap. |
| `CaptureSeconds` | Duration of each transfer leg. This is what controls bandwidth cost. |
| `Threads` | Parallel connections. Raise if a single stream can't saturate the link. |
| `DownloadOnly` | Skip the upload test. Roughly a 10% bandwidth saving. |
| `SkipWhenActive` | Record a "skipped" result instead of measuring while torrents download. |
| `ServerID` | Pin a speedtest.net server ID instead of taking the nearest one. |
| `ResultsFile` | JSON file of measurements and rotation events. Required when enabled. |
| `RetentionDays` | How long results and rotation events are kept. |

### Bandwidth cost

A speedtest transfers as fast as the link allows for the whole capture window, so the cost
scales with your line speed, not with the test count alone:

| Config | Per test | Daily |
|---|---|---|
| `CaptureSeconds: 5`, `DownloadOnly: true`, ~400 Mbps | ~250 MB | ~6 GB |
| `CaptureSeconds: 5`, upload enabled | ~280 MB | ~6.7 GB |
| `CaptureSeconds: 10`, upload enabled | ~560 MB | ~13 GB |

`Enabled` defaults to `false` and `DownloadOnly` to `true` for this reason. If ~6 GB/day is too
much, raise `Interval` to `6h`; decision quality barely changes, because `Cooldown` and
`MaxRotationsPerDay` already dominate how often a rotation can actually fire.

### Verifying the setup

Run a single measurement before enabling the background monitor:

```bash
rss4transmission speedtest --config config.yaml
```

This works even while `SpeedTest.Enabled` is `false`. Check the reported exit IP: if it matches
your *home* public IP rather than the VPN's, the proxy is not being used and every subsequent
measurement would be meaningless.

Pass `--save` to append the result to `ResultsFile`.

### How rotation works

Gluetun has no "switch to a different server" API. The only mechanism is to stop the VPN and
let it auto-restart, which makes Gluetun re-pick from its filter set — the same thing
`RotateTime` already does.

A rotation is only *requested* by the speed monitor; it is carried out by the port-check loop on
its next 5-minute tick, which also resyncs the new forwarded peer port into Transmission. Expect
up to a 5-minute delay between the decision and the restart.

A rotation is requested only when **all** of these hold:

1. The measurement succeeded — a failed or skipped run never triggers a rotation.
2. Download throughput was below `MinDownloadMbps`.
3. At least `Cooldown` has passed since the last rotation.
4. Fewer than `MaxRotationsPerDay` rotations occurred in the trailing 24 hours.

With `SkipWhenActive: true` (the default), no measurement is taken at all while torrents are
downloading, so an active download can never be interrupted by a rotation. Seeding is **not**
protected — a rotation always drops the tunnel and changes the forwarded peer port.

Note that with a narrow server filter such as `SERVER_CITIES=Los Angeles`, a restart can land on
the same server. The `/speedtest` page flags rotations whose exit IP did not change, which is the
signal to widen the filter rather than to rotate more.

Without a `Gluetun` block, `SpeedTest` still runs in measure-only mode: results are recorded and
served, but nothing rotates. Note that the Gluetun client is built once at startup and is not
rebuilt on config reload; the speed monitor inherits that limitation.

### Viewing results

With `--private-listen` set, the private web server serves:

- `GET /speedtest` — recent measurements, current exit IP, and the rotation log
- `GET /metrics` — Prometheus text format, exposing:

```
rss4transmission_speedtest_download_mbps
rss4transmission_speedtest_upload_mbps
rss4transmission_speedtest_latency_ms
rss4transmission_speedtest_jitter_ms
rss4transmission_speedtest_last_run_timestamp_seconds
rss4transmission_speedtest_failures_total
rss4transmission_vpn_rotations_total
rss4transmission_peer_port_open
```

Throughput gauges report the last *successful* measurement, and optional legs that were not
measured are omitted rather than reported as zero — so a failed run or a skipped upload test is
never scraped as a dead link. `rss4transmission_speedtest_last_run_timestamp_seconds` covers
every attempt including failures, which is how you tell "measuring badly" apart from "stopped
measuring".

Both endpoints are unauthenticated, like the history page; keep `--private-listen` off the
public internet.

## Seen Cache

`SeenFile` is a JSON file that records every torrent rss4transmission has dispatched. It
prevents re-downloading the same content and tracks the best preference rank seen for each
identity key. `SeenCacheDays` controls how long records are retained before being pruned.

```yaml
SeenFile:      /config/seen.json
SeenCacheDays: 30
```

## Torrent File Cache

In watch mode, rss4transmission re-fetches every candidate's `.torrent` file on each run in
order to extract per-file labels from the torrent's file list. For pack torrents (one torrent
containing multiple sessions or classes), these downloads happen every few minutes even though
the content never changes.

Pass `--torrent-cache-dir` to cache `.torrent` files on disk and avoid redundant fetches:

```bash
rss4transmission watch --config config.yaml --torrent-cache-dir /data/torrent-cache
rss4transmission once  --config config.yaml --torrent-cache-dir /data/torrent-cache --no-action
```

On a cache hit the file is read from disk; on a miss it is fetched and then written to the
cache. Cache files are named `<sanitized-title>.torrent` and pruned automatically after
`SeenCacheDays` days.

In Docker, set `TORRENT_CACHE_DIR`:

```yaml
environment:
  - TORRENT_CACHE_DIR=/config/torrent-cache
```

## Docker Environment Variables

| Variable | Description |
|---|---|
| `POLL_SECONDS` | Seconds between feed scrapes in watch mode (default: `300`) |
| `LOG_LEVEL` | Log verbosity: `error`, `warn`, `info`, `debug`, `trace` (default: `info`) |
| `HISTORY_FILE` | Path to the history JSON file; enables history recording when set |
| `PRIVATE_LISTEN` | `host:port` or bare port — starts the private history web UI |
| `PUBLIC_LISTEN` | `host:port` — public-facing listener for `/cancel`, `/notify-complete`, and `/healthz` |
| `TORRENT_CACHE_DIR` | Directory to cache fetched `.torrent` files across runs |
| `ACCESS_LOG` | Path to the fail2ban-compatible HTTP access log file (append mode); disabled when empty |
