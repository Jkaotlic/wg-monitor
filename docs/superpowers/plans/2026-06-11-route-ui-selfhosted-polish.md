# Route UI Self-Hosted Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix HR Neo route policy behavior, make route/template UX safer, reorder Telegram commands, polish the dashboard, and replace self-hosted server add with a guided flow.

**Architecture:** Keep existing agent command actions and backend callback surfaces. HR Neo route changes use the current AWG Manager `dns-routes` API fields: explicit `routes` for NDMS and `hrPolicyInterfaces` for policy-backed HydraRoute rules. UI changes stay inside the embedded dashboard assets and Telegram menu registry.

**Tech Stack:** Go, SQLite-backed backend, Telegram callback keyboards, embedded HTML/CSS/JS dashboard, AWG Manager JSON API.

---

### Task 1: HR Neo Route Semantics

**Files:**
- Modify: `internal/agent/actions/route_add_delete.go`
- Modify: `internal/agent/actions/route_status.go`
- Modify: `internal/agent/actions/tunnel_import.go`
- Test: `internal/agent/actions/route_add_delete_test.go`
- Test: `internal/agent/actions/route_status_test.go`
- Test: `internal/agent/actions/tunnel_import_test.go`

- [ ] Add failing tests proving HR Neo add creates policy-backed routes with `routes` omitted and `hrPolicyInterfaces` set to the live tunnel iface.
- [ ] Add failing tests proving snapshot display credits HR Neo policy rules to the selected tunnel.
- [ ] Add failing tests proving imported live tunnels are appended to existing HydraRoute policy rules and HR Neo is restarted.
- [ ] Implement minimal helpers for policy interface append and shared route bind detection.

### Task 2: Route Template UX

**Files:**
- Modify: `internal/backend/tg/routes_add_delete.go`
- Modify: `internal/backend/callbacks/router.go`
- Test: `internal/backend/tg/routes_add_delete_test.go`
- Test: `internal/backend/callbacks/router_test.go`

- [ ] Make preview text explicitly distinguish `DNS / NDMS`, `HydraRoute policy`, and `Static CIDR`.
- [ ] Keep confirm gated by the existing preview hash and overlap checks.

### Task 3: Slash Command Order

**Files:**
- Modify: `internal/backend/tg/menu_registry.go`
- Test: `cmd/backend/commands_test.go`

- [ ] Reorder operator commands by daily workflow: status, check, tunnels, routes, via, direct, amnezia, hidemy, maint, upgrade, menu, keyboard, help.
- [ ] Keep admin-only commands out of operator scope and append admin commands after operator commands.

### Task 4: Dashboard Polish

**Files:**
- Modify: `internal/backend/dashboard_static/app.css`
- Modify: `internal/backend/dashboard_static/app.js`
- Test: existing dashboard handler/static tests plus visual/browser smoke if a local server is started.

- [ ] Stabilize button/action sizing and table alignment.
- [ ] Rename dashboard actions to match operator language: Diagnostics, Check, Tunnels, Routes, AWG Manager, Update.
- [ ] Keep dense operator-console layout without introducing a landing page.

### Task 5: Self-Hosted Add Wizard

**Files:**
- Modify: `internal/backend/callbacks/selfhosted_amnezia.go`
- Modify: `internal/backend/callbacks/selfhosted_admin.go`
- Test: `internal/backend/callbacks/router_test.go` or focused self-hosted tests.

- [ ] Add guided add/edit prompts with required fields grouped as endpoint, SSH access, and advanced paths.
- [ ] Preserve the existing `/selfhosted add key=value` shortcut.
- [ ] Show a preview before saving so admins can catch wrong endpoint or SSH host.

### Task 6: Verification and Release

- [ ] Run targeted tests after each task.
- [ ] Run `go test ./... -count=1`, `go vet ./...`, `govulncheck ./...`, `gosec`, and `git diff --check`.
- [ ] Commit, push `main`, tag next RC, verify CI and release assets.
