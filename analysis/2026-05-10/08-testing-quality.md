# Testing-quality audit — wg-monitor

Date: 2026-05-10
Branch: main (rc18+)
Test invocation: `go test ./... -count=1 -timeout 180s` → **all green** (22 packages).
Race detection: `-race` requires `CGO_ENABLED=1` + gcc; **race detector was NOT runnable** in this environment. CI workflow `.github/workflows/release.yml` does **not** run `go test -race` either (CGO_ENABLED=0 across the matrix).

## Top-line numbers

| Layer                       | Cov.  | Notes |
|-----------------------------|-------|-------|
| `pkg/wire`                  | 100%  | wire types — pristine |
| `internal/backend/cmd`      | 95.3% | command queue — best in repo |
| `internal/backend/upstream` | 93.8% | |
| `internal/backend/state`    | 91.2% | FSM — strong |
| `internal/backend/heartbeat`| 86.2% | |
| `internal/agent/cmdloop`    | 84.8% | |
| `internal/backend/retention`| 83.8% | |
| `internal/backend`          | 78.4% | handler |
| `internal/backend/alerts`   | 63.1% | |
| `internal/backend/db`       | 58.7% | |
| `internal/backend/tg`       | 56.7% | |
| `internal/backend/callbacks`| 54.4% | low side |
| `internal/backend/realert`  | 36.9% | LOW |
| `internal/agent/checks`     | 42.1% | DNS/external_reach 0%-cov |
| `cmd/deploy`                | 16.8% | mostly action-glue |
| `cmd/agent` / `cmd/backend` | 0.0%  | main() only — fine |
| **TOTAL**                   | **44.6%** | |

Source/test ratio: **101 .go non-test, 73 _test.go** in tree (worktrees excluded). One end-to-end integration file: `cmd/backend/integration_test.go` (3 tests, 217 lines), which is appropriate.

---

## Findings (severity: HIGH / MED / LOW)

### TEST-01 — HIGH — race detector never runs
- File: `Makefile:57-58`, `.github/workflows/release.yml`
- The `test` target is bare `go test ./...`; CI only builds with `CGO_ENABLED=0`. There is **no path** in the project that runs `-race`. The codebase has multiple sources of intentional concurrency that explicitly need it: `Dispatcher.ensureTopic` (mutex-protected lazy topic create, double-checked locking), heartbeat `Watcher.scan` (map mutates under `mu`), command `Queue` (channel + mutex hybrid), `realert.Poller`, the async goroutine launched from `handler.go` `cmdResultHandler` for relay dispatch, plus `retention.Policy.runLoop` (3 parallel goroutines).
- `TestQueue_ConcurrentEnqueue` exists but only fan-ins enqueues (no parallel reader, no result-record contention).
- Recommendation: add a `make test-race` target plus a CI job (Linux runner only, since CGO is needed) that runs `go test ./... -race -timeout 5m`. Even if it stays green, it's a lockable safety net.

