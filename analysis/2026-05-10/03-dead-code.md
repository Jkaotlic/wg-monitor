# 03 — Dead-code & orphan-files audit (wg-monitor)

Date: 2026-05-10  
Scope: `c:/Users/user/Projects/wg-monitor` @ HEAD `a811914 (main)`  
Method: `go vet ./...` (clean), `go test ./... -count=1` (all green), manual cross-package grep for exported symbols, deprecated-marker scan, repo-tree inventory. `staticcheck` install was blocked by sandbox; results are best-effort static cross-references rather than dataflow analysis.

Severity legend:
- **HIGH**: pure dead code or stale-state artifact, removal saves real surface
- **MED**: deprecated/legacy fields kept for back-compat — candidate for removal in next major
- **LOW**: cosmetic / docs / build-artifact hygiene

---

## File-level dead code

### DEAD-01 (HIGH) — `internal/backend/tg/control_panel.go` is a fully orphaned file
- File: `internal/backend/tg/control_panel.go` (entire file)
- Marked `// DEPRECATED v0.6.0: ... Remove in v0.7.0.`
- Exported surface: `TunnelEntry`, `ControlPanelKeyboard()`, `ControlPanelText()`
- Cross-ref: zero call-sites in production code or tests outside the file itself (verified by grepping `ControlPanelText|ControlPanelKeyboard|tg\.ControlPanel` against the entire tree, excluding worktrees). Replaced by `reply_keyboard.go`.
- Project is past v0.7.0 (currently v0.11.x per memory).
- **Recommendation: DELETE the entire file.** No further migration needed — replacement (`ReplyKeyboardForTopic` + `compat_btn`) has been live for many releases.

### DEAD-02 (HIGH) — `cmd/deploy/templates/agent.yaml.tmpl` retains dead `awg` block
- File: `cmd/deploy/templates/agent.yaml.tmpl` lines 12-14
  ```yaml
  checks:
    awg:
      handshake_max_age_sec: 180
  ```
- Background: rc18 removed `awg_iface`/`expected_exit_ip` from `AgentParams` and `AgentState`. The agent's `AWGCheckConfig.HandshakeMaxAgeSec` *does* still have effect, so this single setting is live, BUT the value `180` is also the default (`internal/agent/config.go:121` `HandshakeMaxAge()` falls back to `180*time.Second`). So rendering it accomplishes nothing.
- **Recommendation: REMOVE the `awg:` sub-block from the template** — the default already handles it. If kept, leave a comment noting "explicit-default for documentation; can be deleted".

---

## Type / function level — production-orphan symbols

### DEAD-03 (HIGH) — `Queue.OriginRef` is test-only
- File: `internal/backend/cmd/queue.go:75`
- Production callers: zero. `internal/backend/handler.go:242` and the `CommandSink` interface use `ConsumeOriginRef` (the consume-and-delete sibling).
- Only ref outside queue.go itself is `internal/backend/cmd/queue_test.go:193,208`.
- **Recommendation: DELETE `OriginRef` and the two tests that exercise it.** Keep only `EnqueueWithRef`/`ConsumeOriginRef`. (Or, weaker: keep but mark "test-only" — the cost of keeping a public read-only Look method is small.)

### DEAD-04 (MED) — `OpkgRunner.DryRun` is interface-only / test-only
- File: `internal/agent/actions/opkg.go:272` (the function)  
  Interface: `internal/agent/actions/runner.go:38` `OpkgExecutor` requires `DryRun`
- Marked `// DEPRECATED 2026-05-06: kept for the OpkgExecutor interface contract + any external callers (none known in the tree).`
- Production runner only calls `SmartUpgrade` (`runner.go:110`). All `DryRun` calls in repo are `opkg_test.go` (6 sites).
- **Recommendation: REMOVE `DryRun` from the `OpkgExecutor` interface AND from `OpkgRunner`, and delete the corresponding tests.** No external Go consumers — this is an `internal/` package.

### DEAD-05 (MED) — `AWGCheckConfig` deprecated fields
- File: `internal/agent/config.go:113-116`
  - `Interface` (yaml: `interface`)
  - `ExpectedExitIP` (yaml: `expected_exit_ip`)
  - `MarkerURL` (yaml: `marker_url`)
  - `RoutingProbeURL` (yaml: `routing_probe_url`)
- Comments correctly note "deprecated (legacy parse only)".
- Production read-side: zero. Only `internal/agent/config_test.go` references the Go fields (just to assert that legacy YAML still parses).
- These are kept so old `config.yaml` on routers in production doesn't fail parse — that *is* a legitimate reason at v0.11. But yaml unmarshalling already silently ignores unknown keys for non-strict-mode `yaml.Unmarshal`. **Verify** strict-mode is off (currently it is — plain `yaml.Unmarshal` at `config.go:165`), then **DELETE these four struct fields**. Behavior identical: unknown YAML keys are dropped. Only `HandshakeMaxAgeSec` remains, which is the only one with effect.
- Side-effect: `config_test.go` has 3 tests that probe these fields — those should be reduced to "load doesn't error on legacy yaml" smoke tests rather than asserting field values.
- **Recommendation: DELETE the four deprecated fields; rewrite tests to assert "legacy yaml still parses" without poking the struct fields.**

