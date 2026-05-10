# Deployment / IaC Audit — wg-monitor

Scope: `.github/workflows/release.yml`, systemd unit, Keenetic init.d, Caddyfile, backend.yaml.tmpl.
Severity scale: **CRIT** > **HIGH** > **MED** > **LOW** > **INFO**.

---

## CI / Release pipeline (`.github/workflows/release.yml`)

### DEPLOY-01 — HIGH — Action versions are not SHA-pinned
`release.yml:27,29,56,68,80`
All `uses:` lines reference floating tags (`actions/checkout@v4`, `actions/setup-go@v5`, `actions/upload-artifact@v4`, `actions/download-artifact@v4`, `softprops/action-gh-release@v2`). A tag can be force-moved by a compromised maintainer; the `release` job has `contents: write`, so a poisoned action can publish arbitrary artifacts under your tag. **Fix:** pin to commit SHA + comment with the tag (`uses: actions/checkout@b4ffde65...   # v4.2.2`).

### DEPLOY-02 — MED — Top-level `permissions:` is missing
`release.yml:8` (job `build`)
The `build` job runs with the default `GITHUB_TOKEN` permissions of the repo (potentially `read-write` if repo default is "permissive"). Add a top-level `permissions: contents: read` and only grant `contents: write` on the `release` job (currently done — keep it). Make the rest least-priv at workflow root.

### DEPLOY-03 — MED — Go toolchain pinned only to minor (`1.26`)
`release.yml:31`
`setup-go` with `'1.26'` resolves to the latest patch at build time, so two consecutive tag pushes can produce binaries built with different toolchains. `go.mod` declares `go 1.26.2`. **Fix:** use `go-version-file: go.mod` (preferred — single source of truth) or pin patch (`'1.26.2'`). Also enables Go module/build cache via `setup-go`'s built-in cache (currently disabled — no `cache: true`).

### DEPLOY-04 — MED — No Go build/module cache
`release.yml:29-31`
`setup-go@v5` defaults `cache: true` only if `go.sum` is found in `working-directory`. Verify, or explicitly add `cache: true` and `cache-dependency-path: go.sum`. With 7 matrix legs every run does a cold module download — slow, costs free-tier minutes, and can fail when proxy.golang.org is flaky.

### DEPLOY-05 — LOW — `generate_release_notes: true` already on, good
`release.yml:83-84`
`fail_on_unmatched_files: true` and `generate_release_notes: true` are correctly set. **No action.**

### DEPLOY-06 — MED — UPX on agent binary may trip antivirus / breaks debug
`release.yml:23-24,52-54`
UPX-packed ARM/MIPS agents trip Windows Defender / ClamAV heuristics ("Suspicious.UPX"); also breaks `strings`, gdb, perf-symbol resolution and prevents `setcap`/file-integrity tooling. On Keenetic this is mostly fine, but document the trade-off in README, and consider `upx --no-lzma` or skipping UPX for `arm64` (only mipsle truly benefits — Keenetic flash size). **Fix:** drop UPX on `arm64`; keep only for `mipsle`. Also add `--lzma` is fine but `upx -t dist/<name>` smoke-test step is missing — a corrupted pack will only be discovered by users.

### DEPLOY-07 — LOW — No release artifact signing
`release.yml:79-84`
Checksums published, but no signature (cosign / minisign / GPG). For a deploy wizard that downloads its own binary updates this matters: any GitHub-account compromise replaces binaries silently. **Recommendation:** add `cosign sign-blob --yes dist/*` step (keyless OIDC, no secret to leak).

### DEPLOY-08 — INFO — Missing GOOS/GOARCH coverage
`release.yml:13-24`
Wizard: linux/amd64, win/amd64, darwin/amd64, darwin/arm64. **Missing:** linux/arm64 (Apple-Silicon Linux dev boxes, Raspberry Pi running the wizard), windows/arm64 (Surface). Backend: only linux/amd64 — fine as the VPS profile is fixed. Agent: arm64 + mipsle. **Missing:** mips (big-endian — older Keenetics), arm (32-bit — Keenetic Lite/Start). Verify against actual hardware spread.

