# Audit Remediation Batch — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Fix the 2026-07-06 audit findings that are NOT the Deploy-to-router cluster (that cluster is closed structurally by the provisioning rework plan).

**Architecture:** Independent, mostly-small fixes across agent, backend, CI, and repo hygiene. Each task is self-contained and independently testable; order is by severity (HIGH → MED → LOW). Findings + evidence: `analysis/2026-07-06/full-audit-and-landscape.md`.

**Tech Stack:** Go 1.26.4, GitHub Actions, git.

## Global Constraints

- Go 1.26.4; `CGO_ENABLED=0` builds; `-race` in CI.
- No new Go module deps unless a task explicitly adds a CI tool.
- TDD where a behavior changes; every task ends `go build ./... && go vet ./... && go test ./<pkg>/...` green.
- Precision over churn: touch only the finding's site; do not opportunistically refactor neighbors.
- Feature branch `fix/audit-2026-07-06`; PR at end. (May run in parallel with the provisioning rework branch — no file overlap except CI, sequence CI tasks last if both branches touch `.github/`.)

---

### Task 1 [HIGH]: Agent per-command execution timeout

**Files:** Modify `internal/agent/cmdloop/loop.go` (dispatch at `:138`) or wrap `internal/agent/actions/runner.go` `Execute`/`DefaultExec`. Test: `internal/agent/actions/runner_test.go` (or a new `runner_timeout_test.go`).

**Fix:** Wrap each action's `Execute` in `context.WithTimeout` (default 45s; per-action overrides: `opkg_upgrade`/`tunnel_import` 300s, `firmware_install` 600s). Mirror the existing pattern in `internal/agent/actions/router_doctor.go:50`.

- [ ] Write a failing test: a fake exec that blocks forever → assert the runner returns a deadline error within the budget (use a small test budget), not hang.
- [ ] Run → FAIL.
- [ ] Implement a central `withActionTimeout(ctx, action) (context.Context, cancel)` applied in `Runner.Execute`; give long actions their override.
- [ ] Run → PASS; `go test ./internal/agent/... -v`.
- [ ] Commit: `fix(agent): bound per-command execution to prevent command-channel wedge`.

---

### Task 2 [HIGH]: DNS check — parallelize probes, treat cancellation as inconclusive

**Files:** Modify `internal/agent/checks/dns.go` (`:116-259`, endpoint loop + `rknProbe`). Test: `internal/agent/checks/dns_test.go`.

**Fix:** Probe endpoints and RKN domains **concurrently** (goroutine + `sync.WaitGroup`, mirror `internal/agent/checks/external_reach.go`) so total stays within the 10s per-check budget; when the parent ctx is cancelled/deadline-exceeded, mark the probe **inconclusive/skipped**, NOT `sus`/`failed` (removes false RKN-block / `dns=fail` alerts).

- [ ] Write a failing test: a slow fake resolver that would exceed the budget sequentially → assert (a) it completes within budget via concurrency, and (b) a context-cancelled probe yields inconclusive, not a failure count.
- [ ] Run → FAIL.
- [ ] Implement concurrent probing + cancellation-aware result classification.
- [ ] Run → PASS.
- [ ] Commit: `fix(agent): parallelize DNS/RKN probes; cancellation is inconclusive not failure`.

---

### Task 3 [HIGH]: `tg.Client.DownloadFile` bot-token redaction

**Files:** Modify `internal/backend/tg/client.go` (`DownloadFile` `:351-374`). Test: `internal/backend/tg/client_test.go`. Also audit `internal/backend/callbacks/router.go:1749-1753` to stop relaying raw `err.Error()` into chat for TG-transport errors.

**Fix:** Apply the existing `redactURLError` to the error returned by `DownloadFile` (as `callWith`/`SendDocument` already do). Ensure the chat-facing message at `router.go:1749` uses the redacted error.