### DEAD-06 (LOW) — `heartbeat.Config.StaleAfter` is legacy single-knob
- File: `internal/backend/heartbeat/watcher.go:33`, `cmd/backend/main.go:66`, `internal/backend/config.go:58`
- Comment says "deprecated, see StaleAfter{Static,Mobile}". The fallback in `staleFor()` (lines 51, 60) routes through this when the kind-aware fields are zero.
- The backend config defaulting at `internal/backend/config.go:135-140` *already* fills `StaleAfterStaticSec`/`StaleAfterMobileSec` from `StaleAfterSec` if absent — so the fallback in watcher.go is dead at runtime as long as the deployed config goes through `LoadConfig`.
- **Recommendation: keep for one more release** (defensive — old Stage-1 yaml in the wild may still set only `stale_after_sec`). Plan removal in next major; remove the fallback branch in `watcher.go:46-65` and the `StaleAfter` struct field at the same time.

---

## Orphan tests for fields/funcs already gone

### DEAD-07 (MED) — `internal/agent/config_test.go` tests dead fields
- Lines: 43-44, 176, 182-183 (and many bodies that include `interface:`/`expected_exit_ip:`/`marker_url:` in the YAML literal)
- 6 tests embed legacy YAML keys into the test fixture body, and 2 of them assert `cfg.Checks.AWG.Interface == "awg0"` / `cfg.Checks.AWG.ExpectedExitIP == "..."` — i.e. they verify that the dead fields parse, not that they do anything.
- Rationale to keep: protects against accidental strict-mode in `LoadConfig` that would break old configs.
- **Recommendation: collapse the 6 tests into a single `TestLoadConfig_LegacyAwgFields_StillParse` that just asserts no error.** Remove the field-value assertions. Drop the `interface:`, `expected_exit_ip:`, `marker_url:` lines from all *other* test bodies — they only obscure intent now.

### DEAD-08 (LOW) — `cmd/deploy/templates_test.go:36-56` `TestRenderAgentYAML` includes negative-assertion for stale fields
- File: `cmd/deploy/templates_test.go:55-56`  
  ```go
  // awg_iface / expected_exit_ip — deprecated в агенте; шаблон не должен их рендерить.
  for _, dont := range []string{"awg_iface:", "expected_exit_ip:"} {
  ```
- Test is healthy and useful — a regression-guard that the template never re-introduces the dead keys.
- **Recommendation: KEEP.** Cited only because it's the "don't" half of a pair where the "do" assertions still mention `awg_iface=awg0` etc. (`actions.go` `vpsAddUser` still sends placeholders to the CLI — see DEAD-10).

---

## Cross-cutting: stale CLI surface

### DEAD-09 (MED) — `cmd/wg-monitor-cli/main.go` AWGIface/ExpectedExitIP options + DB columns
- Files: `cmd/wg-monitor-cli/main.go:74-112,140`, `cmd/wg-monitor-cli/list_users.go:52`, `internal/backend/db/users.go:22-29,49,53,57-65,79,135,145`, `internal/backend/db/migrations.sql:5-6` (`awg_iface TEXT NOT NULL`, `expected_exit_ip TEXT NOT NULL`)
- The DB schema still has these columns as `NOT NULL`. The CLI requires non-empty values. The wizard (`cmd/deploy/actions.go:771`) hardcodes `--awg-iface=awg0 --expected-exit-ip=0.0.0.0` because the agent silently ignores them after the awg-manager pivot.
- This is the "load-bearing dead column" pattern: the column exists only because dropping it requires a SQLite ALTER TABLE migration on every deployed VPS. Removal is a bigger change than this audit's scope.
- **Recommendation: schedule a v0.12 migration** that (a) adds DB migration to drop both columns, (b) drops the CLI flags `--awg-iface`/`--expected-exit-ip`, (c) removes the placeholders in the wizard. Until then, leave them — they're benign.

### DEAD-10 (LOW) — `cmd/deploy/actions.go:720` stale comment
- Comment: `// error after we've already collected awg_iface/expected_exit_ip.`
- The wizard no longer collects these — they're hardcoded placeholders now. Comment is misleading.
- **Recommendation: update comment** to "error after we've already collected the nickname".

---

## Build artifacts / repo hygiene

### DEAD-11 (LOW) — committed-but-gitignored binaries pollute working tree
- Untracked binaries in repo root: `agent.exe` (10 MB), `backend.exe` (16 MB), `deploy.exe` (14 MB)
- Untracked binaries in `bin/`: `wg-monitor`, `wg-monitor-backend`, `wg-monitor-backend-linux-amd64`, `wg-monitor-cli`, `wg-monitor-cli-linux-amd64`, `wg-monitor-deploy.exe`, plus subdirs `linux-amd64`, `linux-arm64`, `linux-mipsle`
- Untracked binaries in `dist/`: `wg-monitor-agent`, `wg-monitor-agent-arm64`, `wg-monitor-backend-amd64`
- `.gitignore` correctly excludes `/bin/`, `/dist/`, `*.exe`. **None are committed**, so this is purely local hygiene.
- **Recommendation: optional `make clean` target** to wipe local artifacts. No git action needed.