### DEPLOY-09 — LOW — `if-no-files-found: error` good, but no SBOM
`release.yml:60`
Add an SBOM step (`anchore/sbom-action`) and attach to release — useful for downstream consumers and CVE scanning of the release.

---

## systemd unit (`cmd/deploy/templates/wg-monitor-backend.service`)

### DEPLOY-10 — HIGH — Missing core hardening directives
`wg-monitor-backend.service:15-22`
Present: `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `ReadWritePaths`, `LimitNOFILE`. **Missing all of:**

```
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
ProtectProc=invisible
ProcSubset=pid
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources @mount
CapabilityBoundingSet=
AmbientCapabilities=
UMask=0077
```

The backend is a pure-Go HTTP service binding 127.0.0.1:8080 + SQLite — none of these will break it. `systemd-analyze security wg-monitor-backend.service` will currently report ~5+ ("MEDIUM") and trivially go below 2.0 ("OK").

### DEPLOY-11 — MED — No memory / task accounting & limits
`wg-monitor-backend.service:7-13`
Missing `MemoryAccounting=true`, `MemoryMax=512M` (or similar), `TasksMax=256`, `CPUAccounting=true`. A goroutine leak or runaway query → host OOM. Set conservative caps so OOM-killer targets the unit, not the kernel.

### DEPLOY-12 — MED — `Restart=on-failure` misses clean-exit crashes
`wg-monitor-backend.service:12-13`
`on-failure` skips exit code 0 and SIGTERM-style exits. Use `Restart=always` with `RestartSec=5s` plus `StartLimitIntervalSec=300 StartLimitBurst=5` to avoid flap-loop and to recover from clean panics that exit 0 due to deferred cleanup.

### DEPLOY-13 — LOW — No `WatchdogSec`
`wg-monitor-backend.service:7-13`
With the existing `/healthz` endpoint, wiring `WatchdogSec=60` + `sd_notify("WATCHDOG=1")` from a small healthcheck goroutine gives systemd auto-restart on hung process. Optional but cheap.

### DEPLOY-14 — LOW — `Type=simple` — prefer `Type=notify`
`wg-monitor-backend.service:8`
`simple` reports "started" the instant fork succeeds — dependencies starting `After=` see the unit ready before the listener binds. `notify` (with `coreos/go-systemd/daemon.SdNotify(false, "READY=1")` after `Listen()`) closes that race. Minor.

---

## Keenetic init.d (`cmd/deploy/templates/S99wg-monitor`)

### DEPLOY-15 — MED — `rc.func` SIGTERM→SIGKILL behavior depends on Entware version
`S99wg-monitor:12`
Defers entirely to `/opt/etc/init.d/rc.func`. Older Entware `rc.func` versions `kill -TERM` and immediately `kill -KILL` without grace, or vice-versa — race where SQLite WAL is mid-checkpoint when SIGKILL hits → DB corruption (especially relevant on flash storage with no `fsync` ordering guarantees). **Fix:** override stop with explicit grace window:

```sh
preshutdown() {
    if [ -n "$(pidof $PROCS)" ]; then
        kill -TERM $(pidof $PROCS) 2>/dev/null
        for i in 1 2 3 4 5 6 7 8 9 10; do
            sleep 1; pidof $PROCS >/dev/null || break
        done
        pidof $PROCS >/dev/null && kill -KILL $(pidof $PROCS)
    fi
}
```

### DEPLOY-16 — MED — No log redirection — agent log location is implicit
`S99wg-monitor:7`
`ARGS` does not pipe to a logfile. The diag script reads `/opt/var/log/wg-monitor.log`, but nothing here writes there. Either the agent self-rotates internally (verify in code), or logs go to syslog via Entware's wrapper. **Action:** confirm and document; add `LOGFILE=/opt/var/log/wg-monitor.log` and pre-rotate on size (Entware has `logrotate` opkg).

### DEPLOY-17 — LOW — `PROCS=wg-monitor` may collide with backend binary
`S99wg-monitor:5`
`pidof wg-monitor` matches any process whose argv[0] basename starts with "wg-monitor". Agent binary should be named `wg-monitor-agent` and `PROCS` updated, otherwise running both backend and agent on the same host (unusual but possible during dev) causes `stop` to kill the wrong PID.

### DEPLOY-18 — LOW — No PID file path declared
`S99wg-monitor:1-13`
`rc.func` defaults derive PID from `pidof` — fragile under dual-binary scenarios above. Setting `PIDFILE=/opt/var/run/wg-monitor.pid` and writing it from the agent itself eliminates the race.

---

## Caddyfile (`cmd/deploy/templates/Caddyfile.tmpl`)

### DEPLOY-19 — HIGH — No security headers
`Caddyfile.tmpl:9-21`
Missing `Strict-Transport-Security`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY` (or `Content-Security-Policy: frame-ancestors 'none'`), `Permissions-Policy`. The backend serves no HTML today, but it serves JSON for the wizard — bot tokens / chat IDs in error responses can leak via XS-Leaks if a future endpoint adds HTML. **Fix block:**

