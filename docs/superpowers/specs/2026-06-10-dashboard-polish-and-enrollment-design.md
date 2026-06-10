# Dashboard Polish and Enrollment Design

**Date:** 2026-06-10
**Status:** Approved direction: Option A, polish the current embedded dashboard
**Owner:** Anex + Codex

## Goal

Improve the existing built-in `wg-monitor-backend` dashboard so it becomes a practical operator console instead of a dense table of raw actions.

This slice keeps the current embedded HTML/CSS/JS architecture and adds four user-visible capabilities:

- polished, readable fleet UI with button hover, active, loading, success, and error states;
- clear Russian operator text that explains agent state, incident state, AWG Manager availability, Telegram topic binding, and deploy state;
- web enrollment for a new agent into a selected Telegram group or topic;
- one-click opening of an agent's AWG Manager web UI when an `awgm_url` is stored.

## Current State

The repo already has a dashboard foundation:

- `internal/backend/dashboard_handler.go` registers `/dashboard/`, `/dashboard/login`, `/v1/dashboard/login`, `/v1/dashboard/logout`, `/v1/dashboard/summary`, dashboard deploy routes, dashboard command routes, and command-result polling.
- Dashboard auth already uses a dedicated dashboard token and an HttpOnly session cookie named `wg_dashboard_session`.
- `dashboardSummaryAgent` already exposes `awgm_url`, `has_topic`, `deploy_mode`, `pending_version`, `last_seen_at`, `agent_version`, and active incidents.
- `internal/backend/dashboard_static/index.html`, `app.css`, and `app.js` render the first dashboard UI.
- `internal/backend/wizard_handler.go` already has `wizardEnrollmentHandler`, `wizardListAgentsHandler`, and deploy/action handlers that the dashboard can reuse or wrap.
- `internal/backend/db.UsersRepo.UpdateTelegramTopic` can bind a router to a Telegram group and topic.
- `internal/backend/config.go` stores the primary Telegram `chat_id` and `extra_chat_ids`, but `Deps` currently does not expose those values to dashboard handlers.

The current UI problems are mainly presentation and workflow gaps:

- action buttons are grouped as a flat strip and do not communicate running or completed state;
- row text is compact but not explanatory;
- AWG Manager is represented by a restart action, not by a visible "open web UI" path;
- there is no dashboard flow for creating/enrolling a new agent;
- the dashboard does not surface the actual Telegram chat or topic identifiers.

## Non-Goals

- No React, Vite, separate Node build, or separate dashboard service in this slice.
- No AWG Manager password storage, password display, browser autofill, or secret proxying.
- No destructive tunnel, route, firmware, reboot, or token-rotation actions from the polished dashboard.
- No role system or multi-workspace selector.
- No automatic Telegram forum-topic creation from the web in this slice unless the backend already has all required bot/config wiring in process. The web flow may bind a provided `thread_id`, or create an enrollment with `thread_id=0` and clearly tell the operator that topic creation is handled by the existing Telegram/CLI ensure-topic path.
- No release publication, backend deployment to production, or live fleet mutation as part of writing this spec.

## UX Direction

The dashboard should feel like a dense, calm control room:

- high contrast, restrained colors, clear status rails, no marketing hero;
- compact table/list for scanning the fleet;
- a right-side agent drawer for detail and actions;
- short Russian copy that names the current condition and the next useful action;
- buttons with icons where the icon is already available in `tabler-icons-lite.css`, with short text labels for risky or important actions;
- every command button has an immediate visual state: idle, queued, waiting, ok, error.

The first viewport remains the working dashboard. No landing page is added.

## Layout

### Header

The header shows:

- dashboard title: `Fleet Control`;
- backend version;
- dashboard connection state;
- last refresh timestamp;
- primary actions: refresh, add agent, logout.

The refresh button changes state while summary data is loading. If the refresh fails, the header shows a concise error while the last known data remains visible when available.

### KPI Row

Keep the current KPI row, but make labels and helper text operational:

- Agents: total enrolled agents;
- Online: reporting recently;
- Alerts: active hard incidents;
- Deploys: pending updates.

Helper text should be computed from available data, for example:

- `3 agents need attention`;
- `all agents clear`;
- `2 deploys are waiting for heartbeat confirmation`.

### Fleet List

The fleet list stays table-based on desktop and becomes stacked rows on narrow screens.

Each row contains:

- nickname and kind;
- status badge: online, alert, offline, never seen;
- last seen with a readable age;
- version and pending version;
- AWG interface and expected exit IP;
- Telegram group/topic indicator;
- AWG Manager indicator;
- compact action group.

Each row gets a left status rail:

- green for online and clear;
- red for active hard incident;
- amber for pending deploy or stale heartbeat;
- gray for never seen or no data.

Rows are clickable and open the agent drawer. Action buttons inside rows do not trigger row selection twice.

### Agent Drawer

The right drawer is the main cleanup for "everything in one pile". It groups details into sections:

- `Состояние`: readable status sentence, last report, active incidents;
- `Telegram`: group/chat id, topic id, and whether topic routing is bound;
- `AWG Manager`: URL, deploy mode, auth label if present, open web button, restart AWG Manager button;
- `Deploy`: current version, pending version, deploy agent button;
- `Checks`: diag, force recheck, tunnels, routes, PingCheck, direct check, via-tunnel check;
- `Last command`: current browser-session command result for this agent.

Examples of drawer copy:

- `Агент онлайн: последний отчет 42 сек назад. Активных hard-инцидентов нет.`
- `Есть hard-инцидент: dns, 4 падения подряд. Начни с Diagnostics или Force recheck.`
- `AWG Manager URL сохранен. Можно открыть веб-интерфейс в новой вкладке.`
- `AWG Manager URL не сохранен. Быстрый вход недоступен, но restart awgmgr можно выполнить через агента.`
- `Topic не привязан. Уведомления не будут попадать в отдельную тему этого роутера.`

### Buttons and States

Every actionable button follows the same state model in `app.js`:

- `idle`: normal;
- `hover`: CSS highlight;
- `active`: pressed styling;
- `queued`: command accepted by backend;
- `waiting`: result polling in progress;
- `ok`: command result received with success;
- `error`: command enqueue or result polling failed;
- `disabled`: action unavailable because required metadata is absent.

Examples:

- `Open AWG` is disabled when `awgm_url` is empty.
- `Restart AWG` remains available for enrolled agents because it uses the command queue.
- `Deploy` is disabled until a non-empty target version is entered.
- `Add agent` shows loading during enrollment creation and disables duplicate submit.

The UI stores button states only in browser memory. A page reload may lose transient command UI state, but command result polling still works when the operator has the command id in the current session.

## Add Agent Flow

Add a primary `Add agent` button in the header. It opens a modal wizard with three sections on one compact form.

### Inputs

- `Nickname`: required, same validation intent as existing backend nickname validation.
- `Kind`: segmented control, `static` or `mobile`.
- `Telegram group`: select from backend-known groups:
  - primary `telegram.chat_id`;
  - configured `telegram.extra_chat_ids`;
  - custom numeric chat id field for emergency use.
- `Topic mode`:
  - `No topic yet`: submit `thread_id=0`;
  - `Existing topic`: operator enters numeric `thread_id`.
- `Expected exit IP` and `AWG interface`: not required for enrollment in this slice because `wizardEnrollmentHandler` currently creates default values. These fields can be added later when the backend enrollment contract supports them cleanly.

### Submit Behavior

The dashboard calls a new endpoint:

```text
POST /v1/dashboard/enrollments
```

Request:

```json
{
  "nickname": "bronya",
  "kind": "mobile",
  "telegram_chat_id": -1003651873378,
  "telegram_thread_id": 0
}
```

Response:

```json
{
  "nickname": "bronya",
  "backend_url": "https://wgmonitor.example.test",
  "raw_token": "secret-token",
  "telegram_chat_id": -1003651873378,
  "telegram_thread_id": 0,
  "message": "Agent enrollment created. Save the token now; it will not be shown again."
}
```

Backend behavior:

1. Validate dashboard auth and JSON content type.
2. Validate nickname and kind through the same rules used by wizard enrollment.
3. Validate `telegram_chat_id`:
   - `0` means primary configured chat;
   - primary chat id is allowed;
   - any id listed in `telegram.extra_chat_ids` is allowed;
   - a custom numeric id is accepted only when the request marks it as custom, so the UI does not accidentally submit an invalid default.
4. Call the same enrollment logic as `wizardEnrollmentHandler` to create or rotate the agent token.
5. Bind Telegram routing with `UsersRepo.UpdateTelegramTopic(id, telegram_chat_id, telegram_thread_id)`.
6. Return the raw token once, plus backend URL and binding metadata.

The response page must clearly tell the operator that the raw token is shown once and must be copied into the target agent config or deploy flow.

## Telegram Group Data

Dashboard summary should expose enough Telegram metadata to render and search groups:

```json
{
  "telegram": {
    "primary_chat_id": -1003651873378,
    "extra_chat_ids": [-1001111111111]
  },
  "agents": [
    {
      "nickname": "bronya",
      "telegram_chat_id": -1003651873378,
      "telegram_thread_id": 123,
      "has_topic": true
    }
  ]
}
```

Implementation shape:

- add `TelegramPrimaryChatID int64` and `TelegramExtraChatIDs []int64` to `backend.Deps`, or equivalent narrowly named fields;
- wire those fields from `cmd/backend/main.go` using loaded config;
- include `telegram_chat_id` and `telegram_thread_id` in `dashboardSummaryAgent`;
- keep `has_topic` for compatibility with the existing frontend.

If no group labels exist, the UI labels groups by id:

- `Primary group (-1003651873378)`;
- `Extra group (-1001111111111)`;
- `Custom group`.

Human-friendly group names can be a later slice.

## AWG Manager Web Entry

The dashboard never stores or reveals AWG Manager credentials.

When `agent.awgm_url` is non-empty:

- show `Open AWG Manager`;
- open the URL in a new tab with `target="_blank"` and `rel="noreferrer noopener"`;
- show a short note: `Откроется веб-интерфейс AWG Manager. Логин остается на стороне AWG Manager.`

When `agent.awgm_url` is empty:

- show disabled `Open AWG`;
- show: `AWG Manager URL не сохранен. Добавь его через deploy sync или awgm-url-patch.`

"Quick login" in this slice means quick navigation to the saved AWG Manager web UI. It does not mean automatic credential submission.

## API Changes

### Summary

Extend existing `GET /v1/dashboard/summary`.

New top-level field:

```json
"telegram": {
  "primary_chat_id": -1003651873378,
  "extra_chat_ids": [-1001111111111]
}
```

New agent fields:

```json
"telegram_chat_id": -1003651873378,
"telegram_thread_id": 123
```

Existing fields stay stable:

- `awgm_url`;
- `has_topic`;
- `active_incidents`;
- `pending_version`;
- `deploy_mode`.

### Enrollment

New dashboard-authenticated route:

```text
POST /v1/dashboard/enrollments
```

The route should reuse a shared helper extracted from wizard enrollment rather than duplicating token generation and `UpsertEnrollment` logic.

### Optional Topic Binding Update

If implementation needs a separate binding endpoint, it may add:

```text
PUT /v1/dashboard/agents/{nickname}/telegram-topic
```

Request:

```json
{
  "telegram_chat_id": -1003651873378,
  "telegram_thread_id": 123
}
```

This endpoint is optional for the first implementation because the add-agent flow can bind immediately after enrollment.

## Safety Model

- Dashboard auth remains separate from wizard auth and agent auth.
- Enrollment returns a raw token only once, exactly like the wizard path.
- No token is written to browser storage by the app.
- The raw token result panel includes a clear warning to save it now.
- AWG Manager credentials are not returned, edited, or proxied.
- Custom Telegram chat ids require explicit operator input.
- Command buttons remain limited to the already allowlisted safe dashboard/wizard actions.
- Dangerous commands remain out of scope.
- Structured JSON errors are used for all new endpoints.

## Error Handling

Enrollment errors:

- `401 unauthorized`: missing or invalid dashboard session;
- `400 bad_json`: invalid JSON or missing required fields;
- `400 invalid_nickname`: nickname does not pass backend validation;
- `400 invalid_kind`: kind is not `static` or `mobile`;
- `400 invalid_telegram_chat`: selected group is not allowed or custom chat id is malformed;
- `500 internal`: token generation or DB write failed.

UI behavior:

- Keep modal open on validation errors.
- Put the error next to the field when possible.
- Show a toast for backend/network errors.
- Disable submit while a request is in flight.
- After success, refresh summary and show the enrollment result panel.

## Testing Strategy

Backend tests:

- summary includes top-level Telegram group data when `Deps` is configured;
- summary includes each agent's `telegram_chat_id` and `telegram_thread_id`;
- dashboard enrollment requires auth;
- dashboard enrollment rejects invalid nickname, invalid kind, and invalid Telegram group;
- dashboard enrollment creates an agent and returns `raw_token` once;
- dashboard enrollment binds `telegram_chat_id` and `telegram_thread_id`;
- dashboard enrollment can use primary chat with `telegram_chat_id=0`;
- existing wizard enrollment tests still pass.

Frontend/static tests:

- static app contains `Add agent`;
- static app contains `Open AWG Manager`;
- no static asset references external CDN URLs;
- the JS renders disabled AWG open state for missing `awgm_url`;
- the JS renders enabled AWG open link for present `awgm_url`;
- button state helpers render queued/waiting/ok/error classes without layout shift.

Manual visual verification:

- open `/dashboard/login` and verify the login copy is readable Russian, not mojibake;
- login and open `/dashboard/`;
- verify desktop layout at about 1365x768;
- verify mobile/narrow layout around 390x844;
- verify no button text overflows;
- verify the drawer, add-agent modal, command result panel, and toasts are legible.

Verification commands:

```text
go test ./internal/backend -count=1
go test ./cmd/backend ./internal/backend ./internal/backend/db -count=1
go test ./... -count=1
git diff --check
```

Browser verification is required before claiming the UI implementation complete. Passing Go tests alone is not enough for this slice.

## Rollout Notes

The implementation should land behind the existing dashboard enablement path. No extra production feature flag is required because dashboard routes already exist only when `dashboard.enabled` and `dashboard.token_file` are configured.

After implementation and tests:

1. build the backend with embedded assets;
2. deploy through the existing backend update path;
3. verify `/healthz`;
4. login to `/dashboard/`;
5. confirm summary renders live fleet data;
6. open at least one stored AWG Manager URL;
7. create a test enrollment only when the operator explicitly wants to mutate the live fleet.

## Open Decisions for Implementation Plan

These are implementation choices, not design blockers:

- whether group selection uses a native `<select>` or segmented list plus custom input;
- whether the agent drawer replaces the current command-result drawer or sits above it;
- whether frontend JS is kept in one file for this slice or split into small modules under the embedded static folder.
