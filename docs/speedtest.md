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
  MinUploadMbps:      0                      # 0 disables the upload floor
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
| `MinDownloadMbps` | Download measurements below this make a rotation eligible. |
| `MinUploadMbps` | Same for upload. `0` (default) disables it; needs `DownloadOnly: false`. |
| `Cooldown` | Suppresses a rotation this soon after the previous one. |
| `MaxRotationsPerDay` | Cap on rotations in the trailing 24 hours; `0` disables the cap. |
| `CaptureSeconds` | Duration of each transfer leg. This is what controls bandwidth cost. |
| `Threads` | Parallel connections. Raise if a single stream can't saturate the link. |
| `DownloadOnly` | Skip the upload test: ~10% less bandwidth, but a dead upload leg goes unseen. |
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

### Watching upload separately

The two directions fail independently. An exit can carry download at full rate while uploading
essentially nothing — the measurements table shows `-0.0` in the **Up** column, which is what
speedtest-go reports for a leg that moved no data. `MinDownloadMbps` cannot see this, so the
daemon happily sits on a dead exit.

That matters if you seed on a private tracker: nothing uploads, and your ratio decays while every
download-side metric looks healthy. Setting a floor rotates away from such an exit:

```yaml
SpeedTest:
  DownloadOnly:  false   # required: nothing measures upload without it
  MinUploadMbps: 5
```

Pick a floor well under your real upstream — the point is to catch *dead*, not merely slow. A
line that normally uploads 40 Mbps is well served by `5`.

`MinUploadMbps` with `DownloadOnly: true` is rejected at startup rather than silently rotating on
every measurement: with no upload leg, `UploadMbps` stays at zero and would always read as below
the floor.

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
start it again, which makes Gluetun re-pick from its filter set — the same thing `RotateTime`
already does.

Both halves are explicit: Gluetun stays stopped until it is told to start, so the stop is
confirmed before the start is issued. If the stop is refused, or Gluetun still reports the tunnel
running, the rotation is abandoned and the tunnel is left alone. If the status cannot be read at
all, the start is issued anyway — a tunnel left down blocks Transmission behind the killswitch,
which is far worse than starting one that was already running.

A rotation is only *requested* by the speed monitor; it is carried out by the port-check loop on
its next 5-minute tick, which also resyncs the new forwarded peer port into Transmission. Expect
up to a 5-minute delay between the decision and the restart.

A rotation is requested only when **all** of these hold:

1. The measurement succeeded — a failed or skipped run never triggers a rotation.
2. Throughput was below a configured floor: download below `MinDownloadMbps`, or upload below
   `MinUploadMbps`. Download is reported when both are.
3. At least `Cooldown` has passed since the last rotation — of any kind, including one you asked
   for from the page.
4. Fewer than `MaxRotationsPerDay` **automatic** rotations occurred in the trailing 24 hours.
5. No other rotation is already pending or under way.

The two budgets treat a manual rotation differently on purpose. `Cooldown` exists to stop the
tunnel being dropped repeatedly, so every rotation resets it. `MaxRotationsPerDay` is a budget on
the daemon's own churn, so clicking **Rotate VPN now** does not spend it — otherwise a few clicks
would silently disable the automatic rotation the setting exists to govern.

With `SkipWhenActive: true` (the default), no measurement is taken at all while torrents are
downloading, so an active download can never be interrupted by a rotation. Seeding is **not**
protected — a rotation always drops the tunnel and changes the forwarded peer port.

Note that with a narrow server filter such as `SERVER_CITIES=Los Angeles`, a restart can land on
the same server. The `/speedtest` page flags rotations whose exit IP did not change, which is the
signal to widen the filter rather than to rotate more.

Two ntfy alerts bracket each rotation: one when it is requested, naming the exit being left, and
one once the tunnel is back up, naming the exit it landed on. The second alert also flags a
reconnect to the same exit. See
[VPN Rotation Notifications](notifications.md#vpn-rotation-notifications).

Without a `Gluetun` block, `SpeedTest` still runs in measure-only mode: results are recorded and
served, but nothing rotates. Note that the Gluetun client is built once at startup and is not
rebuilt on config reload; the speed monitor inherits that limitation.

## Viewing results

With `--private-listen` set, the private web server serves:

- `GET /speedtest` — recent measurements, current exit IP, and the rotation log. Every rotation is
  logged, not only the speedtest-driven ones: the **Source** column reads `speedtest`, `schedule`
  (`Gluetun.RotateTime` elapsed), `closed-port` (`Gluetun.ClosedPortChecks` exceeded) or `manual`
  (the page's button). `rss4transmission_vpn_rotations_total` counts all four
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

### On-demand buttons

The `/speedtest` page carries two buttons for the things you most often want to do while looking
at it:

- **Run speedtest now** — queues a measurement instead of waiting for the next `Interval`. It
  ignores `SkipWhenActive`: an explicit click means "measure now", even though an active download
  drags the number down. It is also measure-only — the result is recorded but never fed to the
  rotation policy, so clicking it can't cause a surprise VPN restart minutes later. Clicking again
  while one is already queued is harmless; the requests coalesce into a single run.
- **Rotate VPN now** — asks Gluetun to re-pick an egress immediately, rather than on the port
  monitor's next 5-minute tick. When torrents are downloading (or rss4transmission can't reach
  Transmission to find out), the button asks for confirmation naming the count before it does
  anything; confirming rotates and interrupts those downloads. A rotation takes about a minute; a
  second click while one is under way is refused rather than dropping the tunnel twice.

Each button appears only when the thing it drives exists: without Gluetun there is no rotate
button, and with `SpeedTest.Enabled: false` there is no page at all. Both post to the private
listener and are unauthenticated like the rest of it — anyone who can reach `--private-listen`
can drop your VPN tunnel.

Neither button blocks: both return as soon as the work is queued, and the page's 60-second
auto-refresh is what eventually shows the new row.
