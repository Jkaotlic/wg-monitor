# Provision token atomicity + agent observability — design

**Date:** 2026-07-13
**Status:** approved (operator)

## Background

`snekhaev` went dark on 2026-07-08 and stayed dark. Live investigation (via
the awg-manager terminal) found the network path fully healthy — DNS resolves,
`curl …/healthz` returns `{"status":"ok","version":"v0.14.2"}` in 0.4s, the
agent process is running — yet every `POST /v1/report` gets **HTTP 401**: the
agent's token in `/opt/etc/wg-monitor/config.yaml` no longer matches the
backend DB. `reporter-state.json` is frozen at the moment the desync began.

Two root causes made this both *happen* and *hard to diagnose*:

1. **Non-atomic token rotation.** The dashboard Provision/Repair path
   `UpsertEnrollment`s (overwrites `token_hash`) synchronously *before* the
   async relay install runs (`provision_handler.go`). On 2026-07-08 the relay's
   `downloading` step failed (DNS was broken that day), so the router never got
   the new `config.yaml` — leaving the DB rotated but the router on the old
   token → permanent silent 401. A failed repair left the agent *worse* than
   before.

2. **The agent's diagnostic screams into `/dev/null`.** The agent already has a
   purpose-built line — `slog.Error("agent auth rejected by backend — token
   likely rotated or revoked … cannot recover without redeploy")`
   ([`client.go`](../../../internal/agent/client.go)) — but its comment assumes
   *journald*. On Keenetic/Entware there is no journald; S99 sends stderr to
   `/dev/null` (confirmed live: `/proc/<pid>/fd/2 -> /dev/null`). The one log
   line that would have made this a 30-second `grep` was discarded, turning it
   into a multi-round live-probing hunt.

This design fixes both so the same class of incident cannot recur silently. A
third, infrastructural root cause (the backend lives behind KeenDNS-cloud on a
home Pi, giving a fleet-facing IP that rotates and collides with fleet routers'
own cloud IPs) is **explicitly out of scope** — see below.

## Goals

- A partial/failed provision or repair must **never** leave the backend DB
  holding a token the router does not have.
- A token-rejected (or otherwise stuck) agent must be **diagnosable in one
  command** from the awg-manager terminal — via both a persisted log file and a
  self-describing state file — even when it cannot phone home.

## Non-goals / out of scope

- **Fix #3 (infra):** moving the backend off KeenDNS-cloud onto a stable
  dedicated public endpoint. Operator decision, tracked separately.
- Changing the agent's heartbeat DNS resolution (no custom-resolver fallback).
- Any change to the wizard-deferred (`cmd/deploy`) provisioning path, which
  already commits the token only after the router-side bootstrap succeeds
  (`commit_existing_agent_token_hash`). This design brings the *dashboard*
  engine path in line with that already-correct behaviour.

---

## Fix #2 — Token atomicity (backend)

### Root cause

`provision_handler.go` mints and persists the token (`UpsertEnrollment` writes
`token_hash`) *before* `engine.Start`. The relay install that actually writes
the token onto the router runs asynchronously and can fail at any step. Any
failure after the DB write but before the router receives `config.yaml` yields
a permanent desync. There is no rollback (the raw token is not stored; the old
hash is overwritten).

### Design: commit the DB token only when the router has persisted it

The engine already streams the relay's `__WG_STEP__` markers and advances the
job step-by-step in `runner.go`'s `onLine`. The `config_written` marker
([`steps.go`](../../../internal/backend/provision/steps.go) `StepConfigWritten`)
is emitted by the relay at exactly the instant the router has written the new
`config.yaml`. That is the correct commit point.

1. **Split mint from persist.** Generate the raw enrollment token *without*
   writing the DB. The existing single-flight mint lock still wraps raw-token
   generation + JobJSON build (no DB write inside it now).

2. **Add `CommitToken func() error` to `provision.StartReq`.** In `runner.go`'s
   `onLine`, when the `config_written` marker is *applied* (guarded by the
   existing `StepPending` check, so it fires at most once), invoke
   `CommitToken`. Capture any error in a worker-local variable; after
   `d.Relay(...)` returns, if the commit errored, `fail()` the job (even on
   `rc==0`) with a new hint.

3. **The `CommitToken` closure is built by the handler**, closing over the
   nickname, the freshly-minted raw token, kind and thread id:
   `func() error { return d.DB.Users().UpsertEnrollment(nick, rawToken, kind, threadID) }`
   (plus the existing DeployInfo write). Per job kind:
   - `KindProvision`, `KindRepairReinstall` → deferred via `CommitToken`.
   - `KindRegister` (mint-only, no relay) → keeps the synchronous
     `UpsertEnrollment` (there is no relay run to gate on).
   - `KindRepairRepoint` (no token rotation) → no `CommitToken` (unchanged).

### Why `config_written` is the right point

- Failure at `downloading` / `checksum_ok` / before `config_written` →
  the marker never fires → `CommitToken` never called → **DB untouched → no
  desync.** This is the exact fix for the 2026-07-08 failure.
