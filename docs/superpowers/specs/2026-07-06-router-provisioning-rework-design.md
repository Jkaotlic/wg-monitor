# Router provisioning & repair rework — design

**Date:** 2026-07-06
**Status:** approved design (brainstorm complete) → next: writing-plans
**Topic:** unify dashboard router onboarding + revival into one guided, async, verified engine

## Context

The dashboard grew three overlapping, poorly-bounded entry points for the single
job of "get / keep an agent running on a router":

- **Add agent** — a large form that only mints an enrollment token (installs nothing);
  it collects arch / ssh / deploy_mode / ring that the awgm install path then ignores.
- **Deploy to router** — first-time install, but requires `awgm_url` to already be set
  via a *separate* Edit action, so onboarding is Add → Edit → Deploy across three modals.
- **Revive** — re-points `backend.url` + restarts, for a dark agent; same relay engine,
  separate flow / modal / handler.

Shared problems across Deploy + Revive:

- Result is the **raw awg-manager terminal transcript** dumped into a `<pre>` — login
  prompts, echoed commands, shell noise; no parsed success/failure.
- **No progress**: a multi-minute install blocks the request synchronously; the button
  sits in "waiting", then a transcript appears. Closing the tab kills it (request-scoped
  context).
- **No verification the agent actually came online** — the dashboard `bootstrap_install`
  path prints success on the router script's rc=0. The wizard's `deferred_bootstrap`
  path already does far more (waits for first heartbeat + token auth, idempotency,
  expiry, two-phase token commit); the dashboard reused none of it.
- The relay types credentials into a PTY and scrapes echoed output — inherently brittle.

The 2026-07-06 audit independently flagged a cluster on this exact surface (release
signature not verified on the install path; freshly-minted token echoed into the
dashboard result; request-scoped context aborting an in-flight install; no timeout on
the direct AWGM terminal path; no idempotency/replay guard; relay transport untested).
This rework closes that cluster structurally rather than patch-by-patch.

## Goals

- **One guided flow.** A single "Provision router" entry replaces "Add agent"; a first
  step forks into **Install now** or **Register token only** (register-for-later).
- **Live, step-by-step progress** that survives a browser disconnect.
- **Verified install**: release signature checked before install; agent confirmed
  actually online before the job is called successful.
- **Unified repair**: an existing dark agent is repaired by the same engine, choosing
  **quick re-point URL** or **full re-provision**.
- **No secret leakage** into the UI; **no raw transcript** surfaced.
- Fold in the deploy-surface audit findings as structural properties of the new engine.

## Non-goals

- Reworking the CLI wizard's `deferred-awgm` path (it stays as the operator-machine
  fallback; it is already the more mature path). Alignment is welcome but not required.
- A public status page or any new internet-facing surface (unchanged constraint).
- Adopting a frontend framework/bundler — the dashboard stays vanilla / zero-build /
  `go:embed` / CSP-friendly.
- The non-deploy audit fixes (DNS probe parallelization, `DownloadFile` token
  redaction, relay-goroutine / topic-creation races, agent per-command timeout,
  mojibake, staticcheck-in-CI, etc.) are **out of scope for this spec** but ride
  alongside in the same implementation plan as independent tasks.

## Design overview

A backend **provisioning engine** runs an out-of-band operation against a router (over
the awg-manager terminal relay) as an **asynchronous, server-owned job** that emits
**structured progress steps** and ends with **agent-online verification**. Four operator
intents map onto it:

| Intent | Entry | Engine work |
|---|---|---|
| Provision — install now | topbar "Provision router" | verify checksums → mint enrollment → install → verify-online |
| Register only | topbar "Provision router" → step 0 | mint token + record metadata (no relay; instant) |
| Repair — re-point | agent drawer "Repair" | rewrite `backend.url` + restart → verify-online |
| Repair — reinstall | agent drawer "Repair" | full idempotent re-provision → verify-online |

## Components

### `internal/backend/provision` (new package)

```go
type JobKind  string // "provision" | "register" | "repair_repoint" | "repair_reinstall"
type JobState string // "running" | "success" | "failed"

type Step struct {
    Name   string // stable id, see step catalog below
    Status string // "pending" | "active" | "done" | "failed"
    Detail string // e.g. "arm64", "v0.13.9", "first report 4s ago"
}

type Job struct {
    ID       string
    Nickname string
    Kind     JobKind
    State    JobState
    Steps    []Step    // seeded from a per-kind template so the client renders the
                       // full checklist as "pending" immediately
    Version  string
    Hint     string    // friendly message on failure (see Error handling)
    Tail     string    // bounded, token-redacted transcript tail, only on failure
    Started  time.Time
    ended    time.Time // for TTL eviction
}
```

