# Dashboard Polish and Enrollment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Polish the existing embedded dashboard, add agent enrollment into Telegram groups/topics, and expose quick AWG Manager web entry points.

**Architecture:** Keep the current Go backend plus embedded static HTML/CSS/JS. Extend dashboard summary and add one dashboard-authenticated enrollment endpoint that reuses wizard enrollment primitives and existing DB topic binding.

**Tech Stack:** Go `net/http`, SQLite repositories under `internal/backend/db`, embedded static assets in `internal/backend/dashboard_static`, vanilla JavaScript, CSS, existing Tabler-lite icon/css assets.

---

### Task 1: Backend Summary Metadata

**Files:**
- Modify: `internal/backend/handler.go`
- Modify: `internal/backend/dashboard_handler.go`
- Modify: `internal/backend/dashboard_handler_test.go`
- Modify: `cmd/backend/main.go`

- [x] **Step 1: Add failing summary test**

Add a test in `internal/backend/dashboard_handler_test.go` that creates a user, binds it to `telegram_chat_id=-100200` and `telegram_thread_id=123`, calls `GET /v1/dashboard/summary`, and asserts:

```go
var got struct {
    Telegram struct {
        PrimaryChatID int64   `json:"primary_chat_id"`
        ExtraChatIDs  []int64 `json:"extra_chat_ids"`
    } `json:"telegram"`
    Agents []struct {
        Nickname         string `json:"nickname"`
        TelegramChatID  int64  `json:"telegram_chat_id"`
        TelegramThreadID int64 `json:"telegram_thread_id"`
    } `json:"agents"`
}
```

Expected values: `primary_chat_id=-100100`, `extra_chat_ids=[-100200]`, agent chat id `-100200`, thread id `123`.

- [x] **Step 2: Run focused test to verify it fails**

Run: `go test ./internal/backend -run TestDashboardSummaryIncludesTelegramMetadata -count=1`

Expected: FAIL because the summary lacks `telegram` and agent Telegram fields.

- [x] **Step 3: Implement summary metadata**

Add to `Deps`:

```go
TelegramPrimaryChatID int64
TelegramExtraChatIDs  []int64
```

Add summary shape:

```go
type dashboardTelegramSummary struct {
    PrimaryChatID int64   `json:"primary_chat_id"`
    ExtraChatIDs  []int64 `json:"extra_chat_ids"`
}
```

Add `Telegram dashboardTelegramSummary` to `dashboardSummary`.

Add `TelegramChatID int64` and `TelegramThreadID int64` to `dashboardSummaryAgent`, populated from `db.User`.

Wire `cfg.Telegram.ChatID` and `cfg.Telegram.ExtraChatIDs` into `backend.Deps` in `cmd/backend/main.go`.

- [x] **Step 4: Run focused test to verify it passes**

Run: `go test ./internal/backend -run TestDashboardSummaryIncludesTelegramMetadata -count=1`

Expected: PASS.

### Task 2: Dashboard Enrollment API

**Files:**
- Modify: `internal/backend/wizard_handler.go`
- Modify: `internal/backend/dashboard_handler.go`
- Modify: `internal/backend/dashboard_handler_test.go`

- [x] **Step 1: Add failing enrollment tests**

Add tests for:

- `POST /v1/dashboard/enrollments` requires dashboard auth;
- invalid kind returns `400`;
- unknown non-custom chat id returns `400`;
- valid primary chat with `telegram_chat_id=0` creates the agent and returns `raw_token`;
- valid extra chat with `telegram_thread_id=222` creates the agent and binds chat/thread.

Use `NewMux(Deps{DB: d, DashboardToken: "secret", TelegramPrimaryChatID: -100100, TelegramExtraChatIDs: []int64{-100200}})`.

- [x] **Step 2: Run focused tests to verify they fail**

Run: `go test ./internal/backend -run "TestDashboardEnrollment" -count=1`

Expected: FAIL because the route does not exist.

- [x] **Step 3: Extract shared enrollment helper**

In `wizard_handler.go`, extract helper:

