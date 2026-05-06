# wg-monitor — Stage 2 design (inline-button callbacks + StaleHards re-alert poller)

**Status:** Approved (operational defaults + architecture confirmed)
**Date:** 2026-04-28
**Parent spec:** `2026-04-25-wg-monitor-design.md` (sections 5.3, 6.2, 6.3, 6.4)
**Predecessor:** Stage 1.5 — `v0.3.0-checksfix` (2026-04-28, all 4 checks live-verified)
**Estimated size:** 0.5 session per parent spec section 14

## 1. Motivation

Stage 1 шлёт HARD/RECOVERY алерты, но без интерактивности. Когда инцидент срабатывает в неудобное время (ночью, во время встречи), у админа нет способа сказать «вижу, не тревожь» — алерт молчит до RECOVERY либо генерирует новый HARD при следующих fail'ах. Stage 2 добавляет inline-кнопки на HARD-алертах для четырёх идемпотентных действий (silence/ack/history/mute) и периодический re-alert на залипший HARD старше 6 часов, чтобы забытые инциденты не терялись.

## 2. Goals & non-goals

### Цели

1. Inline-кнопки `[⏸1ч][⏸4ч][⏸24ч][✅Ack]` / `[📋История 24ч][🔇Mute до утра]` на каждом HARD-алерте.
2. Long-poll `getUpdates` на стороне backend, обработка `callback_query`, фильтр по `admin_user_id`.
3. Edit оригинального сообщения после действия: убрать кнопки, дописать строку статуса.
4. StaleHards re-alert poller (5-min ticker), отправляет напоминание для HARD старше 6 часов, **без кнопок** (read-only сигнал).
5. Schema-добавка: одна колонка `acked` в `incident_state` + KV-таблица `tg_state` для persistence offset'а.
6. ~40 новых unit-тестов; live verification на testkeen.

### Не-цели (Stage 3+)

- Уровень 2 кнопок (`Diag`, `Test now`, `Restart AWG`) — Stage 3.
- ack_token / TTL / single-use semantics для destructive callbacks — Stage 3.
- Per-user mute (заглушает все алерты юзера, не одну пару user+check) — YAGNI.
- Inline-кнопка «Unack» — YAGNI (Ack снимается автоматически на recovery, см. §3.1).
- `wg-monitor-cli silence/ack/unmute` — пока не нужен, есть кнопка; добавим в Stage 5 если выяснится потребность.

## 3. Operational defaults (resolved)

| Параметр | Значение | Обоснование |
|---|---|---|
| **Ack semantics** | привязан к инциденту, авто-снимается при recovery, не глушит RECOVERY | Q1 = вариант A. Семантически отличается от silence; «увидел, починю утром, не тревожь меня». |
| **Mute granularity** | per-incident (`incident_state.silenced_until`) | Q2a = вариант A. Консистентно с другими silence-кнопками; schema готова. |
| **Mute cutoff** | configurable, default 9:00 в `Europe/Moscow` (`alerts.mute_cutoff_hour`) | Q2b = вариант C. `time.LoadLocation` корректно работает с возможным возвратом DST. |
| **Mute boundary** | следующее `cutoff:00` строго в будущем (нет min-window) | E8: edge-case в 8:55 МСК = 5 мин mute, accept. |
| **Re-alert poller cadence** | `time.Tick(5 minutes)` | Q3 = вариант A. Re-alert через 6h — некритичен по точности; 12 SQL-запросов/час дёшево. |
| **Re-alert interval** | `now - 6h` (per parent spec §5.3) | Не меняется. |
| **Callback edit UX** | edit оригинала, кнопки убраны, status-line добавлена в конец | Q4 = вариант A. Видимый аудит-лог в чате; кнопок нет — невозможно случайно повторно нажать (заменяет ack_token из спеки 6.4). |
| **Re-alert format** | `🔁 [Vasya] check — STILL DOWN ... Re-alert #N (every 6h)` | Q5a = вариант A. Explicit urgency. |
| **Re-alert buttons** | **нет кнопок** | Q5b. Re-alert — read-only сигнал «всё ещё горит». Trade-off: silence-after-expire требует CLI-fallback (Stage 5). |
| **History format** | минимальный transitions list (не каждый report) | Q6 = вариант A. Помещается в 4096-char limit при типичных 5-30 transitions; truncate at 30 with notice. |