### TEST-02 — HIGH — heartbeat watcher tests are wall-clock flaky
- File: `internal/backend/heartbeat/watcher_test.go:46,71,102,129,156,167`
- Six tests use `time.Sleep(80–250ms)` to give the goroutine enough wall time to tick. Under load (CI shared runners, Windows time-slicing) this WILL flake. The `Watcher` already accepts `ScanEvery` for cadence, but does not allow injecting a fake clock or a "single-tick" entrypoint.
- The `TestWatcherSuppressesOfflineAfterResume` (line 140) is especially fragile: it sleeps 80 ms expecting "still inside grace", then 250 ms expecting "past grace". A 30 ms scheduler hiccup can flip the assertion.
- Fix: extract a testable `scan(ctx, now time.Time)` (it's already `scan(ctx)` reading `time.Now()`). Add a `Now func() time.Time` field on `Watcher` (mirror what `retention.Policy` already does at `policy.go:30,33-38`), and write tests that call `scan` synchronously twice with controlled "now" — same coverage, no sleeps.

### TEST-03 — HIGH — `realert.Poller.tick` tested but `Poller.Run` is not (0% on Run/WaitForExit)
- File: `internal/backend/realert/poller.go:40,55` (cov 0.0%)
- `tick` itself has unit tests (`TestTickStaleHardSendsRealert`, etc.) and the integration test exercises Run for ~1.5s. But there is no test that the ticker fires more than once, no test that `WaitForExit` actually waits, and `neighborSummaries` is at **9.5%** coverage — only the "non-tunnel returns nil" branch executes. Tunnel-path neighbour rendering, the silenced details branch, and the json-unmarshal-error fallback are entirely uncovered.
- Add: 2 table-driven tests on `neighborSummaries(uid, "tunnel_X")` with seeded events, and a `TestPoller_Run_FiresOncePerInterval` using `TickEvery: 25ms` + integration-style assertion on the fake TG.

### TEST-04 — HIGH — `Dispatcher` happy-path well covered; `SendOffline` and `collectNeighbors` are 0% / not asserted
- File: `internal/backend/alerts/dispatcher.go:158` (`SendOffline` 0.0%), `:128` (`collectNeighbors` 0.0%)
- `SendOffline` is the load-bearing function the heartbeat watcher uses to actually emit the OFFLINE message. It has zero unit coverage. The watcher test only asserts the **fake** `OfflineSender` was called — never that the real dispatcher's SendOffline correctly resolves the topic and renders text.
- `collectNeighbors` (0.0%) is exercised indirectly by `TestDispatcherCreatesTopicLazily`-family tests via `Hard` path, but the implementation has multiple silent error returns (`json.Unmarshal` failures, missing `tunnel_name`) that no test asserts.
- Add: `TestSendOffline_*` (happy + topic-creation + topic-error path) and `TestCollectNeighbors_*` (empty, multiple, malformed JSON).

### TEST-05 — MED — handler's async relay tests are time.Sleep-loops (correct pattern, but redundant)
- File: `internal/backend/handler_test.go:294-300, 537-546, 609-619`, `cmd/backend/integration_test.go:344-350`
- Tests poll `time.Now().Before(deadline)` with 10ms sleeps for 500ms or 2s. This works but couples each test to wall-clock latency. Three different tests do the same poll loop with no shared helper.
- Refactor: extract a single `waitForRelay(t, getCounter, deadline)` helper so behaviour is consistent and a single bug-fix moves them all.
- Not flaky today (500ms is generous), but on a loaded CI Windows runner this can blow up; bumping the budget piecemeal is what we'd avoid.

### TEST-06 — MED — `time.Now()` is hardcoded in production critical-path code
- File: `internal/backend/heartbeat/watcher.go:93,121`; `internal/backend/realert/poller.go:111,154`; `internal/backend/alerts/dispatcher.go:48,92,109`
- `retention.Policy` exposes `Now func() time.Time` (`policy.go:30`) — good, used by tests at `policy_test.go:54,83`. Heartbeat, realert, dispatcher don't. This is the upstream cause of TEST-02's sleeps. Adopting the same pattern unblocks deterministic, sub-millisecond tests for the four most-critical state-machine surfaces.
- Severity MED only because tests *do* cover these paths today; HIGH for "new tests" that need to be added.

### TEST-07 — MED — `Poller.tick` does not test "send error keeps `LastAlertAt` un-bumped" race
- File: `internal/backend/realert/poller.go:154` (uses `BumpLastAlertAt`); `internal/backend/db/state.go:117`
- `TestTickSendErrorPreservesLastAlertAt` (`poller_test.go:128`) exists, good. But the comment at `poller.go:152-154` says `BumpLastAlertAt` exists specifically to avoid race-overwriting an FSM Recovery that occurred between `StaleHards` and the bump. There is no test that simulates that interleave (Recovery transition writes between the StaleHards read and the Bump). Without a race test or a contention-mock test, this is regression-prone.
- Add: in `state_test.go`, write a test that calls `BumpLastAlertAt` while a concurrent `Save("ok")` flips status, asserting `BumpLastAlertAt` does NOT resurrect a hard-only field.

### TEST-08 — MED — handler 64.1% on `reportHandler`; ingest edge cases missing
- File: `internal/backend/handler.go:92` (cov 64.1%), `:204` cmdResultHandler 71.2%
- Existing tests cover happy + auth + bad JSON + too-large. Missing:
  - Report with `Checks=nil` (empty slice) — does it 200 or 400? No test.
  - Report with duplicate check names in same payload — first-wins or last-wins? No test.
  - `Resumed=true` with `Resumer=nil` (handler nil-safety) — `TestReportNotResumedSkipsResumer` covers Resumed=false but not the Resumed=true + nil Resumer combo.
  - `cmdResultHandler` with `originRef` consume miss + non-nil `RoutesNotifier` — does it skip cleanly?
- These are 5–10 LoC tests each.

### TEST-09 — MED — `realert.neighborSummaries` 9.5% — silently returns nil on most error paths
- File: `internal/backend/realert/poller.go:75-108`
- The function has 4 branches (non-tunnel early-return, db error, json-unmarshal error, normal path). Only branch 1 is tested. Realert messages with neighbours are visible to the operator and have wrong/missing data on json-unmarshal-error today without anyone noticing.
- Add: 3 explicit tests after a clock-injection refactor (TEST-06).

### TEST-10 — LOW — 2-second sleep in `client_test.go` slows the suite
- File: `internal/agent/client_test.go:74-86`
- `TestClient_SendReport_ContextCancellation` makes the fake handler `time.Sleep(2 * time.Second)` to give the 50ms cancellation a chance to fire. The test cancels in 50ms so the actual wait is ~50ms — but the goroutine in the server keeps sleeping. On a tight CI it's harmless; combined with `httptest.NewServer` cleanup (which waits for in-flight handlers) it can add seconds to total suite time. Replace with `<-ctx.Done()` in the handler.

### TEST-11 — LOW — fake TG implementations duplicated 6+ times, drift risk
- Files: `internal/backend/alerts/dispatcher_test.go:21`, `internal/backend/realert/poller_test.go:14`, `internal/backend/callbacks/router_test.go:16`, `internal/backend/heartbeat/watcher_test.go:15`, `internal/backend/handler_test.go:170,491,561`, `cmd/backend/integration_test.go:404`.
- Each file defines its own `fakeTG`/`fakeRouterTG`/`fakeCmdSink`/`fakeMaintNotifier`/`fakeRoutesNotifier`. Each implements the relevant interface differently (some return msg-id `len(sent)*100`, some return `1`, some return `0`). When the production interface gains a method (e.g. `SendMessageWithReplyKeyboard` was added recently), every fake must be updated. No central `internal/backend/tg/tgtest` helper exists.
- Recommend: create `internal/testutil/fakes` with one canonical `*FakeTG` implementing every variant, parameterised. Reduces churn on interface evolution.

### TEST-12 — LOW — fixture hygiene: tokens are 64-char hex literals duplicated everywhere
- Files: many `_test.go` define `tok := "0000...0000"`/`"aaaa...aaaa"` 64-char strings inline.
- Once is fine. After 30+ of these, mistakes (61 chars vs 64) become silent — `db.Users().Insert` accepts whatever length. No central `tokenN(t)` helper.
- Add `testutil.NewToken(t, prefix string) string` — produces a deterministic 64-char hex token salted by prefix, fails fast if length wrong.

### TEST-13 — LOW — `dispatcher_test.go` `fakeTG` does not check `parseMode`
- File: `internal/backend/alerts/dispatcher_test.go:44,51`
- Both Send methods receive `parseMode` and discard it. The dispatcher always passes `""` today; if production ever passes `MarkdownV2` (and given the MEMORY note about TG MarkdownV2 gotchas, this is a real risk), the change is invisible to tests.
- Add a single assertion in one HARD-path test that `parseMode == ""` (or whatever the production contract is).

### TEST-14 — LOW — integration test's "trigger 3 fails → HARD" loop reads-modifies-writes through real DB & FSM
- File: `cmd/backend/integration_test.go:94-100`
- Good: this is what an integration test should do. But there's no test variant where the 3 fails interleave with another goroutine's report, which is the production reality (multiple agents reporting simultaneously). No assertion that two concurrent reports for two different users don't corrupt each other's incident_state (the DB has UNIQUE(user_id, check_name), and SQLite single-writer ought to handle it, but no test confirms).
- Add: parallelised `t.Run` inside `TestStage2EndToEnd` that drives 3 users concurrently and asserts each ends up in HARD.

### TEST-15 — LOW — `cmd/deploy` 16.8% — almost all `actions.go` SSH/upload paths are 0%
- File: `cmd/deploy/actions.go` — 25+ functions at 0.0% coverage.
- Most of these are wizard flows that interactively call SSH; they're hard to unit-test by design. State, templates, secrets-file IO are all decently covered (`secrets_test.go`, `state_test.go`, `templates_test.go`). The 0.0% functions are deliberately glue.
- Recommendation: leave as-is; an end-to-end smoke test (already exists in `smoke_test.go`) is the right escape hatch. Don't add coverage for coverage's sake.

### TEST-16 — LOW — no fuzz tests anywhere
- `grep -rn "func Fuzz" --include="*.go"` returns 0 matches.
- Candidates that would benefit: `wire.IsValidCommandAction`/`IsValidCommandResultStatus` (input from network), `callbacks.parse*` (callback_data parsing — hostile-ish input), `agent/awgmgr` JSON unmarshal paths.
- Low priority; existing table tests cover the named cases.

---

## Mocking-boundary verdict

Specifically requested for `internal/backend/handler_test.go` and `dispatcher_test.go`:

- **handler_test.go**: boundaries are correct — `*db.DB` is real (using `t.TempDir`), `Dispatcher`/`Resumer`/`CommandSink`/`TGNotifier`/`RoutesNotifier`/`MaintNotifier` are all faked at their interface seam, the HTTP layer is `httptest.NewServer`. This is the right shape: real persistence + fake outbound. Two minor issues already covered (TEST-05 polling helper, TEST-08 missing edges).
- **dispatcher_test.go**: also correct — real DB, fake `TGSender`. The only over-mock concern is that `fakeTG` always returns nil error from `CreateForumTopic`, so the "topic create failed → no Save" path is uncovered. Easy fix: add a single test with `topicErr: errors.New("...")`.

No instances found of mocking too deep (e.g., faking `*sql.DB`) or mocking too shallow (e.g., faking the raw HTTP byte stream).

---

## Recommended next steps (ordered)

1. **TEST-01**: Add `make test-race` and a CI job. ~30 min including local verification (needs gcc on dev box).
2. **TEST-06 + TEST-02**: Inject `Now func() time.Time` into `Watcher`, `Dispatcher`, `Poller`. Mirror `retention.Policy` pattern. Then rewrite the 6 sleep-driven heartbeat tests deterministically. ~2 h.
3. **TEST-04**: Add `SendOffline` and `collectNeighbors` direct unit tests. ~1 h.
4. **TEST-03 + TEST-09**: Cover `Run` and `neighborSummaries` in realert. ~1 h.
5. **TEST-08**: Plug the 4 missing handler edges. ~45 min.
6. **TEST-11 + TEST-12**: Centralise fakes/fixtures into `internal/testutil/`. ~2 h, payoff is monotonically positive over time.

Estimated total to close all HIGH/MED items: ~7 hours of focused work. None require production code rewrites — only minimal seam additions (`Now func() time.Time`).
