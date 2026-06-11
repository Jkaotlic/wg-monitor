# OPKG Cron Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add dashboard-driven installation, status, and log viewing for a router-side scheduled `opkg update && opkg upgrade` script, with router disk-space guards and version visibility.

**Architecture:** Add agent commands `opkg_cron_status`, `opkg_cron_install`, `opkg_cron_logs`, and `opkg_cron_remove` over the existing command queue. The agent owns router truth: it writes a small shell script under `/opt/etc/wg-monitor`, writes an Entware cron entry, checks/starts cron best-effort, checks `/opt` free space before install and every scheduled run, and self-truncates logs. The dashboard only enqueues commands and renders structured command output, while `version_audit` provides AWG Manager, HR-Neo, and firmware versions.

**Tech Stack:** Go `net/http`, existing `wire.Command`, existing agent `ExecFunc`, Entware paths under `/opt`, static dashboard HTML/CSS/JS, Markdown docs.

---

### Task 1: Wire Contract

**Files:**
- Modify: `pkg/wire/types.go`
- Modify: `pkg/wire/maintenance.go`
- Test: `pkg/wire/types_test.go`

- [ ] Add `opkg_cron_status`, `opkg_cron_install`, `opkg_cron_logs`, `opkg_cron_remove` to the command allowlist.
- [ ] Add `OpkgCronStatus` payload with installed, schedule, script path, cron path, log path, cron available, free/total KB, last run, last status, and log tail.
- [ ] Run: `go test ./pkg/wire -run "TestIsValidCommandAction|TestCommandResult" -count=1`.

### Task 2: Agent OPKG Cron Module

**Files:**
- Create: `internal/agent/actions/opkg_cron.go`
- Test: `internal/agent/actions/opkg_cron_test.go`
- Modify: `internal/agent/actions/runner.go`
- Test: `internal/agent/actions/runner_test.go`

- [ ] Write failing tests for install refusing low `/opt` free space, install writing script and cron entry, status parsing installed schedule/log state, log tail reading, and remove deleting script/cron entry.
- [ ] Implement helpers with injectable `ExecFunc` and paths suitable for tests.
- [ ] The generated shell script must run `opkg update && opkg upgrade`, check `/opt` free KB first, lock itself, append logs, and trim logs to a small cap.
- [ ] Wire commands through `Runner.Execute`.
- [ ] Run: `go test ./internal/agent/actions -run "TestOpkgCron|TestRunner_OpkgCron" -count=1`.

### Task 3: Dashboard Command Surface

**Files:**
- Modify: `internal/backend/dashboard_handler.go`
- Test: `internal/backend/dashboard_handler_test.go`
- Modify: `internal/backend/dashboard_static/index.html`
- Modify: `internal/backend/dashboard_static/app.js`
- Modify: `internal/backend/dashboard_static/app.css`

- [ ] Allow the new `opkg_cron_*` actions through dashboard command dispatch with validated args.
- [ ] Add drawer controls for OPKG schedule status/install/logs/remove and Versions.
- [ ] Render structured `opkg_cron_*` and `version_audit` outputs as compact operator panels.
- [ ] Run: `go test ./internal/backend -run "TestDashboard" -count=1`.

### Task 4: Docs And Verification

**Files:**
- Create: `docs/operations/opkg-cron-dashboard.md`

- [ ] Document paths, schedule format, disk-space behavior, log cleanup, dashboard workflow, and rollback/remove.
- [ ] Run: `go test ./...`.
- [ ] Run: `go vet ./...`.
- [ ] Run: `govulncheck ./...`.
- [ ] Do not push, tag, or create RC unless the user explicitly asks.