### DEAD-12 (HIGH) — three locked subagent worktrees in `.claude/worktrees/`
- Paths:
  - `.claude/worktrees/agent-a2770a298009fc19a` — branch `worktree-agent-a2770a298009fc19a`, HEAD `83731b2 feat(deploy): add known-hosts mgmt, secrets export/import, and doctor command` (2026-05-09)
  - `.claude/worktrees/agent-a3962b6d3ecb3f481` — HEAD `82b8592 fix(deploy): actionable SSH errors + upload progress dots` (2026-05-09)
  - `.claude/worktrees/agent-a99d1aa9114534157` — HEAD `ad4f063 feat(deploy): auto-create TG topic, smoke test action, yes-to-all override` (2026-05-09)
- All three were already merged into `main`: commits `9536099`, `0871c70`, `48670f2`. Confirmed in `git log` — visible at the head of `main`.
- They double the file count for every grep (worktree variants of every file appear in search results).
- Status flag `locked` per `git worktree list`.
- **Recommendation: REMOVE all three worktrees.** Procedure (sequential):
  1. `git worktree unlock <path>` for each
  2. `git worktree remove <path>` for each
  3. `git branch -D worktree-agent-...` to remove the per-worktree branches
- Do not run destructively without operator confirmation per auto-mode rules — flag for user.

---

## Documentation drift

### DEAD-13 (LOW) — `docs/superpowers/specs/2026-04-25-wg-monitor-design.md` is now historical
- This spec describes the original `awg_routing` check via `curl --interface ... ifconfig.me` (lines 85-101) and `expected_exit_ip` as a per-user value. The 2026-04-29 awg-manager pivot replaced it. Multiple downstream specs reference this one but the implementation diverged.
- Same for `docs/superpowers/plans/2026-04-26-wg-monitor-stage-0.md` and `...stage-1.md` — they still propose `awg_iface` parsing/storage that has since been silently deprecated.
- These are historical plans, not living docs — no harm leaving them, but a top-of-file `> SUPERSEDED 2026-04-29: see ...checks-fix.md` banner would help future readers (or future-Claude) avoid acting on outdated guidance.
- **Recommendation: add 2-line `> SUPERSEDED` banner** to `2026-04-25-wg-monitor-design.md`, `stage-0.md`, `stage-1.md` pointing at the awg-manager pivot. Optional / cosmetic.

### DEAD-14 (LOW) — `docs/research/2026-04-30-fleet-monitoring-landscape.md` is fine as-is
- One-off landscape research doc, dated. Keep — it's the kind of thing that's useful to look back at even when stale.
- No action.

---

## Already-clean surfaces (verified, NO action)

- `pkg/wire/types.go` — every exported `Command*` symbol is referenced by both agent and backend (rc18 cleanup looks complete: no `awg_iface`/`expected_exit_ip` in wire format).
- `cmd/deploy/state.go` — `AgentState` has 9 fields, all read by `actions.go`/`menu.go`. No leftover fields after rc18.
- `cmd/deploy/templates.go` `AgentParams` — three fields, all consumed by the template.
- `internal/backend/state/fsm.go`, `internal/backend/realert/poller.go`, `internal/backend/retention/policy.go`, `internal/backend/cmd/queue.go` (minus DEAD-03), `internal/backend/upstream/*` — every exported function has a non-test caller in the tree.
- `internal/agent/checks/*.go` — every check type is constructed in `cmd/agent/main.go`.
- `internal/agent/awgmgr/client.go` — every public client method (`StartTunnel`, `ReplaceConf`, `GetEnv`, `DeleteTunnel`, …) has a caller in `internal/agent/actions/`.
- `internal/agent/keenetic/*.go` — `FetchIfaceMap` / `NDMC.Show` / `ParseDNSEndpoints` all called.
- `internal/backend/tg/maint_panel.go`, `keyboard.go`, `reply_keyboard.go`, `tunnels_panel.go` — every exported renderer is wired through `callbacks/router.go`.

---

## Suggested execution order

1. **Quickwin (no risk):** DEAD-01 (delete control_panel.go) + DEAD-03 (delete `OriginRef`) + DEAD-10 (comment fix). One commit, no behavior change.
2. **Cleanup (low risk):** DEAD-02 (template), DEAD-04 (DryRun), DEAD-07 (test reduction). Need to re-run tests; behavior unchanged.
3. **Operator action:** DEAD-12 (remove three worktrees) — requires user confirmation since worktrees are user state.
4. **Schedule for v0.12:** DEAD-05, DEAD-06, DEAD-09 (DB migration) — needs deployment-coordinated.
5. **Optional:** DEAD-13 (spec banners), DEAD-11 (`make clean`), DEAD-14 (no-op).