- [ ] Write a failing test: force a `*url.Error` whose URL contains the bot token → assert the returned error string does NOT contain the token.
- [ ] Run → FAIL.
- [ ] Wrap the error with `redactURLError`; adjust the callback to not leak raw transport errors.
- [ ] Run → PASS.
- [ ] Commit: `fix(tg): redact bot token in DownloadFile errors (no token to chat)`.

---

### Task 4 [MED]: Backend relay-goroutine bound + fleet-batch edit ordering

**Files:** Modify `internal/backend/handler.go` (7 raw `go func` relay sites at `:955-1050` → route through `spawnRelay`/`relaySem`); `internal/backend/callbacks/fleet_batch.go` (`:132-155` serialize per-`BulkID` edits). Tests: `handler_test.go`, `fleet_batch_test.go`.

- [ ] Write a failing test asserting bulk-result relay respects the `relayConcurrencyLimit` semaphore (count concurrent in-flight ≤ limit) and fleet-batch edits are coalesced/ordered per BulkID.
- [ ] Run → FAIL.
- [ ] Route all 7 sites through `spawnRelay`; add single-flight/coalesce for `fleetBatchStore` edits.
- [ ] Run → PASS.
- [ ] Commit: `fix(backend): bound cmd-result relay goroutines; order fleet-batch edits`.

---

### Task 5 [MED]: Cross-package topic-creation lock

**Files:** Modify `internal/backend/alerts/` (extract the topic-creation lock out of `Dispatcher.ensureTopic` into a shared per-userID lock in the `alerts` package) + `internal/backend/callbacks/admin_topics.go:131,168` to acquire it. Test: new `-race` test exercising concurrent `EnsureTopicForUser` from both paths.

- [ ] Write a failing `-race` test: concurrent `Dispatcher.ensureTopic` + `admin_topics` `EnsureTopicForUser` for one user → assert a single topic id, no duplicate creation.
- [ ] Run with `-race` → FAIL (data race / double create).
- [ ] Add a shared `alerts` per-user lock; both callers acquire it.
- [ ] Run `go test -race ./internal/backend/...` → PASS.
- [ ] Commit: `fix(backend): shared lock for topic creation (no duplicate topics)`.

---

### Task 6 [HIGH, DISPUTED — verify first]: alert-dispatch `r.Context()`

**Files:** `internal/backend/handler.go:746`. Test: `handler_test.go`.

**Fix (only if verified):** the 2026-06-18 audit called a similar claim false but only checked server-shutdown, not client-disconnect. **First verify**: write a test where the report POST's context is cancelled mid-handler and assert whether the in-flight `Dispatcher.Handle` TG send is aborted. If confirmed, switch line 746 to `relayParent(d)` + a bounded timeout (matching `handler.go:517`). If not reproducible, close as no-op with a note.

- [ ] Write the verification test (cancel request ctx during dispatch; assert send behavior).
- [ ] If it reproduces → switch to `relayParent(d)`; test now shows the send completes. If not → document and skip.
- [ ] `go test ./internal/backend/ -run Dispatch -v`.
- [ ] Commit (if changed): `fix(backend): server-owned context for HARD/Recovery alert dispatch`.

---

### Task 7 [HIGH]: `.gitattributes` `*.tmpl eol=lf` + no-CR test

**Files:** Modify `.gitattributes`; add a Go test asserting embedded/rendered deploy templates contain no `\r`.

- [ ] Add `*.tmpl text eol=lf`, `*.timer text eol=lf` (and explicit lines for `awgm-bootstrap.sh.tmpl`, `wg-monitor-backup.service.tmpl`, `wg-monitor-backup.timer`). Run `git add --renormalize .`.
- [ ] Add `cmd/deploy/templates_test.go` case: for every embedded template, assert `!strings.Contains(rendered, "\r")`.
- [ ] Run → PASS; verify `git check-attr text eol -- cmd/deploy/templates/awgm-bootstrap.sh.tmpl` shows `eol: lf`.
- [ ] Commit: `fix(build): pin *.tmpl to eol=lf; test rejects CR in deploy templates`.

