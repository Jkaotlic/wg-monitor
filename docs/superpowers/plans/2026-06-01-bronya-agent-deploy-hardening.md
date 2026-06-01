# Bronya Agent Deploy Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the new-router `bronya` deploy/operator flow fail closed: no green deploy unless the backend confirms the agent path, no destructive Telegram callback without the right actor confirmation, and no final commit/push/RC until the whole scoped fix set is tested together.

**Architecture:** Keep fixes small and aligned with existing Go packages. Backend callbacks own Telegram ACL, actor-scoped confirmation state, and guided next-step keyboards. `cmd/deploy` owns deploy truth: AWGM, legacy SSH, pull deploy, component updates, and token probes must return errors when backend confirmation is pending or failed.

**Tech Stack:** Go, PowerShell on Windows, SQLite-backed backend state, Telegram callback handlers, deploy CLI, `go test`.

---

## Operating Rules

- Do not publish a new RC for each small fix.
- During implementation, run scoped package tests only.
- Before commit/push, run the final gates in this file.
- After commit/push, create one RC only if the full scope is green.
- Prefer wizard/backend deploy truth over direct router SSH.
- Keep unrelated worktree changes untouched.

## File Map

- `internal/backend/callbacks/router.go`: callback routing, ACL, operator/topic checks, command enqueue decisions.
- `internal/backend/callbacks/maint.go`: pending maintenance confirmation store and actor-scoped consumption.
- `internal/backend/callbacks/routes_wizard.go`: route add/delete draft ownership and confirm consumption.
- `internal/backend/callbacks/access_panel.go`: admin access destructive confirmations.
- `internal/backend/callbacks/selfhosted_amnezia.go`: self-hosted Amnezia issue/manage/delete/toggle flows.
- `internal/backend/callbacks/notifier.go`: command-result next-step keyboards.
- `internal/backend/callbacks/routes_notifier.go`: route-result next-step keyboards.
- `internal/backend/callbacks/parse.go`: callback-data shape validation.
- `internal/backend/tg/maint_panel.go`: maintenance confirmation texts/keyboards.
- `cmd/deploy/actions.go`: AWGM, legacy SSH, pull deploy, deploy completion truth.
- `cmd/deploy/awgm_completion.go`: shared backend confirmation reporting.
- `cmd/deploy/update_components.go`: component update result aggregation.
- `cmd/deploy/doctor.go`: deploy doctor health/auth/heartbeat truth.
- `internal/agent/checks/hydraroute.go`: route health reporting must only fail HydraRoute/HR-Neo when that routing mechanism is actually in use.
- `internal/agent/checks/hydraroute_test.go`: mechanism-aware routing health tests.
- `pkg/wire/types.go`: command action allowlist for agent-side tunnel operations.
- `internal/agent/actions/runner.go`: agent command execution for per-tunnel restart.
- `internal/backend/tg/tunnels_panel.go`: per-tunnel action keyboard.
- `internal/backend/alerts/smart_reply.go`: incident smart-reply restart callback names.

## Current Status

- [x] `opkg_upgrade` requires explicit confirmation from callback, slash command, and reply keyboard.
- [x] Missing router in callback ACL fails closed and does not enqueue commands.
- [x] Unbound router with no owner and no topic fails closed instead of global allow.
- [x] Maintenance confirmations are scoped to the Telegram actor that opened the confirmation.
- [x] Route rebind confirmations are scoped to the Telegram actor that opened the confirmation.
- [x] Route add/delete wizard drafts are scoped to the Telegram actor that started the wizard.
- [x] Access panel `remove_op` and `unbind_owner` require ask/confirm.
- [x] `opkg_disable` repair action requires ask/confirm.
- [x] Self-hosted Amnezia delete requires ask/confirm.
- [x] Self-hosted Amnezia issue flow is available to authorized operators, while manage/edit/toggle/delete stays admin-only.
- [x] AWGM deploy does not apply success metadata until backend completion is confirmed.
- [x] Legacy SSH deploy does not apply success metadata until backend completion is confirmed.
- [x] Doctor AWGM-only direct-SSH skip requires backend heartbeat plus auth probe.
- [x] Guided result keyboards now offer direct next checks after import, route apply, and connectivity checks.
- [x] Routing health is mechanism-aware: HR-Neo/HydraRoute, NDMS, IP/static, and sing-box router do not create false failures for mechanisms the user is not using.
- [x] Tunnels panel exposes a clear per-tunnel restart action that performs disable+enable for that tunnel.