## 4. Architecture

```
                       ┌─────────────────────────────┐
                       │   wg-monitor-backend.exe    │
                       └──────────────┬──────────────┘
                                      │
   ┌────────────────────┬─────────────┼──────────────────┬───────────────────┐
   │                    │             │                  │                   │
   ▼                    ▼             ▼                  ▼                   ▼
┌────────┐         ┌─────────┐   ┌──────────┐      ┌──────────┐       ┌──────────┐
│Caddy   │         │heartbeat│   │ alerts/  │      │callbacks/│ NEW   │ realert/ │ NEW
│ /v1/*  │         │ watcher │   │dispatcher│      │  router  │       │  poller  │
│   ↓    │         │  (30s)  │   │+keyboard │      │+actions  │       │  (5min)  │
│http    │         └─────────┘   └─────┬────┘      └────┬─────┘       └────┬─────┘
│handler │              │              │ HARD/RECOV     │ apply+edit       │ re-alert
└───┬────┘              │              ▼                ▼                  ▼
    │ POST/v1/report    │           ┌────────────────────────────────────────┐
    ▼                   │           │            tg/ (extended)              │
┌──────────┐            │           │  SendMessage / EditMessageText /       │
│  state/  │            │           │  AnswerCallbackQuery / GetUpdates /    │
│   FSM    │            │           │  SendMessageWithKeyboard               │
└────┬─────┘            │           └────────────────────┬───────────────────┘
     │                  │                                │
     ▼                  ▼                                ▼
   ┌──────────────────────────────────────────────────────────────┐
   │                    db/ (SQLite, repos)                        │
   │  users · events · incident_state(+acked) · daily_soft_flaps  │
   │  tg_state(KV) NEW                                             │
   └──────────────────────────────────────────────────────────────┘
```

Три новые горутины (стартуют в `cmd/backend/main.go`):

1. **`callbacks.Run(ctx)`** — long-poll `getUpdates` (TG hold 30 сек), парсит `callback_query`, фильтрует по `admin_user_id`, диспатчит на action handlers. Один loop на весь backend.
2. **`realert.Run(ctx)`** — `time.Tick(5*time.Minute)`, в каждом тике зовёт `state.StaleHards(now-6h)`, для каждого row отправляет re-alert и update'ит `LastAlertAt`.
3. (без изменений) `heartbeat.Run(ctx)`.

## 5. Schema changes

### 5.1 New column: `incident_state.acked`

```sql
ALTER TABLE incident_state ADD COLUMN acked INTEGER NOT NULL DEFAULT 0;
```

`acked = 1` при нажатой `[✅ Ack]`, `0` иначе. Сбрасывается в FSM-handler'е `Recovery` transition (строки `case state.Recovery:` в `alerts/dispatcher.go` — добавляется `next.Acked = false` перед Save).

`acked_until` (уже в schema из Stage 1) остаётся unused в Stage 2 — зарезервирован под будущие TTL-варианты, если понадобятся.

### 5.2 New table: `tg_state` (KV)

