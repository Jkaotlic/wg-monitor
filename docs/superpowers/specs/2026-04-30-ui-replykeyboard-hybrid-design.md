# Design: ReplyKeyboard hybrid UX + diag_now reply fix

**Date:** 2026-04-30
**Status:** draft awaiting user review
**Branch target:** `feature/stage-2`
**Estimated tag:** `v0.6.0-ui-rework`

## 1. Problem statement

After live use of `v0.5.0-awgmgr-pivot`, the operator (project owner) reports
three concrete frustrations:

1. **Pinned control panel scrolls out of view.** The persistent
   inline keyboard placed in each user's topic via `wg-monitor-cli init-menu`
   sits at message #42 (or wherever it was pinned). New alerts push it up;
   to use a button you have to scroll. Operator's verdict: "тупость".

2. **`diag_now` and `opkg_upgrade` button taps return no visible result.**
   Both buttons trigger correctly (the agent fetches `/api/diagnostics/result`
   from awg-manager and returns the body in a `wire.CommandResult`), but the
   operator never sees that body in Telegram. Result: tap, silence, lose
   confidence in the bot.

3. **Alerts and buttons are formatted for an engineer, not a human.** Field
   names like `awg_version`, `mtu`, `handshake_age_sec`, button labels like
   `📊 Diag` / `▶ Pingcheck`, raw error strings — fine for the project owner
   himself, but unusable when this gets rolled out to ~10 non-technical end
   users (the original onboarding goal of Stage 5).

## 2. Goals

- **G1.** Buttons must be reachable in **one tap**, no scrolling, in the
  operator's own topic — without depending on a pinned message.
- **G2.** A non-technical user must be able to ask "what's happening?" and
  get a human-readable answer plus exactly the right actions for the current
  state, with no need to know what `pingcheck` or `restart_tunnel` mean.
- **G3.** When an action button is pressed, the user must see textual
  feedback within ≤5 seconds (success, failure, or "in progress").
- **G4.** No regression on existing HARD-alert ack/silence/mute workflow.

## 3. Non-goals

- Not changing the agent ⇄ backend wire protocol.
- Not changing the FSM (3-fail HARD, 2-OK RECOVERY, realert poller).
- Not adding a web UI or public status page (operator constraint).
- Not changing how alerts are *triggered* — only how they're *presented*.
- Not multilingual yet (Russian only, like everything else in this project).

## 4. Design overview

The new UX combines a tiny **ReplyKeyboard** (always visible at the bottom
of the chat) with **smart context-aware inline replies** sent by the bot
when those reply-keyboard buttons are pressed.

```
┌── Telegram supergroup, topic 👤 testkeen ────────────────────────┐
│ Bot: ✅ testkeen — всё работает.                                 │
│      Туннель amnezia: handshake 47 сек назад.                    │
│      DNS отвечает.                                               │
│      [📋 Подробнее]                                              │
│                                                                  │
│ ─────────────────────────────────────────────────────────────── │
│ [Type a message...]                                              │
│                                                                  │
│ [📊 Что происходит?]    [🆘 Помощь]                              │ ← ReplyKeyboard
└──────────────────────────────────────────────────────────────────┘
```

**Three components:**

1. **ReplyKeyboard** — two buttons, always at the bottom: `[📊 Что
   происходит?]` and `[🆘 Помощь]`. `is_persistent: true`. Set globally
   for the supergroup; the bot resends it with each of its own messages
   to keep clients refreshed (mitigation for the desktop-client
   ReplyKeyboard bug).

2. **Smart inline reply** — when the operator presses a ReplyKeyboard
   button, the bot determines the topic (`message_thread_id`) and
   responds with a tailored message. Content depends on state:
   `OK` / `DEGRADED` / `HARD` / `OFFLINE`. Inline action buttons appear
   only when relevant (no "Restart tunnel" button when nothing is broken).

3. **HARD-alert inline keyboard** — unchanged in mechanism, only labels
   are humanised. Stays attached to each red alert because
   silence/ack/mute are intrinsically per-incident and cannot be lifted
   to ReplyKeyboard without losing context.

The pinned control panel and `wg-monitor-cli init-menu` command are
**removed**. Existing pinned messages stay (the bot can't unpin its own
old messages without admin user action), but the operator can manually
unpin them after migration. They'll just be inert.

## 5. UI components

### 5.1 ReplyKeyboard layout

In **per-router topics** (`👤 testkeen`, `👤 mobile1`, etc):

```
[📊 Что происходит?]
[🆘 Помощь]
```