## Remaining Tasks

### Task 1: Finish deploy false-success fixes

**Files:**
- Modify: `cmd/deploy/update_components.go`
- Modify: `cmd/deploy/update_components_test.go`
- Modify: `cmd/deploy/actions.go`
- Modify: `cmd/deploy/actions_test.go`

- [x] Add or adjust a focused test where one selected component update fails and `actionUpdateComponents` returns a non-nil error.
- [x] Implement aggregation so any failed selected update makes `update-components` exit non-green.
- [x] Add or adjust focused tests where pull deploy ACK timeout and heartbeat timeout return non-nil errors while preserving pending state.
- [x] Implement non-green return for pending/timeout pull deploy states.
- [x] Run: `go test ./cmd/deploy -run "Test(ActionUpdateComponents|RunPullDeploy|PullDeploy)" -count=1`

### Task 2: Tighten deploy token/auth truth

**Files:**
- Modify: `cmd/deploy/actions.go`
- Modify: `cmd/deploy/actions_test.go`

- [x] Add table tests for `probeAgentTokenValid`: accept only expected success statuses and reject redirects, 401/403, and 5xx.
- [x] Implement strict status handling for `probeAgentTokenValid`.
- [x] Verify legacy SSH confirmed-success path calls `ensureTopicAfterSuccessfulInstall`; add it if missing.
- [x] Run: `go test ./cmd/deploy -run "Test(ProbeAgentTokenValid|LegacySSH|ApplyLegacySSH)" -count=1`

### Task 3: Decide and close toggle-confirm policy

**Files:**
- Inspect: `internal/backend/tg/tunnels_panel.go`
- Inspect: `internal/backend/tg/pingcheck_panel.go`
- Inspect: `internal/backend/callbacks/router.go`
- Inspect: `internal/backend/callbacks/selfhosted_amnezia.go`

- [x] Classify `tunnel_enable`, `tunnel_disable`, `pingcheck_toggle`, and `amz_selfhosted_toggle` as operational toggles, not destructive deletes/upgrades.
- [x] Keep destructive operations (`tunnel_delete`, access removals, self-hosted delete, opkg upgrade/disable, maintenance restarts) behind ask/confirm flows.
- [x] Document why operational toggles remain one-tap: they are reversible, panel-scoped, and tunnel toggles are guarded by live snapshot stale-button refresh before enqueue.
- [x] Run: `go test ./internal/backend/callbacks ./internal/backend/tg -run "Test(Parse|RouterTunnel|SelfHosted|Opkg|Access|Maint|RouteWizard|ACL|RouterAllows|Notifier|RoutesPanel)" -count=1`

### Task 4: Make route health mechanism-aware

**Files:**
- Modify: `internal/agent/checks/hydraroute.go`
- Add or modify: `internal/agent/checks/hydraroute_test.go`

- [x] Add a focused test where HydraRoute is installed but stopped, NDMS DNS routing is enabled, and the `hydraroute` check reports OK instead of a false failure.
- [x] Add a focused test where HydraRoute is installed but stopped, HR-Neo/HydraRoute DNS routing is enabled, and the `hydraroute` check still fails.
- [x] Add a focused test where IP/static routing or sing-box router is the active mechanism and stopped HydraRoute does not report broken.
- [x] Implement mechanism classification from AWG Manager DNS routes, static routes, and system info.
- [x] Do not add sing-box route redistribution; only classify it so test-mode sing-box routers do not create false HydraRoute alarms.
- [x] Run: `go test ./internal/agent/checks -run "TestHydraRouteCheck" -count=1`

