# Admin Panel Hub + Auto-buttons in new topics

**Date:** 2026-05-11
**Target version:** v0.12.0 (or v0.11.0-rc24 if landing pre-v0.12)
**Status:** Approved (brainstorming)

## Goal

Operator wants two things:

1. **Admin hub** — a single Telegram-side entry point (`/panel` slash command) that lets the admin push the Maintenance / Routes / Status panels into any router's per_router topic remotely, without needing to navigate into that topic and tap the reply-keyboard.
2. **Auto-buttons in fresh topics** — when a new per_router topic is created, the reply-keyboard buttons (`📊 Что происходит?`, `🛠 Обслуживание`, etc.) should appear immediately, not after the first alert. Today reply-keyboards only attach to bot-originated messages, so a brand-new topic has no buttons until something happens.

Both share the same insight: the bot already knows how to render every panel — what's missing is a way to **trigger** that rendering from outside the per_router topic.

## Non-goals

- Per-router admin permissions (already covered by the existing `AdminUserID` gate).
- Persistent hub message that stays pinned across restarts — `/panel` posts a fresh hub each time.
- Slash shortcuts `/maint`, `/routes`, `/status` — operator preferred a single `/panel` entry point with kb navigation.
- Welcome message customization per router — one template, one icon, identical text everywhere.
- Multi-language support.
- Pagination of router list in hub kind-pick screen — punted until 8+ routers materialise.

## User flow

### Trigger: `/panel`

Operator (admin only) types `/panel` in any topic. The bot deletes the command message (per `DeleteUserCommandMessages`) and posts the Home screen as a fresh message.

### Screen 1 — Home

```
🎛 Панель управления

Что открыть?

[🛠 Maintenance]  [📦 Routes]
[📊 Status]       [🪄 Оживить топики]
[✖ Закрыть]
```

### Screen 2 — Router pick (after tapping a panel-kind button)

```
🎛 Maintenance → выбери роутер:

[testkeen]
[router-foo]
[⚠ router-bar (нет топика)]
[« Назад]  [✖ Закрыть]
```

- One router per row.
- Users without `TelegramThreadID` rendered with `⚠` prefix and a dead callback (`panel:0:no_topic:<userID>`) — tap toasts "У роутера нет топика — сделай /ensure_topics".
- **Skip rule:** if exactly one user exists AND that user has a `TelegramThreadID`, the bot skips Screen 2 entirely and goes straight to Screen 3.
- Empty-user case: hub edits to "Роутеров нет. Сначала добавь — wizard или CLI `add-user`." with `« Назад`.

### Screen 3 — Result (after publication)

The chosen panel is sent as a **new message in the target per_router topic** using the existing builders (`openMaintPanelMessage` / `openRoutesPanelMessage` / `dispatchSmartReply`). The hub message itself edits to:

```
🎛 Панель управления

✅ Maintenance отправлен в топик @testkeen.

[« К панели]  [✖ Закрыть]
```

On failure (e.g. TG 400 "thread not found"):

```
🎛 Панель управления

❌ Топик роутера @testkeen похоже удалён. Сделай /recreate_topic внутри его топика или /ensure_topics.

[« К панели]  [✖ Закрыть]
```

No auto-self-heal — hub is an admin surface, escalate explicitly.

### Screen 4 — Awaken confirm

After tapping `🪄 Оживить топики`:

```
🎛 Панель управления

🪄 Оживить топики (отправить приветствие с кнопками во все per_router топики)

Будут затронуты: 3 топика

[✓ Подтвердить]  [« Назад]
```

Counts only users with `TelegramThreadID != nil`. Confirm publishes a welcome message into each such topic (with a 200ms sleep between sends). Hub edits to:

```
🎛 Панель управления

✅ Оживлено: 3 топика, 0 ошибок.

[« К панели]  [✖ Закрыть]
```

Errors per-user surface as additional lines (truncated at 4096 chars).

### Welcome message (auto on topic create)

```
👋 Топик роутера {nickname} готов.

Кнопки внизу — то, что я умею. Тапни 📊 чтобы посмотреть статус прямо сейчас.
```

Sent with `ReplyKeyboardForTopic("per_router")` (or compat-inline-kb if `ui.compat_inline_keyboard: true`), which attaches the persistent button set to the topic.

## Architecture

### New file

`internal/backend/callbacks/panel_hub.go` (~250 lines):

- `adminPanelOpen(ctx, m)` — posts Screen 1.
- `handlePanelCallback(ctx, q, args)` — dispatches on `args.PanelScreen` (parsed from `panel:0:<screen>:<rest>`):
  - `home` → render Screen 1 (edit current).
  - `kind:<maint|routes|status>` → render Screen 2 (or skip to publish if 1-user shortcut).
  - `push:<userID>:<kind>` → publish via synthetic Message + render Screen 3.
  - `no_topic:<userID>` → toast only.
  - `awaken_confirm` → Screen 4.
  - `awaken_do` → loop SendWelcome → Screen with results.
  - `close` → edit to empty inline-kb.
