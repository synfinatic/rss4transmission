# VPN Speed Testing & Egress Rotation

Gluetun picks a VPN server from whatever filter you configured (`SERVER_CITIES`,
`SERVER_HOSTNAMES`, and friends), and some of those servers are much slower than others.
`RotateTime` and `ClosedPortChecks` react to *time* and to *port state* — neither one notices
that the exit you landed on is only doing 20 Mbps.

The `SpeedTest` block runs a real speedtest.net measurement over the tunnel on a fixed interval
and asks Gluetun to re-pick an egress when throughput sits below a threshold.

This page assumes the Gluetun sidecar is already set up. See
[Gluetun Config](deployment.md#gluetun-config) for the `Gluetun` block, `RotateTime`, and
`ClosedPortChecks`.

## Enabling the Gluetun HTTP proxy

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

## Configuration

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

## Bandwidth cost

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

## Verifying the setup

Run a single measurement before enabling the background monitor:

```bash
rss4transmission speedtest --config config.yaml
```

This works even while `SpeedTest.Enabled` is `false`. Check the reported exit IP: if it matches
your *home* public IP rather than the VPN's, the proxy is not being used and every subsequent
measurement would be meaningless.

Pass `--save` to append the result to `ResultsFile`.

## How rotation works

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

## Viewing results

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