```
header {
    Strict-Transport-Security "max-age=63072000; includeSubDomains; preload"
    X-Content-Type-Options nosniff
    Referrer-Policy no-referrer
    Cross-Origin-Opener-Policy same-origin
    -Server
}
```

### DEPLOY-20 — MED — No TLS min-version / cipher policy
`Caddyfile.tmpl:9-21`
Caddy's default is TLS 1.2+ which is fine, but explicit `tls { protocols tls1.2 tls1.3 }` makes intent auditable and survives Caddy default changes. Consider also `tls { ciphers TLS_AES_...}` for strict deployments.

### DEPLOY-21 — MED — No rate-limiting / abuse protection
`Caddyfile.tmpl:9-21`
No `caddy-ratelimit` plugin, no `caddy-defender`. Agents POST heartbeats freely; with bearer-token auth (assumed) a leaked token spammed at thousands of req/s → DB write contention → SQLite WAL bloat. **Fix:** add `caddy-ratelimit` (e.g. 60 r/min per IP for non-auth endpoints; per-token for `/heartbeat`).

### DEPLOY-22 — LOW — `auto_https disable_redirects` is intentional?
`Caddyfile.tmpl:5`
This disables the HTTP→HTTPS 301. If the wizard intentionally only listens on 443 (TLS-ALPN-01), fine — but verify port 80 isn't expected to redirect; users typing the bare domain will get a connection refused. Document or remove.

### DEPLOY-23 — LOW — `header_up X-Real-IP {remote_host}` — also need X-Forwarded-For chain handling
`Caddyfile.tmpl:11-12`
`X-Real-IP` is set; `X-Forwarded-For` is appended automatically by Caddy. If backend ever logs client IP, prefer `{remote}` for IPv6 correctness over `{remote_host}` (loses port; usually fine). Also, no `trusted_proxies private_ranges` — if backend adopts `RealIP` middleware later it'll trust whatever upstream sends.

### DEPLOY-24 — LOW — `request_body max_size 1MB` good, but no per-route variation
`Caddyfile.tmpl:14-16`
1 MB blanket cap is reasonable for JSON heartbeats. Future binary uploads (firmware push?) will hit this — split routes if needed.

### DEPLOY-25 — INFO — Caddy logs to stderr → journald
`Caddyfile.tmpl:17-20`
`output stderr` + systemd-managed Caddy means logs go to journald. `format console` is human-friendly but harder to ship to a SIEM — consider `format json` for prod.

---

## Backend config (`backend.yaml.tmpl`)

### DEPLOY-26 — INFO — `listen: 127.0.0.1:8080` + Caddy proxy: correct
`backend.yaml.tmpl:1`, `Caddyfile.tmpl:10`
Loopback-only bind with Caddy reverse-proxy — correct posture. **No action.**