- **Store:** in-memory `map[string]*Job` behind a mutex, TTL eviction (~30 min after
  terminal state), bounded count. Single-operator scale — tiny. A `Sweep()` on a ticker
  (mirror `cmd/queue.go`).
- **Single-flight per nickname:** a per-nickname lock prevents two concurrent
  provision/repair jobs for the same agent (mirrors existing action locks).

**Step catalog** (templates per kind; client maps name → label/icon):

- provision / repair_reinstall: `terminal_connected`, `arch_detected`, `downloading`,
  `checksum_ok`, `config_written`, `init_installed`, `service_started`, `verify_online`
- repair_repoint: `terminal_connected`, `backend_url_rewritten`, `service_restarted`,
  `verify_online`
- register: single synthetic `token_minted` (completes immediately, no job goroutine
  strictly needed — may return the token inline; modelled as an instant success job for
  a uniform client path)

### `provision.Runner`

- `Start(kind, req) (jobID string, err error)`:
  1. Validate inputs (nickname, awgm_url via `validateDashboardAWGMURL` incl.
     private/loopback rejection; anti-downgrade check for install/reinstall).
  2. For install/reinstall: **fetch + verify** `checksums.txt` **and** `checksums.txt.sig`
     via a single shared helper wrapping `releasesig.VerifyChecksumsSignature` (see
     "Security hardening"). Abort on missing/invalid signature.
  3. Mint enrollment (DB stores only the hash; a fresh `config.yaml` needs the raw token).
  4. Create `Job{Steps: template, State: running}`, register it, return `jobID`
     immediately.
  5. Launch a goroutine bound to a **server-owned context** —
     `ctx, cancel := context.WithTimeout(d.baseCtx, budget)` (NOT `r.Context()`), so a
     browser disconnect cannot cancel it and a wedged run is bounded.
- Goroutine body: run the relay with **line-streamed stdout**, parse `__WG_STEP__`
  markers into `Job.Steps` live; on script rc=0 run **verify-online**; set the terminal
  state (+ `Hint`/`Tail` on failure). Always cleanup (`terminal/stop`, temp files).

`d.baseCtx` is a long-lived context owned by the backend process (cancelled only on
shutdown). It is threaded into `Deps` at wiring time in `cmd/backend/main.go`.

### Relay upgrade (`internal/awgmrelay/awgm-relay.py`)

- The router-side bootstrap script prints **line markers between phases**:
  `echo __WG_STEP__ downloading`, `__WG_STEP__ checksum_ok`,
  `__WG_STEP__ config_written`, `__WG_STEP__ init_installed`,
  `__WG_STEP__ service_started`. The relay's `run_bootstrap` already forwards router
  stdout, so markers pass through. `terminal_connected` is emitted by the relay itself
  once the shell prompt is reached; `arch_detected <arch>` after `system_info`.
- **Go side switches from `exec … CombinedOutput()` (buffer-all-then-return) to
  `StdoutPipe` + `bufio.Scanner`** so the backend sees lines as they arrive — this is
  the source of "live".
- **Redaction at the boundary:** the backend knows the raw token (it minted it), so any
  transcript tail kept for diagnosis is passed through
  `strings.ReplaceAll(tail, rawToken, "«redacted»")`. The **raw transcript is never
  returned to the client** — only structured steps, plus a bounded redacted tail on
  failure. This removes the token-leak class structurally.
- Marker parsing is defensive: unknown markers are ignored; a `__WG_STEP__` line that
  names a step not in the template is dropped, not surfaced.

### Signature verification (shared helper)

Introduce one helper (e.g. `releaseVerifiedChecksums(ctx, version) (map[string]string, error)`)
that fetches `checksums.txt` + `checksums.txt.sig` and verifies via
`releasesig.VerifyChecksumsSignature` before returning the parsed map. All checksum
consumers (agent self-update, backend update CLI, deploy wizard, and this engine) should
route through the same helper so "forgot to verify" becomes structurally impossible
(the TUF single-verified-metadata-client principle). Minimum for this spec: the engine
uses it; refactoring the other three call sites onto it is a follow-up task in the plan.

### Verify-online (backend-native)

After the relay reports rc=0, the engine polls the backend's **own report/heartbeat
state** for the agent: a fresh report received *after* the job start timestamp, whose
auth used the newly-minted token. The backend receives agent reports directly, so it is
the source of truth — unlike the deferred path, which has the relay call back into a
wizard API. Poll up to ~120s with a `verify_online` step; success detail =
"first report Ns ago"; failure → `agent_offline` hint.

### HTTP endpoints (dashboard, session-authed)

