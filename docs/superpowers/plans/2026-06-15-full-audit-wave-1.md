# Full Audit Wave 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Re-audit the current `wg-monitor` main branch against actual code, fix the first high-confidence production bug batch, verify fully, commit, push, and continue the audit without publishing an rc.

**Architecture:** Treat old audit docs as leads, not truth. Each fix starts with current-code verification, a failing regression test, a minimal production change, then focused and full verification.

**Tech Stack:** Go 1.26.x module, standard `go test`/`go vet`, GitHub Actions workflows, `gopkg.in/yaml.v3`, `modernc.org/sqlite`, local git on `main`.

---

### Task 1: Baseline And Fact Refresh

**Files:**
- Read: `.github/workflows/ci.yml`
- Read: `.github/workflows/release.yml`
- Read: `docs/audit-2026-06-11.md`
- Read: `docs/newaudit.md`
- Read: `internal/releasesig/signature.go`
- Read: `internal/agent/actions/update_backend_url.go`
- Read: `cmd/deploy/restore_backup.go`

- [ ] **Step 1: Confirm tracked git state**

Run: `git status --short --branch`
Expected: tracked files clean before code edits; untracked historical artifacts may remain untouched.

- [ ] **Step 2: Confirm current security gates**

Run: `Get-Content .github\workflows\ci.yml`
Expected: CI includes `go vet`, `go test`, `-race`, `govulncheck`, `gosec`, and `grype`.

- [ ] **Step 3: Confirm release signature consumers**

Run: `rg -n "VerifyChecksumsSignature|SignatureRequiredForVersion|checksums.txt.sig" internal cmd .github`
Expected: release workflow signs checksums and all release-download paths either verify signatures for new versions or explicitly gate old versions.

- [ ] **Step 4: Run baseline focused checks**

Run: `go test ./internal/releasesig ./internal/releaseorigin ./internal/agent/actions ./internal/agent/cmdloop ./cmd/deploy -count=1`
Expected: tests pass before production changes; any failure becomes the first root-cause investigation.

### Task 2: Harden Restore Backup Validation

**Files:**
- Modify: `cmd/deploy/restore_backup.go`
- Modify: `cmd/deploy/restore_backup_test.go`
- Possibly import: `gopkg.in/yaml.v3`

- [ ] **Step 1: Write failing tests for invalid restored backend.yaml**

Add tests showing `InspectRestoreBackup` rejects malformed YAML and rejects an empty/non-mapping `backend.yaml` before any remote restore script can run.

Run: `go test ./cmd/deploy -run "TestInspectRestoreBackupRejectsInvalidBackendYAML|TestInspectRestoreBackupRejectsEmptyBackendYAML" -count=1`
Expected: fails because `InspectRestoreBackup` currently only checks that `backend.yaml` exists.

- [ ] **Step 2: Implement minimal local YAML validation**

Add a helper in `restore_backup.go` that reads extracted `backend.yaml`, unmarshals it with `yaml.v3`, and requires a non-empty mapping/document.

- [ ] **Step 3: Verify focused restore tests**

Run: `go test ./cmd/deploy -run "TestInspectRestoreBackup|TestBuildRestoreRemoteScript" -count=1`
Expected: all restore inspection and script safety tests pass.

### Task 3: Continue High-Risk Audit Slice

**Files:**
- Read: `internal/backend/retention/policy.go`
- Read: `internal/backend/db/db.go`
- Read: `internal/agent/cmdloop/loop.go`
- Read: `internal/backend/dashboard_handler.go`
- Read: `cmd/deploy/awgm_client.go`
- Read: `cmd/deploy/awgm_relay.go`

- [ ] **Step 1: Verify old high-risk findings against current code**

Run: `rg -n "VACUUM|resultCache|randFloat64|InsecureSkipVerify|_create_unverified_context|dashboardSessionValue" internal cmd`
Expected: identify which findings are already fixed, which are intentionally accepted, and which still need a regression test.

- [ ] **Step 2: Pick the next confirmed bug with a reproducible local test**

Choose only a bug where root cause is demonstrated in current code. Do not patch speculative risk without a failing test or an explicit documented acceptance.

- [ ] **Step 3: Follow TDD for that bug**

Run the new focused test first and watch it fail, implement the minimal fix, then rerun the focused package.

### Task 4: Verification, Commit, Push

**Files:**
- Any files changed by Tasks 2-3.

- [ ] **Step 1: Run full local verification**

Run: `go test ./... -count=1 -timeout 180s`
Expected: exit 0.

Run: `go vet ./...`
Expected: exit 0.

Run: `git diff --check`
Expected: no whitespace errors.

- [ ] **Step 2: Security checks where locally available**

Run: `govulncheck ./...`
Expected: exit 0 or document tool/network/environment failure exactly.

- [ ] **Step 3: Commit and push without rc**

Run: `git status --short`
Expected: only intended tracked changes.

Run: `git add <intended files>`

Run: `git commit -m "fix: harden audit findings"`

Run: `git push origin main`

Expected: push succeeds; do not tag or publish an rc.

### Task 5: Continue Audit

**Files:**
- Create or update: `docs/operations/2026-06-15-full-audit-wave-1.md` only if a durable checkpoint is needed after the first pushed batch.

- [ ] **Step 1: Re-open the remaining findings list**

Use current code and test output to decide the next batch after push.

- [ ] **Step 2: Repeat root-cause-first loop**

For each bug: current evidence, failing test, minimal fix, focused verification, full verification before the next commit/push batch.

---

## Self-Review

Spec coverage: Covers full-audit workflow, current-doc verification, bug fixes, accumulated changes, commit+push without rc, and continuation after push.

Placeholder scan: No TBD, TODO, or "implement later" placeholders remain. Open-ended audit work is bounded into a repeatable wave with concrete commands.

Type consistency: The only new helper planned for Task 2 stays local to `cmd/deploy/restore_backup.go` and tests through existing `InspectRestoreBackup`.
