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
      - HISTORY_LISTEN=     # host:port or bare port — enables history UI
      - CANCEL_LISTEN=      # public-facing /cancel and /healthz only
      - TORRENT_CACHE_DIR=  # directory to cache .torrent files (e.g. /config/torrent-cache)
    volumes:
      - /volume1/docker/transmission/rss4transmission:/config
    # Option A — Traefik routes /cancel and /healthz externally (CANCEL_LISTEN not needed):
    networks:
      - internal
      - traefik
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.rss4tx.rule=Host(`rss4transmission.yourdomain.com`) && (PathPrefix(`/cancel`) || PathPrefix(`/healthz`))"
      - "traefik.http.routers.rss4tx.entrypoints=websecure"
      - "traefik.http.routers.rss4tx.tls.certresolver=letsencrypt"
      - "traefik.http.services.rss4tx.loadbalancer.server.port=8080"
    # Option B — no Traefik; firewall port-forwards directly to CANCEL_LISTEN port:
    # Set CANCEL_LISTEN=0.0.0.0:8080 and add:
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
      - HISTORY_LISTEN=
      - CANCEL_LISTEN=      # e.g. 0.0.0.0:8080; port-forward from your firewall to this port
      - TORRENT_CACHE_DIR=
    # Uncomment to expose CANCEL_LISTEN externally:
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
| `HISTORY_LISTEN` | `host:port` or bare port — starts the history/cancel web UI |
| `CANCEL_LISTEN` | `host:port` — separate public-facing listener for `/cancel` and `/healthz` |
| `TORRENT_CACHE_DIR` | Directory to cache fetched `.torrent` files across runs |