Two rows, one button each. `resize_keyboard: true` so it's visually
compact. `is_persistent: true` so clients prefer showing it over the
plain text keyboard.

In **operations topics** (`📊 Сводка`, `🔧 Системное`):

```
[📋 Список юзеров]    [📊 Здоровье флота]
```

Operator-wide commands. Same persistence settings.

In **message_thread_id == 0** (general / non-topic posts, shouldn't
happen but handle it): no ReplyKeyboard at all. Bot responds
"эта команда работает только в топике пользователя или в Сводке."

### 5.2 Smart inline reply by state

When `📊 Что происходит?` is pressed in a per-router topic, the bot
looks at the latest report + active incidents for that user and
chooses one of four templates:

**OK** — no active HARD incidents, last report < 90 sec old:
```
✅ testkeen — всё работает.

Туннель amnezia: handshake 47 сек назад, ping ok (12 ms).
DNS отвечает на запросы.
Роутер последний раз отчитывался: 23 сек назад.

[📋 Подробнее]
```
The `[📋 Подробнее]` inline button (single, optional tap) shows raw
diagnostic fields for power users / debugging.

**DEGRADED** — at least one check warning but no HARD yet:
```
⚠️ testkeen — есть подозрения.

Туннель amnezia: handshake 142 сек назад (норма до 180).
Ping: 3 неудачи подряд из 5.
Роутер пока не считает это сбоем, но подозрительно.

Действия:
[🔁 Перезапустить туннель]   [▶ Проверить связь]   [📋 Подробнее]
```

**HARD** — at least one active HARD incident (i.e. row in `incident_state`
without recovery):
```
🔴 testkeen — есть проблема.

Туннель amnezia не отвечает уже 4 минуты.
Ping проваливается 5 раз подряд.
Auto-restart awg-manager сделал 2 раза, не помогло.

Что можно сделать:
[🔁 Перезапустить туннель]   [📊 Запустить диагностику]
[⏸ Замолчать на час]   [📋 Подробнее]
```

**OFFLINE** — no agent_heartbeat in > 5 min:
```
📵 testkeen — роутер не на связи.

Последний отчёт: 14 минут назад.
Возможные причины: роутер выключен, нет интернета, агент упал.

Действия ограничены пока агент не появится:
[📋 Последний отчёт]
```

When `🆘 Помощь` is pressed, the bot replies with a static help message
explaining what the icons mean, what each action does, and links to
the operator guide. No inline buttons.

### 5.3 HARD-alert inline keyboard

Today (verbatim from `tg/keyboard.go:38-74`):
```
[⏸ 1ч] [⏸ 4ч] [⏸ 24ч] [✅ Ack]
[📋 История 24ч] [🔇 Mute до утра]
[🔁 Restart] [📊 Diag] [▶ Pingcheck]   (only on tunnel_*)
```

Proposed humanised labels (mechanism unchanged, callback_data preserved):
```
[⏸ Тише на 1ч] [⏸ Тише на 4ч] [⏸ Тише на 24ч] [✅ Понял]
[📋 История за 24ч] [🔇 Тихо до утра]
[🔁 Перезапуск туннеля] [📊 Диагностика] [▶ Тест связи]
```

Mobile-only row stays:
```
[🔄 Дай отчёт сейчас]
```

### 5.4 Pinned control panel — removal

- `tg/control_panel.go` package stays in the repo for one release as
  reference (commented "DEPRECATED, removed in v0.6.0"), but is no longer
  called from anywhere.
- `cmd/wg-monitor-cli/init_menu.go` becomes a no-op that prints
  "command removed in v0.6.0; ReplyKeyboard is set automatically".
- Existing pinned messages remain in topics. Operator manually unpins.
  No automated cleanup — bot lacks the right to unpin arbitrary chat
  members' pinned messages without explicit admin action.

## 6. Behaviour & flows

### 6.1 ReplyKeyboard installation

**Initial install (per-supergroup, one-time).** Bot installs ReplyKeyboard
when:
- It posts the first HARD alert in a new per-router topic (existing flow:
  topic auto-created via `createForumTopic` on first alert).
- The operator types `/start` or `/menu` in any topic — bot responds with
  the appropriate ReplyKeyboard for that topic context.