- Pure builder helpers: `panelHomeText/Kb`, `panelKindPickText/Kb`, `panelResultText/Kb`, `panelAwakenConfirmText/Kb`, `panelAwakenResultText/Kb`.

### Edits to existing files

| File | Change |
|---|---|
| `internal/backend/callbacks/admin_topics.go` | Add `case "/panel": r.adminPanelOpen(ctx, m); return true` in `handleAdminCommand`. Extend `/topic_help` with a line about `/panel`. |
| `internal/backend/callbacks/router.go` | Add `case "panel": r.handlePanelCallback(ctx, q, args); return` in `HandleCallback`. |
| `internal/backend/callbacks/parse.go` | Add `"panel"` to `validActions`. Extend `Args` with a `PanelScreen string` field + parsing of the screen and screen-specific subargs from the callback `data` tail. |
| `internal/backend/tg/client.go` | Add `SetMyCommands(ctx, []BotCommand) error` (thin wrapper over TG `setMyCommands`). `BotCommand` struct with `Command`, `Description` fields. |
| `cmd/backend/main.go` | On startup, call `tgClient.SetMyCommands(ctx, []tg.BotCommand{{Command: "panel", Description: "Открыть панель управления"}, {Command: "ensure_topics", Description: "Создать темы для всех роутеров"}, {Command: "recreate_topic", Description: "Пересоздать тему текущего роутера"}, {Command: "this_is", Description: "Привязать этот топик к роутеру"}, {Command: "topic_help", Description: "Шпаргалка по управлению темами"}})`. Non-fatal on failure (warn-log). |
| `internal/backend/alerts/topics.go` | Add `SendWelcome(ctx, tg WelcomeSender, chatID, threadID int64, nickname string, kb any) error`. `WelcomeSender` interface = single `SendMessageWithReplyKeyboard` method. |
| `internal/backend/alerts/dispatcher.go` | After `EnsureTopicForUser` in `ensureTopic` returns a fresh id (i.e. when the previous `TelegramThreadID` was nil — detect by comparing to pre-call state), call `SendWelcome`. Errors log + continue (non-blocking). |
| `cmd/wg-monitor-cli/main.go` (ensure-topics action) | Same: after `EnsureTopicForUser` succeeds and was a fresh create, `SendWelcome`. |
| `internal/backend/callbacks/admin_topics.go` (`adminEnsureTopics`, `adminRecreateTopic`) | Same: after `EnsureTopicForUser` fresh-create, `SendWelcome`. For `recreate_topic` always send (new thread_id = new welcome). |

### Callback layout

Format: `panel:<userID>:<screen>[:<subargs>]`. Aligns with the existing `action:userID:...args` convention used by every other callback in `parse.go`.

```
panel:0:home
panel:0:kind:<maint|routes|status>
panel:<userID>:push:<maint|routes|status>
panel:<userID>:no_topic
panel:0:awaken_confirm
panel:0:awaken_do
panel:0:close
```

`userID` is 0 for hub-global screens, real router-user-id for `push` and `no_topic` (which target a specific router). `aclAllow` admin-override gates the entire flow — non-admin taps never reach `handlePanelCallback`. The `args.UserID==0` allow branch in `aclAllow` covers the hub-global screens.

### Args extension

`Args` (in `parse.go`) gains:

- `PanelScreen string` — `"home" | "kind" | "push" | "no_topic" | "awaken_confirm" | "awaken_do" | "close"`.
- `PanelKind string` — `"maint" | "routes" | "status"` (populated for `kind` and `push` screens).

Parsing rule: when `args.Action == "panel"`, the third token (after `panel:userID`) is the screen name; the fourth token (if present) is the kind. Validate against fixed allowlists; reject unknown screens/kinds.

### Synthetic Message pattern

To publish a panel into a non-current thread, build a fake `tg.Message` and pass it through the existing builder:

```go
synth := &tg.Message{
    MessageID:       0,                          // sentinel: do not delete (router.go:521)
    Chat:            tg.Chat{ID: cfg.ChatID},
    From:            tg.User{ID: cfg.AdminUserID},
    MessageThreadID: user.TelegramThreadID,      // target thread
}
switch kind {
case "maint":  r.openMaintPanelMessage(ctx, synth, user)
case "routes": r.openRoutesPanelMessage(ctx, synth, user)
case "status": r.dispatchSmartReply(ctx, synth, user)
}
```