```sql
CREATE TABLE IF NOT EXISTS tg_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

Используется как KV-стор. Stage 2 пишет один ключ — `last_update_id`. Stage 3+ добавит новые ключи в эту же таблицу.

### 5.3 Migration: idempotent helper

SQLite не поддерживает `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`. В `db.go` добавляется helper:

```go
func migrateAcked(d *sql.DB) error {
    var n int
    if err := d.QueryRow(
        `SELECT count(*) FROM pragma_table_info('incident_state') WHERE name='acked'`).Scan(&n); err != nil {
        return err
    }
    if n == 0 {
        _, err := d.Exec(`ALTER TABLE incident_state ADD COLUMN acked INTEGER NOT NULL DEFAULT 0`)
        return err
    }
    return nil
}
```

Вызывается после `Exec(migrationsSQL)` в `Open()`.

`tg_state` идёт через обычный `CREATE TABLE IF NOT EXISTS` в `migrations.sql`.

### 5.4 Updated query: `StaleHards()`

В `internal/backend/db/state.go::StaleHards()` добавляется условие:

```sql
WHERE current_status = 'hard'
  AND last_alert_at < ?
  AND (silenced_until IS NULL OR silenced_until < CURRENT_TIMESTAMP)
  AND acked = 0    -- NEW
