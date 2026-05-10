# API Contract Audit — wg-monitor

**Date:** 2026-05-10
**Scope:** HTTP API between agent (router) and backend (VPS).
**Files:** `internal/backend/handler.go`, `internal/backend/auth.go`, `pkg/wire/*`, `internal/agent/client.go`, `internal/backend/cmd/queue.go`, `cmd/backend/main.go`.
**Endpoints:** `GET /healthz`, `POST /v1/report`, `GET /v1/cmd?wait=N`, `POST /v1/cmd/result`.

---

## Executive summary

The API surface is small (4 endpoints), the wire types live in a single shared package (`pkg/wire`) imported by both agent and backend, and auth is uniform Bearer-token middleware. That's the good news: agent/backend cannot drift on Go-level type definitions because they compile against the same package. JSON tag changes, however, *can* drift across versions if agent and backend release cadence diverges (rc-chain pattern observed in v0.11). No OpenAPI doc exists.

The medium-pain issues are: (a) **no idempotency** on `/v1/report` — a TCP retry duplicates events in the DB; (b) **error responses are inconsistent** (plain text from `http.Error`, no JSON envelope); (c) **status code semantics are off** for some validation paths (400 used where 422 fits, agent-token-missing returns 401 even when the token format is malformed); (d) **no per-token rate limit** — a misbehaving agent can flood the events table; (e) **no `ReadTimeout`/`WriteTimeout`** on `http.Server`, only `ReadHeaderTimeout` — slow-loris-style attacks on the body of `/v1/report` are not capped (long-poll on `/v1/cmd` correctly bounded to 60s by handler logic).

