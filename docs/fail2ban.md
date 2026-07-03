# fail2ban Integration

rss4transmission can write a structured access log that fail2ban can monitor to detect and block
brute-force attempts against the `/cancel` endpoint.

## Threat model

The `/cancel` endpoint is publicly accessible so ntfy notification recipients can cancel pending
Transmission downloads. The only authentication is an HMAC-signed token embedded in the ntfy
notification. An attacker who discovers the endpoint URL could probe it with random or guessed
tokens. Without a ban mechanism there is no cost to repeated attempts.

Enabling an access log lets fail2ban observe failed token validations and ban source IPs that
exceed a configurable threshold.

## Enabling access logging

Set `--access-log` (or the `ACCESS_LOG` environment variable in Docker) to the path where
rss4transmission should append access log entries. The directory must already exist; the file
is created if it does not exist and opened in append mode so entries survive restarts.

```bash
rss4transmission watch \
  --public-listen 0.0.0.0:8080 \
  --access-log /var/log/rss4transmission/access.log \
  ...
```

Docker example:

```yaml
services:
  rss4transmission:
    environment:
      - ACCESS_LOG=/logs/access.log
    volumes:
      - /host/path/rss4transmission-logs:/logs
```

fail2ban on the host then reads `/host/path/rss4transmission-logs/access.log`.

## Log line format

The access log uses logrus `TextFormatter` with RFC3339 timestamps and `key=value` fields:

```
time="2026-07-01T10:30:00Z" level=warning msg="cancel access" client_ip=1.2.3.4 endpoint=/cancel method=GET result=invalid_token
time="2026-07-01T10:30:01Z" level=info msg="cancel access" client_ip=1.2.3.4 endpoint=/cancel method=POST result=cancelled
```

Fields are sorted alphabetically after `time`/`level`/`msg`: `client_ip`, `endpoint`, `method`,
`result`.

Result values logged per outcome:

| Outcome | Level | `result=` |
|---|---|---|
| Missing or invalid token signature | `warning` | `invalid_token` |
| Token expired | `warning` | `expired` |
| Torrent ID not in store (already cancelled or never existed) | `warning` | `not_found` |
| Transmission RPC call failed | `warning` | `error` |
| Confirmation page rendered successfully | `info` | `ok` |
| Torrent cancelled successfully | `info` | `cancelled` |

Note: IPv6 addresses are quoted by logrus because they contain colons, e.g.
`client_ip="2001:db8::1"`.

## fail2ban filter

Create `/etc/fail2ban/filter.d/rss4transmission.conf`:

```ini
[INCLUDES]
before = common.conf

[Definition]
# logrus TextFormatter RFC3339 timestamp: time="2026-07-01T10:30:00Z"
datepattern = {^LN-BEG}time="%%Y-%%m-%%dT%%H:%%M:%%S

# Match any warning-level cancel access line (invalid_token, expired, not_found, error).
# IPv6 addresses are quoted by logrus, so "? handles both quoted and unquoted forms.
failregex = ^time="[^"]*"\s+level=warning\s+msg="cancel access"\s+client_ip="?<HOST>"?

ignoreregex =
```

## fail2ban jail

Create `/etc/fail2ban/jail.d/rss4transmission.conf`:

```ini
[rss4transmission]
enabled  = true
port     = http,https
logpath  = /var/log/rss4transmission/access.log
filter   = rss4transmission
maxretry = 10
findtime = 10m
bantime  = 1h
```

This bans a source IP for 1 hour after 10 warning-level events within 10 minutes. Adjust
`maxretry`, `findtime`, and `bantime` to suit your threat model.

After creating both files, reload fail2ban:

```bash
fail2ban-client reload
```

## Client IP resolution

The client IP logged is determined in this order:

1. `CF-Connecting-IP` — Cloudflare's definitive visitor IP (IPv4 or IPv6)
2. `CF-Connecting-IPv6` — Cloudflare IPv6-specific fallback
3. `X-Forwarded-For` — first entry in the list (set by Traefik, nginx, and most proxies)
4. `X-Real-IP` — set by nginx `proxy_set_header X-Real-IP $remote_addr`
5. `RemoteAddr` — TCP peer address (used when not behind a proxy)

When running behind a reverse proxy, configure the proxy to set a trustworthy
`X-Forwarded-For` header so the correct visitor IP is logged. When Cloudflare is in front,
`CF-Connecting-IP` is used automatically.
