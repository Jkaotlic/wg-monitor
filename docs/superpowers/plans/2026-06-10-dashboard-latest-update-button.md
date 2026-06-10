# Dashboard Latest Update Button Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the dashboard show an explicit one-click update button for the newest operator release instead of forcing manual version entry, render command results as readable operator panels instead of raw JSON, and collect the deploy metadata needed when creating a new router from the dashboard.

**Architecture:** Add release metadata to the dashboard summary and use it in the embedded static UI. The backend discovers the newest release with operator semantics: the newest published RC for the current base version may beat the stable tag, because this project runs RCs operationally after stable. Keep the existing manual deploy modal as a custom-version fallback.

**Tech Stack:** Go `net/http`, GitHub Releases API JSON, existing dashboard static HTML/CSS/JS, existing wizard deploy endpoint.

---

### Task 1: Backend Release Metadata

**Files:**
- Modify: `internal/backend/dashboard_handler.go`
- Modify: `internal/backend/dashboard_handler_test.go`

- [x] Add tests for `dashboardOperatorLatestRelease` that prove `v0.13.0-rc120` beats stable `v0.13.0`.
- [x] Add a dashboard summary test proving `latest_version` is present.
- [x] Implement a small GitHub releases fetcher with a short timeout and fallback to `serverVersion`.

### Task 2: Dashboard UI

**Files:**
- Modify: `internal/backend/dashboard_static/index.html`
- Modify: `internal/backend/dashboard_static/app.css`
- Modify: `internal/backend/dashboard_static/app.js`
- Modify: `internal/backend/dashboard_handler_test.go`

- [x] Add static smoke tests for `Update to latest` and `Custom version`.
- [x] Render `Update to <latest>` per row and in the drawer.
- [x] Disable the button for agents already on latest.
- [x] Render pending state when `pending_version` equals latest.
- [x] Keep manual deploy as `Custom version` with the latest tag prefilled.
- [x] Render command results as readable status/key-value/list panels, with raw JSON hidden behind a details block.
- [x] Expand Add agent with deploy mode, AWG Manager URL/auth, SSH host/port/user, arch, ring, and expected MAC.
- [x] Include deploy metadata in the enrollment payload and readable enrollment result.

### Task 2.5: Enrollment Deploy Metadata

**Files:**
- Modify: `internal/backend/dashboard_handler.go`
- Modify: `internal/backend/dashboard_handler_test.go`

- [x] Add a dashboard enrollment test proving deploy metadata is stored.
- [x] Persist optional deploy metadata without wiping already-known DB fields.

### Task 3: Deploy CLI Semantics

**Files:**
- Modify: `cmd/deploy/github.go`
- Modify: `cmd/deploy/github_test.go`

- [x] Add a test proving `GetLatestRelease` chooses `v0.13.0-rc120` over `v0.13.0`.
- [x] Change release comparison so RCs published after stable can be selected for operator updates.

### Task 4: Verification And Release

- [x] Run focused tests for dashboard and deploy release selection.
- [x] Run `go test ./... -count=1`.
- [x] Run `go vet ./...`.
- [x] Run `git diff --check`.
- [ ] Commit, push `main`, tag next RC, verify GitHub release assets, deploy backend, verify `/healthz`.