- `POST /v1/dashboard/provision` — body: `{ kind: "provision"|"register", nickname, agent_kind,
  telegram_group, thread_id, awgm_url?, awgm_auth?, root_password?, awgm_login?,
  awgm_password?, awgm_api_key?, version? }`. (`"provision"` = install-now; the body `kind`
  values match `JobKind`.)
  - `register`: mint token + persist metadata → return the token payload (and an instant
    completed job for a uniform client path). No relay.
  - `provision`: validate + verify checksums + mint + start job → return `{ job_id, steps }`.
- `GET /v1/dashboard/provision/{job_id}` — return the `Job` (steps, state, version, hint,
  redacted tail). Poll target.
- `POST /v1/dashboard/agents/{nickname}/repair` — body:
  `{ mode: "repoint"|"reinstall", root_password, new_backend_url?, version?, awgm_login?,
  awgm_password?, awgm_api_key? }` → start job → `{ job_id, steps }`.
- The existing `POST …/deploy-router` and `…/revive` become thin adapters onto the new
  engine initially, then are removed once the frontend no longer calls them.

## Data flow (provision — install now)

```
Browser: wizard form → POST /v1/dashboard/provision {kind:"install", …}
Backend: validate → fetch+VERIFY checksums(.sig) → mint enrollment (DB: hash only)
       → Job{steps:template, running} → respond {job_id, steps}
       → [goroutine, server ctx + timeout]:
            relay(bootstrap_install, step-marker script)
              → stream stdout line-by-line → parse __WG_STEP__ → update Job.Steps live
            → rc=0 → step verify_online → poll own report-state for fresh authed report
            → Job.State = success | failed (+ hint, + redacted tail on failure)
Browser: poll GET /provision/{job_id} every ~1.5s → render checklist → terminal card
```

## Dashboard IA & guided wizard

- Topbar **"Add agent" → "Provision router"**.
- Agent drawer Recovery section: **"Repair"** (replaces separate Revive + Deploy-to-router
  for existing agents). A register-only agent that is later installed uses the same
  reinstall path (idempotent).

**Provision modal (internal steps, vanilla show/hide panels — no bundler):**

- **Step 0 — mode:** `⚡ Поставить сейчас` / `🎫 Только зарегать токен`
- **Step 1 — identity + placement:** nickname, kind (static/mobile), Telegram group +
  topic. (The essential subset of today's Add-agent form; arch/ssh/deploy_mode/ring are
  dropped — the engine auto-detects arch and uses the awgm path.)
- **Step 2 — router access (install-now only):** AWG Manager URL + auth (api-key OR
  login/pass) + root password + target version (default: latest stable).
- **Step 3 — run:** register-only → **token card** ("save it now"); install-now → kick
  off job → **progress view**.

**Progress view (shared component):** a checklist rendered from `job.steps`, polled live;
each row `⏳ → ✓ / ✗` with detail. Terminal:

- success → `✓ Агент онлайн — <version>, первый репорт <n>s назад` + Close
- failed → `✗ Упало на «<step>»` + friendly hint + collapsible **redacted** tail +
  `[Повторить] / [Закрыть]`

**Repair modal (from drawer):**

- **Step 0 — mode:** `🔗 Быстрый re-point URL` / `♻️ Полная переустановка`
- **Step 1 — access:** root password (+ new URL for re-point; + version for reinstall;
  + optional awgm auth under a disclosure)
- **Step 2 — run:** → same progress view. Re-point has the shorter step set.

The shared progress-render is a small declarative registry (`[{name,label,icon}]`),
replacing the current 4-place `isX`/`formatX`/`remember`/`format` duplication pattern for
result types. **Auto-refresh pauses while any job modal / progress view is open**, which
also fixes the drawer-edit-clobber finding.

## Error handling

- **Friendly hints** per failure class (a small map, in the `alerts.HintFor` spirit):
  - `terminal_session_active` → "AWG Manager terminal занят — закрой сессию в web-UI и повтори"
  - `auth_failed` → "root-пароль или awgm-логин не подошёл"
  - `arch_unsupported` → "роутер сообщил неподдерживаемую архитектуру"
  - `checksum_mismatch` → "бинарь не сошёлся с подписанной суммой — релиз/зеркало битые"
  - `download_failed` → "роутер не смог скачать бинарь с backend"
  - `service_not_started` → "init-скрипт не поднял сервис"
  - `agent_offline` → "поставлен, но не позвонил домой за 120с — проверь связь роутер→backend"
- **Single-flight per nickname** — concurrent provision/repair of the same agent is
  rejected with a clear message.
- **Cleanup** always runs `terminal/stop`; the server-context timeout kills a wedged
  subprocess and marks the job "timed out at step X".
