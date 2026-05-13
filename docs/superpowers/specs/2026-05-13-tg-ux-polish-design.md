# TG UX polish — diagnostics refit, error hints, /help

**Date:** 2026-05-13
**Target version:** v0.12.0-rc5

## Goal

Bring every user-facing Telegram message into a consistent, informative
shape, replace bare `err.Error()` dumps with actionable hints, and add a
discoverable `/help` surface so operators and admins do not have to guess
what each button does.

The trigger is one concrete failure mode: tapping "📊 Диагностика" on a
fresh router yields

```
📊 Диагностика:

```
❌ Не удалось: awgmgr diagnostics/result: HTTP 400: {"error":true,"message":"no report available","code":"NO_REPORT"}
```
```

which is technically correct, visually broken (code-fence wrapping an
error), and unactionable. Auditing the codebase against this case
surfaced ~9 other sites where raw error text reaches the user with no
recovery path.

## Non-goals

- No new diagnostic checks on the router side. We render the existing
  awg-manager `/api/diagnostics/result` JSON better; we do not invent
  new probes.
- No structured wire-protocol change. Diag passes through as a string
  body in `wire.CommandResult.Output` (raw JSON for success, typed code
  for error) — the format stays a string contract, parsing happens
  backend-side.
- No persistent help DB. `/help` text is static, built from a
  package-level constant.
- No internationalisation. All strings stay Russian.
- No retroactive renaming of existing label/emoji conventions where they
  are already consistent across files (the audit found ❌/⚠️/✅/🔴/🟡/⚪ to
  be used consistently in the categories described in Section 1).

## Section 1 — Canonical message template

Every bot-to-user message is a card with three optional blocks:

```
<badge> <Label>: <one-line summary>
                                                   ← blank line
<details / multi-line body, code-fence only for monospaced output>
                                                   ← blank line
💡 <hint — concrete next step or short explanation>
```

Rules:

- **Badges:** ✅ ok · 🔴 hard · 🟡 warning · ⚪ inactive · ❌ failed ·
  ⏳ pending · ℹ info. ⚠️ stays reserved for confirm screens
  (`maint_restart`, `maint_fw_install`).
- **Code-fence ``` ```:** only for machine-output that benefits from
  monospaced rendering — JSON dumps, opkg logs, raw route tables.
  Never for human-language errors.
