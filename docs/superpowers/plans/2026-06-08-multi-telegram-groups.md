# Multi Telegram Groups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one backend and one bot serve multiple operator supergroups while each router belongs to exactly one group.

**Architecture:** Store the router's owning Telegram chat in `users.telegram_chat_id`; NULL means the primary configured `telegram.chat_id` for backward compatibility. Resolve every per-router action by `(chat_id, message_thread_id)`, not by `message_thread_id` alone, and send alerts/lifecycle messages to the router's effective chat. Allow callbacks/messages only from the primary chat, configured extra chats, or the admin private panel.

**Tech Stack:** Go, SQLite via `modernc.org/sqlite`, Telegram forum topics, existing backend callback and alert packages.

---

### Task 1: Persist Router Telegram Chat

**Files:**
- Modify: `internal/backend/db/migrations.sql`
- Modify: `internal/backend/db/db.go`
- Modify: `internal/backend/db/users.go`
- Test: `internal/backend/db/users_test.go`

- [ ] **Step 1: Write the failing DB tests**

Add tests proving an old NULL chat row resolves to the primary chat, and a secondary chat binding rejects the same topic id from another group:

```go
func TestUsersGetByChatThreadIDUsesDefaultChatForLegacyRows(t *testing.T) {
	d := openTestDB(t)
	id, err := d.Users().Insert("legacy", "tok", "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(id, 42); err != nil {
		t.Fatal(err)
	}
	got, err := d.Users().GetByChatThreadID(-100, 42, -100)
	if err != nil {
		t.Fatal(err)
	}
	if got.Nickname != "legacy" {
		t.Fatalf("nickname = %q", got.Nickname)
	}
	if _, err := d.Users().GetByChatThreadID(-200, 42, -100); !errors.Is(err, db.ErrUserNotFound) {
		t.Fatalf("wrong chat lookup err = %v", err)
	}
}
```

```go
func TestUsersTopicBindingIncludesTelegramChatID(t *testing.T) {
	d := openTestDB(t)
	id, err := d.Users().Insert("tenant", "tok", "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateTelegramTopic(id, -200, 42); err != nil {
		t.Fatal(err)
	}
	got, err := d.Users().GetByChatThreadID(-200, 42, -100)
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectiveTelegramChatID(-100) != -200 {
		t.Fatalf("effective chat = %d", got.EffectiveTelegramChatID(-100))
	}
	if _, err := d.Users().GetByChatThreadID(-100, 42, -100); !errors.Is(err, db.ErrUserNotFound) {
		t.Fatalf("primary chat lookup err = %v", err)
	}
}
```

- [ ] **Step 2: Run DB tests and verify RED**

Run: `$env:GOCACHE = (Join-Path (Get-Location) '.gocache'); go test ./internal/backend/db -run 'TestUsers(GetByChatThreadID|TopicBinding)' -count=1`

Expected: FAIL because `UpdateTelegramTopic`, `GetByChatThreadID`, `EffectiveTelegramChatID`, and `telegram_chat_id` do not exist yet.

- [ ] **Step 3: Implement DB storage**

Add `telegram_chat_id INTEGER`, an idempotent migration, `User.TelegramChatID`, `User.EffectiveTelegramChatID(defaultChatID int64) int64`, `UsersRepo.UpdateTelegramTopic`, and `UsersRepo.GetByChatThreadID`.

- [ ] **Step 4: Run DB tests and verify GREEN**

Run: `$env:GOCACHE = (Join-Path (Get-Location) '.gocache'); go test ./internal/backend/db -count=1`

Expected: PASS.

### Task 2: Route Alerts and Topic Creation to Effective Chat

**Files:**
- Modify: `internal/backend/alerts/topics.go`
- Modify: `internal/backend/alerts/dispatcher.go`
- Modify: `internal/backend/alerts/lifecycle_notifier.go`
- Modify: `internal/backend/alerts/deploy_notifier.go`
- Test: `internal/backend/alerts/dispatcher_test.go`
- Test: `internal/backend/alerts/lifecycle_notifier_test.go`

- [ ] **Step 1: Write failing alert tests**

Add tests that bind a router to `-200` and assert hard alerts, wake/sleep, and auto-created topics use `-200`, not the primary `-100`.

- [ ] **Step 2: Run alert tests and verify RED**

Run: `$env:GOCACHE = (Join-Path (Get-Location) '.gocache'); go test ./internal/backend/alerts -run '(Secondary|TelegramChat)' -count=1`

Expected: FAIL because send paths still use `Config.ChatID` only.