```

Так Ack эффективно глушит re-alert до RECOVERY-сброса.

## 6. Components

### 6.1 `tg/` — extensions

**Новые файлы:**

- **`keyboard.go`** — pure-data builder.
  ```go
  type InlineKeyboardButton struct{ Text, CallbackData string `json:"..."` }
  type InlineKeyboardMarkup struct{ InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"` }
  func HardAlertKeyboard(userID int64, checkName string) InlineKeyboardMarkup
  ```
  Возвращает 2 ряда: `[⏸1ч][⏸4ч][⏸24ч][✅Ack]` / `[📋История 24ч][🔇Mute до утра]`.

- **`updates.go`** — long-poll и Update-типы.
  ```go
  type Update struct { UpdateID int64; CallbackQuery *CallbackQuery }
  type CallbackQuery struct { ID string; From User; Message Message; Data string }
  type Message struct { MessageID int64; Chat Chat; MessageThreadID *int64; Text string }
  type User struct { ID int64 }
  type Chat struct { ID int64 `json:"id"` }

  // GetUpdates(ctx, offset, timeoutSec) → []Update
  //   timeout=30 (TG hold), allowed_updates=["callback_query"]
  ```

**Дополнения в `client.go`:**

```go
SendMessageWithKeyboard(ctx, chatID, threadID, text, parseMode, replyTo, markup *InlineKeyboardMarkup) (int64, error)
AnswerCallbackQuery(ctx, callbackID, text string) error
EditMessageText(ctx, chatID, messageID, text, parseMode string, markup *InlineKeyboardMarkup) error
```

`EditMessageText` контракт по `markup`:
- `markup == nil` — поле `reply_markup` не отправляется (TG не трогает существующий keyboard).
- `markup == &{}` (пустой) — `reply_markup: {"inline_keyboard": []}` (TG удаляет кнопки).

### 6.2 `internal/backend/callbacks/` — NEW

**`router.go`:**

```go
type Router struct {
    db        *db.DB
    tg        TGClient
    cfg       Config
    actions   map[string]Action
}

type Config struct {
    ChatID         int64
    AdminUserID    int64
    MuteCutoffHour int           // 0..23, default 9
    MuteLocation   *time.Location // default Europe/Moscow
}

type Action interface {
    Apply(ctx, q *tg.CallbackQuery, args ParsedArgs) (statusLine string, err error)
}

func (r *Router) Run(ctx context.Context) error
//   loop:
//     updates = tg.GetUpdates(offset, 30s)
//     on err → log + sleep min(2^attempt, 60s) + retry
//     for u in updates:
//       handleUpdate(ctx, u)
//     offset = lastUpdateID + 1
//     persist offset to tg_state.last_update_id
```

**`actions.go`** — четыре action handler'а:

| Action key (callback_data prefix) | Описание | DB write | Status line |
|---|---|---|---|
| `silence:<uid>:<check>:1h` | TTL=1h | `silenced_until = now+1h` | `⏸ Silenced до HH:MM МСК (admin)` |
| `silence:<uid>:<check>:4h` | TTL=4h | same with 4h | same |
| `silence:<uid>:<check>:24h` | TTL=24h | same with 24h | same |
| `ack:<uid>:<check>` | До OK | `acked = 1` | `✅ Ack'ed (до восстановления)` |
| `mute:<uid>:<check>` | До next 9:00 МСК | `silenced_until = nextCutoff()` | `🔇 Muted до 09:00 МСК (DD MMM)` |
| `history:<uid>:<check>` | 24h transitions list | (read) | (отдельное сообщение в топик; AnswerCallback silent; **не** edit'ит оригинал) |

`Mute.nextCutoff()` алгоритм:
```go
loc, _ := time.LoadLocation("Europe/Moscow")
now := time.Now().In(loc)
target := time.Date(now.Year(), now.Month(), now.Day(), cutoffHour, 0, 0, 0, loc)
if !target.After(now) {
    target = target.AddDate(0, 0, 1)
}
return target.UTC()
```

**Allowlist:**
```go
if q.From.ID != cfg.AdminUserID {
    tg.AnswerCallbackQuery(q.ID, "not authorized")
    log.Warn("rejected callback", "from", q.From.ID, "data", q.Data)
    return
}
```

`AdminUserID = 136513775` (из parent spec §6.3, передаётся через config).

**Callback_data format** (TG лимит 64 байта):
```
silence:42:awg_handshake:4h    (~30 bytes)
ack:42:awg_handshake           (~22 bytes)
mute:42:awg_handshake          (~23 bytes)
history:42:awg_handshake       (~26 bytes)
```

Парсер: `strings.Split(data, ":")` + switch по `parts[0]`.

### 6.3 `internal/backend/realert/` — NEW

**`poller.go`:**

```go
type Poller struct {
    d   *db.DB
    tg  TGSender
    cfg Config
}

type Config struct {
    ChatID       int64
    RealertEvery time.Duration  // default 6h
    TickEvery    time.Duration  // default 5min
}

func (p *Poller) Run(ctx context.Context) error
//   ticker := time.NewTicker(p.cfg.TickEvery)
//   for {
//     select {
//       case <-ctx.Done(): return
//       case <-ticker.C: p.tick(ctx)
//     }
//   }

func (p *Poller) tick(ctx context.Context) {
    cutoff := time.Now().Add(-p.cfg.RealertEvery)
    stale, err := p.d.State().StaleHards(cutoff)
    if err != nil { log.Error; return }
    for _, sh := range stale {
        u, _ := p.d.Users().GetByID(sh.UserID)
        st, _ := p.d.State().Get(sh.UserID, sh.CheckName)
        count := int(time.Since(*st.HardSince) / p.cfg.RealertEvery)
        text := alerts.FormatRealert(u.Nickname, sh.CheckName, *st.HardSince, count)
        _, err := p.tg.SendMessage(ctx, p.cfg.ChatID, u.TelegramThreadID, text, "", nil)
        if err != nil { log.Error; continue }   // не апдейтим LastAlertAt → следующий тик попробует снова
        now := time.Now()
        st.LastAlertAt = &now
        // LastAlertMsgID НЕ меняем — он держит ссылку на оригинальный HARD для RECOVERY-reply.
        p.d.State().Save(sh.UserID, sh.CheckName, st)
    }
}
```

### 6.4 `alerts/` — модификации

**`dispatcher.go`:**

В `case state.Hard:` (строки 45-65 текущего файла) — заменить:
```go
mid, err := di.tg.SendMessage(ctx, di.cfg.ChatID, &threadID, text, "", nil)
```
на:
```go
kb := tg.HardAlertKeyboard(userID, checkName)
mid, err := di.tg.SendMessageWithKeyboard(ctx, di.cfg.ChatID, &threadID, text, "", nil, &kb)
```

В `case state.Recovery:` — добавить `next.Acked = false` перед Save (см. §5.1).

`TGSender` interface (строка 13) расширяется:
```go
type TGSender interface {
    SendMessage(...) (int64, error)
    SendMessageWithKeyboard(...) (int64, error)   // NEW
    CreateForumTopic(...) (int64, error)
}
```

**`format.go`:**

Новая функция:
```go
type RealertArgs struct {
    Nickname     string
    CheckName    string
    HardSince    time.Time
    RealertCount int
}
func FormatRealert(args RealertArgs) string
//   "🔁 [Vasya] AWG handshake — STILL DOWN
//    Hard since: 2026-04-28 09:03 МСК (8h ago)
//    Re-alert #2 (every 6h)"
```

### 6.5 `cmd/backend/main.go` — wiring

Три блока (плюс `go ...`):

```go
moscowLoc, err := time.LoadLocation("Europe/Moscow")
if err != nil { return fmt.Errorf("load Europe/Moscow: %w", err) }

cb := callbacks.New(d, tgClient, callbacks.Config{
    ChatID:         cfg.TG.ChatID,
    AdminUserID:    cfg.TG.AdminUserID,
    MuteCutoffHour: cfg.Alerts.MuteCutoffHour,
    MuteLocation:   moscowLoc,
})
go cb.Run(ctx)

rp := realert.NewPoller(d, tgClient, realert.Config{
    ChatID:       cfg.TG.ChatID,
    RealertEvery: time.Duration(cfg.Alerts.RealertEveryHours) * time.Hour,
    TickEvery:    time.Duration(cfg.Alerts.RealertTickMinutes) * time.Minute,
})
go rp.Run(ctx)
```

Defaults для config-полей применяются в `config.go::Load()` если YAML не задаёт значения: `RealertEveryHours=6`, `RealertTickMinutes=5`, `MuteCutoffHour=9`.

### 6.6 Config schema (backend.yaml)

```yaml
tg:
  chat_id: -1003651873378
  admin_user_id: 136513775     # NEW: для callback allowlist
  bot_token_file: /etc/wg-monitor/bot-token.txt
alerts:
  fail_threshold: 3
  recovery_threshold: 2
  mute_cutoff_hour: 9          # NEW
  realert_every_hours: 6       # NEW (override; default 6h)
  realert_tick_minutes: 5      # NEW (override; default 5min)
```

## 7. Data flow

### 7.1 Callback path

```
TG-юзер нажимает [⏸ 4ч]
   ↓
TG → backend (через getUpdates long-poll)
   ↓
callbacks.Router.handleUpdate(u):
   q := u.CallbackQuery
   if q.From.ID != AdminUserID → answerCallback("not authorized") → return
   parts := split(q.Data, ":")    // ["silence","42","awg_handshake","4h"]
   action := r.actions["silence"] // ParsedArgs{UserID:42, CheckName:"awg_handshake", TTL:4h}
   statusLine, err := action.Apply(ctx, q, args)
       db.State().Get(42, "awg_handshake")
       state.SilencedUntil = now + 4h
       db.State().Save(...)
   tg.AnswerCallbackQuery(q.ID, "")
   tg.EditMessageText(
       chatID = q.Message.Chat.ID,
       messageID = q.Message.MessageID,
       text = q.Message.Text + "\n\n" + statusLine,
       markup = &InlineKeyboardMarkup{},   // empty → удаляет кнопки
   )
```

### 7.2 Re-alert path

```
realert.Poller тик каждые 5 мин
   ↓
cutoff = now - 6h
stale = db.State().StaleHards(cutoff)
   ↓
для каждого sh:
   u = db.Users().GetByID(sh.UserID)
   st = db.State().Get(sh.UserID, sh.CheckName)
   count = floor((now - st.HardSince) / 6h)
   text = FormatRealert(u.Nickname, sh.CheckName, *st.HardSince, count)
   tg.SendMessage(chatID, threadID, text, "", nil)   // БЕЗ keyboard'а
   if ok:
     st.LastAlertAt = &now
     // LastAlertMsgID — НЕ меняется (держит ссылку на оригинальный HARD)
     db.State().Save(...)
```

## 8. Edge cases

| # | Сценарий | Поведение |
|---|---|---|
| **E1** | Кнопка нажата на recovered инцидент | action.Apply пишет в state — безвредно. Edit-message показывает «Silenced...» — visually awkward, но не опасно. **Принимаем.** |
| **E2** | Кнопка нажата дважды (race) | Первый клик убирает кнопки → второй callback не возникает. Если возникает (network race) — EditMessageText вернёт `Message is not modified` → swallow. |
| **E3** | Poller и FSM Recovery параллельно | SQLite WAL mode + write-row-keyed-by-PK. StaleHards SQL не вернёт уже recovered row. Race окно есть, но без повторных алертов. **Безопасно.** |
| **E4** | TG GetUpdates timeout/5xx | Exponential backoff `min(2^attempt, 60s)`, log warn, retry. Reset на success. |
| **E5** | EditMessageText 5xx после успешного DB write | Swallow + log error. Юзер видит spinner закрылся, но кнопки остались. Повторный клик идемпотентен. **Принимаем.** |
| **E6** | History — нет events | Сообщение `📋 История [Vasya] / awg_handshake — 24ч\n(нет событий за период)`. |
| **E7** | History — >30 transitions (≈4096 chars) | Truncate at 30 с пометкой `... (older entries truncated)`. |
| **E8** | Mute boundary (нажато в 8:55 МСК) | `nextCutoff` возвращает today 9:00 МСК = 5 мин mute. Edge-case, но не баг. **Принимаем.** Если выявится потребность — добавим min-window 1h в Stage 5. |

## 9. Error handling table

| Слой | Ошибка | Поведение |
|---|---|---|
| **GetUpdates loop** | TG API timeout/5xx | Exponential backoff, log warn |
| **GetUpdates loop** | TG API 4xx (bot kicked etc.) | Log error + return из горутины. Backend жив, callbacks deg'нуты. Stage 5 → systemd restart. |
| **GetUpdates loop** | Malformed JSON | Log error, drop batch, continue (offset не двигается → retry) |
| **Callback action** | DB write failure | AnswerCallbackQuery с error text, **не** edit'им (state неконсистентен) |
| **Callback action** | EditMessageText 4xx (`message can't be edited`, >48h) | Log warn, swallow. Action в БД применён — главное. |
| **Callback action** | EditMessageText 5xx | Log error, swallow (см. E5) |
| **Realert tick** | StaleHards SQL error | Log error, skip tick |
| **Realert tick** | tg.SendMessage error | Log error, **не** обновляем LastAlertAt → retry на следующем тике |
| **Realert tick** | Unknown user (orphan state row) | Log warn, не отправляем, не обновляем LastAlertAt. Решение для Stage 5 (data-migration). |
| **Logging** | Все error path'ы | Structured slog с `user_id, check_name, action, err` |

## 10. Testing strategy

TDD cycle для каждого нового пакета (паттерн Stage 1.5).

| Слой | Файлы тестов | Кол-во |
|---|---|---|
| `tg/` | `keyboard_test.go`, `client_test.go` (расширение), `updates_test.go` | ~10 |
| `callbacks/` | `router_test.go`, `actions_test.go` (in-mem SQLite + mock TG) | ~15 |
| `realert/` | `poller_test.go` (in-mem SQLite + mock TG) | ~6 |
| `db/` | `state_test.go` (расширение для acked), `kv_test.go` (новый) | ~5 |
| `state/fsm.go` | `fsm_test.go` (расширение для acked-reset на recovery) | ~3 |
| **Integration** | `cmd/backend/integration_test.go` (полный flow) | 1 |

**Integration test:** HARD → callback Silence(1h) parsed → state updated → message edited → 1h later silence expires → realert poller picks up → re-alert sent. Использует `httptest` для TG-mock'а, in-mem SQLite. ~80 строк.

**Live verification (финальная фаза плана):**
1. Build linux/arm64 → upload backend на VPS Main → restart `wg-monitor-backend.service`.
2. Спровоцировать HARD на testkeen (`down nwg0` → handshake age >180s × 3).
3. Получить TG-сообщение с inline-keyboard.
4. Нажать `[⏸ 1ч]` → проверить: SQL row обновлён (`silenced_until = now+1h`), сообщение в TG отредактировано (status-line + кнопок нет).
5. Восстановить awg → recovery message reply на оригинальный HARD msg_id.
6. Спровоцировать stale HARD (через `wg-monitor-cli` или авторизованный `UPDATE incident_state SET hard_since = datetime('now', '-7 hours') WHERE ...`) → дождаться 5-min tick → re-alert message без кнопок.

## 11. YAGNI (явно НЕ в Stage 2)

- Уровень 2 кнопок (`Diag`, `Test now`, `Restart AWG`) — Stage 3.
- ack_token / TTL / single-use semantics — Stage 3 (там destructive ops).
- Per-user mute (silence всех алертов юзера) — пока никто не просил.
- `[Unack]` кнопка — Ack снимается автоматически на recovery.
- `wg-monitor-cli silence/ack` — пока есть кнопки, и re-alert без кнопок переживём через UPDATE state SQL вручную.
- Web-UI / Prometheus / RBAC / multi-admin — parent spec §12.

## 12. Future tech-debt (явные `TODO`)

1. **Schema versioning** — текущий подход `CREATE TABLE IF NOT EXISTS` + ad-hoc helpers (`migrateAcked`) не масштабируется на >5 миграций. Stage 3+ должен ввести `schema_version` table с пронумерованными up/down скриптами.
2. **Orphan state rows** — на agent upgrade с переименованием check'ов остаются rows со stale check_name. Нужен `DELETE FROM incident_state WHERE check_name NOT IN (<current>)` в Stage 5 (data-migration step). См. parent spec memory `feedback_session_breaks` / Stage 1.5 closing notes.
3. **`-version` flag в агенте** — `Version` через ldflags вкачен, но `flag.Bool("version", ...)` в `cmd/agent/main.go` не зарегистрирован. Trivial, можно прихватить в Stage 2 plan'е как мини-задача.
4. **Re-alert без кнопок trade-off** — silence-after-expire требует ручного UPDATE в БД. Если станет частым — добавим `wg-monitor-cli silence` в Stage 5.

## 13. Open questions

Все resolved в brainstorming-сессии 2026-04-28. Если в ходе implementation выяснятся новые — фиксируем здесь.

## 14. Spec self-review (2026-04-28, post-write)

**Placeholder scan:** clean. Все TBD/TODO либо разрешены в §3, либо явно перечислены в §11 (YAGNI) и §12 (future tech-debt). Нет размытых требований.

**Internal consistency:**
- §3 ↔ §6: defaults в operational table (cutoff=9, realert=6h, tick=5min) совпадают с config-schema §6.6 и wiring §6.5.
- §5.1 ↔ §6.4: Acked-сброс на recovery упоминается в обоих местах, ссылается на одну и ту же точку (`alerts/dispatcher.go::case state.Recovery:`).
- §5.4 ↔ §6.3: `acked = 0` в SQL-фильтре StaleHards и логика «Ack глушит re-alert» в poller — идут вместе.
- §6.4 ↔ §6.3: `TGSender` interface расширяется, обоим клиентам (alerts.Dispatcher, realert.Poller) виден разный subset, реализация одна.

**Scope:** одна реализация-сессия (~0.5 как и в parent spec §14). Не требует декомпозиции.

**Ambiguity:**
- «`markup == nil`» vs «`markup == &{}`» в §6.1 — один параграф контракта, не двусмысленно.
- `nextCutoff()` boundary в 8:55 МСК — явно разрешено в §3 («следующее cutoff:00 строго в будущем», без min-window) и в E8.
- `History` действие — «не edit'ит оригинал, шлёт новое сообщение» — повторно явно сказано в §6.2 actions table и §10 testing strategy.

**Verdict:** spec ready for user review.