```go
func createAgentEnrollment(database *db.DB, nickname, kind string, threadID int64) (wizardEnrollmentResp, int64, error)
```

The helper trims nickname/kind, defaults empty kind to `db.KindStatic`, validates kind, generates the token with `newAgentEnrollmentToken`, calls `database.Users().UpsertEnrollment`, and returns response plus user id.

- [x] **Step 4: Add dashboard enrollment handler**

Add route in `registerDashboardRoutes`:

```go
mux.Handle("POST /v1/dashboard/enrollments", requestIDMiddleware()(dashAuth(dashboardEnrollmentHandler(d))))
```

Add request/response types in `dashboard_handler.go`:

```go
type dashboardEnrollmentReq struct {
    Nickname         string `json:"nickname"`
    Kind             string `json:"kind"`
    TelegramChatID   int64  `json:"telegram_chat_id"`
    TelegramThreadID int64  `json:"telegram_thread_id"`
    CustomTelegramChat bool `json:"custom_telegram_chat"`
}
```

Validate selected chat: `0`, primary, extra, or explicit custom numeric. Bind with `UpdateTelegramTopic(userID, chatID, threadID)`.

- [x] **Step 5: Run focused tests to verify they pass**

Run: `go test ./internal/backend -run "TestDashboardEnrollment" -count=1`

Expected: PASS.

### Task 3: Dashboard Static UI Polish

**Files:**
- Modify: `internal/backend/dashboard_static/index.html`
- Modify: `internal/backend/dashboard_static/app.css`
- Modify: `internal/backend/dashboard_static/app.js`
- Modify: `internal/backend/dashboard_handler_test.go`

- [x] **Step 1: Add static smoke tests**

Extend `TestDashboardStaticRequiresSessionAndServesEmbeddedApp` or add a new test that serves `/dashboard/`, `/dashboard/app.js`, and `/dashboard/app.css`, then asserts the assets contain:

```text
Add agent
Open AWG Manager
agent-drawer
data-state
```

Expected: FAIL before UI changes.

- [x] **Step 2: Run static smoke test to verify it fails**

Run: `go test ./internal/backend -run TestDashboardStatic -count=1`

Expected: FAIL because the current static app lacks the new UI markers.

- [x] **Step 3: Update HTML structure**

Add:

- header `Add agent` button;
- fleet table columns for Telegram and AWG;
- right-side `agentDrawer`;
- `addAgentModal`;
- enrollment result panel;
- keep existing deploy modal and result drawer.

- [x] **Step 4: Update CSS**

Add stable dimensions and states:

- status rails on rows;
- `.action-btn[data-state="queued|waiting|ok|error"]`;
- responsive stacked rows below 760px;
- drawer and modal layouts with max widths;
- disabled AWG link styling.

- [x] **Step 5: Update JS**

Add:

- selected agent state and drawer rendering;
- `openAWG` link rendering from `agent.awgm_url`;
- per-button state map keyed by `nickname:action`;
- add-agent modal submit to `/v1/dashboard/enrollments`;
- Telegram group options from `summary.telegram`;
- readable Russian status/help text.

- [x] **Step 6: Run static smoke test to verify it passes**

Run: `go test ./internal/backend -run TestDashboardStatic -count=1`

Expected: PASS.

### Task 4: Final Verification

**Files:**
- Verify all modified files.

- [x] **Step 1: Run backend focused tests**

Run: `go test ./internal/backend -count=1`

Expected: PASS.

- [x] **Step 2: Run broader package tests**

Run: `go test ./cmd/backend ./internal/backend ./internal/backend/db -count=1`

Expected: PASS.

- [x] **Step 3: Run full test suite**

Run: `go test ./... -count=1`

Expected: PASS or record exact unrelated failure if environment prevents full suite.

- [x] **Step 4: Check diff hygiene**

Run: `git diff --check`

Expected: no output.

- [x] **Step 5: Visual verification**

Start or use a local backend/dashboard test target, open `/dashboard/login` and `/dashboard/`, and verify desktop and narrow layouts show readable text, no overlap, no broken buttons, and an enabled `Open AWG Manager` when `awgm_url` exists.