### DEPLOY-27 — LOW — `log_level: info` is correct for prod
`backend.yaml.tmpl:2`, `internal/backend/config.go:109-110`
Default is `info` if unset; template explicitly sets it. **No action.** Consider exposing `log_format: json|text` knob — `cmd/backend/main.go:43` already uses JSONHandler unconditionally, which is fine for prod but unfriendly during local debug.

### DEPLOY-28 — INFO — `bot_token_file` separated from main config — good
`backend.yaml.tmpl:11`
Token file mode 0600 owned by `wgmonitor`, main YAML can stay readable. Best practice. **No action.**

---

## Cross-cutting concerns

### DEPLOY-29 — HIGH — No backup strategy for `/var/lib/wg-monitor/state.db`
SQLite WAL DB is the single source of truth for users, agents, alert state, retention history. Loss = full re-onboard. **Fix:**
- Add a `wg-monitor-backend.timer` + service running `sqlite3 state.db ".backup /var/lib/wg-monitor/backups/state-$(date +%F).db"` daily, with `tmpfiles.d` retaining 7 days.
- Document restore procedure.
- Wizard menu item "Backup now" / "Restore from backup".

### DEPLOY-30 — HIGH — No external watchdog / monitoring of the monitor itself
The monitor monitors agents, but if backend is down, no one notices except by absence of alerts. **Fix:**
- Add a cron-driven `curl https://$DOMAIN/healthz | grep ok` from a second host (or uptime-kuma / healthchecks.io / dead-man-snitch).
- Document this in deploy wizard ("Step 7: register external uptime probe").
- Optional: have agents also alert (out-of-band, via direct TG) if backend is unreachable for >N minutes.

### DEPLOY-31 — MED — No log rotation policy documented
- **VPS backend:** logs → stdout → journald → `journald.conf` defaults (4 weeks, 10% disk). Probably OK, but verify `SystemMaxUse=` is set conservatively in deploy wizard so journal doesn't fill /var.
- **VPS Caddy:** stderr → journald, same.
- **Keenetic agent:** unclear (see DEPLOY-16) — flash wear-out risk if writes are unbounded.

### DEPLOY-32 — MED — No locale / ASCII-path verification in templates
`backend.yaml.tmpl`, `Caddyfile.tmpl`, `agent.yaml.tmpl` — all paths are ASCII (`/etc/wg-monitor`, `/var/lib/wg-monitor`, `/opt/etc/wg-monitor`). **Good.** But the wizard runs on Windows and may pass user input (`{{.Domain}}`, `{{.Email}}`) that contains non-ASCII (IDN domains, Cyrillic emails). `Caddyfile.tmpl:9` `{{.Domain}}` is a Caddy site address — Caddy needs the punycode form for IDNs. **Fix:** in deploy wizard, IDN-encode domain before substitution; reject non-ASCII email or convert.

### DEPLOY-33 — LOW — UPX-packed binaries fail integrity verification by some EDRs
Already covered in DEPLOY-06; cross-cuts deployment because field operators may have corporate AV stripping the agent on transfer to Keenetic via the wizard's [4] Add Router SSH copy. Mitigate by SHA256-verifying after upload (smoke step probably already does — verify).

### DEPLOY-34 — INFO — `Documentation=https://github.com/anex/wg-monitor`
`wg-monitor-backend.service:3`
Verify this URL is correct — repo owner per release.yml is `${{ github.repository_owner }}` (dynamic), but unit hard-codes `anex`. If repo gets transferred / forked org, this becomes stale.

### DEPLOY-35 — LOW — Caddy email `{{.Email}}` — no validation
`Caddyfile.tmpl:5`
ACME registration uses this. If wizard allows empty / invalid, Caddy startup fails silently for cert renewal. Wizard should require RFC-5322 syntactic validity at input time.

---

## Summary counts

| Severity | Count |
|----------|-------|
| CRIT     | 0     |
| HIGH     | 5     |
| MED      | 12    |
| LOW      | 13    |
| INFO     | 5     |

Top-3 to act on first: **DEPLOY-10** (systemd hardening), **DEPLOY-19** (Caddy security headers), **DEPLOY-29** (DB backup).