- **Anti-downgrade:** reject a target version below the agent's last-installed version
  unless an explicit "allow downgrade" flag is set.
- **Idempotency:** the router-side nickname guard (`exit 11` on foreign nickname) stays;
  reinstall is idempotent; the job is server-authoritative and the client only polls, so
  there is no replay surface from the browser.

## Security hardening (closes the deploy-surface audit cluster)

| Property | Mechanism |
|---|---|
| Release signature verified before install | shared fetch+verify helper (`releasesig`) |
| No token in the UI | raw transcript never returned; tail token-redacted |
| Install survives browser disconnect | job on server-owned context |
| Wedged terminal bounded | server `context.WithTimeout` + subprocess kill |
| No unbounded replay | single-flight + server-authoritative job + nickname guard |
| Agent actually online | mandatory `verify_online` step |
| Relay transport tested | engine + relay covered (see Testing) |

## Testing

- **Engine (Go):** fake relay (stub `runRelayProcess`) that streams scripted step-marker
  sequences — success / mid-failure / timeout — asserting `Job` step progression,
  terminal state, and token redaction in `Tail`.
- **Verify-online:** fake report-state → asserts it waits, then succeeds / times out to
  `agent_offline`.
- **Signature:** provision refuses to start when the `.sig` is missing or invalid (the
  P0 regression test).
- **Relay (Python):** extend `internal/awgmrelay/test_install_mode.py` — assert step
  markers are emitted and the token never appears in structured (non-transcript) output.
- **HTTP:** handler tests for provision (install + register), repair (both modes), and
  poll — job lifecycle, validation, single-flight rejection.
- **Frontend:** `node --check` + a Playwright smoke with a mocked job that progresses to
  success and to failure, asserting the checklist renders and no raw token appears.

## Rollout / migration

1. Land the engine + endpoints + relay markers behind the new frontend, with
   `/deploy-router` and `/revive` kept as thin adapters onto the engine.
2. Switch the dashboard to "Provision router" + "Repair"; remove "Add agent",
   "Deploy to router", and "Revive" UI.
3. Remove the old adapter endpoints once nothing calls them.
4. Backend-only change (front is embedded) → one backend redeploy on the VPS; the agent
   binary is unchanged. First real run should be exercised on one router (the flow is not
   yet field-validated).

## Out of scope (tracked in the same implementation plan, separate tasks)

The non-deploy 2026-07-06 audit findings: DNS probe parallelization (false-positive
RKN alerts), `tg.Client.DownloadFile` token redaction, backend relay-goroutine bound +
fleet-batch edit ordering, cross-package topic-creation lock, agent per-command execution
timeout, `.gitattributes` `*.tmpl eol=lf` + `\r` test, SAST tool pinning in CI, mojibake
strings, `staticcheck` in CI + dead-code removal, module-path / systemd URL fix, missing
LICENSE, dependency refresh. These are independent of the provisioning engine and are
sequenced separately.

## Live grounding (2026-07-06, read-only against production)

Validated the load-bearing assumptions against the real fleet before committing the design:

- **Arch detection** — live awg-manager `/api/system/info` on a KN-1811 returns
  `data.goArch:"arm64"` inside a `{success,data}` envelope (awg-manager 2.15.1); matches
  the relay's `normalize_arch` / `goArch` read. Terminal model confirmed:
  `/api/terminal/status` → `{installed,running,sessionActive}`, exactly what
  `ensure_terminal` checks.
- **Report cadence → verify-online budget** — dashboard summary over 11 agents: reporting
  agents' `last_seen_age_sec` median ≈ 68s (interval_sec = 60), min 13s. A fresh agent
  phones home within ~1 interval, so the **120s verify-online budget ≈ 2 intervals** is
  sound (consider 150s for extra cold-start margin).
- **awgm is the real path** — deploy_mode across the fleet: awgm = 9, deferred-awgm = 1 →
  the rework rightly centers on the awg-manager terminal path.
- **Concrete repair demand** — 2 agents currently offline/dark and 2 pinned on ancient
  versions (rc59, rc70); these are exactly the re-point / reinstall cases repair targets.
- **Urgency note** — production backend `/healthz` reports **v0.13.9**, i.e. the current
  (vulnerable) Deploy-to-router is already live, not merely staged. The P0 hardening
  folded into this rework fixes a live surface, not a pre-release one.

## Open questions

None blocking. Grounded defaults: verify-online budget 120s (≈2× the measured 68s report
median; 150s if we want more cold-start margin); job TTL 30 min; poll interval 1.5s;
provision job timeout 10 min (install) / 5 min (repair) — all tunable.
