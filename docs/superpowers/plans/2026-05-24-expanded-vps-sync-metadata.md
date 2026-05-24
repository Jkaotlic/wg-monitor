# Expanded VPS Sync Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `[6] Sync from VPS` restore more safe wizard metadata on another operator PC.

**Architecture:** Extend the existing `/v1/wizard/agents` contract and `users` deploy metadata columns with safe portable fields only: `last_deploy`, `deploy_mode`, `awgm_url`, `awgm_auth`, and `expected_mac`. Keep machine-local and secret material out of the sync path: `preferred_iface`, passwords, API keys, raw agent tokens, and SSH private key paths remain local or backup-only.

**Tech Stack:** Go, modernc SQLite migrations, existing deploy wizard sync client, existing backend wizard endpoints.

---

### Task 1: Lock Expanded Sync Behavior With Tests

**Files:**
- Modify: `cmd/deploy/vps_sync_test.go`
- Modify: `internal/backend/wizard_handler_test.go`
- Modify: `internal/backend/db/users_test.go`

- [ ] Add tests showing `MergeAgents` imports portable AWG metadata from remote agents while preserving local `preferred_iface`.
- [ ] Add tests showing `expected_mac` fills when local is empty, but a local non-empty value is not overwritten by a conflicting remote value.
- [ ] Add backend list/put tests showing the new fields round-trip through the DB and wizard JSON.

### Task 2: Extend Backend Storage And API

**Files:**
- Modify: `internal/backend/db/db.go`
- Modify: `internal/backend/db/users.go`
- Modify: `internal/backend/wizard_handler.go`

- [ ] Add idempotent nullable columns for `last_deploy`, `deploy_mode`, `awgm_url`, `awgm_auth`, and `expected_mac`.
- [ ] Extend `db.User`, scan paths, `DeployInfo`, and `UpdateDeployInfo`.
- [ ] Extend `wizardAgent` and `wizardPutAgentReq` JSON without making new fields required.

### Task 3: Extend Deploy Sync Client

**Files:**
- Modify: `cmd/deploy/vps_sync.go`

- [ ] Add the same fields to `RemoteAgent`.
- [ ] Include them in `PushAgent` and `AgentStateToRemote`.
- [ ] Update `MergeAgents` so remote portable metadata fills local state, with guarded `expected_mac` conflict handling and no `preferred_iface` sync.

### Task 4: Docs, Verification, Release

**Files:**
- Create: `docs/releases/v0.13.0-rc36.md`
- Modify: `README.md`
- Modify: `DEPLOY.md`

- [ ] Document the wider but non-secret sync pool.
- [ ] Run focused tests, `go test ./cmd/deploy`, `go test ./internal/backend/...`, `go test ./...`, and `git diff --check`.
- [ ] Commit, push, tag `v0.13.0-rc36`, verify GitHub release assets and prerelease flag.