- **Hint mandatory** when badge is ❌. Optional for 🟡. Forbidden for ✅
  unless the success itself is incomplete (e.g. "сработало, но эффект
  через минуту").
- No bare `err.Error()` in user-visible strings. All error rendering
  flows through `HintFor()` (Section 2).
- Trailing colon rule: `Label: <summary>` if the summary fits inline;
  `Label\n\n<details>` if details go on their own lines.

This template is owned by a new helper `internal/backend/alerts/card.go`:

```go
type Card struct {
    Badge   string // "✅", "🔴", ...
    Label   string // "📊 Диагностика"
    Summary string // one-line; renders after the colon
    Details string // optional multi-line body; pass with hasCodeFence=true if monospaced
    Hint    string // "💡 ..."; renders below details
}

func (c Card) Render(opts CardOpts) string
```

`opts.CodeFenceDetails bool`, `opts.MaxBytes int` (for chunk caps).
Caller passes the assembled `Card`; renderer produces the final string.
The existing `FormatCommandResult` switches to constructing `Card`
instances per action.

## Section 2 — Centralised error hint dictionary

New file `internal/backend/alerts/error_hints.go`:

```go
// HintFor maps a raw error string + action name to a user-friendly
// (summary, hint) pair. Falls back to a generic "что-то пошло не так"
// + the trimmed first line as hint when no pattern matches.
func HintFor(action, raw string) (summary, hint string)
```

Match order: literal substring tests, longest-pattern-first. Patterns:

| Match in `raw` | Summary | Hint |
|---|---|---|
| `NO_REPORT` / `no report available` | `отчёт ещё не сформирован` | (resolved by Section 3 auto-trigger; reaches user only if trigger itself failed) `Запусти ещё раз — awg-manager не успел подготовить отчёт.` |
| `DIAG_TIMEOUT` (typed) | `диагностика не уложилась в 36с` | `awg-manager запустил отчёт, но не успел его собрать. Попробуй ещё раз — обычно это занимает 30–60с.` |
| `HTTP 502` / `HTTP 503` (from `awgmgr`) | `awg-manager недоступен` | `Зайди по SSH и выполни: \`/opt/etc/init.d/S99awg-manager status\`. Если упал — \`/opt/etc/init.d/S99awg-manager restart\`.` |
| `HTTP 401` / `HTTP 403` (from `awgmgr`) | `awg-manager не пускает агент` | `Токен агента устарел. В wizard: «📦 Установить агент» переустановит токен на роутере.` |
| `connection refused` / `dial tcp` | `agent не достучался до awg-manager` | `awg-manager не слушает порт 2222. \`netstat -tln │ grep 2222\` на роутере покажет, поднят ли он.` |
| `timeout` (CommandResult.Status=`timeout`) | `агент не уложился в лимит` | `Роутер занят (CPU/диск). Подожди минуту; если повторится — \`top\` + \`logread\` диагностируют причину.` |
| `locked` (CommandResult.Status=`locked`) | `другая операция держит lock` | `Подожди ~30с — параллельная команда ещё не отпустила lock-файл. Если зависло > 2 минут — попроси админа: \`rm /opt/var/run/wg-monitor.lock\`.` |
| `database is locked` (SQLite from admin paths) | `SQLite занят` | `Это transient. Подожди 1–2 секунды и повтори.` |
| default (no match) | `что-то пошло не так` | `Деталь: \`<первая 200 символов raw, очищенная>\`. Покажи админу или попробуй ещё раз через минуту.` |

Existing error-rendering sites switch over:
- `internal/backend/alerts/command_result.go::FormatCommandResult` —
  replaces the bare `"❌ Не удалось: " + body` with
  `Card{Badge:"❌", Summary, Hint}` from `HintFor`.
- `internal/backend/callbacks/admin_topics.go` — 4 sites
  (`adminEnsureTopics`, `adminRecreateTopic`, `adminThisIs` reply
  helpers).
- `internal/backend/callbacks/access_panel.go::accessHomeMessage` —
  DB read failure.
- `internal/backend/callbacks/panel_hub.go` — toast strings on roster
  failures.

Toasts (AnswerCallbackQuery body, capped at 200 chars) get the
`summary` only — hint goes into the followup edited message when one
exists.

## Section 3 — Diag flow refit

### Agent side

New endpoint wrapper in `internal/agent/awgmgr/client.go`:

```go
// DiagRun POSTs /api/diagnostics/run. Idempotent on awg-manager 2.8.2 —
// re-posting during an in-flight run returns the same {status:"running"}
// body without re-starting.
func (c *Client) DiagRun(ctx context.Context) error
```

Rewrite `internal/agent/actions/runner.go::DiagNow` (action handler):

```
GET /api/diagnostics/result
  HTTP 200 → return body, status=ok
  HTTP 400 with NO_REPORT in body:
    POST /api/diagnostics/run
    if 200:
      for i := 0; i < 12; i++ {  // 12 × 3s = 36s budget
        sleep 3s
        GET /api/diagnostics/result
        if HTTP 200 → return body, status=ok
        if HTTP 400 with NO_REPORT → continue
        if other error → return that error, status=err
      }
      return Output="DIAG_TIMEOUT", status=err
    if !200 → return that error verbatim with typed prefix, status=err
  other HTTP → return verbatim with typed prefix, status=err
```

Typed prefix means: when bubbling a HTTP error, the agent prepends a
stable token (`HTTP_502:`, `NO_REPORT:`, `DIAG_TIMEOUT:`, `REFUSED:`)
so the backend's `HintFor` can pattern-match without parsing JSON.

### Backend side

New file `internal/backend/alerts/diag_report.go`:

```go
// ParseDiagReport extracts headline facts from the awg-manager JSON
// report. Returns a Card-ready summary string + details bullets +
// fallback raw flag (true when JSON parse fails — caller falls back
// to dumping the raw body in a code-fence, preserving today's behaviour
// for unknown shapes).
func ParseDiagReport(raw string) (summary string, bullets []string, rawFallback bool)
```

Output structure for the success path (parsed):

```
📊 Диагностика — всё ок (2 559 мс)

📅 Снято: 2026-05-13 07:20:40 UTC
⚙ Версия awg-manager 2.8.2, backend kernel, RAM 489 MB
⏱ Uptime: 1d 17h 30m
🌐 WAN: ✅ eth3 up · ⚪ apcli0 / apclii0
🔗 Туннели: <per-tunnel from .tunnels[] if present>
🧭 DNS: <from .dns[] if present>

💡 Полный JSON-отчёт — кнопка ниже.

[📄 Полный отчёт]   [🔁 Перезапустить диагностику]   [✖ Закрыть]
```

For the error path, `Card{Badge:"❌", Label:"📊 Диагностика", Summary,
Hint}` from `HintFor("diag_now", raw)`:

```
❌ Диагностика — диагностика не уложилась в 36с

💡 awg-manager запустил отчёт, но не успел его собрать. Попробуй ещё
раз — обычно это занимает 30–60с.

[🔁 Попробовать снова]   [ℹ Помощь]   [✖ Закрыть]
```

Buttons:
- `📄 Полный отчёт` — callback `diag_raw:<userID>:<token>`, new short-
  lived token (5-min TTL, like rebind). Tap fetches the cached raw
  body and sends it as a new message in a code-fence.
- `🔁 Перезапустить диагностику` / `🔁 Попробовать снова` — re-enqueues
  `diag_now` exactly like the existing tunnel-action button.
- `ℹ Помощь` — opens the diag-specific help screen (Section 5).
- `✖ Закрыть` — edits message to remove the keyboard (like
  `routes_close`).

The cached raw body lives in `internal/backend/callbacks/` as a
`diagReportCache` (sync.Map keyed by token, 5-min TTL). Cache is
populated by the result handler before rendering the card.

### Parser scope

`ParseDiagReport` reads only top-level documented fields:

- `system.appVersion` (string)
- `system.backend` (string)
- `system.totalMemoryMB` (number)
- `system.uptime` (string)
- `system.kernelModule.loaded` (bool)
- `wan.anyUp` (bool)
- `wan.interfaces` (map[string]{up bool, label string})
- `generatedAt` (string, RFC3339; converted to Moscow time)
- `durationMs` (number)

Unknown fields are ignored, not errors. If the JSON parse itself fails,
`rawFallback = true` and the caller wraps the raw body in a code-fence
exactly like today.

## Section 4 — /help command

### Bot menu entry

Extend `cmd/backend/main.go` `SetMyCommands` call to include `/help`
alongside the existing `/panel` etc.

### Dispatch

New file `internal/backend/callbacks/help.go`:

```go
func (r *Router) handleHelpCommand(ctx context.Context, m *tg.Message)
```

Wired into `handleAdminCommand` (admin gets the full text) AND into the
operator-allowed path added in [router-operators design](2026-05-12-router-operators-design.md)
so operators reach it from any router topic where they have access.
A non-operator non-admin who types `/help` outside a router topic gets
a short generic message ("Этот бот работает в твоей группе мониторинга;
у тебя нет прав, попроси админа добавить тебя.").

### Role detection

```go
func (r *Router) helpRole(userID int64) string {
    if r.cfg.AdminUserID != 0 && userID == r.cfg.AdminUserID { return "admin" }
    if r.userHasAnyOperatorOrOwnerBinding(userID) { return "operator" }
    return "none"
}
```

`userHasAnyOperatorOrOwnerBinding` issues one indexed SELECT against
`users.telegram_user_id` and one indexed SELECT against
`router_operators.telegram_user_id`. Cheap, no caching.

### Content

The body of `/help` is built from three sections, conditionally
included:

**Section A — для всех (operator + admin):**
- Что значат алерты: ✅ / 🟡 / 🔴 / 📵 — с одной строкой объяснения
- 7 кнопок в топике (📊 Что происходит? · 🎛 Туннели · 🛣 Маршруты ·
  🛠 Обслуживание · 🌍 Через тоннель? · 🇷🇺 Напрямую? · ⬆ Обновить
  пакеты) — по строке "что делает + когда жать"
- Что значат inline-кнопки под алертом (⏸ Тише на X · ✅ Понял ·
  📋 История · 🔇 Тихо до утра · 🔁 Перезапуск туннеля · 📊 Диагностика ·
  ▶ Тест связи)

**Section B — только admin:**
- `/panel` — главный хаб (что внутри: 🛠 Обслуживание / 🛣 Маршруты /
  📊 Status / 🪄 Оживить топики / 👥 Доступ)
- `/this_is <nickname>` — привязать топик
- `/ensure_topics`, `/recreate_topic`, `/topic_help` — на одной строке

**Section C — только operator (без admin):**
- «Ты в whitelist на роутере(ах) <list>. Ты можешь всё то же, что
  владелец, кроме `/panel` и slash-команд админа.»

Content is a single `const helpAdminBody = "..."` + `const
helpOperatorBody = "..."` in `help.go`; assembly is straight
concatenation. Sized to fit in one TG message (< 3500 chars). If
operator binding list is long, truncate with "..., и ещё N".

The existing `/topic_help` stays as an admin alias (deprecated note in
its body pointing at `/help`).

## Section 5 — Per-panel "ℹ Помощь" buttons

Each panel-hub screen (`maint`, `routes`, `tunnels` (per-router),
`access`, `status`) gets a new inline-keyboard row positioned just
above `✖ Закрыть` / `« Назад`:

```
[ℹ Помощь]
```

Callback grammar: `panel:0:help:<screen>` where `<screen>` ∈
{maint, routes, tunnels, access, status, diag}.

Tap → `EditMessageText` replaces the panel body with a help blurb
specific to the screen (one short paragraph per visible button: what
it does, what risk to expect). Bottom row is
`[« Назад к панели]   [✖ Закрыть]`. Back-button re-renders the panel
from cache.

`diag` is a synthetic screen: it's reached only from the diag-result
message's `ℹ Помощь` inline button, not from `/panel`. Body explains:
"Диагностика — короткий отчёт о системе/туннелях/DNS. Запуск
~30–60с. Не меняет состояние, только читает." + list of what's in
the report.

Help blurbs live in `internal/backend/tg/help_panels.go` as a
`map[string]string`.

## Architecture

```
internal/backend/alerts/
  card.go                  NEW — Card struct + Render
  card_test.go             NEW
  error_hints.go           NEW — HintFor() pattern table
  error_hints_test.go      NEW
  diag_report.go           NEW — ParseDiagReport
  diag_report_test.go      NEW
  command_result.go        EDIT — switch to Card + HintFor
  command_result_test.go   EDIT — update assertions
  format.go                EDIT — HardAlert/Recovery use Card
  smart_reply.go           EDIT — already structured; minor consistency

internal/backend/callbacks/
  help.go                  NEW — /help dispatch + role detection
  help_test.go             NEW
  diag_cache.go            NEW — short-lived raw-body cache for "Full report" button
  diag_cache_test.go       NEW
  router.go                EDIT — dispatch help + diag_raw callbacks; wire diag_cache
  parse.go                 EDIT — add "help" action and panel_help screen
  panel_hub.go             EDIT — render "ℹ Помощь" rows in all panels
  admin_topics.go          EDIT — error-site replacements via HintFor
  access_panel.go          EDIT — same
  router_test.go           EDIT — operator /help routes through correctly

internal/backend/tg/
  help_panels.go           NEW — help blurb map
  maint_panel.go           EDIT — append "ℹ Помощь" row
  routes_panel.go          EDIT — same
  tunnels_panel.go         EDIT — same
  reply_keyboard.go        no change

internal/agent/
  awgmgr/client.go         EDIT — add DiagRun + typed-prefix on errors
  awgmgr/client_test.go    EDIT
  actions/runner.go        EDIT — diag_now: trigger + poll
  actions/runner_test.go   EDIT

cmd/backend/main.go        EDIT — register /help in SetMyCommands

docs/superpowers/specs/
  2026-05-12-router-operators-design.md  no change
```

## Testing strategy

Unit (Go test, in-package):

- `card_test.go` — render permutations: badge+summary only; badge+summary+details (no code-fence); badge+summary+details (code-fence); badge+summary+hint; full quad. Truncation at `MaxBytes`.
- `error_hints_test.go` — every row from the dictionary table matches; default fallback fires for unknown raw; the `<raw>` injection in the default hint is sanitised (no trailing newline, no embedded ``` that would break the fence in callers).
- `diag_report_test.go` — fixture: full JSON from the testkeen probe (truncated form). Asserts: each documented field is rendered; missing fields are skipped silently; raw fallback fires on malformed JSON; raw fallback fires on JSON missing `system.appVersion` (sentinel — we only "succeed parsing" if at least one core field is present, otherwise display raw).
- `command_result_test.go` — existing OK / Error / Timeout / Locked tests rewritten to assert Card structure (no raw `err.Error()` text). NEW: `TestFormatCommandResult_DiagNoReportTriggered` asserts that on `DIAG_TIMEOUT` payload, the rendered card has summary "диагностика не уложилась в 36с" and hint mentions retry.
- `diag_cache_test.go` — Put/Get cycle; TTL eviction; same token retrieved twice returns body twice (no consume-on-read; the button is re-tappable until expiry).
- `help_test.go` — admin gets all sections; operator gets A + C only; non-operator non-admin gets generic deny.
- `runner_test.go` — diag_now success path (existing); NO_REPORT → triggers POST /run + poll loop succeeds on iteration N; NO_REPORT → /run returns 500 → bubbles HTTP_500; /run succeeds, poll never sees 200 → DIAG_TIMEOUT typed prefix.
- `awgmgr/client_test.go::TestClient_DiagRun_*` — happy 200, error 500 wrapping.
- `panel_hub_test.go` and panel-renderer tests — every screen has the new `ℹ Помощь` row in its inline keyboard.

Integration:

- `cmd/backend/integration_test.go` already has a diag-result fake.
  Extend it: when the agent first GETs `/api/diagnostics/result` the
  fake returns 400+NO_REPORT, then on POST `/run` returns 200, then
  the next GET returns 200 with the canned body. Assert the rendered
  TG card is the parsed one.

Manual / acceptance on testkeen:

- Tap "📊 Диагностика" in a router topic where awg-manager has just
  rebooted (NO_REPORT state). Expect: ~30–40s delay, then the parsed
  card. Tap "📄 Полный отчёт" → JSON in code-fence.
- Tap "📊 Диагностика" when awg-manager is stopped. Expect: HTTP
  refused → friendly summary + service-restart hint.
- Type `/help` as admin (one router topic, then DM). Expect both reach
  the body, no formatting drift.
- Type `/help` as operator in operator's router topic. Expect Section
  A + C only.

## Mixed-version rollout

The change set is backend-only on the rendering side and agent-only on
the diag-trigger side. Agent-old + backend-new: the new backend
gracefully degrades — if it gets a CommandResult Output that doesn't
start with a typed-prefix and isn't valid JSON, `HintFor` falls
through to the generic case and `ParseDiagReport` sets `rawFallback`.
No crashes, just less-pretty hints.

Agent-new + backend-old: the agent emits typed prefixes (`HTTP_502:`
etc.). The old backend's `FormatCommandResult` keeps the legacy
`"❌ Не удалось: HTTP_502: ..."` rendering — uglier than the new one
but no worse than today. No need to gate.

So no feature-flag, no DB migration, no protocol bump. Roll
backend-first then agents (the wizard's existing flow does this).

## Open questions

- The per-panel `[ℹ Помощь]` adds one row to every keyboard. Maintenance
  panel is already tall; verify on phone that nothing wraps badly. If
  cramped, push help into a callback toast instead. (Will assess
  during implementation; reversible.)
- Diag JSON might be > 4096 bytes raw; the "Полный отчёт" button
  reuses the existing `paginate` helper so this is handled.

## Spec self-review

- Placeholders: none. Every section names files, functions, callback
  grammar, and test names.
- Internal consistency: button labels and callback grammar are
  consistent across sections 3, 4, 5.
- Scope: one implementation plan can cover all five sections; they
  share infrastructure (`Card`, `HintFor`, `help_panels.go`) but each
  has a small, independent slice of files.
- Ambiguity: `userHasAnyOperatorOrOwnerBinding` was named — it goes in
  `internal/backend/db/users.go` (or a new method on the existing
  `Users()` repo) to keep DB access concentrated. Default fallback
  hint includes raw — clarified that raw is trimmed to 200 chars and
  sanitised.

## Status

Implemented on 2026-05-13 via subagent-driven execution of
[2026-05-13-tg-ux-polish.md](../plans/2026-05-13-tg-ux-polish.md), 15 tasks,
commits `6823e5b..2eacdcf` on `main`. Full test suite (21 packages) green;
`go vet ./...` clean. Outstanding: manual smoke on testkeen (see plan Task 15
Step 3 checklist) before tagging `v0.12.0-rc5`.

Final method name landed as `Users().HasAnyOperatorOrOwnerBinding` (the
ambiguity note above used `userHasAnyOperatorOrOwnerBinding` as a working
name).

Minor implementation deviations from the original spec, captured here so the
plan and spec stay self-consistent:

- `Card.Badge` was set to `""` (empty) for actions whose label already
  carries a verb-emoji (`pingcheck_now`, `restart_tunnel`, `diag_now` success).
  The original spec implied a non-empty badge everywhere; doing so caused
  doubled-emoji output (`"📊 📊 Диагностика: ..."`) and was reverted to empty.
- The diag success Card hint changed from "Полный JSON-отчёт — кнопка ниже"
  to "Полный JSON-отчёт доступен по кнопке ниже." (minor wording).
- `helpOperatorBody` reworded to avoid the literal substring `/panel` (the
  operator test asserts the absence of `/panel` in operator-rendered help to
  prevent admins reading "operators see /panel"-style text).
- `TGNotifier.NotifyCommandResult` interface gained a `userID int64`
  parameter (5th position) because `cmdpkg.MessageRef` does not carry
  `UserID`; callers in `handler.go`, the integration-test fake
  (`recordingNotifier`) and the unit-test fake (`relayCapture`) were
  updated accordingly.
- `MaintPanelKeyboard` last row was split (was `[🔄 Проверить апдейты]
  [✖ Закрыть]`, now three rows: updates, ℹ help, close) so the help row
  inserts cleanly.
