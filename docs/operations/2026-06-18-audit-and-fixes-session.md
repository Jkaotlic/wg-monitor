# Session — full audit + HR-Neo fix + audit remediation (2026-06-18)

> Branch `main`, started at HEAD `23470cfd` (tag `v0.13.0-rc133`), ended at `218d3c3e`.
> Nothing pushed to remote yet (commits are local on `main`). Live router access was
> available this session via the operator's WireGuard tunnel to the home LAN (192.168.31.x).

## 1. Full production-readiness audit

- Report: [docs/audit-2026-06-18.md](../audit-2026-06-18.md). Charter:
  [docs/superpowers/specs/2026-06-18-full-audit-charter-design.md](../superpowers/specs/2026-06-18-full-audit-charter-design.md).
- Verdict: **conditional GO**. 5 of 6 prior blockers verified closed; method was
  6 parallel Explore recons → personal file:line verification → ground-truth runs
  (`go build/vet/test/govulncheck` all green; `-race` only in CI, no local gcc).
- Findings: SEC-01..05, BE-01/02, DEP-01 (see report for file:line + scenarios).

## 2. HR-Neo "wrong interface" bug (reported by operator, fixed + committed)

- Symptom: routes panel showed HR-Neo policy routes bound to `nwg0` (amnezia), but
  traffic actually egresses `nwg3` (NetherlandsAmsterdamH17).
- Root cause (confirmed on live testkeen router): HR-Neo policy rules have **empty
  `hrPolicyInterfaces`** (global default), so `buildRouteSnapshot` bound them to
  `defaultIface` = the **first** tunnel with `defaultRoute=true`. TWO tunnels carry
  `defaultRoute=true`; the real active default is in `settings.json`
  `download.routeTag="awg-awg12"` (→ awg12 → nwg3) and kernel `table 4098 = default dev nwg3`.
- Fix `421fd2d6`: agent reads `GET /api/settings/get` `download.routeTag`, binds
  fall-through routes to that tunnel; legacy first-defaultRoute fallback if absent.
- Fix `00bb4ed1`: tunnel import (`addIfaceToHydraRoutePolicies`) no longer rewrites
  **empty/global-default** HR-Neo policies to `[newIface]` (would hijack all traffic
  onto the freshly imported tunnel). It only appends to policies with an explicit
  chain (new tunnel = fallback). Operator decision: leave empty/global policies alone.

## 3. Audit remediation — all 8 findings addressed (each TDD, committed to main)

| Finding | Commit | Notes |
|---|---|---|
| SEC-01 | `761251c1` | drop `v0.13.0` signature carve-out; rc<128 stays exempt (legacy) |
| DEP-01 | `fc3b2396` | restore script trap → restore `.bak` + restart old version on post-stop failure |
| BE-01 | `aac689db` | incremental auto-vacuum (one-time convert, then `PRAGMA incremental_vacuum`) |
| BE-02 | `5cfd84ea` | `spawnRelay` semaphore (limit 32, drop+warn on saturation) in handler.go |
| SEC-04 | `dc6946db` | `applyGitHubAPIHeaders` adds Bearer from `GITHUB_TOKEN`/`GH_TOKEN` |
| SEC-05 | `25ca238c` | dashboard session epoch in HMAC (cookie v2→v3); `RotateDashboardSessions()` |
| SEC-03 | `8418a100` | perms already 0600/0700; documented FDE/LUKS requirement (no co-located crypto) |
| SEC-02 | `218d3c3e` | TOFU cert-pin AWG Manager (Go client HTTP+WS), store `<cache>/awgm_pins` |

## 4. Open / parked (pick up here in the new chat)

1. **Deploy fixed agent to router for live e2e** — PARKED (operator: "пока не деплоим").
   When ready: build arm64 agent, deploy to testkeen, trigger `route_status`, confirm
   panel shows `nwg3`. (Deploy = state change on prod router; needs explicit go-ahead.)
2. **SEC-02 Python relay TOFU** — follow-up. Relay already default-secure (insecure
   behind a flag); full TOFU needs fingerprint distribution VPS↔operator + embedded-Python
   verification. Not done to avoid rushing the agent-bootstrap path.
3. **Optional:** annotate `docs/audit-2026-06-18.md` with FIXED statuses per finding.
4. **Push** — all session commits are local on `main`; not pushed yet.

## 5. Live router access (for continuation)

- Reachable over operator WG: router `192.168.31.1` (SSH 222, root — testkeen pw in
  [[feedback_awgmgr_api]] memory), backend VPS `192.168.31.87` (not reachable this session).
- Read AWGM status: `ssh` then `curl -H "X-Requested-With:XMLHttpRequest" http://127.0.0.1:2222/api/...`
  (authEnabled=false on localhost). Key endpoints: `settings/get`, `dns-routes/list`,
  `routing/tunnels`, `tunnels/all`, `system/hydraroute-status`.

## 6. Verify (clean tree, this session)

```
CGO_ENABLED=0 go build ./...    # OK
CGO_ENABLED=0 go vet ./...      # clean
CGO_ENABLED=0 go test ./...     # all pass
```
