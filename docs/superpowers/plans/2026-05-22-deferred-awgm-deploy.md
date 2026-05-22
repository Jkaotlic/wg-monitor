# Deferred AWG Manager Deploy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Queue AWG Manager installs for offline routers and let the VPS complete them when the router wakes.

**Architecture:** Add a deploy-wizard deferred job writer plus a VPS-side systemd timer/runner. Reuse the existing AWG Manager relay Python and extend it with a `deferred_bootstrap` mode.

**Tech Stack:** Go deploy wizard, Python relay script, systemd timer/service, existing wizard API and backend release mirror.

---

### Task 1: Deferred Job Config

**Files:**
- Create: `cmd/deploy/awgm_deferred.go`
- Test: `cmd/deploy/awgm_deferred_test.go`

- [x] Write a failing test proving deferred configs do not store an agent raw token before the router wakes.
- [x] Implement `buildDeferredAWGMConfig`.
- [x] Verify targeted tests pass.

### Task 2: VPS Runner

**Files:**
- Modify: `cmd/deploy/awgm_deferred.go`
- Test: `cmd/deploy/awgm_deferred_test.go`

- [x] Write a failing test for runner script contents.
- [x] Upload relay script, runner, service, timer, and job JSON over backend SSH.
- [x] Enable and start `wg-monitor-deferred-awgm.timer`.

### Task 3: Relay Deferred Mode

**Files:**
- Modify: `cmd/deploy/awgm_relay.go`
- Test: `cmd/deploy/awgm_deferred_test.go`

- [x] Write tests that assert `deferred_bootstrap` support and Python syntax validity.
- [x] Add local enrollment, dynamic bootstrap rendering, metadata update, token marker, and job cleanup.

### Task 4: Wizard Integration

**Files:**
- Modify: `cmd/deploy/actions.go`

- [x] Offer deferred deploy on transient public AWG Manager failures.
- [x] Mark local agent state as pending with the target release.
- [x] Keep live AWG Manager deploy behavior unchanged.

### Task 5: Verification and Release

**Files:**
- Modify: `README.md`
- Create: `docs/releases/v0.13.0-rc23.md`

- [ ] Run targeted tests.
- [ ] Run full `go test ./... -count=1`.
- [ ] Commit, push, tag `v0.13.0-rc23`, and verify GitHub release assets.
