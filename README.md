# RSS4Transmission

[![Tests](https://github.com/synfinatic/rss4transmission/actions/workflows/tests.yml/badge.svg)](https://github.com/synfinatic/rss4transmission/actions/workflows/tests.yml)
[![golangci-lint](https://github.com/synfinatic/rss4transmission/actions/workflows/golangci-lint.yml/badge.svg)](https://github.com/synfinatic/rss4transmission/actions/workflows/golangci-lint.yml)

## About

RSS4Transmission is a tool for fetching torrents over RSS for [Transmission](
https://transmissionbt.com).

## Why?

There are already a few tools that do this... most notably [rss-transmission](
https://github.com/nning/transmission-rss) is the closest, and I frankly stole
a lot of concepts from it.

The biggest difference is that RSS4Transmission is designed for OCD people who
pull down a lot of different files from the same feed and want them saved to
different directories.  I wanted something that would be "nice" and only
read the RSS feed once, even though I've got 10 different categories.

## Images

Pre-built images are available on [DockerHub](https://hub.docker.com/r/synfinatic/rss4transmission).

## Gluetun Compatability

RSS4Transmission now supports integrating with [Gluetun](https://github.com/qdm12/gluetun).

What this means if you launch Transmission, RSS4Transmission and Gluetun under [docker-compose](docker-compose-gluetun.yaml),
RSS4Transmission will take care of restarting the VPN and updating Transmission with the appropriate
[peer port](https://github.com/transmission/transmission/blob/main/docs/Port-Forwarding-Guide.md) information...
assuming that [Gluetun supports your VPN provider](https://github.com/qdm12/gluetun-wiki/blob/main/setup/advanced/vpn-port-forwarding.md)
for that.

Note that this functionality is currently experimental.

## Configuration

### Basic Configuration

Note that this config file works with [docker-compose.yaml](docker-compose.yaml)

```yaml
# how to talk to transmission, defaults shown below
Transmission:
    Host: localhost
    Port:     9091
    Username: admin
    Password: admin
    HTTPS:    false
    Path:     /transmission/rpc

# SeenFile can be overridden via --send-file option
SeenFile: /path/to/seen.json
SeenCacheDays: 30 # default

# examples...
Feeds:
    First:
        DownloadPath: /torrents/first
        Url: https://rss.foo.com/feed
        Regexp:
            - (?i)^MyFancyContent.*
            - (?i)^KindaFancyContent.*
        Exclude:
            - .*720p.*
        MinSize: 100MB
        MaxSize: 10GB
    Second:
        DownloadPath: /torrents/second
        Url: https://rss.foo.com/feed
        Regexp:
            - (?i)^OtherStuff.*
        Exclude:
            - .*Highlights.*
    NeatStuff:
        DownloadPath: /torrents/last
        Url: https://rss.barbaz.com/rss?apikey=xxxxx
        Regexp:
            - (?i)^NeatStuff.*
```

### Gluetun Configuration

Note that this config file works with [docker-compose-gluetun.yaml](docker-compose-gluetun.yaml)

```yaml
# When using gluetun, the networking is slightly different because
# we are using Docker service networking
Transmission:
    Host: gluetun
    Port:     9091
    Username: admin
    Password: admin

Gluetun:
    Host: gluetun
    Port: 8000
    RotateTime: 12h
    ClosedPortChecks: 5

# SeenFile can be overridden via --send-file option
SeenFile: /path/to/seen.json
SeenCacheDays: 30 # default

# your feeds go here...
```

### License

RSS4Transmission is licensed under the [GPLv3](LICENSE).