### Task 5: Add per-tunnel restart action

**Files:**
- Modify: `pkg/wire/types.go`
- Modify: `pkg/wire/types_test.go`
- Modify: `internal/agent/actions/runner.go`
- Modify: `internal/agent/actions/runner_test.go`
- Modify: `internal/backend/tg/tunnels_panel.go`
- Modify: `internal/backend/tg/tunnels_panel_test.go`
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/parse_test.go`
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/router_test.go`
- Modify: `internal/backend/alerts/smart_reply.go`
- Modify: `internal/backend/alerts/smart_reply_test.go`

- [x] Add a new wire action name for restarting one tunnel without reusing global `restart_tunnel`.
- [x] Add parser and stale-panel guard support for the new action with the same tunnel identity args used by enable/disable/delete.
- [x] Add an agent runner test proving restart executes down then up for the selected tunnel and forces a fresh report on success.
- [x] Implement agent-side restart as disable+enable for the resolved tunnel target.
- [x] Add a tunnels panel button that is visually distinct from enable/disable and does not overcrowd stale snapshots.
- [x] Update smart replies that currently say "restart tunnel" but call global `restart_tunnel` so they use the new per-tunnel callback when a tunnel identity is known.
- [x] Run: `go test ./pkg/wire ./internal/agent/actions ./internal/backend/callbacks ./internal/backend/tg ./internal/backend/alerts -run "Test.*(Restart|Tunnel|Parse)" -count=1`

### Task 6: Final scoped verification before commit

**Files:**
- All modified files in `cmd/deploy`, `internal/backend/callbacks`, and `internal/backend/tg`.

- [x] Run: `go test ./cmd/deploy -count=1`
- [x] Run: `go test ./internal/backend/callbacks ./internal/backend/tg -count=1`
- [x] Run: `git diff --check`
- [x] Review: `git diff --stat`
- [x] Review: `git status --short`

### Task 7: Commit, push, and one RC

**Files:**
- No code edits unless final verification exposes an issue.

- [ ] Commit all scoped changes with one clear message.
- [ ] Push `main`.
- [ ] Create the next RC once, after the full scope is green.
- [ ] Verify GitHub Actions and release assets.
- [ ] Report root cause, changed behavior, tests, commit, tag, and any residual risk.

## Final Gates

```powershell
go test ./cmd/deploy -count=1
go test ./internal/backend/callbacks ./internal/backend/tg -count=1
git diff --check
git status --short
```

## Latest Evidence

- `go test ./internal/backend/callbacks -count=1` passed after callback, ACL, actor-confirm, and guided-flow changes.
- `go test ./internal/backend/tg -count=1` passed after maintenance UI changes.
- `git diff --check -- internal/backend/callbacks internal/backend/tg` passed.
- `go test ./cmd/deploy -run "Test(ActionUpdateComponents|RunPullDeploy|MobilePull|PullDeployHeartbeat|ProbeAgentTokenValid|ApplyLegacySSH|ApplyAWGMDeploy|DoctorAWGMOnly)" -count=1` passed after deploy false-success changes.
- `go test ./internal/agent/checks -run "TestHydraRouteCheck" -count=1` passed after routing-mechanism-aware HydraRoute checks.
- `go test ./pkg/wire ./internal/agent/actions ./internal/backend/callbacks ./internal/backend/tg ./internal/backend/alerts -run "Test.*(TunnelRestart|Restart|TunnelPanel|TunnelCallbacks|SmartReply|IsValidCommandAction|CommandResult)" -count=1` passed after adding per-tunnel restart.
- `go test ./internal/agent/checks ./internal/agent/actions -count=1`, `go test ./pkg/wire ./internal/backend/alerts ./internal/backend/callbacks ./internal/backend/tg -count=1`, and `go test ./cmd/deploy -count=1` passed for the current scoped package set.
- `git diff --check`, `git diff --stat`, and `git status --short` were reviewed after the current scoped package tests.
- `go test ./... -count=1` and `go vet ./...` passed as the final pre-commit release gate.
