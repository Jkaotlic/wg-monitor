# Dependency Audit — wg-monitor

**Date:** 2026-05-10
**Module:** github.com/anex/wg-monitor
**Go version (local):** 1.26.2 (windows/amd64) — go.mod declares `go 1.26.2`
**Go version (CI):** `actions/setup-go@v5` with `go-version: '1.26'` (resolves to latest 1.26.x at build time)
**Tools used:** `go list -u -m all`, `go mod why -m`, `go mod tidy`, `govulncheck v1.3.0`

---

## Executive summary

- **No CVEs found in any third-party dependency** via govulncheck (DB at 2026-05-10).
- **2 stdlib CVEs trigger on local toolchain** (Go 1.26.2) — fixed in Go 1.26.3. CI is fine because `go-version: '1.26'` auto-bumps to the latest 1.26.x patch on each build, but the `go 1.26.2` directive in `go.mod` is a stricter floor.
- **5 additional stdlib vulnerabilities** present in modules-imported but not reached by call graph; still resolved by Go 1.26.3.
- **No `replace` directives**, no pre-release/v0 critical deps, no manual `direct vs indirect` mismatches in `go.mod`. `go mod tidy` is clean.
- All direct deps are at most 1 minor behind latest releases (a healthy place to be).
- **Telegram dep is hand-rolled** (`internal/backend/tg`) — no `go-telegram-bot-api` import, intentional choice documented in client.go header.

---

## Findings

### DEP-01 [Critical] Go toolchain pinned to 1.26.2 — 2 active stdlib CVEs

`go.mod` line 3 declares `go 1.26.2`. govulncheck reports **two stdlib vulnerabilities reached by symbols in our code**:

| CVE | Description | Affected call sites |
|-----|-------------|---------------------|
| GO-2026-4971 | Panic in `net.Dial` / `net.LookupPort` on NUL byte (Windows) | `cmd/deploy/update_components.go:299` (`probeReachable` → `net.DialTimeout`); `internal/agent/checks/dns_plain.go:26` (`ProbePlainDNS` → `net.Dialer.DialContext`); `cmd/backend/main.go:163` (`http.Server.ListenAndServe` → `net.Listen`) |
| GO-2026-4918 | Infinite loop in HTTP/2 transport on bad `SETTINGS_MAX_FRAME_SIZE` | `internal/backend/tg/client.go:234` (`Client.DownloadFile` → `http.Client.Do`); `cmd/deploy/github.go:162` (`Downloader.fetchExpectedSha` → `http.Client.Get`) |

Both fixed in Go **1.26.3**.

**Fix:** Bump `go` directive in `go.mod` to `1.26.3` (or omit patch and use `1.26`). Optionally pin `toolchain go1.26.3` for reproducible local builds.

### DEP-02 [High] Six dormant stdlib CVEs in 1.26.2 (not reached, but on imported package level)

govulncheck flagged but did not find call-site reachability:

| CVE | Description |
|-----|-------------|
| GO-2026-4981 | `net` crash on long CNAME response |
| GO-2026-4986 | Quadratic concat in `net/mail.consumeComment` |
| GO-2026-4982 | meta-content URL escaping bypass → XSS in `html/template` |
| GO-2026-4980 | Escaper bypass → XSS in `html/template` |
| GO-2026-4977 | Quadratic concat in `net/mail.consumePhrase` |
| GO-2026-4976 | `net/http/httputil.ReverseProxy` forwards excess query params |

Same fix as DEP-01: bump to Go 1.26.3.

### DEP-03 [Low] CI workflow Go version not pinned to a patch

`.github/workflows/release.yml:31` uses `go-version: '1.26'`. This silently inherits the latest 1.26.x at workflow run time. Combined with `go.mod` floor `1.26.2`, today's CI builds will use **whatever 1.26.x setup-go ships with** — fine in practice but non-reproducible.

**Fix (optional):** pin to `'1.26.3'` once go.mod is bumped, OR add `toolchain go1.26.3` to go.mod and rely on Go's auto-download. Decide consciously.

