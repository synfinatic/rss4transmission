# RSS4Transmission

### About

RSS4Transmission is a tool for fetching torrents over RSS for [Transmission](
https://transmissionbt.com).

### Why?

There are already a few tools that do this... most notably [rss-transmission](
https://github.com/nning/transmission-rss) is the closest, and I frankly stole
a lot of concepts from it.  

The biggest difference is that RSS4Transmission is designed for OCD people who 
pull down a lot of different files from the same feed and want them saved to 
different directories.  I wanted something that would be "nice" and only 
read the RSS feed once, even though I've got 10 different categories.

### Configuration


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

### License

RSS4Transmission is licensed under the [GPLv3](LICENSE).