Pattern mirrors `compat_btn` in `callbacks/compat_inline.go`. `MessageID==0` is already handled by `HandleMessage` (skip deleteMessage). Synthetic dispatch is single-threaded, no race with Dispatcher's mutex.

### Welcome integration boundary

`SendWelcome` is **not** called inside `EnsureTopicForUser`. Reasons:

- `TopicCreator` interface in `alerts/topics.go` is intentionally minimal (only `CreateForumTopic`). Widening it to `SendMessageWithReplyKeyboard` for one welcome line is API bloat.
- `EnsureTopicForUser` with `force=false` is meant to be a no-op when the topic already exists. Welcome-on-noop would double-send.
- Callers already hold the full `*tg.Client` and the keyboard config, so calling `SendWelcome` post-create is one line in 4 places.

Detection of "fresh create vs no-op" in callers: capture `prevThreadID = u.TelegramThreadID` before, compare to returned `tid` after. Equal → no-op, skip welcome. Not equal (or `prev == nil`) → fresh, send welcome. For `recreate_topic` (force=true) always send (new thread always).

## Edge cases and error handling

| Case | Behaviour |
|---|---|
| `/panel` typed by non-admin | `HandleMessage` already rejects via `AdminUserID` gate. |
| `/panel` in General (chat without topics) | Works; hub renders in General. |
| User without `TelegramThreadID` on Screen 2 | Disabled-style button (⚠ prefix), tap → toast. |
| Single user with thread | Skip Screen 2, publish + go to Screen 3 immediately. |
| Single user without thread | Show Screen 2 anyway with disabled button (so admin sees the cause). |
| Zero users | Hub message: "Роутеров нет." |
| Publish into stale thread (TG 400 "thread not found") | Error surfaces in Screen 3 with explicit "сделай /recreate_topic" guidance. No auto-self-heal. |
| `awaken_do` partial failures | Per-user error lines in result message, truncate at 4096. 200ms sleep between sends. |
| `close` tapped twice | TG returns 400 "message is not modified" — log Warn, swallow. |
| Backend restart mid-hub | Hub is stateless; old hub message stays valid, callbacks continue working. |
| Hub message deleted mid-flow | Subsequent callback edit fails with TG 400 — log Warn, no state to clean. |

## Observability

- `slog.Info("panel callback", "screen", screen, "admin", q.From.ID, "user_id", args.UserID)` on every panel callback.
- `slog.Warn("welcome send failed", "user", nickname, "err", err)` for SendWelcome failures (non-fatal everywhere).
- `slog.Info("panel awaken", "sent", k, "failed", m, "elapsed_ms", dur)` for awaken loop.
- No new expvar counters — log volume is low enough.

## Testing

| File | Tests |
|---|---|
| `callbacks/panel_hub_test.go` (new) | `TestPanelHome_RendersFourButtons` (golden text+kb). `TestPanelKindPick_FiltersUsersWithoutThread`. `TestPanelKindPick_SingleUserSkipsToPublish`. `TestPanelPush_SyntheticMessageRoutesToCorrectThread` (mock tg.Client asserts SendMessage(threadID=42) when user.TelegramThreadID=42). `TestPanelAwaken_SkipsUsersWithoutThread` (3 users, 1 without thread → 2 welcome sends). `TestPanelClose_EditsToEmptyKb`. `TestPanelPush_StaleTopicErrorEscalates` (mock returns thread-not-found → error message in Screen 3). |
| `callbacks/admin_topics_test.go` | Add `TestPanel_AdminOnly` mirroring existing `/this_is` admin-gate test. |
| `callbacks/parse_test.go` | Add `panel:*` parse cases. |
| `alerts/topics_test.go` | `TestSendWelcome_IncludesNickname`. `TestSendWelcome_AttachesReplyKeyboard`. |

No DB integration tests needed — hub is purely TG-side. Existing `EnsureTopicForUser` tests already cover the alerts/topics path; welcome is one line in callers and is exercised by `panel_hub_test.go` for the awaken path.

## Migration / rollout

- No DB migrations.
- No `backend.yaml` schema changes.
- Backward compat: existing topics without welcome history will get one on next `/ensure_topics` invocation that creates a NEW topic; OR via the `🪄 Оживить топики` hub action which is the explicit "backfill" path.
- Roll-forward only — old binaries don't see `/panel`, but no callbacks they emit are touched.

## Scope estimate

- New code: `panel_hub.go` ~250 lines, tests ~300 lines.
- Edits: `admin_topics.go` +20, `router.go` +5, `parse.go` +20 (Args extension + parser), `tg/client.go` +30 (SetMyCommands), `cmd/backend/main.go` +10, `alerts/topics.go` +25 (SendWelcome), four caller integrations +5 each.
- Total: ~700 lines code + tests, ~120 lines diff in existing files.

## Open items

None — all design decisions resolved in brainstorming.
