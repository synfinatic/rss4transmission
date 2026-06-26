# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development workflow

**Always use TDD for all changes.** This means:

1. Write a failing test that captures the expected behavior before writing any implementation code.
2. Run `make test` to confirm the test fails for the right reason.
3. Write the minimum implementation to make the test pass.
4. Run `make test` again to confirm it passes.
5. Refactor if needed, keeping tests green throughout.

No implementation change is complete without a corresponding test. If a function is hard to test directly,
restructure it so it can be tested — don't skip the test.

**Never create a git commit unless explicitly asked.** Do not commit as a side effect of completing a
task or fix.

Whenever modifying a markdown file, always wrap lines at 100 characters.

Whenever modifying a markdown file, add an empty line after a paragraph 
before starting an ordered or unordered list.

## Commands

```bash
# Build
make                  # build for current platform → dist/rss4transmission
make build-race       # build with race detector

# Test
make test             # go vet + unit tests
make unittest         # go test ./... only
make test-race        # go test -race ./...

# Run a single test
go test ./cmd/rss4transmission/... -run TestName

# Coverage
make coverage         # run tests with -covermode=atomic, write coverage.out, print total %

# Lint
make lint             # runs golangci-lint
make precheck         # full pre-PR check: test + fmt + tidy + lint

# Security
make vulncheck        # runs govulncheck to detect known vulnerabilities

# Run
make run PROGRAM_ARGS="--help"
go run ./cmd/rss4transmission/... watch --help
```

## Architecture

All code lives in `cmd/rss4transmission/` as a single `main` package. There are no sub-packages.

**Entry point & wiring (`main.go`)**: Parses CLI with `kong`, loads config via `koanf`, opens the
seen-cache, validates feed and extractor configs, creates the `transmissionrpc.Client`, then delegates
to a subcommand. The `RunContext` struct is threaded through every command — it holds the live config,
cache, and Transmission client.

**Two commands**:
- `once` (`once.go`): Groups feeds by URL so each RSS endpoint is fetched exactly once. For each feed,
  applies title-level `Require` filtering to get candidates, fetches each candidate's `.torrent` file,
  extracts labels from file names, unions title + file labels, groups candidates by identity key, picks
  highest-preference winner per key (cross-referenced against cache), and submits winners to Transmission
  via `MetaInfo` (base64 bytes). Supports a `--feed` filter to limit processing to named feeds.
  Dispatches matching items via Transmission RPC, local `.torrent` download, or interactive `promptui`
  mode.
- `watch` (`watch.go`): Wraps `once` in a ticker loop. Also watches the config file for live reloads
  (`koanf` file provider). Optionally manages a Gluetun VPN tunnel.

**Feed filtering (`config.go`, `feed.go`)**: All feeds share `Exclude` (raw regex pre-filter, applied
before anything else) and `MinSize`/`MaxSize`. Label mode adds: `Extractor` references a named extractor
set, `Identity` declares the dedup key labels, `Groups` declares per-group `Require` constraints,
`Prefer` declares ordered preference dimensions. `Regexp` and `Categories` have been removed.
`Feed.Validate()` enforces required label-mode fields at startup.

**Label extraction (`extractor.go`)**: `ExtractorSet` is a named map of `Label` definitions. Each
`Label` has a single-capture `Regexp` and a `Normalize` map (regex keys → canonical values) applied to
the raw match. The same extractor is applied to both RSS item titles and torrent file names. Missing
identity labels cause a candidate to be skipped for that group; missing preference labels rank as lowest
preference.

**Selection (`select.go`)**: For each identity key, candidates are ranked lexicographically by the
`Prefer` dimension list. The winner is the highest-ranked candidate not already in the cache at equal or
higher preference. A torrent covering multiple identity keys (multi-class bundle) is submitted once but
recorded against all covered keys in the cache.

**Seen-cache (`cache.go`)**: JSON file (`SeenFile`) recording every torrented item. Each `CacheRecord`
stores the raw extracted label map and the list of covered identity keys. Entries older than
`SeenCacheDays` are pruned on save. An in-memory **identity index**
(`map[identityKey]bestPreferenceLabels`) is rebuilt on load — never persisted directly. Tracks per-GUID
error hold-downs to avoid spamming retries.

**Gluetun integration (`gluetun.go`)**: Optional VPN management for the Gluetun sidecar. After each
`once` run, `CheckVpnTunnel()` may restart the VPN (via Gluetun's REST API) and sync the forwarded peer
port into Transmission's session settings.

**History web UI (`web.go`)**: The `watch` command accepts `--history-port` (env `HISTORY_PORT` in
Docker). When set to a non-zero port, a small HTTP server is started on `127.0.0.1:<port>` serving a
browsable history of torrented items. Disabled by default (`HISTORY_PORT=0`). In the gluetun
docker-compose, expose the port explicitly via the `ports:` block; in the plain docker-compose the
`network_mode: host` already exposes all ports.

**Config defaults** are defined as a `map[string]interface{}` in `config.go` and loaded before the YAML
file, so koanf's merge semantics provide defaults without nil-checks in code.

**Version metadata** (`Version`, `Tag`, `CommitID`, etc.) is injected at build time via `-ldflags` in
the Makefile.

## Key dependencies

| Package | Purpose |
|---|---|
| `github.com/alecthomas/kong` | CLI parsing |
| `github.com/knadh/koanf/v2` | Config loading with live-reload |
| `github.com/hekmon/transmissionrpc/v3` | Transmission RPC client |
| `github.com/mmcdole/gofeed` | RSS/Atom feed parsing |
| `github.com/sirupsen/logrus` | Structured logging |
| `github.com/manifoldco/promptui` | Interactive TUI prompts |
| inline bencode decoder (`torrent.go`) | Parse `.torrent` files for file list extraction — no external dependency |

## Lint configuration

golangci-lint v2 config is in `.golangci.yaml`. Extra linters enabled beyond defaults: `asciicheck`,
`dupl`, `gocyclo`, `gosec`, `misspell`, `whitespace`. The `gofmt` formatter is enforced — run
`make fmt` before committing.