### DEP-04 [Low] All direct deps trail latest by one minor

| Package | Current | Latest | Notes |
|---|---|---|---|
| `golang.org/x/crypto` | v0.50.0 | v0.51.0 | SSH library — used by `cmd/deploy/ssh.go`. No CVE in 0.50.0 per govulncheck. Routine update. |
| `golang.org/x/mod` | v0.33.0 | v0.36.0 | semver helper. 3 minors behind. |
| `golang.org/x/net` | v0.53.0 | v0.54.0 | dnsmessage only — no CVE in 0.53.0. |
| `golang.org/x/sync` | v0.20.0 | (latest) | up-to-date as of v0.20.0 in current list. |
| `golang.org/x/sys` (indirect) | v0.43.0 | v0.44.0 | |
| `golang.org/x/term` | v0.42.0 | v0.43.0 | |
| `gopkg.in/yaml.v3` | v3.0.1 | v3.0.1 | **Latest** — current major. No update available. |
| `modernc.org/sqlite` | v1.50.0 | v1.50.0 | **Latest**. |
| `github.com/BurntSushi/toml` | v1.6.0 | v1.6.0 | **Latest**. |
| `modernc.org/libc` (indirect) | v1.72.0 | v1.72.3 | Patch only. |
| `modernc.org/cc/v4` (indirect) | v4.27.3 | v4.28.2 | |

**Fix:** Run `go get -u ./... && go mod tidy` periodically. None of these are urgent.

### DEP-05 [Info] modernc.org/sqlite — pure-Go, intentional (not mattn/go-sqlite3)

`internal/backend/db/db.go:2` documents the choice: pure-Go translation of SQLite, no cgo, cross-compiles cleanly. Used as side-effect import (`_ "modernc.org/sqlite"`). This is the right call for a project that builds for `mipsle` agents (CGO unavailable). **Keep as-is.**

### DEP-06 [Info] Telegram client hand-rolled — no library dep

`internal/backend/tg/client.go:1-6` explicitly states: "We picked this over `go-telegram-bot-api/v5` to keep the Stage 1 dep tree lean — full library will earn its place when callbacks land in Stage 2." The library is **not in go.mod**.

The wider concern (the `go-telegram-bot-api` upstream is abandoned) is currently moot. Should the team revisit this in Stage 2/3, evaluate maintained forks like `github.com/OvyFlash/telegram-bot-api` or `github.com/mymmrac/telego` instead of resurrecting the original.

### DEP-07 [Info] No `replace` directives, no v0 critical deps, no manual edits

- `go mod why -m` confirms every direct dep in the `require` block is genuinely imported (cmd/deploy uses x/crypto, x/term, BurntSushi/toml, yaml.v3; agent uses x/net, x/sync; backend uses x/mod, modernc/sqlite).
- `go mod tidy -v` ran clean — no missing/superfluous entries.
- No `replace` directives, no pre-release versions of critical deps, no v0 deps that should worry us (the `v0.x.x` golang.org/x/* line is the upstream's normal versioning scheme — semver-compatible per Go module conventions).

---

## Recommended action order

1. **Bump `go` directive in `go.mod` from `1.26.2` to `1.26.3`** (or `1.26`) and re-run govulncheck — clears DEP-01 and DEP-02 in one move.
2. Optionally pin `.github/workflows/release.yml` `go-version` to the same exact patch for reproducible builds (DEP-03).
3. Schedule routine `go get -u ./... && go mod tidy` for the next release cycle (DEP-04).
4. Add `govulncheck` to CI as a non-blocking job; promote to required once dependency hygiene is steady-state.

---

## Appendix: tool outputs

- `go list -u -m all` — see DEP-04 table.
- `govulncheck -show verbose ./...` — 2 symbol-level + 1 package-level + 5 module-level stdlib vulns; **0 third-party**.
- `go mod tidy -v` — silent (no changes).
- `go mod why -m <each direct dep>` — every direct dep is reached.