---

### Task 8 [MED]: Mojibake strings ×3

**Files:** `internal/backend/callbacks/router.go:740,1757,2009`; delete the vestigial mojibake fallback in `internal/backend/callbacks/actions_test.go:440`.

- [ ] Replace the 3 double-encoded Cyrillic literals with correct UTF-8 (e.g. line 740 → `"это не чат этого роутера"`; recover the intended text from context).
- [ ] Remove the `actions_test.go:440` "accept either correct or mojibake" hedge; assert only the correct string.
- [ ] Run `go test ./internal/backend/callbacks/... -v` → PASS.
- [ ] Commit: `fix(backend): repair mojibake Russian strings sent to Telegram`.

---

### Task 9 [MED]: `staticcheck` in CI + remove verified dead code

**Files:** `.github/workflows/ci.yml` (add a `staticcheck` job mirroring the `gosec` job); delete the ~20 U1000 symbols listed in the audit (verify each with grep before deleting).

- [ ] Add a pinned `staticcheck` CI step. Run it locally, capture output.
- [ ] Delete confirmed-dead symbols (audit §2 list: `router.go` 4 methods; `cmd/deploy/actions.go` orphan add-user chain; `steps.go:145`; etc.) — grep each to confirm zero references first.
- [ ] `go build ./... && go vet ./... && staticcheck ./...` clean; `go test ./...`.
- [ ] Commit: `chore: add staticcheck to CI; remove verified dead code`.

---

### Task 10 [MED]: Pin SAST/SCA tools in CI

**Files:** `.github/workflows/ci.yml:52,64,80`, `release.yml:29,31,33`.

- [ ] Replace `go install …@latest` for `govulncheck`/`gosec`/`grype` with pinned released versions (e.g. `@v2.27.1`), matching the SHA-pinning discipline of the `uses:` steps.
- [ ] Push a branch, confirm the workflow still passes with pinned versions.
- [ ] Commit: `chore(ci): pin govulncheck/gosec/grype to released versions`.

---

### Task 11 [MED]: `route_add`/`route_delete` surface partial-failure

**Files:** `internal/agent/actions/runner.go:666-699`. Test: `runner_routes_test.go`.

- [ ] Write a failing test: `RouteAddJSON` returns a non-empty `Warning` (RoutingRefresh failed) → assert command status is `"partial"`, not `"ok"`.
- [ ] Reuse `routeRebindCommandStatus`'s "non-empty Warning → partial" logic for `route_add`/`route_delete`.
- [ ] Run → PASS.
- [ ] Commit: `fix(agent): route_add/delete report partial on post-change warning`.

---

### Task 12 [MED]: Anti-downgrade floor (update paths) + agent self_update guards

**Files:** `internal/backend/wizard_handler.go:634-639,722-727` (persist highest-installed, reject lower target); `internal/agent/actions/self_update.go` (add `df -k /opt` free-space guard like `opkg.go`; add semver downgrade guard requiring `allow_downgrade`). Tests alongside.

- [ ] Backend: test that a deploy targeting a version below the recorded highest is rejected; implement the floor.
- [ ] Agent: test self_update refuses on insufficient `/opt` space and on an older target without `allow_downgrade`; implement both guards (mirror `opkg.go:79` space check).
- [ ] Run → PASS.
- [ ] Commit: `fix: anti-rollback floor on update paths + self_update space/downgrade guards`.

---

### Task 13 [MED]: `SelfUpdate()` end-to-end test coverage

**Files:** `internal/agent/actions/self_update_test.go` (add integration tests; current end-to-end coverage 5.8%).