**Re-install on every bot message (mitigation for desktop bug).** Every
single message the bot sends — alerts, replies, RECOVERY, anything — sets
`reply_markup` to the appropriate ReplyKeyboard for that topic. This
keeps clients refreshed and works around the
[python-telegram-bot/discussions/4426](https://github.com/python-telegram-bot/python-telegram-bot/discussions/4426)
desktop intermittent-disappearance bug.

**Removal on bot un-add.** No explicit removal step. If the bot is
removed from the supergroup, ReplyKeyboard disappears with it.

### 6.2 `[📊 Что происходит?]` press flow

```
1. Operator presses [📊 Что происходит?] in topic 👤 testkeen
2. Telegram delivers a Message to bot:
   - text:              "📊 Что происходит?"
   - chat.id:           <supergroup>
   - message_thread_id: <topic of testkeen>
   - from.id:           <operator>
3. Bot:
   a. Maps message_thread_id → user_id by querying users.telegram_thread_id
   b. Attempts to delete the operator's message (admin right
      can_delete_messages). On 403 / missing-right error, log at
      WARN level and continue — message just stays in chat. Never abort
      the smart-reply flow on delete failure.
   c. Looks up user state:
      - users.GetByID(user_id)
      - state.GetAll(user_id) → []IncidentState
      - events.LatestPerUser(user_id), events.LatestEvent(user_id, ...) for tunnels
   d. Picks template (OK/DEGRADED/HARD/OFFLINE) per §5.2
   e. Sends bot message with reply_markup = ReplyKeyboard + inline buttons
4. Operator sees the message in seconds; cleared user-message keeps
   topic clean.
```

If the topic does not map to any user (operator pressed in an unknown
topic), bot replies "не понял в каком контексте — нажмите в топике
конкретного роутера или в `📊 Сводка`."

### 6.3 Smart-reply inline button presses

These are existing callback_query mechanism, just exercised from the
new smart-reply message instead of the old pinned panel. Mechanism and
queue (cmd queue → agent long-poll → action runner → CommandResult)
unchanged.

**New requirement (§ 6.5):** when a CommandResult comes back from the
agent, the bot must reply to the original smart-reply message with
the result text, formatted human-readably. This fixes goal G3 (visible
feedback within 5 sec).

### 6.4 HARD-alert presses

Unchanged. callback_data shape `<action>:<user_id>:<check_name>`
preserved. Only display text humanised (§5.3).

### 6.5 `diag_now` / `opkg_upgrade` / `pingcheck_now` / `restart_tunnel` reply fix

**Current bug:** agent's `actions/runner.go:43` returns
`wire.CommandResult{Status, Output, DurationMs}` to the cmd queue. Backend
receives this result somewhere (existing path, in the long-poll response
back to backend). **It is not currently relayed to Telegram.** The operator
sees no reply to their tap.

**Fix:** when the backend's command-result handler receives a
`CommandResult`, look up the originating callback's chat/topic/message_id
(stored alongside the command in cmd queue), then post a reply via
`tg.SendMessageWithKeyboard` with:
- Reply-to the original alert / smart-reply message that triggered the action
- Body formatted per action type:
  - `diag_now`: "📊 Диагностика testkeen:\n\n```\n<output truncated to 3500 chars>\n```"
  - `pingcheck_now`: "▶ Тест связи: <output> (за <DurationMs>мс)"
  - `restart_tunnel`: "🔁 Перезапуск туннеля: <output>"
  - `opkg_upgrade`: "⬆ Обновление пакетов:\n\n<output>" (use full body, may need pagination on >4096)
  - On `Status=err`: prepend "❌ Не удалось:" before output

**Pagination:** if total message body exceeds 4000 chars, the bot splits
it into N **separate sequential messages** (not one truncated message).
Each chunk gets a `(K/N)` prefix on its first line. The (K+1)th chunk is
a reply-to the Kth so the operator sees them as a thread. Telegram raw
limit is 4096 chars; 4000 leaves headroom for the prefix and code-fence
markup.

## 7. Context resolution: topic → user

Every message and callback the bot receives in the supergroup carries
`message_thread_id` (zero if posted to General). The bot maintains a
mapping `users.telegram_thread_id` (already populated by existing code
in `db/users.go:166`).

**Lookup helper (new):**
```go
// db.UsersRepo.GetByThreadID(threadID int64) (*User, error)
// returns ErrUserNotFound when no user owns this topic
```

This is a single SQL query against an indexed column. Existing
`scanUserFull` is reused.

For operations topics (`📊 Сводка`, `🔧 Системное`), there is no user;
those topic IDs are stored in a new `kv` row (e.g.
`kv.summary_topic_id`, `kv.systemic_topic_id`) populated by the
operator via a new CLI command `wg-monitor-cli set-topic
--kind=summary --thread-id=N`.

## 8. Configuration changes

`backend.yaml` gains:
```yaml
ui:
  delete_user_command_messages: true  # default true; bot deletes "📊 Что происходит?" etc
  smart_reply_with_keyboard: true     # default true; resend ReplyKeyboard with each smart reply (desktop bug mitigation)
  diag_max_chars: 3500                # truncation threshold for diag output before pagination
```

No mandatory new fields — all defaults safe for current single-operator setup.

## 9. Migration

1. Deploy v0.6.0 backend to VPS Main.
2. Bot starts. Existing pinned messages from v0.5.0 stay in topics
   but are inert (their callback_data still works for backward
   compatibility — they share the same callback router).
3. Operator types `/menu` in own topic → ReplyKeyboard installs.
4. Operator manually unpins old pinned messages (no automation).
5. After 1 week of soak: remove deprecated `tg/control_panel.go`,
   `wg-monitor-cli init-menu` becomes a hard error.

## 10. Testing strategy

**Unit:**
- `tg.ReplyKeyboardForTopic(topicKind)` — returns correct keyboard for
  `per_router` / `summary` / `systemic` / `unknown`. Table test.
- `alerts.SmartReplyTemplate(state)` — returns correct template (OK /
  DEGRADED / HARD / OFFLINE) given test fixtures of `User`, `[]IncidentState`,
  latest events. Five fixtures = five subtests.
- `db.UsersRepo.GetByThreadID(N)` — table test against test DB.
- `alerts.FormatCommandResult(action, result)` — golden output for each
  action × {ok, err}. 10 testcases.
- `alerts.PaginateMessage(body, maxLen)` — pagination invariant tests
  (always splits cleanly, prefix consistent, never exceeds maxLen).

**Integration:**
- `cmd/backend/integration_test.go` extends with new flow:
  - mock TG receives ReplyKeyboard message "📊 Что происходит?"
  - assert bot sends correct OK template + inline buttons
  - assert bot deletes operator message
  - tap inline `[🔁 Перезапустить туннель]` → cmd queue gets entry → mock
    agent returns CommandResult → assert bot posts reply with result text

**Live verification (operator-driven, manual):**
- Tag `v0.6.0-ui-rework` deployed to VPS Main + testkeen.
- Operator presses `📊 Что происходит?` in `👤 testkeen` topic on
  desktop client. Verify keyboard reappears after press (desktop bug
  mitigation worked).
- Operator presses each inline button on a smart reply during a real or
  forced HARD. Verify reply text arrives within 5 sec for all three
  awgmgr-proxy actions (`diag_now`, `pingcheck_now`, `restart_tunnel`).
- Operator presses `[⬆ Opkg upgrade]` in HARD keyboard. Verify reply is
  paginated correctly (output usually 5-15 KB).

## 11. Open questions resolved during draft

- **Q: Show ReplyKeyboard in operations topics?** → Yes, with operator-wide
  commands (`Список юзеров`, `Здоровье флота`).
- **Q: Delete user's "📊 Что происходит?" message after processing?** →
  Yes by default, configurable via `ui.delete_user_command_messages`.
- **Q: Truncate diag output or paginate?** → Paginate at 4000-char chunks
  with `(N/M)` prefix.
- **Q: Behavior on multi-tunnel router?** → State templates always show
  per-tunnel context; if multi-tunnel + ambiguous action target, the smart
  reply lists each tunnel as a separate inline-button row (e.g.
  `[🔁 Перезапуск amnezia]` `[🔁 Перезапуск secondary]`).

## 12. Out of scope / explicit non-goals

- Web UI / status page — operator-rejected.
- Multi-language — Russian only.
- Multi-operator — single admin (operator), `selective: true` not used.
- Voice/photo replies — text-only.
- Dynamic ReplyKeyboard per state — kept simple at two buttons; state
  variation lives in inline-buttons of the smart reply, not in the bottom
  panel.
- Bot Menu Button (`menu_button` API) — only works in DM, not in
  supergroup, irrelevant here.

## 13. Sources

- [Telegram Bot API — ReplyKeyboardMarkup, is_persistent](https://core.telegram.org/bots/api)
- [python-telegram-bot Discussion #4426 — ReplyKeyboard desktop bug](https://github.com/python-telegram-bot/python-telegram-bot/discussions/4426)
- Internal: `internal/agent/actions/runner.go:43-95` (current action dispatch)
- Internal: `internal/backend/tg/keyboard.go:38-74`,
  `internal/backend/tg/control_panel.go:26-72` (current keyboard layouts)
- Internal: `internal/backend/db/users.go:122-159` (existing user lookup methods)