The low-pain issues are: missing `Content-Type` validation on POST endpoints, no Accept-header negotiation, no `MaxBytesReader` on `/v1/cmd` GET response decoding (agent side), no API versioning policy documented (`/v1/*` is the prefix but there's no doc on what triggers a `/v2/`), no schema versioning inside `wire.Report`.

The high-pain item is conceptual: **`pkg/wire` JSON tags are the de-facto contract, but there's nothing preventing a backend running new tags from rejecting an old agent.** Since rollout is "deploy backend → restart agents one-by-one," there's a narrow but real window where v0.10 agents talk to v0.11 backend. Any field rename in `wire.Check` or `wire.Report` would silently degrade reporting (untagged fields parse as zero-values) — there is no test gate.

---

## Findings

### API-01 [High] No idempotency on POST /v1/report — TCP retry duplicates events

`reportHandler` (`internal/backend/handler.go:92-153`) calls `d.DB.Events().Insert(...)` (`internal/backend/db/events.go:15-21`) with no dedup key. The agent's `Client.SendReport` (`internal/agent/client.go:32-55`) wraps the request in `c.http.Do(req)` with the standard library default transport — Go's `net/http` will **silently retry idempotent requests** on connection-reset, but POST is not retried by the transport. However, the agent reporter's outer scheduler retries on transient errors, and any operator-driven scenario (proxy, VPN reconnect, mobile network glitch) can cause the agent to perceive a timeout while the backend has already committed.

Result: a single check fail/recovery transition can be inserted twice, kicking the FSM into a wrong state (`state.Apply` is called per-event in `handler.go:141`). Soft-alert thresholds may also debounce wrongly because `prev` from `state.Get` doesn't carry occurrence counts.

**Recommendation:**
- Add a request-level idempotency key. Cheapest option: derive a deterministic `event_uid = sha256(userID|check_name|ts_unix_ms)` and add a UNIQUE index on `events.(user_id, check_name, ts)` — `INSERT OR IGNORE` becomes the dedup primitive (modernc.org/sqlite supports it).
- Alternatively, add a header `Idempotency-Key: <uuid>` per report; backend stores the last N keys per user in memory (TTL=10min).
- Document the agent's retry policy in `internal/agent/reporter.go` so future changes don't regress.

**File:** `internal/backend/handler.go:127-132`, `internal/backend/db/events.go:15-21`

---

### API-02 [High] Inconsistent error format — plain text via http.Error, no JSON envelope

Every error path in `handler.go` uses `http.Error(w, "method not allowed", 405)` etc., which writes `Content-Type: text/plain; charset=utf-8`. The success path on `/v1/cmd` returns `application/json`. Agent-side parsing (`internal/agent/client.go:50,80`) only logs `preview, _ := io.ReadAll(...)` — it does NOT parse error JSON. So today this is "consistent enough by accident": every error is plain text on both sides, and the agent treats it as opaque.

This becomes a problem the moment a third client appears (admin UI, a scripted CLI, an external alerting tool) — they need machine-readable errors. Also, `cmdResultHandler:228` returns `"invalid status"` for an enum violation with status 400, but `"id required"` (also 400) and `"bad json"` (400) — the client cannot distinguish "your client sent garbage" from "your enum value is wrong".

**Recommendation:**
- Define `wire.Error{Code string \`json:"code"\`, Message string \`json:"message"\`}` and a single `func writeJSONError(w, code int, errCode, msg string)` helper.
- Codes like `bad_json`, `id_required`, `invalid_status`, `body_too_large`, `unauthorized`, `method_not_allowed`. Document them in a one-page `pkg/wire/errors.md`.
- Migrate handlers in one PR; agent side can ignore the code field — only third parties care.

**File:** `internal/backend/handler.go:95,100,104,109,169,176,194,212,216,221,225,229,236`, `internal/backend/auth.go:29,34,40,43`

---

### API-03 [Medium] Status code semantics: 400 used where 422 / 415 fit better

| Path | Current | Better | Why |
|------|---------|--------|-----|
| `cmdResultHandler:225` `id required` | 400 | 422 | Body parsed correctly, semantic violation of required field |
| `cmdResultHandler:229` `invalid status` | 400 | 422 | Enum violation, parsed JSON was syntactically valid |
| `cmdResultHandler:221`, `reportHandler:109` `bad json` | 400 | 400 (correct) | Syntax error — keep |
| `reportHandler:104`, `cmdResultHandler:216` `body too large` | 413 | 413 (correct) | `http.StatusRequestEntityTooLarge` already used — good |
| `auth.go:29,34,40` malformed/missing token | 401 | 401 (correct) | Per RFC 7235 — keep |
| `auth.go:43` `auth lookup failed` (DB error) | 500 | 500 (correct) | Keep |
| Missing `Content-Type: application/json` on POST | (not checked) | 415 | See API-09 |

The 400-vs-422 distinction matters for monitoring: a 422 spike means "agent is sending well-formed garbage" (deploy regression), a 400 spike means "agent is sending malformed JSON" (encoder bug). Conflating them blinds the dashboard.

**Recommendation:** Bump enum/required-field violations from 400 to 422. Low-risk because the agent client treats any non-2xx as failure (`internal/agent/client.go:49,79,113`).

**File:** `internal/backend/handler.go:225,229`

---

### API-04 [High] No API versioning / backward-compat policy

The path is `/v1/*` but there is no documented contract for what happens at `/v2/`. There are also **no contract tests** that exercise an older `wire.Report` shape against the current handler, so silent field renames cannot be detected before deployment.

Concrete risks observed in the codebase:
- `wire.Report.Resumed bool` was added (commit log shows mobile-router work). It's `omitempty`, so old agents send no field and the backend treats them as `Resumed=false` — graceful. Good.
- `wire.Check.Details map[string]any` is opaque-ish JSON, so additions inside Details are safe.
- However, `wire.CommandResult.Status` is validated against an allow-list (`IsValidCommandResultStatus`, `pkg/wire/types.go:69-73`). If a future agent emits a new status (`"partial"` etc.) **before** the backend whitelist is updated, the backend will 400 the result and lose it. This is a strict-mode violation of Postel's law for an internal system that needs forward compat.
- The action whitelist (`pkg/wire/types.go:36-53`) has the same problem in reverse: a backend issuing a new action to an old agent will... actually be fine, because the agent's `actions/runner.go` does its own dispatch. But the symmetry should be documented.

**Recommendation:**
- Write `docs/api-versioning.md` (one page): "additive changes within `/v1/`, breaking changes go to `/v2/`, `wire.*` JSON tags are SemVer-tracked."
- Relax `IsValidCommandResultStatus` validation: log unknown statuses but accept them (record as-is, downstream notifier shows the raw string). This avoids losing data when a new agent ships before the backend.
- Add a contract test: serialise a "v0.10 agent" `Report` (a pinned struct), POST it to the current handler, assert 200 and event row count.

**File:** `pkg/wire/types.go:36-73`, `internal/backend/handler.go:228-230`

---

### API-05 [High] No `ReadTimeout` / `WriteTimeout` on http.Server — slow body attack

`cmd/backend/main.go:124-128`:

```go
srv := &http.Server{
    Addr:              cfg.Listen,
    Handler:           mux,
    ReadHeaderTimeout: 5 * time.Second,
}
```

Only `ReadHeaderTimeout` is set. `ReadTimeout` (whole-request including body) and `WriteTimeout` (response phase) are unbounded. An attacker can hold an open POST, dribble body bytes at 1 byte/sec, and pin a goroutine forever. `MaxBytesReader` is partially mitigated by `LimitReader(body, maxReportBytes+1)` (`handler.go:98,210`) — but the limit is on bytes-read, not on time-to-read. A 64KB body trickled at 1 byte/sec keeps the connection alive for 18 hours.

The long-poll handler (`/v1/cmd`) explicitly bounds wait time to 60s in handler logic (`handler.go:22,180-182`) — that's fine for cmd. But the GET still has no `WriteTimeout`, so a slow consumer holding the response open is unconstrained.

**Recommendation:**
- Add `ReadTimeout: 30 * time.Second` (covers `/v1/report` and `/v1/cmd/result` body reads, both ≤64KB so 30s is plenty).
- Add `WriteTimeout: 90 * time.Second` (must be > maxCmdWait=60s to allow long-poll, and > 30s for relay-back-to-TG goroutines that may run during the response). Note: setting `WriteTimeout` smaller than `maxCmdWait` would break long-poll — explicitly comment.
- Alternative if you don't want to tune around the long-poll: keep current config but mount `/v1/cmd` on a separate `http.Server` instance with its own (longer) timeouts.

**File:** `cmd/backend/main.go:124-128`

---

### API-06 [Medium] No per-token rate limiting — DDoS-able by misbehaving agent

A compromised or misconfigured agent (e.g., scheduler-firing-every-second bug) can flood `/v1/report` or `/v1/cmd/result`. Each report inserts N rows in `events` and runs `state.Apply`. The fleet is small (~10 routers) so the scenario is "one buggy agent," not "external DDoS" — Bearer auth blocks unauthenticated abuse. But a misbehaving agent can still wedge SQLite (writer-mutex contention) and stall the watcher loop.

**Recommendation:**
- Use `golang.org/x/time/rate` with one `*rate.Limiter` per userID, e.g. `rate.NewLimiter(rate.Every(5*time.Second), 5)` — five reports burst, then one every 5s sustained. Wrap inside auth middleware (after token resolution).
- Return 429 + `Retry-After` header when limited.
- Keep `/healthz` and `/v1/cmd` (long-poll, naturally throttled) outside the limiter.

**File:** `internal/backend/auth.go:23-50`, `internal/backend/handler.go:78-90`

---

### API-07 [Low] /v1/cmd?wait=N negative validation — strconv.Atoi accepts "-0", "00"

`handler.go:174-178`:

```go
n, err := strconv.Atoi(v)
if err != nil || n < 0 {
    http.Error(w, "bad wait", http.StatusBadRequest)
    return
}
```

`Atoi` accepts `"-0"` (returns 0, no error, passes the `< 0` check) and `"00000000000000000000000"` (overflows to int64 boundary). Both are harmless given the `if wait > maxCmdWait` cap, but worth noting that `n == 0` results in `wait = 0` which means the handler returns 204 immediately — that's fine, agents can use it as a "is anything queued right now?" probe, but it should be documented.

**Recommendation:** No code change needed; document that `wait=0` means "non-blocking poll."

**File:** `internal/backend/handler.go:171-183`

---

### API-08 [Medium] No Content-Type validation on POST endpoints

`reportHandler` and `cmdResultHandler` accept any `Content-Type` and try to JSON-decode the body. A request with `Content-Type: application/x-www-form-urlencoded` (e.g., from a misconfigured proxy) is parsed as JSON and rejected with 400 "bad json" — the same code as a malformed JSON body. This conflates two different errors.

**Recommendation:**
- Reject with 415 if `Content-Type` is set and is not `application/json` (allow `application/json; charset=utf-8` and unset for backward compat with old agents).
- One-line check before `json.Unmarshal`.

**File:** `internal/backend/handler.go:107-110, 219-222`

---

### API-09 [Low] No Accept-header / response negotiation

Cosmetic: the backend never inspects `Accept`. An admin UI calling `/v1/cmd` from a browser and asking for `text/html` would still get JSON. Not a bug, but an OpenAPI spec would document this.

**Recommendation:** Defer until a non-agent client appears.

---

### API-10 [Medium] No OpenAPI / API docs

No `docs/api.md`, no `openapi.yaml`. For a solo-maintained project with one client (the agent compiled from the same monorepo), this is acceptable — the contract is enforceable by Go types. But:
- The `route_status`, `route_rebind`, `version_audit`, `firmware_status` actions return JSON-encoded payloads inside `wire.CommandResult.Output` (`pkg/wire/routing.go:1-3`, `pkg/wire/maintenance.go:1-3`). These payload schemas are NOT documented anywhere — the only reference is the Go struct in `pkg/wire/*`. Anyone debugging a stuck agent has to read source to know what `Output` should contain.
- The action whitelist (`pkg/wire/types.go:36-53`) has 16 entries; no docs explain semantics, expected duration, side effects, or which TG buttons map to which actions.

**Recommendation:**
- Cheapest win: a `pkg/wire/README.md` listing each action, its arguments (`Args` keys), expected `Output` schema (link to struct), and side effects.
- Mid-term: generate an OpenAPI spec from struct tags using `swaggo/swag` or hand-write a 100-line yaml. The handler count is so small that hand-writing is fine.

**File:** `pkg/wire/types.go`, `pkg/wire/routing.go`, `pkg/wire/maintenance.go`

---

### API-11 [Low] CORS not configured — fine today, hot-spot for future admin UI

`NewMux` (`handler.go:78-90`) registers handlers without any CORS middleware. Today the only client is the Go agent (no preflight), so this is correct — explicitly *not* sending `Access-Control-Allow-Origin` denies the entire browser ecosystem, which is the right default.

**Recommendation:** When the admin UI lands, add CORS middleware **only on a separate path prefix** (e.g., `/api/admin/*`) so the agent endpoints stay non-CORS.

---

### API-12 [Medium] `/healthz` returns plain text, not JSON — fine, but no version info

`handler.go:80-82`:

```go
mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
    _, _ = io.WriteString(w, "ok\n")
})
```

Reasonable for k8s-style liveness. But it's also the cheapest place to expose backend version + git SHA so deploy/wizard can verify "what's actually running." Today the deploy tool relies on `wg-monitor-backend -version` flag (per memory: deploy chain bug discovery), which requires SSH. A `GET /healthz` returning `{"status":"ok","version":"v0.11.0-rc9","git_sha":"..."}` would let the wizard verify remotely without SSH.

**Recommendation:**
- Switch `/healthz` to JSON: `{"status":"ok","version":"...","build_time":"..."}`. Embed via `-ldflags "-X main.version=..."`.
- Keep response under 256 bytes so it's still cheap.

**File:** `internal/backend/handler.go:80-82`, `cmd/backend/main.go` (ldflags)

---

### API-13 [Low] cmdResultHandler — async notifier goroutines leak on shutdown

`cmdResultHandler:246-278` spawns goroutines via `go func(...){...}()` for the relay-to-TG path. These goroutines have a 30s context timeout but no parent context wired to server shutdown. On `srv.Shutdown(ctx)` the HTTP layer drains, but in-flight notifier goroutines continue using `context.Background()` (`handler.go:247,257,271`). They'll exit within 30s but write to a possibly-closed `tgClient` connection.

**Recommendation:**
- Pass a "server context" through `Deps` and use `context.WithTimeout(serverCtx, 30s)` instead of `context.Background()`.
- Or use `errgroup.WithContext` at the server level and have notifier goroutines respect cancellation.
- Severity is low because the relay is best-effort and 30s is short.

**File:** `internal/backend/handler.go:246-278`

---

### API-14 [Low] `wire.Command.Args map[string]any` — no schema per action

`Command.Args` is opaque `map[string]any`. The agent's action runner (`internal/agent/actions/runner.go`) interprets keys per-action by string lookup. Typo-friendly: enqueueing `Args: {"tunnel_id": "x"}` when the action expects `"tunnelID"` (memory note: tunnelId vs tunnelID casing) silently no-ops on the agent side.

**Recommendation:**
- Document `Args` keys per action in `pkg/wire/README.md` (see API-10).
- Mid-term: introduce typed wrappers, e.g. `wire.RebindArgs{SrcTunnelID, DstTunnelID string}` and helper `func (c *Command) DecodeRebindArgs() (RebindArgs, error)` — caller-side type safety for new actions.

**File:** `pkg/wire/types.go:29-34`, `internal/agent/actions/runner.go`

---

## What's already correct (worth keeping)

- **Shared `pkg/wire` package** prevents agent/backend type drift at compile time. (`pkg/wire/types.go:1-3` header even calls field tags "the contract.")
- **Bearer auth on every `/v1/*` endpoint, /healthz public** — clean separation.
- **`io.LimitReader(r.Body, maxReportBytes+1)` + length check** on POST bodies — cheap defense against memory-pressure attacks. Limits are sensible (64KB report, 16KB result).
- **`maxCmdWait = 60 * time.Second`** explicit cap on long-poll wait — bounded server-side regardless of client param.
- **`ConsumeOriginRef` (`cmd/queue.go:89-101`) atomically deletes the ref** — protects against double-relay if the agent retries `/v1/cmd/result`.
- **Auth middleware ignores trailing whitespace** but rejects `"Bearer "` (empty) and `"Bearer  ..."` (two spaces) — see `auth.go:32-35`. Tests in `auth_test.go:46-65` exercise these edge cases.
- **Test coverage of handler behaviours** is solid: `TestCmdResult_RejectsBadJSON`, `TestCmdResult_RejectsInvalidStatus`, `TestReportRejectsTooLarge`, `TestCmdGet_RequiresAuth`, etc.

---

## Suggested priority

1. **API-05** (server timeouts) — 5-line fix, eliminates slow-loris class.
2. **API-01** (idempotency) — 1 SQL UNIQUE index + `INSERT OR IGNORE`, fixes a latent data-corruption bug.
3. **API-04** (versioning policy) — 1 doc page + relax `IsValidCommandResultStatus` to log-and-accept.
4. **API-06** (per-token rate limit) — 30-line `x/time/rate` integration.
5. **API-02** (JSON error envelope) — refactor; do alongside API-08 (Content-Type) and API-12 (healthz JSON) as one "API hygiene" PR.
6. **API-10** (`pkg/wire/README.md`) — pure docs, helps future-you.

Other items (API-03, API-07, API-09, API-11, API-13, API-14) are nice-to-have or deferred until triggered.