- [ ] Add 2-3 `httptest.Server`-backed tests driving the full `SelfUpdate()` sequence: (a) happy path installs; (b) the signature-required branch actually fires for a non-legacy version; (c) a checksum mismatch mid-sequence leaves `/opt/bin/wg-monitor` untouched (assert temp cleanup).
- [ ] Run → PASS.
- [ ] Commit: `test(agent): end-to-end SelfUpdate coverage (signature branch, failure cleanup)`.

---

### Task 14 [LOW]: Backend low-severity fixes

**Files:** `internal/backend/config.go:274-276` (`MuteCutoffHour *int` so 0 ≠ unset); `internal/backend/retention/policy.go:57-70` (fixed-phase timer); `internal/backend/alerts/dispatcher.go` (injectable `Now` seam for parity).

- [ ] `MuteCutoffHour`: pointer type + test that 0 is honored, unset defaults to 9.
- [ ] Retention: reset timer to fixed phase (test optional).
- [ ] Dispatcher: add `Now func() time.Time` seam (mirrors `Watcher`/`Poller`).
- [ ] Run → PASS; Commit: `fix(backend): mute-cutoff 0-vs-unset, retention timer phase, dispatcher clock seam`.

---

### Task 15 [LOW]: Dashboard cheap wins (icons, contrast, focus, dead CSS)

**Files:** `internal/backend/dashboard_static/vendor/tabler-icons-lite.css` (add the 12 missing `.ti-*` glyphs); `app.css` (`--faint` contrast); `app.js` (modal focus trap + restore); remove `vendor/tabler-lite.css` `<link>` + the now-unneeded `text-transform:none` override.

> NOTE: coordinate with the provisioning-rework frontend tasks if both branches touch `app.js`/`index.html` — land whichever merges first, rebase the other.

- [ ] Add the 12 icon glyph mappings; darken `--faint` (or use `--muted`) to pass WCAG AA; add focus trap; delete dead `tabler-lite.css` link + override.
- [ ] `node --check`; Playwright visual check icons render.
- [ ] Commit: `fix(dashboard): render missing icons, WCAG contrast, modal focus, drop dead CSS`.

---

### Task 16 [LOW]: Repo hygiene

**Files:** `go.mod` + `cmd/deploy/templates/*.service*` (module-path `anex`→`Jkaotlic`, minimum: fix the 4 hardcoded `Documentation=` URLs); add `LICENSE`; `go get -u ./... && go mod tidy`; add `.github/dependabot.yml`.

- [ ] Fix the 4 `Documentation=` URLs (minimum) — or full `go mod edit -module` rename if opted in (wide diff; ask before doing the full rename).
- [ ] Add a `LICENSE` file (ask the operator which license).
- [ ] `go get -u ./... && go mod tidy`; `go test ./...` still green.
- [ ] Add `dependabot.yml` for `gomod` + `github-actions`.
- [ ] Commit: `chore: fix module-path URLs, add LICENSE + dependabot, refresh deps`.

---

## Self-Review

- **Coverage vs audit:** HIGH — T1 (agent timeout), T2 (DNS FP), T3 (DownloadFile token); MED — T4 (relay goroutines), T5 (topic race), T7 (gitattributes), T8 (mojibake), T9 (staticcheck+dead-code), T10 (SAST pinning), T11 (route partial), T12 (anti-downgrade+self_update guards), T13 (SelfUpdate coverage); DISPUTED — T6 (alert r.Context, verify-first); LOW — T14 (backend low), T15 (dashboard), T16 (hygiene). The Deploy-to-router HIGH cluster (unsigned checksums, token leak, r.Context install, AWGM timeout, idempotency, relay coverage) is intentionally NOT here — closed by `2026-07-06-router-provisioning-rework.md`.
- **Placeholders:** each task names files + the concrete change + a test/verify + commit. Two tasks correctly gated on operator input (LICENSE choice T16; full module rename T16) — flagged, not silent.
- **Ordering:** severity-first; T6 explicitly verify-before-fix; T15 flagged for frontend-branch coordination.