- Failure at `service_started` (after `config_written`) → both DB and router
  already hold the new token → consistent; the operator restarts the service,
  no re-provision needed.
- The residual reverse-desync window (router wrote `config.yaml` but the marker
  / commit did not complete) shrinks from "the whole async job including a
  flaky download" to "the instant between the router's write and one local
  sqlite `UPDATE`" — the same small window the wizard two-phase flow already
  accepts.

### Files

- `internal/backend/provision/runner.go` — `StartReq.CommitToken`; `onLine`
  hook on `config_written`; commit-error fail path.
- `internal/backend/provision/steps.go` — new hint for a commit failure (a
  distinct class from the existing step hints).
- `internal/backend/provision_handler.go` — split mint/persist; build the
  `CommitToken` closure for the two relay-driven kinds; `KindRegister` stays
  synchronous.

---

## Fix #1 — Agent observability (agent)

Agent-only. Two independent parts.

### A. Logs to stderr **and** a rotating file

- New `internal/agent/logwriter.go`: a small `io.Writer` that appends to a file
  and rotates by size (at ~1 MiB → rename to `<path>.1`, keep one old file,
  reopen). Pure stdlib, no new dependency.
- `cmd/agent/main.go`: build the slog handler over
  `io.MultiWriter(os.Stderr, logFile)` instead of `os.Stderr` alone. stderr is
  retained so systemd/journald deployments (the VPS backend host, dev) are
  unaffected.
- **Path defaulting:** default `/opt/var/wg-monitor/agent.log`, baked into the
  agent so **already-deployed configs get file logging on the next binary
  update** with no `config.yaml` rewrite. Optional config
  `logging: { file: <path|"">, max_bytes: N }` — empty `file` disables the file
  sink, default on.
- **Best-effort:** if the log file cannot be opened (e.g. read-only rootfs),
  fall back to stderr-only and emit one warning; the agent must never fail to
  start over logging.
- Result: the existing `agent auth rejected …` error line now lands in
  `agent.log`, greppable from the awg-manager terminal.

### B. Auth-reject breadcrumb in the state file

- Extend `reporterState`
  ([`reporter.go`](../../../internal/agent/reporter.go), currently
  `{last_report_at}`) with `last_auth_error_at` and `consecutive_auth_rejects`.
- In `sendOnceLocked`, on a `SendReport` error test
  `errors.Is(err, ErrUnauthorized)`: if so, increment
  `consecutive_auth_rejects`, set `last_auth_error_at`, and persist. On a
  successful report, reset `consecutive_auth_rejects` to 0.
- Result: a dark agent's `reporter-state.json` self-describes, e.g.
  `{"last_report_at":"2026-07-08T…","consecutive_auth_rejects":7000,"last_auth_error_at":"…"}`
  → "token rejected" is obvious from a single `cat`, even with no log file.

### Files

- `internal/agent/logwriter.go` — new rotating writer.
- `cmd/agent/main.go` — MultiWriter wiring; open/rotate/fallback.
- `internal/agent/config.go` — optional `logging` config section.
- `internal/agent/reporter.go` — extend `reporterState`; auth-error accounting
  in `sendOnceLocked`.

---

## Testing

### Fix #2 (provision)
- Fake relay that emits markers up to a chosen step then fails:
  - fails at `downloading` (before `config_written`) → `CommitToken` **not**
    called; job failed; DB `token_hash` unchanged.
  - emits `config_written` then fails at `service_started` → `CommitToken`
    called exactly once; job failed at `service_started` (both sides hold the
    new token, no desync).
  - full success → `CommitToken` called once; job success.
  - `CommitToken` returns an error → job fails with the commit-failure hint.
- Handler-level: a reinstall whose relay fails before `config_written` leaves
  the agent's existing `token_hash` intact (regression test for the exact
  snekhaev desync).

### Fix #1 (agent)
- `logwriter`: writing more than `max_bytes` creates `<path>.1` and caps the
  live file; a fresh writer reopens/append; unopenable path → writer degrades
  to a no-op / stderr fallback without error.
- `reporter`: an `ErrUnauthorized` from `SendReport` increments
  `consecutive_auth_rejects`, sets `last_auth_error_at`, and persists; a
  subsequent success resets the counter to 0; a non-auth error does neither.
- `cmd/agent` wiring: the configured/default log path is included in the slog
  output target (MultiWriter contains the file).

Full `go test ./...`, `go vet`, `gofmt`, and `staticcheck` must stay green.

## Rollout

- **Fix #2** is in the backend binary → requires **redeploying the backend**
  (rpie4). No agent change.
- **Fix #1** is in the agent binary → requires a **fleet redeploy** to take
  effect; already-dark agents (e.g. snekhaev) only gain it once updated. This
  is prevention for the *next* incident, not a cure for the current one.
- Neither fix brings snekhaev back up now — that still needs the one-off token
  re-sync (dashboard Repair re-mint, or a DB `token_hash` update). Fix #2
  ensures the *next* Repair cannot leave the same residue.