- [ ] **Step 3: Implement alert routing**

Make topic ensure return a `{ChatID, ThreadID}` reference. Persist chat id when creating or force-recreating a topic. Send hard/recovery/offline/welcome/lifecycle/deploy messages to `user.EffectiveTelegramChatID(primaryChatID)`.

- [ ] **Step 4: Run alert tests and verify GREEN**

Run: `$env:GOCACHE = (Join-Path (Get-Location) '.gocache'); go test ./internal/backend/alerts -count=1`

Expected: PASS.

### Task 3: Enforce Callback and Message Isolation

**Files:**
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/admin_topics.go`
- Test: `internal/backend/callbacks/router_test.go`

- [ ] **Step 1: Write failing callback tests**

Add tests for:

```go
func TestRouterRejectsSameThreadFromWrongChat(t *testing.T) { /* router in -200/thread 55; callback from -100/thread 55 is rejected */ }
func TestRouterAllowsSecondaryConfiguredGroupTopic(t *testing.T) { /* router in -200/thread 55; callback/message from -200 is accepted */ }
func TestRouterRejectsUnconfiguredGroup(t *testing.T) { /* chat -300 is ignored */ }
```

- [ ] **Step 2: Run callback tests and verify RED**

Run: `$env:GOCACHE = (Join-Path (Get-Location) '.gocache'); go test ./internal/backend/callbacks -run 'WrongChat|SecondaryConfigured|UnconfiguredGroup' -count=1`

Expected: FAIL because routing and ACL still key only on thread id and one chat.

- [ ] **Step 3: Implement callback isolation**

Add `Config.ExtraChatIDs []int64` and a `chatAllowed(chatID int64)` helper. Resolve topic kind with `GetByChatThreadID(chatID, threadID, primaryChatID)`. In `aclAllow`, reject router-scoped actions when callback chat differs from the user's effective chat even if the thread id matches.

- [ ] **Step 4: Run callback tests and verify GREEN**

Run: `$env:GOCACHE = (Join-Path (Get-Location) '.gocache'); go test ./internal/backend/callbacks -count=1`

Expected: PASS.

### Task 4: Config and CLI Binding

**Files:**
- Modify: `internal/backend/config.go`
- Modify: `cmd/backend/main.go`
- Modify: `cmd/wg-monitor-cli/bind_topic.go`
- Modify: `cmd/wg-monitor-cli/ensure_topics.go`
- Modify: `cmd/wg-monitor-cli/main.go`
- Modify: `cmd/deploy/templates/backend.yaml.tmpl`
- Test: `cmd/wg-monitor-cli/ensure_topics_test.go`

- [ ] **Step 1: Write failing CLI/config tests**

Add a test that `ensure-topics` and `bind-topic --chat-id -200 --thread-id 55` persist both `telegram_chat_id` and `telegram_thread_id`.

- [ ] **Step 2: Run CLI tests and verify RED**

Run: `$env:GOCACHE = (Join-Path (Get-Location) '.gocache'); go test ./cmd/wg-monitor-cli -run 'ChatID|BindTopic' -count=1`

Expected: FAIL because bind-topic does not persist chat id.

- [ ] **Step 3: Implement CLI/config support**

Add `telegram.extra_chat_ids` to backend config and pass it into callback router config. Update CLI topic creation/binding to call `UpdateTelegramTopic`. Keep existing config valid when `extra_chat_ids` is omitted.

- [ ] **Step 4: Run full verification**

Run:

```powershell
$env:GOCACHE = (Join-Path (Get-Location) '.gocache')
go test ./internal/backend/db ./internal/backend/alerts ./internal/backend/callbacks ./cmd/wg-monitor-cli ./cmd/backend
```

Expected: PASS.

### Task 5: Release

**Files:**
- Modify only files changed above.

- [ ] **Step 1: Inspect diff**

Run: `git status --short` and `git diff --check`.

- [ ] **Step 2: Commit and push**

Run:

```powershell
git add internal/backend/db internal/backend/alerts internal/backend/callbacks internal/backend/config.go cmd/backend cmd/wg-monitor-cli cmd/deploy/templates/backend.yaml.tmpl docs/superpowers/plans/2026-06-08-multi-telegram-groups.md
git commit -m "feat: isolate telegram operator groups"
git push origin main
```

- [ ] **Step 3: Cut RC and deploy**

Follow the existing `wg-monitor` release lane: tag a new `v0.13.0-rc*`, push the tag, verify GitHub Actions/assets, then deploy to all configured targets.
