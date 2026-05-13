# Router Operators — Design

**Date:** 2026-05-12
**Status:** Draft
**Targets:** v0.11.0 (next RC after the OPKG feed repair work)

## Summary

Today wg-monitor's per-router topic has a single ACL slot — the **owner**,
captured either via CLI `bind_tg_user` or via TOFU on the first inline-button
tap inside the topic. Anyone else who taps a button gets
`«это не твой роутер»`.

Add a second ACL channel — **operators** — a whitelist of additional Telegram
users granted full per-router control (same scope as the owner). Operators
are managed from the existing `/panel` admin hub: a new `👥 Доступ` screen
lists routers, shows the owner + operator set, and offers add / remove /
unbind-owner actions. Adding an operator accepts either a forwarded message
from the target user (preferred) or a manually-typed numeric Telegram user
ID (fallback when `forward_from` is hidden by the target's privacy settings).

Only the single global `cfg.AdminUserID` can open this screen. Operators
themselves cannot promote others — KISS, mirrors current text-command
admin-only convention.

## Why now

- The Telegram topic = router model has a natural multi-user case ("family
  router managed by two people", "shared router with a backup operator")
  that the current single-owner schema can't express.
- Operator binding via CLI (`bind_tg_user --user <id>`) exists but requires
  SSH to the VPS — friction the bot is supposed to remove.
- The admin `/panel` hub from rc24 is the natural home for fleet-level
  admin actions; this slots in alongside Maintenance / Routes / Status /
  Awaken.

## Non-Goals

- **No per-action permission granularity.** Operator can do everything
  owner can, including reboot / firmware install / tunnel import. If a
  use-case needs read-only operators, it's a separate iteration.
- **No CLI command for operator bind/unbind.** All operator management
  flows through the new `/panel` screens. If bulk-management ever becomes
  important, a CLI command can be added without touching the data model.
- **No auto-welcome for operators in router topics.** TOFU and welcome
  messages remain owner-only.
- **No self-claim flow for operators.** An unbound TG user tapping in the
  router topic never gets auto-promoted to operator — only TOFU for the
  primary owner slot still applies. Admin invites operators explicitly.
- **No private-DM notification** to the newly added operator. The bot
  cannot DM a user it has no prior `/start` from. Admin tells them out
  of band.
- **No history-screen / audit-log UI.** `granted_at` and `granted_by`
  are stored for forensics but not rendered in any screen.
- ~~**`HandleMessage` text commands stay admin-only.** Operators control
  routers only through inline buttons in the router topic.~~
  **Revised 2026-05-13:** operators reach owner-parity through the
  reply-keyboard buttons of their router's per_router topic too. Admin-only
  slash commands (`/ensure_topics`, `/this_is`, `/recreate_topic`, `/panel`,
  `/topic_help`) stay admin-only. Operators outside per_router topics
  (summary / systemic / unknown) are dropped. See
  `internal/backend/callbacks/router.go::HandleMessage` operator gate.
- **No race protection against the global admin being unbound mid-flow.**
  Admin is a config-level identity; bot restart picks up the latest config.

## Architecture

### Data model

New SQLite table:

```sql
CREATE TABLE IF NOT EXISTS router_operators (
    user_id          INTEGER NOT NULL,
    telegram_user_id INTEGER NOT NULL,
    granted_by       INTEGER NOT NULL,
    granted_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, telegram_user_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
```

- `user_id` = `users.id`, the per-router record. Naming follows the
  existing convention — `users` rows ARE routers in wg-monitor.
- `telegram_user_id` = the operator's TG user ID.
- `granted_by` = TG ID of the admin who added the entry (stored for
  potential future audit-log UI).
- `granted_at` = bind timestamp.
- Composite primary key prevents duplicate entries; `INSERT OR IGNORE` is
  the standard add path.
- `ON DELETE CASCADE` keeps things tidy if a router record is deleted.

### New repository

`internal/backend/db/router_operators.go`:

```go
type Operator struct {
    UserID         int64
    TelegramUserID int64
    GrantedBy      int64
    GrantedAt      time.Time
}

type RouterOperators struct { db *sql.DB }

func (r *RouterOperators) List(userID int64) ([]Operator, error)
func (r *RouterOperators) Add(userID, telegramUserID, grantedBy int64) error  // INSERT OR IGNORE
func (r *RouterOperators) Remove(userID, telegramUserID int64) error          // DELETE
func (r *RouterOperators) HasAccess(userID, telegramUserID int64) bool        // single-row check
```

The repository is reachable from `db.DB` via a new accessor `db.RouterOperators()`,
mirroring the existing `db.Users()`/`db.State()`/`db.Events()` pattern.

### ACL extension

Single insertion in `internal/backend/callbacks/router.go::aclAllow()` just
before the existing TOFU fallback:

```go
// existing: admin bypass → no-user bypass → lookup → owner-match check
// NEW:
if r.d.RouterOperators() != nil && r.d.RouterOperators().HasAccess(user.ID, q.From.ID) {
    return true
}
// existing TOFU fallback stays unchanged
```

**Revised 2026-05-13:** `HandleMessage` also gets an operator gate. After
the chat-id check, non-admin senders go through:

```go
kind, user := r.resolveTopicKind(m.MessageThreadID)
if kind != "per_router" || user == nil {
    return
}
if !r.d.RouterOperators().HasAccess(user.ID, m.From.ID) {
    return
}
// Operator passes; skip handleAdminCommand (slash commands stay admin-only).
```

### Callback grammar

Action: `access`. Screens, encoded as `access:0:<screen>[:<arg>[:<arg>]]`:

| Callback                                       | Screen / action                                 |
|------------------------------------------------|-------------------------------------------------|
| `access:0:home`                                | List of routers + owner + operator count        |
| `access:0:router:<router_id>`                  | Single router screen with operator buttons      |
| `access:0:add:<router_id>`                     | Start add-operator FSM for this router          |
| `access:0:remove_op:<router_id>:<tg_user_id>`  | Remove one operator                             |
| `access:0:unbind_owner:<router_id>`            | Set `users.telegram_user_id = NULL`             |
| `access:0:back`                                | Back to `/panel` home                           |
| `access:0:cancel_add`                          | Cancel an active add-operator FSM               |

`<router_id>` matches `^\d+$`; `<tg_user_id>` matches `^-?\d+$` (TG user
IDs are positive but defensive parsing).

The constant `0` placeholder in the `<uid>` slot keeps `Parse`'s 3-segment
minimum happy and follows the convention used by `panel:0:home`.

### Admin-only gate at dispatch

`HandleCallback` checks for any `access` action that `q.From.ID ==
cfg.AdminUserID`. Non-admins get a toast `«доступ только у админа»`.
This is a separate gate from `aclAllow()` because `aclAllow()` is about
**which router** a user owns — `access:*` callbacks are router-agnostic
admin actions.

### Add-operator FSM

In-memory `pendingAddOperator` store, mirrors `pendingMaint`:

```go
type pendingAddOperator struct {
    AdminUserID int64
    RouterID    int64
    ExpiresAt   time.Time
}
type pendingAddOperatorStore struct { mu sync.Mutex; m map[int64]*pendingAddOperator }
```

Keyed by `AdminUserID` (one active FSM per admin at a time). TTL 5 min.
Created on `access:0:add:<router_id>` callback; consumed when the admin
sends a qualifying message or expires.

`HandleMessage` extension: when admin sends a message in their **private
DM with the bot** (not in the forum chat — `m.Chat.ID == m.From.ID`) AND
`pendingAddOperator[adminUserID]` is unexpired, treat the message as add-
operator input:

- `m.ForwardFrom != nil` → use `m.ForwardFrom.ID` as the new operator's
  TG ID.
- Else if `m.Text` parses as `int64` → use that.
- Else if `m.ForwardFromChat != nil` (channel forward) → toast / reply
  «не вижу TG user ID, нужно сообщение от человека или цифровой ID»,
  keep FSM alive.
- Else → reply «не понял; перешли сообщение или напиши цифровой ID»,
  keep FSM alive.

On success, `RouterOperators.Add` + reply to admin: `«➕ Добавлен оператор
<id> для <nickname>. Открыть /panel чтобы продолжить.»`. The original
inline panel message is NOT edited — the user is now in DM, not in the
forum topic where the panel lives, so we can't reliably edit-in-place.

### UI text & layout

**Home screen (`access:0:home`):**

```
👥 Управление доступом

Выбери роутер:
[testkeen — owner: @vasya | 2 оператора]
[homekn   — owner: ?      | 0 операторов]
[← Назад]
```

- Owner label: `@<username>` if known (we don't store username today; show
  TG ID instead). Plain numeric `<tg_id>` if no username, `?` if NULL.
  **Initial implementation:** show numeric TG ID (no @username lookup),
  same as existing CLI/log identification of TG users. Username
  enrichment is out of scope.
- Operator count: `len(operators)` from `RouterOperators.List(user_id)`.

**Router screen (`access:0:router:<id>`):**

```
👥 testkeen (KN-1811)

Owner: 123456789
   [✖ Отвязать owner'a]

Операторы:
 • 987654321  [✖]
 • 456789012  [✖]

[➕ Добавить оператора]
[← К списку роутеров]
```

- Owner shown plain; null owner shows `Owner: (не привязан, TOFU)`.
- Each operator row gets its own inline row with the trailing `✖` button.
- Operators sorted by `granted_at ASC` (oldest first).

**Add prompt (after tap on `➕ Добавить оператора`):**

Bot replies in admin's DM:

```
🆔 Добавление оператора для testkeen

Перешли мне любое сообщение от нужного человека ИЛИ напиши его
числовой Telegram ID. Жду 5 минут.

[✖ Отмена]
```

The `Отмена` button maps to `access:0:cancel_add` and clears the pending
entry.

### Files touched

**Created:**

| File | Purpose |
|---|---|
| `internal/backend/db/router_operators.go` | Operator type + RouterOperators repository |
| `internal/backend/db/router_operators_test.go` | Repository tests (add idempotent, list, remove, has-access, CASCADE) |
| `internal/backend/callbacks/access_panel.go` | Screen renderers + pendingAddOperatorStore + AccessAction |
| `internal/backend/callbacks/access_panel_test.go` | Renderer + FSM + handler tests |
| `internal/backend/callbacks/access_acl_test.go` | Admin-only gate tests on access:* callbacks |

**Modified:**

| File | Change |
|---|---|
| `internal/backend/db/migrations.sql` | DDL for `router_operators` |
| `internal/backend/db/db.go` | Add `RouterOperators()` accessor on `*DB` |
| `internal/backend/callbacks/parse.go` | Add `access` to `validActions` + parse case + Args fields |
| `internal/backend/callbacks/parse_test.go` | Cover access grammar |
| `internal/backend/callbacks/panel_hub.go` | Add `👥 Доступ` button to home screen (admin-only) |
| `internal/backend/callbacks/router.go` | aclAllow operator-check + `SetAccessPanel` wiring + HandleCallback dispatch + HandleMessage FSM hook |
| `internal/backend/callbacks/router_test.go` | aclAllow operator-allow / non-operator-deny tests |
| `cmd/backend/main.go` | Wire `SetAccessPanel(store, action)` |

## Detailed design

### `Args` additions in `parse.go`

```go
// AccessScreen is the sub-screen for `access:*` callbacks.
// One of: "home" | "router" | "add" | "remove_op" | "unbind_owner" | "back" | "cancel_add"
AccessScreen string
// AccessRouterID is the users.id of the router for router/add/remove_op/unbind_owner screens.
AccessRouterID int64
// AccessOperatorTGID is the target operator's TG user ID for remove_op.
AccessOperatorTGID int64
```

### `aclAllow` extension

Patch shown above. Pure addition — no existing branch logic changes.

### Admin-only dispatch gate

In `HandleCallback`, before action dispatch:

```go
if strings.HasPrefix(args.Action, "access") {
    if r.cfg.AdminUserID == 0 || q.From.ID != r.cfg.AdminUserID {
        _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "доступ только у админа")
        return
    }
}
```

Placed alongside the existing `panel` admin gate if any, otherwise as the
first thing inside HandleCallback after parse.

### `pendingAddOperator` semantics

Mirror `pendingMaintStore`:

```go
func (s *pendingAddOperatorStore) put(adminID int64, routerID int64, ttl time.Duration)
func (s *pendingAddOperatorStore) get(adminID int64) (*pendingAddOperator, bool)  // unexpired only; evicts expired
func (s *pendingAddOperatorStore) clear(adminID int64)
```

Note: this store is **not single-use on get** — the admin might send a
qualifying message in DM AFTER the screen already moved away. We consume
on the actual write-to-DB success.

### HandleMessage FSM hook

```go
// At the top of HandleMessage, before existing admin-only gate:
if r.cfg.AdminUserID != 0 && m.From.ID == r.cfg.AdminUserID {
    if pending, ok := r.pendingAddOperator.get(m.From.ID); ok && m.Chat.ID == m.From.ID {
        r.processAddOperatorMessage(ctx, m, pending)
        return
    }
}
// existing admin-only gate stays unchanged for everything else
```

`processAddOperatorMessage`:
1. Determine operator TG ID via forward_from / numeric text / fallback.
2. On failure → reply with hint; do NOT clear pending.
3. On success → `RouterOperators.Add(pending.RouterID, opTGID, m.From.ID)` →
   reply `«➕ Добавлен оператор N для R. /panel»` → `r.pendingAddOperator.clear(m.From.ID)`.

### Owner unbind path

`access:0:unbind_owner:<router_id>`:

```go
err := r.d.Users().SetTelegramUserID(routerID, 0)  // 0 = clear; existing API
```

Existing `SetTelegramUserID` signature: takes `int64`, treats zero as
clear. (Verify in users.go; if it doesn't, add a `ClearTelegramUserID`
method.)

Re-render router screen with `Owner: (не привязан, TOFU)`.

## Testing

### Repository

- `TestRouterOperators_AddListRoundTrip` — two ops added, List returns both
  sorted by GrantedAt.
- `TestRouterOperators_AddIdempotent` — same op added twice, second is
  no-op, only one row, original `granted_at` preserved.
- `TestRouterOperators_Remove` — remove one of two, other remains, removed
  one no longer in List.
- `TestRouterOperators_HasAccess` — true/false matrix.
- `TestRouterOperators_CascadeOnUserDelete` — DELETE FROM users WHERE
  id=X also empties router_operators for that user_id.

### Parse

- `TestParse_Access_Home` / `Router` / `Add` / `RemoveOp` / `UnbindOwner` /
  `Back` / `CancelAdd`.
- Negative: unknown screen, missing/empty arguments, non-numeric arguments.

### ACL

- `TestAclAllow_Operator` — non-owner TG user listed in router_operators
  → true.
- `TestAclAllow_FormerOperator` — was operator, then removed, callback now
  → false.
- `TestAclAllow_OperatorDoesNotShortcircuitAdminBypass` — admin still
  bypasses.
- `TestAclAllow_OperatorPreservesOwnerTOFU` — adding an operator does not
  prevent TOFU from later setting an owner.

### Access-callback dispatch

- `TestHandleCallback_AccessNonAdmin_Toast` — non-admin tapping
  `access:0:home` → toast `«доступ только у админа»`, no state change.
- `TestHandleCallback_AccessRouterScreen_RendersOperators` — admin opens
  router screen, sees expected owner + operator buttons.
- `TestHandleCallback_AccessRemoveOp_PerformsDelete` — tap, op gone from
  DB, screen re-renders without it.
- `TestHandleCallback_AccessUnbindOwner_SetsNull` — owner shown as
  unbound after action.
- `TestHandleCallback_AccessAdd_CreatesFSM` — pendingAddOperator entry
  appears with TTL.

### Add-operator FSM through HandleMessage

- `TestHandleMessage_AddOperator_Forward` — admin forwards from user 999
  → row written for (router_id, 999), bot replies confirmation, FSM
  cleared.
- `TestHandleMessage_AddOperator_NumericText` — admin sends `"999"` →
  same outcome.
- `TestHandleMessage_AddOperator_ChannelForward` — `forward_from_chat`
  set, `forward_from` nil → hint reply, FSM remains.
- `TestHandleMessage_AddOperator_Garbage` — admin sends `"hello"` →
  hint reply, FSM remains.
- `TestHandleMessage_AddOperator_NotInDM` — admin sends qualifying
  message but in the forum chat (not DM) → FSM not consumed, normal
  admin-command flow proceeds.
- `TestHandleMessage_AddOperator_NonAdmin_NotConsumed` — non-admin sends
  message while admin's FSM is pending → ignored, FSM remains.
- `TestPendingAddOperator_Expiry` — TTL elapsed, `get` returns false.
- `TestHandleCallback_CancelAdd` — clears pending entry.

### Manual smoke test on testkeen

1. Admin opens `/panel` → `👥 Доступ` → tap `testkeen`.
2. Tap `➕ Добавить оператора`. Bot opens DM and asks for forward / ID.
3. Forward a message from a secondary test account.
4. Verify operator row appears in the router screen.
5. From the secondary account, tap a button on the testkeen maintenance
   panel — should work (no `«это не твой роутер»`).
6. Admin removes the operator via `✖`.
7. Secondary account taps again — now denied.
8. Admin taps `✖ Отвязать owner'a`. Owner taps in their own topic — TOFU
   re-binds them automatically.

## Rollout

- Migration runs at backend startup. Old installations get an empty
  table — ACL behavior identical to current.
- Mixed-version window: backend rc26 + any agent — operators work,
  everything is server-side. No wire-protocol change.

## Open questions

None at this point. All four design questions were resolved
(access model, add method, operator rights, edge cases / scope).
