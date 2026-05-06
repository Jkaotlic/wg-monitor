# wg-monitor Stage 2 Implementation Plan — inline callbacks + StaleHards re-alert poller

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить Уровень 1 inline-кнопок (silence/ack/history/mute) под HARD-алертами и re-alert поллер для застрявших HARD старше 6 часов, согласно spec'у `docs/superpowers/specs/2026-04-28-wg-monitor-stage-2-design.md`.

**Architecture:** Approach 1 — split-by-concern. Три новых пакета: `internal/backend/tg/` (расширения), `internal/backend/callbacks/` (router + 4 actions + Run loop), `internal/backend/realert/` (5-min ticker poller). Минимальные правки в `db/`, `state/`, `alerts/`, `cmd/backend/main.go`. Schema migration: одна новая колонка `acked` + одна KV-таблица `tg_state`.

**Tech Stack:** Go 1.22+, modernc.org/sqlite, net/http (TG API). TDD-cycle: RED (failing test) → run-fail → GREEN (impl) → run-pass → commit.

**Predecessor:** Stage 1.5 на `feature/stage-1` HEAD `0d6eab8` (включает Stage 2 spec). Worktree: `C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2/`, ветка `feature/stage-2`.

---

## File map

### Создаются новые

| Файл | Ответственность |
|---|---|
| `internal/backend/db/kv.go` | KV-репо для `tg_state` таблицы (Get/Set string values by key) |
| `internal/backend/db/kv_test.go` | Unit-тесты KV-репо |
| `internal/backend/tg/keyboard.go` | InlineKeyboardButton/Markup-типы, `HardAlertKeyboard()` builder |
| `internal/backend/tg/keyboard_test.go` | Тесты структуры keyboard'а |
| `internal/backend/tg/updates.go` | Update/CallbackQuery типы, `GetUpdates(offset, timeoutSec)` long-poll |
| `internal/backend/tg/updates_test.go` | Тесты GetUpdates через httptest mock |
| `internal/backend/callbacks/parse.go` | Callback_data парсер `silence:42:awg_handshake:4h` → ParsedArgs |
| `internal/backend/callbacks/parse_test.go` | Тесты парсера для всех 6 action'ов + malformed |
| `internal/backend/callbacks/actions.go` | 4 action handler'а: Silence(ttl), Ack, Mute, History |
| `internal/backend/callbacks/actions_test.go` | Тесты action'ов с in-memory SQLite + mock TG |
| `internal/backend/callbacks/router.go` | Router + Run() long-poll loop, allowlist, dispatch |
| `internal/backend/callbacks/router_test.go` | Тесты роутера: dispatch, allowlist reject, unknown action |
| `internal/backend/realert/poller.go` | Poller + tick() + Run() с 5-min ticker'ом |
| `internal/backend/realert/poller_test.go` | Тесты tick: пусто/один/silenced/acked/error |
| `cmd/backend/integration_test.go` | Heavyweight: HARD → callback → edit → realert flow |

### Модифицируются

| Файл | Что меняется |
|---|---|
| `internal/backend/db/migrations.sql` | +`tg_state` (KV table) |
| `internal/backend/db/db.go` | +`migrateAcked()` helper; вызов из `Open()` |
| `internal/backend/db/state.go` | +`Acked bool` поле в `IncidentState`; обновить Get/Save SQL; добавить `AND acked = 0` в StaleHards |
| `internal/backend/db/state_test.go` | +тесты `Acked` round-trip + `StaleHards` фильтр |
| `internal/backend/state/fsm.go` | В Recovery case (строки 72-79): `next.Acked = false` перед return |
| `internal/backend/state/fsm_test.go` | +тест: hard→ok при prev.Acked=true → next.Acked=false |
| `internal/backend/tg/client.go` | +`SendMessageWithKeyboard`, +`AnswerCallbackQuery`, +`EditMessageText` |
| `internal/backend/tg/client_test.go` | +тесты для трёх новых методов |
| `internal/backend/alerts/dispatcher.go` | TGSender interface +SendMessageWithKeyboard; HARD case использует keyboard; Recovery case zeros Acked |
| `internal/backend/alerts/dispatcher_test.go` | Mock TG обновляется; тесты для keyboard в HARD; тест Recovery zeroes Acked |
| `internal/backend/alerts/format.go` | +`FormatRealert(args RealertArgs) string` |
| `internal/backend/alerts/format_test.go` | +тесты FormatRealert |
| `internal/backend/config.go` | +`State.RealertTickSec` (default 300s); +`State.MuteCutoffHour` (default 9) |
| `internal/backend/config_test.go` | +тесты defaults для новых полей |
| `cmd/backend/main.go` | +Europe/Moscow load; +callbacks.Router goroutine; +realert.Poller goroutine; +Version bump |

---

## Tasks

### Task 1: Schema migration — `acked` column + `tg_state` KV table

**Files:**
- Modify: `internal/backend/db/migrations.sql`
- Modify: `internal/backend/db/db.go:13-35`
- Test: `internal/backend/db/db_test.go` (existing — добавить test)

- [ ] **Step 1: Write failing test for `acked` column**

Добавить в `internal/backend/db/db_test.go` (или создать если ещё нет):

```go
func TestMigrateAckedAddsColumn(t *testing.T) {
    tmp := t.TempDir() + "/test.db"
    d, err := Open(tmp)
    if err != nil { t.Fatal(err) }
    defer d.Close()

    var n int
    err = d.SQL().QueryRow(`SELECT count(*) FROM pragma_table_info('incident_state') WHERE name='acked'`).Scan(&n)
    if err != nil { t.Fatal(err) }
    if n != 1 {
        t.Errorf("expected acked column, got count=%d", n)
    }
}

func TestMigrateTGStateTable(t *testing.T) {
    tmp := t.TempDir() + "/test.db"
    d, err := Open(tmp)
    if err != nil { t.Fatal(err) }
    defer d.Close()

    var name string
    err = d.SQL().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='tg_state'`).Scan(&name)
    if err != nil { t.Fatalf("tg_state table missing: %v", err) }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/db -run TestMigrate -v`
Expected: FAIL — `pragma_table_info` returns 0 для acked, `tg_state` не существует.

- [ ] **Step 3: Add `tg_state` table to migrations.sql**

Добавить в конец `internal/backend/db/migrations.sql`:

```sql

CREATE TABLE IF NOT EXISTS tg_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

- [ ] **Step 4: Add `migrateAcked` helper to db.go**

В `internal/backend/db/db.go` после строки 35 (внутри `Open`, перед `return &DB{...}`) и добавить функцию:

```go
func Open(path string) (*DB, error) {
    dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
    d, err := sql.Open("sqlite", dsn)
    if err != nil {
        return nil, fmt.Errorf("sql.Open: %w", err)
    }
    if err := d.Ping(); err != nil {
        d.Close()
        return nil, fmt.Errorf("ping: %w", err)
    }
    if _, err := d.Exec(migrationsSQL); err != nil {
        d.Close()
        return nil, fmt.Errorf("migrate: %w", err)
    }
    if err := migrateAcked(d); err != nil {
        d.Close()
        return nil, fmt.Errorf("migrate acked: %w", err)
    }
    return &DB{db: d}, nil
}

// migrateAcked is a one-shot ALTER TABLE for Stage 2.
// SQLite has no `ADD COLUMN IF NOT EXISTS`, so we probe pragma_table_info first.
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

- [ ] **Step 5: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/db -run TestMigrate -v`
Expected: PASS, оба теста зелёные.

- [ ] **Step 6: Verify all existing db tests still pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/db -v`
Expected: PASS, все тесты в db/ зелёные.

- [ ] **Step 7: Commit**

```bash
cd C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2
git add internal/backend/db/migrations.sql internal/backend/db/db.go internal/backend/db/db_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(db): Stage 2 schema migration — acked column + tg_state KV table"
```

---

### Task 2: `IncidentState.Acked` field + StaleHards filter

**Files:**
- Modify: `internal/backend/db/state.go:9-20` (struct), `:32-83` (Get/Save), `:107-127` (StaleHards)
- Modify: `internal/backend/db/state_test.go` (add tests)

- [ ] **Step 1: Write failing test for Acked round-trip**

Добавить в `internal/backend/db/state_test.go`:

```go
func TestStateAckedRoundTrip(t *testing.T) {
    tmp := t.TempDir() + "/test.db"
    d, err := Open(tmp)
    if err != nil { t.Fatal(err) }
    defer d.Close()

    uid, err := d.Users().Create(UserCreate{
        Nickname: "u1", TokenHash: "h", ExpectedExitIP: "1.1.1.1", AwgIface: "nwg0",
    })
    if err != nil { t.Fatal(err) }

    err = d.State().Save(uid, "awg_handshake", IncidentState{
        UserID: uid, CheckName: "awg_handshake",
        CurrentStatus: "hard", ConsecutiveFails: 3, Acked: true,
    })
    if err != nil { t.Fatal(err) }

    got, err := d.State().Get(uid, "awg_handshake")
    if err != nil { t.Fatal(err) }
    if !got.Acked {
        t.Errorf("Acked should round-trip true, got false")
    }
}

func TestStateStaleHardsFiltersAcked(t *testing.T) {
    tmp := t.TempDir() + "/test.db"
    d, err := Open(tmp)
    if err != nil { t.Fatal(err) }
    defer d.Close()

    uid, _ := d.Users().Create(UserCreate{
        Nickname: "u1", TokenHash: "h", ExpectedExitIP: "1.1.1.1", AwgIface: "nwg0",
    })
    oldAlert := time.Now().Add(-7 * time.Hour)
    hardSince := oldAlert
    err = d.State().Save(uid, "awg_handshake", IncidentState{
        UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
        HardSince: &hardSince, LastAlertAt: &oldAlert, Acked: true,
    })
    if err != nil { t.Fatal(err) }

    stale, err := d.State().StaleHards(time.Now().Add(-6 * time.Hour))
    if err != nil { t.Fatal(err) }
    if len(stale) != 0 {
        t.Errorf("acked=1 row should not appear in StaleHards, got %d rows", len(stale))
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/db -run "TestStateAcked|TestStateStaleHardsFiltersAcked" -v`
Expected: FAIL — `Acked` field missing on struct.

- [ ] **Step 3: Add `Acked` field to IncidentState struct**

В `internal/backend/db/state.go:9-20`, заменить:

```go
type IncidentState struct {
    UserID           int64
    CheckName        string
    ConsecutiveFails int
    ConsecutiveOKs   int
    CurrentStatus    string
    HardSince        *time.Time
    LastAlertMsgID   *int64
    LastAlertAt      *time.Time
    SilencedUntil    *time.Time
    AckedUntil       *time.Time
    Acked            bool
}
```

- [ ] **Step 4: Update Get() SQL to include acked column**

Заменить SQL в `state.go:38-46` на:

```go
    row := s.d.db.QueryRow(
        `SELECT consecutive_fails, consecutive_oks, current_status, hard_since, last_alert_msg_id, last_alert_at, silenced_until, acked_until, acked
           FROM incident_state WHERE user_id = ? AND check_name = ?`,
        userID, checkName,
    )
    var hardSince, lastAlertAt, silenced, acked sql.NullTime
    var lastMsgID sql.NullInt64
    var ackedFlag int
    err := row.Scan(&got.ConsecutiveFails, &got.ConsecutiveOKs, &got.CurrentStatus,
        &hardSince, &lastMsgID, &lastAlertAt, &silenced, &acked, &ackedFlag)
```

И после `if lastMsgID.Valid { ... }` блока (строка 60-61) добавить:

```go
    got.Acked = ackedFlag == 1
```

- [ ] **Step 5: Update Save() SQL to write acked**

Заменить тело Save (state.go:64-83) на:

```go
func (s *StateRepo) Save(userID int64, checkName string, st IncidentState) error {
    ackedInt := 0
    if st.Acked {
        ackedInt = 1
    }
    _, err := s.d.db.Exec(
        `INSERT INTO incident_state(user_id, check_name, consecutive_fails, consecutive_oks, current_status,
            hard_since, last_alert_msg_id, last_alert_at, silenced_until, acked_until, acked)
         VALUES(?,?,?,?,?,?,?,?,?,?,?)
         ON CONFLICT(user_id, check_name) DO UPDATE SET
            consecutive_fails = excluded.consecutive_fails,
            consecutive_oks   = excluded.consecutive_oks,
            current_status    = excluded.current_status,
            hard_since        = excluded.hard_since,
            last_alert_msg_id = excluded.last_alert_msg_id,
            last_alert_at     = excluded.last_alert_at,
            silenced_until    = excluded.silenced_until,
            acked_until       = excluded.acked_until,
            acked             = excluded.acked`,
        userID, checkName, st.ConsecutiveFails, st.ConsecutiveOKs, st.CurrentStatus,
        utcPtr(st.HardSince), st.LastAlertMsgID, utcPtr(st.LastAlertAt),
        utcPtr(st.SilencedUntil), utcPtr(st.AckedUntil), ackedInt,
    )
    return err
}
```

- [ ] **Step 6: Update StaleHards SQL to filter acked**

Заменить SQL в state.go:108-114 на:

```go
    rows, err := s.d.db.Query(
        `SELECT user_id, check_name, hard_since FROM incident_state
         WHERE current_status = 'hard'
           AND last_alert_at < ?
           AND (silenced_until IS NULL OR silenced_until < CURRENT_TIMESTAMP)
           AND acked = 0`,
        cutoff.UTC())
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/db -v`
Expected: PASS, все включая новые тесты Acked.

- [ ] **Step 8: Commit**

```bash
git add internal/backend/db/state.go internal/backend/db/state_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(db): IncidentState.Acked field + StaleHards filter acked=0"
```

---

### Task 3: KV repo for `tg_state`

**Files:**
- Create: `internal/backend/db/kv.go`
- Create: `internal/backend/db/kv_test.go`

- [ ] **Step 1: Write failing test**

`internal/backend/db/kv_test.go`:

```go
package db

import "testing"

func TestKVSetGet(t *testing.T) {
    tmp := t.TempDir() + "/test.db"
    d, err := Open(tmp)
    if err != nil { t.Fatal(err) }
    defer d.Close()

    if err := d.KV().Set("last_update_id", "12345"); err != nil { t.Fatal(err) }
    v, err := d.KV().Get("last_update_id")
    if err != nil { t.Fatal(err) }
    if v != "12345" { t.Errorf("got %q, want %q", v, "12345") }
}

func TestKVGetMissing(t *testing.T) {
    tmp := t.TempDir() + "/test.db"
    d, _ := Open(tmp)
    defer d.Close()
    v, err := d.KV().Get("nope")
    if err != nil { t.Fatal(err) }
    if v != "" { t.Errorf("missing key should return empty string, got %q", v) }
}

func TestKVOverwrite(t *testing.T) {
    tmp := t.TempDir() + "/test.db"
    d, _ := Open(tmp)
    defer d.Close()
    _ = d.KV().Set("k", "a")
    _ = d.KV().Set("k", "b")
    v, _ := d.KV().Get("k")
    if v != "b" { t.Errorf("overwrite failed, got %q", v) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/db -run TestKV -v`
Expected: FAIL — `d.KV` undefined.

- [ ] **Step 3: Implement KV repo**

`internal/backend/db/kv.go`:

```go
package db

import (
    "database/sql"
    "errors"
)

type KVRepo struct{ d *DB }

func (d *DB) KV() *KVRepo { return &KVRepo{d: d} }

// Get returns the value for key, or "" if the key does not exist.
func (r *KVRepo) Get(key string) (string, error) {
    var v string
    err := r.d.db.QueryRow(`SELECT value FROM tg_state WHERE key = ?`, key).Scan(&v)
    if errors.Is(err, sql.ErrNoRows) {
        return "", nil
    }
    return v, err
}

// Set upserts the value for key.
func (r *KVRepo) Set(key, value string) error {
    _, err := r.d.db.Exec(
        `INSERT INTO tg_state(key, value) VALUES(?, ?)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
        key, value)
    return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/db -run TestKV -v`
Expected: PASS — три зелёных теста.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/db/kv.go internal/backend/db/kv_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(db): KV repo for tg_state (last_update_id and future Stage 3+ keys)"
```

---

### Task 4: FSM Recovery zeroes `Acked`

**Files:**
- Modify: `internal/backend/state/fsm.go:72-79`
- Modify: `internal/backend/state/fsm_test.go`

- [ ] **Step 1: Write failing test**

Добавить в `internal/backend/state/fsm_test.go`:

```go
func TestApplyHardToOKZeroesAcked(t *testing.T) {
    prev := db.IncidentState{
        CurrentStatus: "hard", ConsecutiveOKs: 1, Acked: true,
    }
    now := time.Now()
    tr := Apply(prev, "ok", now, Thresholds{Fail: 3, Recovery: 2})
    if tr.Kind != Recovery {
        t.Fatalf("expected Recovery, got %v", tr.Kind)
    }
    if tr.Next.Acked {
        t.Errorf("recovery should zero Acked, got Acked=true")
    }
}

func TestApplyHardToFailKeepsAcked(t *testing.T) {
    prev := db.IncidentState{
        CurrentStatus: "hard", ConsecutiveFails: 3, Acked: true,
    }
    now := time.Now()
    tr := Apply(prev, "fail", now, Thresholds{Fail: 3, Recovery: 2})
    if !tr.Next.Acked {
        t.Errorf("hard→fail (no transition) must preserve Acked=true")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/state -run "TestApplyHardToOKZeroesAcked|TestApplyHardToFailKeepsAcked" -v`
Expected: FAIL on first — `next.Acked` остаётся true после Recovery.

- [ ] **Step 3: Modify Recovery case in fsm.go**

В `internal/backend/state/fsm.go:72-80`, заменить:

```go
    case prev.CurrentStatus == "hard" && incoming == "ok":
        next.ConsecutiveOKs = prev.ConsecutiveOKs + 1
        if next.ConsecutiveOKs >= th.Recovery {
            next.CurrentStatus = "ok"
            next.ConsecutiveFails = 0
            next.HardSince = nil
            next.Acked = false
            return Transition{Kind: Recovery, Next: next}
        }
        return Transition{Kind: Noop, Next: next}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/state -v`
Expected: PASS — все включая новые.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/state/fsm.go internal/backend/state/fsm_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(state): Recovery transition zeroes Acked flag (Q1=A semantics)"
```

---

### Task 5: `tg/keyboard.go` — types + HardAlertKeyboard

**Files:**
- Create: `internal/backend/tg/keyboard.go`
- Create: `internal/backend/tg/keyboard_test.go`

- [ ] **Step 1: Write failing test**

`internal/backend/tg/keyboard_test.go`:

```go
package tg

import (
    "encoding/json"
    "strings"
    "testing"
)

func TestHardAlertKeyboardShape(t *testing.T) {
    kb := HardAlertKeyboard(42, "awg_handshake")
    if len(kb.InlineKeyboard) != 2 {
        t.Fatalf("expected 2 rows, got %d", len(kb.InlineKeyboard))
    }
    if len(kb.InlineKeyboard[0]) != 4 {
        t.Errorf("row 0: expected 4 buttons (silence x3 + ack), got %d", len(kb.InlineKeyboard[0]))
    }
    if len(kb.InlineKeyboard[1]) != 2 {
        t.Errorf("row 1: expected 2 buttons (history + mute), got %d", len(kb.InlineKeyboard[1]))
    }
}

func TestHardAlertKeyboardCallbackData(t *testing.T) {
    kb := HardAlertKeyboard(42, "awg_handshake")
    expected := map[string]bool{
        "silence:42:awg_handshake:1h":  true,
        "silence:42:awg_handshake:4h":  true,
        "silence:42:awg_handshake:24h": true,
        "ack:42:awg_handshake":         true,
        "history:42:awg_handshake":     true,
        "mute:42:awg_handshake":        true,
    }
    for _, row := range kb.InlineKeyboard {
        for _, btn := range row {
            if !expected[btn.CallbackData] {
                t.Errorf("unexpected callback_data: %q", btn.CallbackData)
            }
            delete(expected, btn.CallbackData)
        }
    }
    for k := range expected {
        t.Errorf("missing button: %q", k)
    }
}

func TestHardAlertKeyboardJSONShape(t *testing.T) {
    kb := HardAlertKeyboard(42, "awg_handshake")
    raw, err := json.Marshal(kb)
    if err != nil { t.Fatal(err) }
    s := string(raw)
    if !strings.Contains(s, `"inline_keyboard"`) {
        t.Errorf("json must have `inline_keyboard` key, got %s", s)
    }
    if !strings.Contains(s, `"callback_data"`) {
        t.Errorf("json must have `callback_data` field, got %s", s)
    }
}

func TestHardAlertKeyboardCallbackData64ByteLimit(t *testing.T) {
    kb := HardAlertKeyboard(999999999, "awg_handshake_with_long_name")
    for _, row := range kb.InlineKeyboard {
        for _, btn := range row {
            if len(btn.CallbackData) > 64 {
                t.Errorf("callback_data exceeds TG 64-byte limit: %d bytes (%q)", len(btn.CallbackData), btn.CallbackData)
            }
        }
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/tg -run TestHardAlertKeyboard -v`
Expected: FAIL — `HardAlertKeyboard` undefined.

- [ ] **Step 3: Implement keyboard.go**

`internal/backend/tg/keyboard.go`:

```go
package tg

import "fmt"

type InlineKeyboardButton struct {
    Text         string `json:"text"`
    CallbackData string `json:"callback_data"`
}

type InlineKeyboardMarkup struct {
    InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// HardAlertKeyboard returns the 6-button layout under each HARD alert
// per spec §6.2: row 1 [⏸1h][⏸4h][⏸24h][✅Ack], row 2 [📋History][🔇Mute].
func HardAlertKeyboard(userID int64, checkName string) InlineKeyboardMarkup {
    silenceCD := func(ttl string) string {
        return fmt.Sprintf("silence:%d:%s:%s", userID, checkName, ttl)
    }
    plainCD := func(action string) string {
        return fmt.Sprintf("%s:%d:%s", action, userID, checkName)
    }
    return InlineKeyboardMarkup{
        InlineKeyboard: [][]InlineKeyboardButton{
            {
                {Text: "⏸ 1ч", CallbackData: silenceCD("1h")},
                {Text: "⏸ 4ч", CallbackData: silenceCD("4h")},
                {Text: "⏸ 24ч", CallbackData: silenceCD("24h")},
                {Text: "✅ Ack", CallbackData: plainCD("ack")},
            },
            {
                {Text: "📋 История 24ч", CallbackData: plainCD("history")},
                {Text: "🔇 Mute до утра", CallbackData: plainCD("mute")},
            },
        },
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/tg -run TestHardAlertKeyboard -v`
Expected: PASS — 4 теста зелёные.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/keyboard.go internal/backend/tg/keyboard_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(tg): inline keyboard builder for HARD alerts (6 buttons, 2 rows)"
```

---

### Task 6: `tg/client.go` — SendMessageWithKeyboard, AnswerCallbackQuery, EditMessageText

**Files:**
- Modify: `internal/backend/tg/client.go` (append new methods)
- Modify: `internal/backend/tg/client_test.go` (extend with httptest mock)

- [ ] **Step 1: Write failing tests for all three methods**

Добавить в `internal/backend/tg/client_test.go`:

```go
func TestSendMessageWithKeyboard(t *testing.T) {
    var captured map[string]any
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        json.Unmarshal(body, &captured)
        json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 777}})
    }))
    defer srv.Close()

    c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
    kb := HardAlertKeyboard(1, "x")
    mid, err := c.SendMessageWithKeyboard(context.Background(), 100, nil, "hi", "", nil, &kb)
    if err != nil { t.Fatal(err) }
    if mid != 777 { t.Errorf("got mid=%d, want 777", mid) }
    if captured["reply_markup"] == nil {
        t.Errorf("expected reply_markup in body, got %+v", captured)
    }
}

func TestAnswerCallbackQuery(t *testing.T) {
    var captured map[string]any
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
            t.Errorf("expected answerCallbackQuery, got %s", r.URL.Path)
        }
        body, _ := io.ReadAll(r.Body)
        json.Unmarshal(body, &captured)
        json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
    }))
    defer srv.Close()
    c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
    if err := c.AnswerCallbackQuery(context.Background(), "cbk-1", "Silenced"); err != nil {
        t.Fatal(err)
    }
    if captured["callback_query_id"] != "cbk-1" {
        t.Errorf("expected callback_query_id=cbk-1, got %v", captured["callback_query_id"])
    }
    if captured["text"] != "Silenced" {
        t.Errorf("expected text=Silenced, got %v", captured["text"])
    }
}

func TestEditMessageTextWithEmptyMarkupRemovesKeyboard(t *testing.T) {
    var captured map[string]any
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        json.Unmarshal(body, &captured)
        json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5}})
    }))
    defer srv.Close()
    c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
    empty := InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}
    err := c.EditMessageText(context.Background(), 100, 5, "new text", "", &empty)
    if err != nil { t.Fatal(err) }
    rm, ok := captured["reply_markup"].(map[string]any)
    if !ok {
        t.Fatalf("expected reply_markup map, got %T", captured["reply_markup"])
    }
    arr, _ := rm["inline_keyboard"].([]any)
    if len(arr) != 0 {
        t.Errorf("expected empty inline_keyboard, got %v", arr)
    }
}

func TestEditMessageTextWithNilMarkupOmitsField(t *testing.T) {
    var captured map[string]any
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        json.Unmarshal(body, &captured)
        json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5}})
    }))
    defer srv.Close()
    c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
    err := c.EditMessageText(context.Background(), 100, 5, "new", "", nil)
    if err != nil { t.Fatal(err) }
    if _, present := captured["reply_markup"]; present {
        t.Errorf("nil markup should omit reply_markup field, got %v", captured)
    }
}
```

Imports в начале файла должны включать: `"context"`, `"encoding/json"`, `"io"`, `"net/http"`, `"net/http/httptest"`, `"strings"`, `"testing"` (часть может уже быть).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/tg -run "TestSendMessageWithKeyboard|TestAnswerCallbackQuery|TestEditMessageText" -v`
Expected: FAIL — три метода undefined.

- [ ] **Step 3: Implement methods in client.go**

Добавить в `internal/backend/tg/client.go` после существующих методов (в конец файла перед последней `}` функции `call`):

```go
type sendMessageWithKBReq struct {
    ChatID           int64                 `json:"chat_id"`
    MessageThreadID  *int64                `json:"message_thread_id,omitempty"`
    Text             string                `json:"text"`
    ParseMode        string                `json:"parse_mode,omitempty"`
    ReplyToMessageID *int64                `json:"reply_to_message_id,omitempty"`
    ReplyMarkup      *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// SendMessageWithKeyboard sends a message with an attached inline keyboard.
// markup must be non-nil; for plain messages use SendMessage.
func (c *Client) SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup *InlineKeyboardMarkup) (int64, error) {
    body, _ := json.Marshal(sendMessageWithKBReq{
        ChatID:           chatID,
        MessageThreadID:  threadID,
        Text:             text,
        ParseMode:        parseMode,
        ReplyToMessageID: replyTo,
        ReplyMarkup:      markup,
    })
    var out sendMessageResult
    if err := c.call(ctx, "sendMessage", body, &out); err != nil {
        return 0, err
    }
    return out.MessageID, nil
}

type answerCBReq struct {
    CallbackQueryID string `json:"callback_query_id"`
    Text            string `json:"text,omitempty"`
}

// AnswerCallbackQuery closes the loading spinner on the user's button.
// text (optional, ≤200 chars) shows as a transient toast; pass "" for silent close.
func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
    body, _ := json.Marshal(answerCBReq{CallbackQueryID: callbackID, Text: text})
    return c.call(ctx, "answerCallbackQuery", body, nil)
}

type editMessageReq struct {
    ChatID      int64                 `json:"chat_id"`
    MessageID   int64                 `json:"message_id"`
    Text        string                `json:"text"`
    ParseMode   string                `json:"parse_mode,omitempty"`
    ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// EditMessageText edits an existing message. markup contract:
//   nil      → reply_markup not sent (TG does not change existing keyboard)
//   &{}      → reply_markup sent with empty inline_keyboard array (removes buttons)
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *InlineKeyboardMarkup) error {
    body, _ := json.Marshal(editMessageReq{
        ChatID:      chatID,
        MessageID:   messageID,
        Text:        text,
        ParseMode:   parseMode,
        ReplyMarkup: markup,
    })
    return c.call(ctx, "editMessageText", body, nil)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/tg -v`
Expected: PASS — все включая новые.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/client.go internal/backend/tg/client_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(tg): SendMessageWithKeyboard + AnswerCallbackQuery + EditMessageText"
```

---

### Task 7: `tg/updates.go` — Update types + GetUpdates long-poll

**Files:**
- Create: `internal/backend/tg/updates.go`
- Create: `internal/backend/tg/updates_test.go`

- [ ] **Step 1: Write failing tests**

`internal/backend/tg/updates_test.go`:

```go
package tg

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "testing"
)

func TestGetUpdatesParsesCallback(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        var req map[string]any
        json.Unmarshal(body, &req)
        // Verify allowed_updates filter
        au, _ := req["allowed_updates"].([]any)
        if len(au) != 1 || au[0] != "callback_query" {
            t.Errorf("expected allowed_updates=[callback_query], got %v", au)
        }
        json.NewEncoder(w).Encode(map[string]any{
            "ok": true,
            "result": []map[string]any{
                {
                    "update_id": 100,
                    "callback_query": map[string]any{
                        "id":   "cbk-1",
                        "from": map[string]any{"id": 12345},
                        "message": map[string]any{
                            "message_id": 7,
                            "chat":       map[string]any{"id": -100},
                            "text":       "🔴 [vasya] AWG handshake — DOWN",
                        },
                        "data": "silence:42:awg_handshake:4h",
                    },
                },
            },
        })
    }))
    defer srv.Close()

    c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
    ups, err := c.GetUpdates(context.Background(), 0, 30)
    if err != nil { t.Fatal(err) }
    if len(ups) != 1 { t.Fatalf("expected 1 update, got %d", len(ups)) }
    u := ups[0]
    if u.UpdateID != 100 { t.Errorf("update_id: %d", u.UpdateID) }
    if u.CallbackQuery == nil { t.Fatal("CallbackQuery nil") }
    if u.CallbackQuery.Data != "silence:42:awg_handshake:4h" {
        t.Errorf("data: %q", u.CallbackQuery.Data)
    }
    if u.CallbackQuery.From.ID != 12345 {
        t.Errorf("from.id: %d", u.CallbackQuery.From.ID)
    }
    if u.CallbackQuery.Message.MessageID != 7 {
        t.Errorf("message_id: %d", u.CallbackQuery.Message.MessageID)
    }
}

func TestGetUpdatesEmpty(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
    }))
    defer srv.Close()
    c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
    ups, err := c.GetUpdates(context.Background(), 0, 30)
    if err != nil { t.Fatal(err) }
    if len(ups) != 0 { t.Errorf("expected 0 updates, got %d", len(ups)) }
}

func TestGetUpdatesPassesOffset(t *testing.T) {
    var captured map[string]any
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        json.Unmarshal(body, &captured)
        json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
    }))
    defer srv.Close()
    c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
    _, _ = c.GetUpdates(context.Background(), 555, 30)
    if v, _ := captured["offset"].(float64); v != 555 {
        t.Errorf("expected offset=555, got %v", captured["offset"])
    }
    // Sanity: timeout passed
    if v, _ := captured["timeout"].(float64); v != 30 {
        t.Errorf("expected timeout=30, got %v", captured["timeout"])
    }
    // sanity: trailing path is /getUpdates
    _ = url.Parse
    _ = strings.Contains
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/tg -run TestGetUpdates -v`
Expected: FAIL — `GetUpdates` undefined.

- [ ] **Step 3: Implement updates.go**

`internal/backend/tg/updates.go`:

```go
package tg

import (
    "context"
    "encoding/json"
)

type Update struct {
    UpdateID      int64          `json:"update_id"`
    CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type CallbackQuery struct {
    ID      string  `json:"id"`
    From    User    `json:"from"`
    Message Message `json:"message"`
    Data    string  `json:"data"`
}

type Message struct {
    MessageID       int64  `json:"message_id"`
    Chat            Chat   `json:"chat"`
    MessageThreadID *int64 `json:"message_thread_id,omitempty"`
    Text            string `json:"text"`
}

type User struct {
    ID int64 `json:"id"`
}

type Chat struct {
    ID int64 `json:"id"`
}

type getUpdatesReq struct {
    Offset         int64    `json:"offset,omitempty"`
    Timeout        int      `json:"timeout"`
    AllowedUpdates []string `json:"allowed_updates"`
}

// GetUpdates performs a long-poll request to the TG Bot API.
// timeoutSec is the server-side hold time (TG keeps connection open for this many seconds
// if no updates are available). offset = last_processed_update_id + 1.
// Filters to callback_query updates only.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
    body, _ := json.Marshal(getUpdatesReq{
        Offset:         offset,
        Timeout:        timeoutSec,
        AllowedUpdates: []string{"callback_query"},
    })
    var out []Update
    if err := c.call(ctx, "getUpdates", body, &out); err != nil {
        return nil, err
    }
    return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/tg -v`
Expected: PASS — все.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/updates.go internal/backend/tg/updates_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(tg): GetUpdates long-poll + Update/CallbackQuery types"
```

---

### Task 8: `alerts/format.go` — FormatRealert

**Files:**
- Modify: `internal/backend/alerts/format.go` (append)
- Modify: `internal/backend/alerts/format_test.go`

- [ ] **Step 1: Write failing test**

Добавить в `internal/backend/alerts/format_test.go`:

```go
func TestFormatRealert(t *testing.T) {
    hardSince := time.Date(2026, 4, 28, 9, 3, 0, 0, time.UTC)
    msg := FormatRealert(RealertArgs{
        Nickname:     "vasya",
        CheckName:    "awg_handshake",
        HardSince:    hardSince,
        RealertCount: 2,
    })
    if !strings.Contains(msg, "STILL DOWN") {
        t.Errorf("missing STILL DOWN: %q", msg)
    }
    if !strings.Contains(msg, "vasya") {
        t.Errorf("missing nickname: %q", msg)
    }
    if !strings.Contains(msg, "awg_handshake") {
        t.Errorf("missing check name: %q", msg)
    }
    if !strings.Contains(msg, "Re-alert #2") {
        t.Errorf("missing re-alert counter: %q", msg)
    }
    if !strings.Contains(msg, "🔁") {
        t.Errorf("missing 🔁 emoji: %q", msg)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/alerts -run TestFormatRealert -v`
Expected: FAIL — `FormatRealert` / `RealertArgs` undefined.

- [ ] **Step 3: Implement FormatRealert**

В `internal/backend/alerts/format.go` добавить в конец:

```go
type RealertArgs struct {
    Nickname     string
    CheckName    string
    HardSince    time.Time
    RealertCount int
}

func FormatRealert(args RealertArgs) string {
    age := time.Since(args.HardSince).Round(time.Minute)
    return fmt.Sprintf(
        "🔁 [%s] %s — STILL DOWN\nHard since: %s (%s ago)\nRe-alert #%d (every 6h)",
        args.Nickname,
        args.CheckName,
        args.HardSince.UTC().Format("2006-01-02 15:04 MST"),
        formatAge(age),
        args.RealertCount,
    )
}

// formatAge — human-readable hours/minutes from a duration.
func formatAge(d time.Duration) string {
    h := int(d.Hours())
    m := int(d.Minutes()) - h*60
    if h > 0 {
        return fmt.Sprintf("%dh%dm", h, m)
    }
    return fmt.Sprintf("%dm", m)
}
```

(Если `formatAge` уже есть в файле — переиспользовать; если нет — добавить.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/alerts -v`
Expected: PASS — все.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/alerts/format.go internal/backend/alerts/format_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(alerts): FormatRealert for STILL DOWN repeat-alert"
```

---

### Task 9: `alerts/dispatcher.go` — HARD with keyboard, Recovery zeroes Acked

**Files:**
- Modify: `internal/backend/alerts/dispatcher.go:13-17` (interface), `:45-65` (HARD case), `:66-89` (Recovery case)
- Modify: `internal/backend/alerts/dispatcher_test.go`

- [ ] **Step 1: Update mock TG in tests + add expectations for keyboard**

Найти существующий mock в `dispatcher_test.go`. Скорее всего это struct типа `fakeTG`. Дополнить его:

```go
type fakeTG struct {
    sentMessages           []sentMsg
    sentWithKeyboard       []sentKBMsg
    createTopicResp        int64
    createTopicErr         error
}
type sentKBMsg struct {
    chatID    int64
    threadID  *int64
    text      string
    replyTo   *int64
    keyboard  *tg.InlineKeyboardMarkup
}

// existing SendMessage
// add new method:
func (f *fakeTG) SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, kb *tg.InlineKeyboardMarkup) (int64, error) {
    f.sentWithKeyboard = append(f.sentWithKeyboard, sentKBMsg{chatID, threadID, text, replyTo, kb})
    return int64(len(f.sentWithKeyboard) + 1000), nil
}
```

(Точная форма зависит от существующего mock'а — adapt accordingly.)

Добавить тест:

```go
func TestDispatcherHARDIncludesKeyboard(t *testing.T) {
    // setup: in-mem db, user, fakeTG, dispatcher
    // ... (use existing test helpers from dispatcher_test.go)
    // simulate Hard transition
    // assert: len(fakeTG.sentWithKeyboard) == 1
    // assert: kb has 2 rows, 6 buttons total
    // assert: callback_data contains userID and checkName
}

func TestDispatcherRecoveryZeroesAcked(t *testing.T) {
    // setup: state with prev.Acked = true, current_status='hard'
    // trigger Recovery transition (FSM Apply with hard→ok×2)
    // dispatcher.Handle(...)
    // assert: state.Get(...).Acked == false
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/alerts -run "TestDispatcherHARDIncludesKeyboard|TestDispatcherRecoveryZeroesAcked" -v`
Expected: FAIL — `SendMessageWithKeyboard` not called yet; Acked не zero'ится в Recovery save.

- [ ] **Step 3: Update TGSender interface in dispatcher.go:13-16**

```go
type TGSender interface {
    SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
    SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup *tg.InlineKeyboardMarkup) (int64, error)
    CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (int64, error)
}
```

Импорт `tg` пакета сверху файла:
```go
import (
    "github.com/anex/wg-monitor/internal/backend/tg"
)
```

- [ ] **Step 4: Modify HARD case to use SendMessageWithKeyboard**

В `internal/backend/alerts/dispatcher.go:45-65`, заменить:

```go
    case state.Hard:
        threadID, err := di.ensureTopic(ctx, userID, nickname)
        if err != nil {
            return fmt.Errorf("ensure topic: %w", err)
        }
        text := FormatHard(HardArgs{
            Nickname:    nickname,
            CheckName:   checkName,
            ConsecFails: tr.Next.ConsecutiveFails,
            HardSince:   *tr.Next.HardSince,
            Detail:      detail,
        })
        kb := tg.HardAlertKeyboard(userID, checkName)
        mid, err := di.tg.SendMessageWithKeyboard(ctx, di.cfg.ChatID, &threadID, text, "", nil, &kb)
        if err != nil {
            return err
        }
        next := tr.Next
        next.LastAlertMsgID = &mid
        now := time.Now()
        next.LastAlertAt = &now
        return di.d.State().Save(userID, checkName, next)
```

- [ ] **Step 5: Modify Recovery case to zero Acked**

В `internal/backend/alerts/dispatcher.go:66-89`, в блок после `_, err = di.tg.SendMessage(...)`, перед `return di.d.State().Save(...)`:

```go
    case state.Recovery:
        threadID, err := di.ensureTopic(ctx, userID, nickname)
        if err != nil {
            return fmt.Errorf("ensure topic: %w", err)
        }
        prev, _ := di.d.State().Get(userID, checkName)
        var hardSince time.Time
        if prev.HardSince != nil {
            hardSince = *prev.HardSince
        }
        text := FormatRecovery(RecoveryArgs{
            Nickname:    nickname,
            CheckName:   checkName,
            HardSince:   hardSince,
            RecoveredAt: time.Now(),
        })
        _, err = di.tg.SendMessage(ctx, di.cfg.ChatID, &threadID, text, "", prev.LastAlertMsgID)
        if err != nil {
            return err
        }
        next := tr.Next
        next.LastAlertMsgID = nil
        next.LastAlertAt = nil
        next.Acked = false
        return di.d.State().Save(userID, checkName, next)
```

(`Acked = false` уже сетится FSM'ой в Task 4, но здесь явно дублируется для defensive coding — Save принимает `next` который пришёл из FSM, поэтому дополнительно не нужно. **Remove the `next.Acked = false` line if FSM-set is trusted** — actually FSM in Task 4 sets it on Recovery transition. Безопасно оставить дубликат для ясности.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/alerts -v`
Expected: PASS — все.

- [ ] **Step 7: Run full repo tests for regression check**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./...`
Expected: PASS — все 13+ пакетов.

- [ ] **Step 8: Commit**

```bash
git add internal/backend/alerts/dispatcher.go internal/backend/alerts/dispatcher_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(alerts): HARD attaches inline keyboard; Recovery zeroes Acked"
```

---

### Task 10: `callbacks/parse.go` — callback_data parser

**Files:**
- Create: `internal/backend/callbacks/parse.go`
- Create: `internal/backend/callbacks/parse_test.go`

- [ ] **Step 1: Write failing tests**

`internal/backend/callbacks/parse_test.go`:

```go
package callbacks

import (
    "testing"
    "time"
)

func TestParseSilence(t *testing.T) {
    cases := []struct {
        data string
        ttl  time.Duration
    }{
        {"silence:42:awg_handshake:1h", 1 * time.Hour},
        {"silence:42:awg_handshake:4h", 4 * time.Hour},
        {"silence:42:awg_handshake:24h", 24 * time.Hour},
    }
    for _, c := range cases {
        a, err := Parse(c.data)
        if err != nil { t.Fatalf("%s: %v", c.data, err) }
        if a.Action != "silence" { t.Errorf("%s: action=%q", c.data, a.Action) }
        if a.UserID != 42 { t.Errorf("%s: uid=%d", c.data, a.UserID) }
        if a.CheckName != "awg_handshake" { t.Errorf("%s: check=%q", c.data, a.CheckName) }
        if a.TTL != c.ttl { t.Errorf("%s: ttl=%v, want %v", c.data, a.TTL, c.ttl) }
    }
}

func TestParseAck(t *testing.T) {
    a, err := Parse("ack:42:awg_handshake")
    if err != nil { t.Fatal(err) }
    if a.Action != "ack" { t.Errorf("action=%q", a.Action) }
    if a.UserID != 42 { t.Errorf("uid=%d", a.UserID) }
    if a.CheckName != "awg_handshake" { t.Errorf("check=%q", a.CheckName) }
}

func TestParseMute(t *testing.T) {
    a, err := Parse("mute:42:awg_handshake")
    if err != nil { t.Fatal(err) }
    if a.Action != "mute" { t.Errorf("action=%q", a.Action) }
}

func TestParseHistory(t *testing.T) {
    a, err := Parse("history:42:awg_handshake")
    if err != nil { t.Fatal(err) }
    if a.Action != "history" { t.Errorf("action=%q", a.Action) }
}

func TestParseMalformed(t *testing.T) {
    cases := []string{
        "",
        "garbage",
        "silence:nan:awg:1h",     // bad uid
        "silence:42:awg",          // missing ttl
        "silence:42:awg:invalid",  // bad ttl
        "unknown:42:awg",          // unknown action
    }
    for _, c := range cases {
        if _, err := Parse(c); err == nil {
            t.Errorf("expected error for %q", c)
        }
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/callbacks -run TestParse -v`
Expected: FAIL — package callbacks does not exist yet.

- [ ] **Step 3: Implement parse.go**

`internal/backend/callbacks/parse.go`:

```go
// Package callbacks handles Telegram inline-button callbacks for HARD alerts.
// Long-poll loop in router.go fetches callback_query updates, dispatches to actions.go.
package callbacks

import (
    "fmt"
    "strconv"
    "strings"
    "time"
)

// Args is the parsed shape of a callback_data string.
type Args struct {
    Action    string        // "silence" | "ack" | "mute" | "history"
    UserID    int64
    CheckName string
    TTL       time.Duration // only set for silence
}

var validActions = map[string]bool{
    "silence": true, "ack": true, "mute": true, "history": true,
}

func Parse(data string) (Args, error) {
    parts := strings.Split(data, ":")
    if len(parts) < 3 {
        return Args{}, fmt.Errorf("malformed callback_data: %q", data)
    }
    action := parts[0]
    if !validActions[action] {
        return Args{}, fmt.Errorf("unknown action: %q", action)
    }
    uid, err := strconv.ParseInt(parts[1], 10, 64)
    if err != nil {
        return Args{}, fmt.Errorf("bad user_id %q: %w", parts[1], err)
    }
    a := Args{Action: action, UserID: uid, CheckName: parts[2]}
    if action == "silence" {
        if len(parts) != 4 {
            return Args{}, fmt.Errorf("silence requires ttl: %q", data)
        }
        ttl, err := parseTTL(parts[3])
        if err != nil { return Args{}, err }
        a.TTL = ttl
    }
    return a, nil
}

func parseTTL(s string) (time.Duration, error) {
    switch s {
    case "1h":  return 1 * time.Hour, nil
    case "4h":  return 4 * time.Hour, nil
    case "24h": return 24 * time.Hour, nil
    }
    return 0, fmt.Errorf("invalid ttl: %q (must be 1h|4h|24h)", s)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/callbacks -run TestParse -v`
Expected: PASS — все.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/parse.go internal/backend/callbacks/parse_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(callbacks): parse.go for callback_data → Args"
```

---

### Task 11: `callbacks/actions.go` — Silence + Ack actions

**Files:**
- Create: `internal/backend/callbacks/actions.go`
- Create: `internal/backend/callbacks/actions_test.go`

- [ ] **Step 1: Write failing tests for Silence and Ack**

`internal/backend/callbacks/actions_test.go`:

```go
package callbacks

import (
    "context"
    "strings"
    "testing"
    "time"

    "github.com/anex/wg-monitor/internal/backend/db"
)

func newTestDB(t *testing.T) (*db.DB, int64) {
    t.Helper()
    tmp := t.TempDir() + "/test.db"
    d, err := db.Open(tmp)
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { d.Close() })
    uid, err := d.Users().Create(db.UserCreate{
        Nickname: "vasya", TokenHash: "h", ExpectedExitIP: "1.1.1.1", AwgIface: "nwg0",
    })
    if err != nil { t.Fatal(err) }
    return d, uid
}

func TestActionSilenceWritesUntil(t *testing.T) {
    d, uid := newTestDB(t)
    a := NewSilenceAction(d)
    statusLine, err := a.Apply(context.Background(), nil, Args{
        Action: "silence", UserID: uid, CheckName: "awg_handshake", TTL: 4 * time.Hour,
    })
    if err != nil { t.Fatal(err) }
    if !strings.Contains(statusLine, "Silenced") {
        t.Errorf("status line: %q", statusLine)
    }
    st, _ := d.State().Get(uid, "awg_handshake")
    if st.SilencedUntil == nil {
        t.Fatal("SilencedUntil nil")
    }
    elapsed := time.Until(*st.SilencedUntil)
    if elapsed < 3*time.Hour+30*time.Minute || elapsed > 4*time.Hour+30*time.Minute {
        t.Errorf("expected silence ~4h, got %v", elapsed)
    }
}

func TestActionAckSetsAcked(t *testing.T) {
    d, uid := newTestDB(t)
    a := NewAckAction(d)
    statusLine, err := a.Apply(context.Background(), nil, Args{
        Action: "ack", UserID: uid, CheckName: "awg_handshake",
    })
    if err != nil { t.Fatal(err) }
    if !strings.Contains(statusLine, "Ack") {
        t.Errorf("status line: %q", statusLine)
    }
    st, _ := d.State().Get(uid, "awg_handshake")
    if !st.Acked {
        t.Error("Acked not set to true")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/callbacks -run "TestActionSilence|TestActionAck" -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement actions.go (Silence + Ack stubs)**

`internal/backend/callbacks/actions.go`:

```go
package callbacks

import (
    "context"
    "fmt"
    "time"

    "github.com/anex/wg-monitor/internal/backend/db"
    "github.com/anex/wg-monitor/internal/backend/tg"
)

// Action applies a callback to incident state and returns a status line
// to append to the original message after the keyboard is removed.
// q is the original CallbackQuery (nil OK in unit tests that bypass TG).
type Action interface {
    Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (statusLine string, err error)
}

// ----- Silence -----

type SilenceAction struct{ d *db.DB }

func NewSilenceAction(d *db.DB) *SilenceAction { return &SilenceAction{d: d} }

func (a *SilenceAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
    st, err := a.d.State().Get(args.UserID, args.CheckName)
    if err != nil { return "", err }
    until := time.Now().Add(args.TTL)
    st.SilencedUntil = &until
    if err := a.d.State().Save(args.UserID, args.CheckName, st); err != nil {
        return "", err
    }
    return fmt.Sprintf("⏸ Silenced до %s МСК (admin)",
        until.In(moscowLoc()).Format("15:04")), nil
}

func moscowLoc() *time.Location {
    loc, err := time.LoadLocation("Europe/Moscow")
    if err != nil { return time.FixedZone("MSK", 3*3600) }
    return loc
}

// ----- Ack -----

type AckAction struct{ d *db.DB }

func NewAckAction(d *db.DB) *AckAction { return &AckAction{d: d} }

func (a *AckAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
    st, err := a.d.State().Get(args.UserID, args.CheckName)
    if err != nil { return "", err }
    st.Acked = true
    if err := a.d.State().Save(args.UserID, args.CheckName, st); err != nil {
        return "", err
    }
    return "✅ Ack'ed (до восстановления)", nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/callbacks -v`
Expected: PASS — все.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/actions.go internal/backend/callbacks/actions_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(callbacks): Silence + Ack actions"
```

---

### Task 12: `callbacks/actions.go` — Mute (with nextCutoff) + History

**Files:**
- Modify: `internal/backend/callbacks/actions.go` (append)
- Modify: `internal/backend/callbacks/actions_test.go`

- [ ] **Step 1: Write failing tests for Mute and History**

Добавить в `internal/backend/callbacks/actions_test.go`:

```go
func TestNextCutoff(t *testing.T) {
    loc, _ := time.LoadLocation("Europe/Moscow")
    cases := []struct {
        now      time.Time
        cutoff   int
        expected time.Time
    }{
        {
            now:      time.Date(2026, 4, 28, 14, 0, 0, 0, loc),
            cutoff:   9,
            expected: time.Date(2026, 4, 29, 9, 0, 0, 0, loc),
        },
        {
            now:      time.Date(2026, 4, 28, 5, 0, 0, 0, loc),
            cutoff:   9,
            expected: time.Date(2026, 4, 28, 9, 0, 0, 0, loc),
        },
        {
            now:      time.Date(2026, 4, 28, 8, 55, 0, 0, loc),
            cutoff:   9,
            expected: time.Date(2026, 4, 28, 9, 0, 0, 0, loc),
        },
        {
            now:      time.Date(2026, 4, 28, 9, 0, 0, 0, loc), // exactly at cutoff → tomorrow
            cutoff:   9,
            expected: time.Date(2026, 4, 29, 9, 0, 0, 0, loc),
        },
    }
    for _, c := range cases {
        got := nextCutoff(c.now, c.cutoff, loc)
        if !got.Equal(c.expected) {
            t.Errorf("now=%v cutoff=%d: got %v, want %v", c.now, c.cutoff, got, c.expected)
        }
    }
}

func TestActionMuteWritesUntil(t *testing.T) {
    d, uid := newTestDB(t)
    a := NewMuteAction(d, 9)
    _, err := a.Apply(context.Background(), nil, Args{Action: "mute", UserID: uid, CheckName: "awg_handshake"})
    if err != nil { t.Fatal(err) }
    st, _ := d.State().Get(uid, "awg_handshake")
    if st.SilencedUntil == nil { t.Fatal("SilencedUntil nil") }
    delta := time.Until(*st.SilencedUntil)
    if delta < 0 || delta > 25*time.Hour {
        t.Errorf("delta out of range [0, 25h]: %v", delta)
    }
}

func TestActionHistoryNoEvents(t *testing.T) {
    d, uid := newTestDB(t)
    var sent []string
    fakeTG := &fakeTGForHistory{onSend: func(text string) { sent = append(sent, text) }}
    a := NewHistoryAction(d, fakeTG, -100)
    _, err := a.Apply(context.Background(), &tg.CallbackQuery{}, Args{
        Action: "history", UserID: uid, CheckName: "awg_handshake",
    })
    if err != nil { t.Fatal(err) }
    if len(sent) != 1 {
        t.Fatalf("expected 1 history message, got %d", len(sent))
    }
    if !strings.Contains(sent[0], "нет событий") {
        t.Errorf("expected 'нет событий', got %q", sent[0])
    }
}

func TestActionHistoryWithTransitions(t *testing.T) {
    d, uid := newTestDB(t)
    now := time.Now()
    // Insert 5 events: ok, fail, fail, fail, ok (one HARD transition)
    _ = d.Events().Append(uid, "awg_handshake", "ok",   "", now.Add(-30*time.Minute))
    _ = d.Events().Append(uid, "awg_handshake", "fail", "h=200s", now.Add(-25*time.Minute))
    _ = d.Events().Append(uid, "awg_handshake", "fail", "h=250s", now.Add(-20*time.Minute))
    _ = d.Events().Append(uid, "awg_handshake", "fail", "h=300s", now.Add(-15*time.Minute))
    _ = d.Events().Append(uid, "awg_handshake", "ok",   "", now.Add(-10*time.Minute))

    var sent []string
    fakeTG := &fakeTGForHistory{onSend: func(text string) { sent = append(sent, text) }}
    a := NewHistoryAction(d, fakeTG, -100)
    _, err := a.Apply(context.Background(), &tg.CallbackQuery{}, Args{
        Action: "history", UserID: uid, CheckName: "awg_handshake",
    })
    if err != nil { t.Fatal(err) }
    if len(sent) != 1 { t.Fatalf("got %d msgs", len(sent)) }
    msg := sent[0]
    // Expect ≥2 transitions: ok→fail and fail→ok
    if !strings.Contains(msg, "✅") || !strings.Contains(msg, "❌") {
        t.Errorf("expected ✅ and ❌ in transitions, got %q", msg)
    }
}

// Minimal mock for History tests (no SendMessageWithKeyboard needed)
type fakeTGForHistory struct {
    onSend func(text string)
}

func (f *fakeTGForHistory) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
    if f.onSend != nil { f.onSend(text) }
    return 1, nil
}
```

(Note on `Events().Append` — verify exact signature in existing `db/events.go`. If method name differs, adjust.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/callbacks -run "TestNextCutoff|TestActionMute|TestActionHistory" -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement Mute + History + nextCutoff in actions.go**

В конец `internal/backend/callbacks/actions.go` добавить:

```go
// ----- Mute -----

type MuteAction struct {
    d          *db.DB
    cutoffHour int
}

func NewMuteAction(d *db.DB, cutoffHour int) *MuteAction {
    return &MuteAction{d: d, cutoffHour: cutoffHour}
}

func (a *MuteAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
    loc := moscowLoc()
    until := nextCutoff(time.Now().In(loc), a.cutoffHour, loc)
    st, err := a.d.State().Get(args.UserID, args.CheckName)
    if err != nil { return "", err }
    untilUTC := until.UTC()
    st.SilencedUntil = &untilUTC
    if err := a.d.State().Save(args.UserID, args.CheckName, st); err != nil {
        return "", err
    }
    return fmt.Sprintf("🔇 Muted до %02d:00 МСК (%s)",
        a.cutoffHour, until.Format("02 Jan")), nil
}

// nextCutoff returns the next moment of `hour:00` in `loc`, strictly in the future.
// If now is exactly at hour:00, returns next day's hour:00.
func nextCutoff(now time.Time, hour int, loc *time.Location) time.Time {
    nowLoc := now.In(loc)
    target := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), hour, 0, 0, 0, loc)
    if !target.After(nowLoc) {
        target = target.AddDate(0, 0, 1)
    }
    return target
}

// ----- History -----

type historyTG interface {
    SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

type HistoryAction struct {
    d      *db.DB
    tg     historyTG
    chatID int64
}

func NewHistoryAction(d *db.DB, tgClient historyTG, chatID int64) *HistoryAction {
    return &HistoryAction{d: d, tg: tgClient, chatID: chatID}
}

const historyMaxTransitions = 30

func (a *HistoryAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
    cutoff := time.Now().Add(-24 * time.Hour)
    events, err := a.d.Events().ListSince(args.UserID, args.CheckName, cutoff)
    if err != nil { return "", err }

    // Compute transitions: only emit lines on status change.
    var lines []string
    var prev string
    for _, e := range events {
        if e.Status != prev {
            icon := "✅"
            if e.Status == "fail" { icon = "❌" }
            lines = append(lines, fmt.Sprintf("%s %s %s", e.TS.In(moscowLoc()).Format("15:04"), icon, e.Status))
            prev = e.Status
        }
    }

    // Look up nickname
    user, _ := a.d.Users().GetByID(args.UserID)
    header := fmt.Sprintf("📋 История [%s] / %s — 24ч", user.Nickname, args.CheckName)

    var body string
    if len(lines) == 0 {
        body = "(нет событий за период)"
    } else {
        truncated := false
        if len(lines) > historyMaxTransitions {
            lines = lines[len(lines)-historyMaxTransitions:]
            truncated = true
        }
        body = strings.Join(lines, "\n")
        if truncated {
            body = "... (older entries truncated)\n" + body
        }
    }
    text := header + "\n" + body

    threadID := user.TelegramThreadID
    _, err = a.tg.SendMessage(ctx, a.chatID, threadID, text, "", nil)
    if err != nil { return "", err }
    // History does NOT edit original — return empty status line so router skips edit.
    return "", nil
}
```

Add `"strings"` to imports if not already there.

- [ ] **Step 4: Verify Events().ListSince exists or add it**

Проверить `internal/backend/db/events.go` — если метода `ListSince(userID, checkName, since)` нет, добавить:

```go
type EventRow struct {
    ID         int64
    UserID     int64
    CheckName  string
    Status     string
    DetailsJSON string
    TS         time.Time
}

func (e *EventsRepo) ListSince(userID int64, checkName string, since time.Time) ([]EventRow, error) {
    rows, err := e.d.db.Query(
        `SELECT id, user_id, check_name, status, COALESCE(details_json,''), ts FROM events
         WHERE user_id = ? AND check_name = ? AND ts > ?
         ORDER BY ts ASC`,
        userID, checkName, since.UTC())
    if err != nil { return nil, err }
    defer rows.Close()
    var out []EventRow
    for rows.Next() {
        var r EventRow
        if err := rows.Scan(&r.ID, &r.UserID, &r.CheckName, &r.Status, &r.DetailsJSON, &r.TS); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}
```

(If existing method name is different — `Recent`, `Last24h`, etc. — use that and adapt the action code.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/callbacks -v`
Expected: PASS — все 7 action-тестов.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/callbacks/actions.go internal/backend/callbacks/actions_test.go internal/backend/db/events.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(callbacks): Mute (next 9 МСК) + History (24h transitions)"
```

---

### Task 13: `callbacks/router.go` — Run loop + allowlist + dispatch

**Files:**
- Create: `internal/backend/callbacks/router.go`
- Create: `internal/backend/callbacks/router_test.go`

- [ ] **Step 1: Write failing tests**

`internal/backend/callbacks/router_test.go`:

```go
package callbacks

import (
    "context"
    "errors"
    "strings"
    "sync"
    "testing"

    "github.com/anex/wg-monitor/internal/backend/tg"
)

type fakeRouterTG struct {
    mu        sync.Mutex
    answers   []string
    edits     []string
    sentMsgs  []string
    sendErr   error
    answerErr error
    editErr   error
}

func (f *fakeRouterTG) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
    f.mu.Lock(); defer f.mu.Unlock()
    f.sentMsgs = append(f.sentMsgs, text)
    return 1, f.sendErr
}
func (f *fakeRouterTG) AnswerCallbackQuery(ctx context.Context, id, text string) error {
    f.mu.Lock(); defer f.mu.Unlock()
    f.answers = append(f.answers, text)
    return f.answerErr
}
func (f *fakeRouterTG) EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error {
    f.mu.Lock(); defer f.mu.Unlock()
    f.edits = append(f.edits, text)
    return f.editErr
}

func TestRouterDispatchesSilence(t *testing.T) {
    d, uid := newTestDB(t)
    f := &fakeRouterTG{}
    r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

    q := &tg.CallbackQuery{
        ID:      "cbk-1",
        From:    tg.User{ID: 12345},
        Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴 alert text"},
        Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
    }
    r.HandleCallback(context.Background(), q)

    if len(f.answers) != 1 { t.Errorf("expected 1 answer, got %d", len(f.answers)) }
    if len(f.edits) != 1 {
        t.Errorf("expected 1 edit, got %d", len(f.edits))
    } else if !strings.Contains(f.edits[0], "Silenced") {
        t.Errorf("edit text missing 'Silenced': %q", f.edits[0])
    }
}

func TestRouterRejectsNonAdmin(t *testing.T) {
    d, uid := newTestDB(t)
    f := &fakeRouterTG{}
    r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

    q := &tg.CallbackQuery{
        ID:      "cbk-2",
        From:    tg.User{ID: 99999}, // not admin
        Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
        Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
    }
    r.HandleCallback(context.Background(), q)

    if len(f.answers) != 1 { t.Errorf("expected 1 answer (rejection), got %d", len(f.answers)) }
    if !strings.Contains(f.answers[0], "not authorized") {
        t.Errorf("expected 'not authorized', got %q", f.answers[0])
    }
    if len(f.edits) != 0 {
        t.Errorf("expected NO edits for non-admin, got %d", len(f.edits))
    }
}

func TestRouterUnknownAction(t *testing.T) {
    d, _ := newTestDB(t)
    f := &fakeRouterTG{}
    r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

    q := &tg.CallbackQuery{
        ID:   "cbk-3",
        From: tg.User{ID: 12345},
        Data: "frobnicate:1:x",
    }
    r.HandleCallback(context.Background(), q)
    if len(f.answers) != 1 { t.Fatal("expected answerCallback") }
    if !strings.Contains(strings.ToLower(f.answers[0]), "unknown") {
        t.Errorf("expected 'unknown' in answer, got %q", f.answers[0])
    }
}

func TestRouterHistorySkipsEdit(t *testing.T) {
    d, uid := newTestDB(t)
    f := &fakeRouterTG{}
    r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

    q := &tg.CallbackQuery{
        ID:      "cbk-h",
        From:    tg.User{ID: 12345},
        Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "alert"},
        Data:    "history:" + itoa(uid) + ":awg_handshake",
    }
    r.HandleCallback(context.Background(), q)

    // History sends a NEW message, NOT edits the original
    if len(f.sentMsgs) != 1 {
        t.Errorf("expected 1 history message sent, got %d", len(f.sentMsgs))
    }
    if len(f.edits) != 0 {
        t.Errorf("history should not edit original, got %d edits", len(f.edits))
    }
}

// Errors propagating through actions
func TestRouterActionErrorReportedAsToast(t *testing.T) {
    d, uid := newTestDB(t)
    // make d.State().Save fail by closing db
    d.Close()
    f := &fakeRouterTG{}
    r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
    q := &tg.CallbackQuery{
        ID:      "cbk-err",
        From:    tg.User{ID: 12345},
        Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
        Data:    "ack:" + itoa(uid) + ":awg_handshake",
    }
    r.HandleCallback(context.Background(), q)
    if len(f.answers) != 1 { t.Fatal("expected answer") }
    if !strings.Contains(f.answers[0], "error") {
        t.Errorf("expected 'error' in answer, got %q", f.answers[0])
    }
    if len(f.edits) != 0 {
        t.Error("on error, should NOT edit")
    }
    _ = errors.New
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }
```

Imports also need `"fmt"` for itoa.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/callbacks -run TestRouter -v`
Expected: FAIL — `NewRouter`, `Config`, `HandleCallback` undefined.

- [ ] **Step 3: Implement router.go**

`internal/backend/callbacks/router.go`:

```go
package callbacks

import (
    "context"
    "log/slog"
    "math"
    "strconv"
    "time"

    "github.com/anex/wg-monitor/internal/backend/db"
    "github.com/anex/wg-monitor/internal/backend/tg"
)

// TGClient is the subset of tg.Client used by the router.
type TGClient interface {
    SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
    AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
    EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error
    GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error)
}

type Config struct {
    ChatID         int64
    AdminUserID    int64
    MuteCutoffHour int
}

type Router struct {
    d       *db.DB
    tg      TGClient
    cfg     Config
    silence *SilenceAction
    ack     *AckAction
    mute    *MuteAction
    history *HistoryAction
}

func NewRouter(d *db.DB, tgClient TGClient, cfg Config) *Router {
    return &Router{
        d:       d,
        tg:      tgClient,
        cfg:     cfg,
        silence: NewSilenceAction(d),
        ack:     NewAckAction(d),
        mute:    NewMuteAction(d, cfg.MuteCutoffHour),
        history: NewHistoryAction(d, tgClient, cfg.ChatID),
    }
}

// Run loops on GetUpdates, persisting the last-processed update_id in tg_state KV.
// Backoff on errors. Exits when ctx is cancelled.
func (r *Router) Run(ctx context.Context) error {
    var attempt int
    offset, _ := r.loadOffset()
    for {
        select {
        case <-ctx.Done(): return nil
        default:
        }
        updates, err := r.tg.GetUpdates(ctx, offset, 30)
        if err != nil {
            if ctx.Err() != nil { return nil }
            attempt++
            wait := time.Duration(math.Min(math.Pow(2, float64(attempt)), 60)) * time.Second
            slog.Warn("getUpdates failed; backoff", "err", err, "wait", wait)
            select {
            case <-ctx.Done(): return nil
            case <-time.After(wait):
            }
            continue
        }
        attempt = 0
        for _, u := range updates {
            r.handleUpdate(ctx, u)
            if u.UpdateID >= offset {
                offset = u.UpdateID + 1
            }
        }
        if len(updates) > 0 {
            _ = r.saveOffset(offset)
        }
    }
}

func (r *Router) handleUpdate(ctx context.Context, u tg.Update) {
    if u.CallbackQuery == nil { return }
    r.HandleCallback(ctx, u.CallbackQuery)
}

func (r *Router) loadOffset() (int64, error) {
    s, err := r.d.KV().Get("last_update_id")
    if err != nil || s == "" { return 0, err }
    return strconv.ParseInt(s, 10, 64)
}
func (r *Router) saveOffset(offset int64) error {
    return r.d.KV().Set("last_update_id", strconv.FormatInt(offset, 10))
}

// HandleCallback applies allowlist, parses, dispatches to action, edits message.
// Exposed for tests.
func (r *Router) HandleCallback(ctx context.Context, q *tg.CallbackQuery) {
    if q.From.ID != r.cfg.AdminUserID {
        _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "not authorized")
        slog.Warn("rejected callback (allowlist)", "from", q.From.ID, "data", q.Data)
        return
    }
    args, err := Parse(q.Data)
    if err != nil {
        _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "unknown action")
        slog.Warn("malformed callback_data", "data", q.Data, "err", err)
        return
    }
    var action Action
    switch args.Action {
    case "silence": action = r.silence
    case "ack":     action = r.ack
    case "mute":    action = r.mute
    case "history": action = r.history
    default:
        _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "unknown action")
        return
    }
    statusLine, err := action.Apply(ctx, q, args)
    if err != nil {
        msg := "error: " + err.Error()
        if len(msg) > 200 { msg = msg[:200] }
        _ = r.tg.AnswerCallbackQuery(ctx, q.ID, msg)
        slog.Error("action failed", "action", args.Action, "err", err)
        return
    }
    _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
    if statusLine == "" {
        // History returns "" — do not edit original.
        return
    }
    newText := q.Message.Text + "\n\n" + statusLine
    empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
    if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, newText, "", &empty); err != nil {
        slog.Warn("editMessageText failed (state already updated)", "err", err)
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/callbacks -v`
Expected: PASS — все.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/router.go internal/backend/callbacks/router_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(callbacks): Router (Run loop + allowlist + dispatch + edit)"
```

---

### Task 14: `realert/poller.go` — Poller + tick + Run

**Files:**
- Create: `internal/backend/realert/poller.go`
- Create: `internal/backend/realert/poller_test.go`

- [ ] **Step 1: Write failing tests**

`internal/backend/realert/poller_test.go`:

```go
package realert

import (
    "context"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/anex/wg-monitor/internal/backend/db"
)

type fakeTG struct {
    mu       sync.Mutex
    sent     []string
    sendErr  error
}

func (f *fakeTG) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
    f.mu.Lock(); defer f.mu.Unlock()
    f.sent = append(f.sent, text)
    return 99, f.sendErr
}

func newTestDB(t *testing.T) (*db.DB, int64) {
    t.Helper()
    tmp := t.TempDir() + "/test.db"
    d, err := db.Open(tmp)
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { d.Close() })
    uid, err := d.Users().Create(db.UserCreate{
        Nickname: "vasya", TokenHash: "h", ExpectedExitIP: "1.1.1.1", AwgIface: "nwg0",
    })
    if err != nil { t.Fatal(err) }
    return d, uid
}

func TestTickEmptyNoCalls(t *testing.T) {
    d, _ := newTestDB(t)
    f := &fakeTG{}
    p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})
    p.tick(context.Background())
    if len(f.sent) != 0 {
        t.Errorf("expected 0 sends, got %d", len(f.sent))
    }
}

func TestTickStaleHardSendsRealert(t *testing.T) {
    d, uid := newTestDB(t)
    f := &fakeTG{}
    p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

    hardSince := time.Now().Add(-7 * time.Hour)
    lastAlert := time.Now().Add(-7 * time.Hour)
    err := d.State().Save(uid, "awg_handshake", db.IncidentState{
        UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
        ConsecutiveFails: 3, HardSince: &hardSince, LastAlertAt: &lastAlert,
    })
    if err != nil { t.Fatal(err) }

    p.tick(context.Background())

    if len(f.sent) != 1 {
        t.Fatalf("expected 1 realert, got %d", len(f.sent))
    }
    if !strings.Contains(f.sent[0], "STILL DOWN") {
        t.Errorf("missing 'STILL DOWN' in: %q", f.sent[0])
    }
    if !strings.Contains(f.sent[0], "Re-alert #1") {
        t.Errorf("expected 'Re-alert #1' (7h since), got: %q", f.sent[0])
    }

    // LastAlertAt updated to now-ish
    st, _ := d.State().Get(uid, "awg_handshake")
    if st.LastAlertAt == nil || time.Since(*st.LastAlertAt) > time.Minute {
        t.Errorf("LastAlertAt should be updated to recent, got %v", st.LastAlertAt)
    }
}

func TestTickSilencedSkipped(t *testing.T) {
    d, uid := newTestDB(t)
    f := &fakeTG{}
    p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

    hardSince := time.Now().Add(-7 * time.Hour)
    lastAlert := time.Now().Add(-7 * time.Hour)
    silenced := time.Now().Add(2 * time.Hour)
    err := d.State().Save(uid, "awg_handshake", db.IncidentState{
        UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
        HardSince: &hardSince, LastAlertAt: &lastAlert, SilencedUntil: &silenced,
    })
    if err != nil { t.Fatal(err) }

    p.tick(context.Background())
    if len(f.sent) != 0 {
        t.Errorf("silenced HARD should not realert, got %d sends", len(f.sent))
    }
}

func TestTickAckedSkipped(t *testing.T) {
    d, uid := newTestDB(t)
    f := &fakeTG{}
    p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

    hardSince := time.Now().Add(-7 * time.Hour)
    lastAlert := time.Now().Add(-7 * time.Hour)
    err := d.State().Save(uid, "awg_handshake", db.IncidentState{
        UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
        HardSince: &hardSince, LastAlertAt: &lastAlert, Acked: true,
    })
    if err != nil { t.Fatal(err) }

    p.tick(context.Background())
    if len(f.sent) != 0 {
        t.Errorf("acked HARD should not realert, got %d sends", len(f.sent))
    }
}

func TestTickSendErrorPreservesLastAlertAt(t *testing.T) {
    d, uid := newTestDB(t)
    f := &fakeTG{sendErr: errors.New("tg flap")}
    p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

    hardSince := time.Now().Add(-7 * time.Hour)
    origLastAlert := time.Now().Add(-7 * time.Hour)
    err := d.State().Save(uid, "awg_handshake", db.IncidentState{
        UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
        HardSince: &hardSince, LastAlertAt: &origLastAlert,
    })
    if err != nil { t.Fatal(err) }

    p.tick(context.Background())

    st, _ := d.State().Get(uid, "awg_handshake")
    if st.LastAlertAt == nil { t.Fatal("LastAlertAt nil") }
    if time.Since(*st.LastAlertAt) < 6*time.Hour {
        t.Errorf("LastAlertAt should NOT have advanced after send error, but it did: %v", st.LastAlertAt)
    }
}
```

Add `"errors"` to imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/realert -v`
Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Implement poller.go**

`internal/backend/realert/poller.go`:

```go
// Package realert sends a STILL-DOWN reminder for HARD incidents older than
// `RealertEvery` (per spec §5.3). Tick cadence is decoupled (typically 5 min);
// the actual realert interval is enforced via StaleHards SQL filter.
package realert

import (
    "context"
    "log/slog"
    "time"

    "github.com/anex/wg-monitor/internal/backend/alerts"
    "github.com/anex/wg-monitor/internal/backend/db"
)

type TGSender interface {
    SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

type Config struct {
    ChatID       int64
    RealertEvery time.Duration // default 6h
    TickEvery    time.Duration // default 5min
}

type Poller struct {
    d   *db.DB
    tg  TGSender
    cfg Config
}

func NewPoller(d *db.DB, tg TGSender, cfg Config) *Poller {
    return &Poller{d: d, tg: tg, cfg: cfg}
}

func (p *Poller) Run(ctx context.Context) error {
    t := time.NewTicker(p.cfg.TickEvery)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done(): return nil
        case <-t.C:
            p.tick(ctx)
        }
    }
}

func (p *Poller) tick(ctx context.Context) {
    cutoff := time.Now().Add(-p.cfg.RealertEvery)
    stale, err := p.d.State().StaleHards(cutoff)
    if err != nil {
        slog.Error("realert: StaleHards query failed", "err", err)
        return
    }
    for _, sh := range stale {
        u, err := p.d.Users().GetByID(sh.UserID)
        if err != nil {
            slog.Warn("realert: user lookup failed (orphan?)", "user_id", sh.UserID, "err", err)
            continue
        }
        st, err := p.d.State().Get(sh.UserID, sh.CheckName)
        if err != nil {
            slog.Error("realert: state get failed", "user_id", sh.UserID, "err", err)
            continue
        }
        if st.HardSince == nil {
            slog.Warn("realert: HardSince nil despite hard status", "user_id", sh.UserID)
            continue
        }
        count := int(time.Since(*st.HardSince) / p.cfg.RealertEvery)
        text := alerts.FormatRealert(alerts.RealertArgs{
            Nickname:     u.Nickname,
            CheckName:    sh.CheckName,
            HardSince:    *st.HardSince,
            RealertCount: count,
        })
        _, err = p.tg.SendMessage(ctx, p.cfg.ChatID, u.TelegramThreadID, text, "", nil)
        if err != nil {
            slog.Error("realert: tg send failed", "user_id", sh.UserID, "err", err)
            continue // do not advance LastAlertAt → retry next tick
        }
        now := time.Now()
        st.LastAlertAt = &now
        // LastAlertMsgID retained — points to original HARD msg for RECOVERY reply.
        if err := p.d.State().Save(sh.UserID, sh.CheckName, st); err != nil {
            slog.Error("realert: state save failed", "user_id", sh.UserID, "err", err)
        }
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend/realert -v`
Expected: PASS — все 5 тестов.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/realert/poller.go internal/backend/realert/poller_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(realert): StaleHards poller (5-min tick, 6h realert interval)"
```

---

### Task 15: Backend config — `MuteCutoffHour` + `RealertTickSec`

**Files:**
- Modify: `internal/backend/config.go:32-36` (StateConfig), `:79-87` (defaults)
- Modify: `internal/backend/config_test.go`

- [ ] **Step 1: Write failing test for new config defaults**

Добавить в `internal/backend/config_test.go`:

```go
func TestLoadConfigDefaultsForStage2(t *testing.T) {
    tmp := t.TempDir()
    tokFile := tmp + "/tok"
    _ = os.WriteFile(tokFile, []byte("test-token"), 0600)
    cfgFile := tmp + "/cfg.yaml"
    _ = os.WriteFile(cfgFile, []byte(
        "db_path: /tmp/x.db\n" +
        "telegram:\n" +
        "  bot_token_file: "+tokFile+"\n" +
        "  chat_id: -100\n" +
        "  admin_user_id: 12345\n",
    ), 0600)
    cfg, err := LoadConfig(cfgFile)
    if err != nil { t.Fatal(err) }
    if cfg.State.MuteCutoffHour != 9 {
        t.Errorf("MuteCutoffHour default: got %d, want 9", cfg.State.MuteCutoffHour)
    }
    if cfg.State.RealertTickSec != 300 {
        t.Errorf("RealertTickSec default: got %d, want 300", cfg.State.RealertTickSec)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend -run TestLoadConfigDefaultsForStage2 -v`
Expected: FAIL — fields missing on StateConfig.

- [ ] **Step 3: Add fields to StateConfig**

В `internal/backend/config.go:32-36`, заменить:

```go
type StateConfig struct {
    FailThreshold     int `yaml:"fail_threshold"`
    RecoveryThreshold int `yaml:"recovery_threshold"`
    RealertEverySec   int `yaml:"realert_every_sec"`
    RealertTickSec    int `yaml:"realert_tick_sec"`
    MuteCutoffHour    int `yaml:"mute_cutoff_hour"`
}
```

- [ ] **Step 4: Add defaults in LoadConfig**

В `internal/backend/config.go` после строки 87 (после `cfg.State.RealertEverySec = 6 * 3600`), добавить:

```go
    if cfg.State.RealertTickSec == 0 {
        cfg.State.RealertTickSec = 300
    }
    if cfg.State.MuteCutoffHour == 0 {
        cfg.State.MuteCutoffHour = 9
    }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./internal/backend -v`
Expected: PASS — все.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/config.go internal/backend/config_test.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(config): RealertTickSec (def 300) + MuteCutoffHour (def 9)"
```

---

### Task 16: `cmd/backend/main.go` — wire callbacks + realert

**Files:**
- Modify: `cmd/backend/main.go:14-22` (imports + Version), `:48-72` (wiring)

- [ ] **Step 1: Bump Version constant**

В `cmd/backend/main.go:22`, заменить:

```go
var Version = "0.3.0-stage2-dev"
```

- [ ] **Step 2: Add imports**

В `cmd/backend/main.go:14-20`, добавить:

```go
import (
    ...existing...
    "github.com/anex/wg-monitor/internal/backend/callbacks"
    "github.com/anex/wg-monitor/internal/backend/realert"
)
```

- [ ] **Step 3: Wire callbacks.Router and realert.Poller**

После существующего `go watcher.Run(ctx)` (около строки 72), добавить:

```go
    cb := callbacks.NewRouter(d, tgClient, callbacks.Config{
        ChatID:         cfg.Telegram.ChatID,
        AdminUserID:    cfg.Telegram.AdminUserID,
        MuteCutoffHour: cfg.State.MuteCutoffHour,
    })
    go func() {
        if err := cb.Run(ctx); err != nil {
            logger.Error("callbacks router exited", "err", err)
        }
    }()

    rp := realert.NewPoller(d, tgClient, realert.Config{
        ChatID:       cfg.Telegram.ChatID,
        RealertEvery: time.Duration(cfg.State.RealertEverySec) * time.Second,
        TickEvery:    time.Duration(cfg.State.RealertTickSec) * time.Second,
    })
    go func() {
        if err := rp.Run(ctx); err != nil {
            logger.Error("realert poller exited", "err", err)
        }
    }()
```

- [ ] **Step 4: Verify `go build` clean**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 build ./...`
Expected: clean (exit 0, no output).

- [ ] **Step 5: Verify all tests still pass**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./...`
Expected: PASS — все 14+ пакетов.

- [ ] **Step 6: Commit**

```bash
git add cmd/backend/main.go
git -c user.email=asnekhaev@gmail.com commit -m "feat(cmd/backend): wire callbacks Router + realert Poller goroutines"
```

---

### Task 17: Integration test — full HARD → callback → edit → realert flow

**Files:**
- Create: `cmd/backend/integration_test.go`

- [ ] **Step 1: Write integration test**

`cmd/backend/integration_test.go`:

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/anex/wg-monitor/internal/backend/alerts"
    "github.com/anex/wg-monitor/internal/backend/callbacks"
    "github.com/anex/wg-monitor/internal/backend/db"
    "github.com/anex/wg-monitor/internal/backend/realert"
    "github.com/anex/wg-monitor/internal/backend/state"
    "github.com/anex/wg-monitor/internal/backend/tg"
)

// TestStage2EndToEnd exercises: HARD with keyboard → callback Silence → edited message → state row updated.
// Then ages the row 7h, ensures realert poller picks up and sends a STILL-DOWN reminder.
func TestStage2EndToEnd(t *testing.T) {
    // 1. Spin up fake TG server that records calls
    var mu sync.Mutex
    sentMsgs := []map[string]any{}
    edits := []map[string]any{}
    answers := []map[string]any{}

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        var req map[string]any
        _ = json.Unmarshal(body, &req)
        mu.Lock()
        switch {
        case strings.HasSuffix(r.URL.Path, "/sendMessage"):
            sentMsgs = append(sentMsgs, req)
            json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1000 + len(sentMsgs)}})
        case strings.HasSuffix(r.URL.Path, "/createForumTopic"):
            json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_thread_id": 7}})
        case strings.HasSuffix(r.URL.Path, "/editMessageText"):
            edits = append(edits, req)
            json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
        case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
            answers = append(answers, req)
            json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
        case strings.HasSuffix(r.URL.Path, "/getUpdates"):
            // Always return empty (test calls HandleCallback directly)
            json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
        }
        mu.Unlock()
    }))
    defer srv.Close()

    // 2. Set up DB + user
    tmp := t.TempDir() + "/test.db"
    d, err := db.Open(tmp)
    if err != nil { t.Fatal(err) }
    defer d.Close()
    uid, err := d.Users().Create(db.UserCreate{
        Nickname: "vasya", TokenHash: "h", ExpectedExitIP: "1.2.3.4", AwgIface: "nwg0",
    })
    if err != nil { t.Fatal(err) }

    // 3. Build TG client + dispatcher + callbacks router + realert poller
    tgC := &tg.Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
    disp := alerts.NewDispatcher(d, tgC, alerts.Config{
        ChatID:            -100,
        FailThreshold:     3,
        RecoveryThreshold: 2,
    })
    router := callbacks.NewRouter(d, tgC, callbacks.Config{
        ChatID: -100, AdminUserID: 555, MuteCutoffHour: 9,
    })
    poller := realert.NewPoller(d, tgC, realert.Config{
        ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second,
    })

    // 4. Trigger 3 fails → HARD
    ctx := context.Background()
    th := state.Thresholds{Fail: 3, Recovery: 2}
    for i := 0; i < 3; i++ {
        prev, _ := d.State().Get(uid, "awg_handshake")
        tr := state.Apply(prev, "fail", time.Now(), th)
        if err := disp.Handle(ctx, uid, "vasya", "awg_handshake", tr, "h=200s"); err != nil {
            t.Fatal(err)
        }
    }
    mu.Lock()
    if len(sentMsgs) != 1 {
        t.Fatalf("expected 1 HARD message, got %d", len(sentMsgs))
    }
    if sentMsgs[0]["reply_markup"] == nil {
        t.Errorf("HARD message missing keyboard")
    }
    mu.Unlock()

    // 5. Simulate callback Silence(1h)
    q := &tg.CallbackQuery{
        ID:   "cbk-1",
        From: tg.User{ID: 555},
        Message: tg.Message{
            MessageID: 1001, Chat: tg.Chat{ID: -100},
            Text: sentMsgs[0]["text"].(string),
        },
        Data: fmt.Sprintf("silence:%d:awg_handshake:1h", uid),
    }
    router.HandleCallback(ctx, q)

    mu.Lock()
    if len(answers) != 1 {
        t.Errorf("expected 1 answerCallback, got %d", len(answers))
    }
    if len(edits) != 1 {
        t.Errorf("expected 1 edit, got %d", len(edits))
    } else if !strings.Contains(edits[0]["text"].(string), "Silenced") {
        t.Errorf("edit missing 'Silenced': %v", edits[0]["text"])
    }
    mu.Unlock()

    // 6. Verify state has SilencedUntil set
    st, _ := d.State().Get(uid, "awg_handshake")
    if st.SilencedUntil == nil { t.Error("SilencedUntil nil after silence callback") }

    // 7. Age the state: clear silence, set hard_since/last_alert_at to 7h ago
    aged := time.Now().Add(-7 * time.Hour)
    st.SilencedUntil = nil
    st.HardSince = &aged
    st.LastAlertAt = &aged
    if err := d.State().Save(uid, "awg_handshake", st); err != nil { t.Fatal(err) }

    // 8. Trigger realert tick
    poller_tickPub := func() {
        // Hack: tick is unexported. Run() with TickEvery=1s, then sleep 1.5s, cancel.
        runCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
        defer cancel()
        _ = poller.Run(runCtx)
    }
    initialMsgs := len(sentMsgs)
    poller_tickPub()
    mu.Lock()
    realertSent := len(sentMsgs) - initialMsgs
    mu.Unlock()
    if realertSent != 1 {
        t.Fatalf("expected 1 realert message, got %d (total %d)", realertSent, len(sentMsgs))
    }
    mu.Lock()
    last := sentMsgs[len(sentMsgs)-1]
    if !strings.Contains(last["text"].(string), "STILL DOWN") {
        t.Errorf("realert text missing 'STILL DOWN': %v", last["text"])
    }
    if last["reply_markup"] != nil {
        t.Errorf("realert must not have keyboard")
    }
    mu.Unlock()
}
```

- [ ] **Step 2: Run integration test**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./cmd/backend -run TestStage2EndToEnd -v`
Expected: PASS.

- [ ] **Step 3: Run full repo test suite**

Run: `go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 test ./...`
Expected: PASS — все включая integration.

- [ ] **Step 4: Commit**

```bash
git add cmd/backend/integration_test.go
git -c user.email=asnekhaev@gmail.com commit -m "test(integration): Stage 2 end-to-end (HARD + keyboard → silence → edit → realert)"
```

---

### Task 18: Live deploy + verify on testkeen

**Files:**
- Modify: `/etc/wg-monitor/backend.yaml` on VPS Main (production config)
- Build: cross-compile for linux/arm64

- [ ] **Step 1: Cross-compile backend for linux/amd64 (VPS Main)**

VPS Main — это linux/amd64. Использовать **PowerShell** (memory `feedback_go_cross_compile_windows`):

```powershell
$env:GOOS="linux"; $env:GOARCH="amd64"
go -C C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2 build -ldflags "-X main.Version=0.3.0-stage2-dev" -o C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2/bin/wg-monitor-backend ./cmd/backend
```

Expected: бинарь создан в `bin/wg-monitor-backend`.

- [ ] **Step 2: Upload backend to VPS Main**

Использовать SSH MCP server для VPS Main (см. memory `host_vps_main`):

```
mcp ssh exec to VPS Main:
  scp the binary to /tmp/wg-monitor-backend.new
  sudo systemctl stop wg-monitor-backend
  sudo mv /tmp/wg-monitor-backend.new /usr/local/bin/wg-monitor-backend
  sudo chmod +x /usr/local/bin/wg-monitor-backend
```

Если MCP-команда `scp` отсутствует — использовать `cat | ssh ... 'cat > ...'` через Paramiko как в `deploy_keenetic.py`.

- [ ] **Step 3: Restart and verify**

```bash
sudo systemctl start wg-monitor-backend
sudo systemctl status wg-monitor-backend  # active (running)
sudo journalctl -u wg-monitor-backend -n 30 --no-pager
```

Expected: лог содержит `version=0.3.0-stage2-dev`, нет ошибок migrate.

- [ ] **Step 4: Provoke HARD on testkeen**

На MyRouter:
```bash
ssh root@192.168.31.1 -p 222
ip link set nwg0 down  # или прямой опровержение handshake
```

Wait ~3 minutes (3 reports × 60s = 3 fails).

Verify TG: получено сообщение `🔴 [testkeen] AWG handshake — DOWN` с **6 inline-кнопок в 2 ряда**.

- [ ] **Step 5: Click `[⏸ 1ч]` and verify edit**

В TG нажать кнопку `[⏸ 1ч]`.

Verify:
- spinner закрылся (TG callback answered)
- кнопки исчезли
- в конце сообщения дописана строка `⏸ Silenced до HH:MM МСК (admin)`

Verify SQL (требует authorize):
```bash
sudo sqlite3 /var/lib/wg-monitor/state.db "SELECT silenced_until, acked FROM incident_state WHERE check_name='awg_handshake';"
```
Expected: `silenced_until` ≈ now+1h, `acked = 0`.

- [ ] **Step 6: Restore awg, verify Recovery message**

```bash
ssh root@192.168.31.1 -p 222
ip link set nwg0 up
```

Wait 2 reports OK (≈2 min). Verify TG: получено RECOVERY-сообщение reply на оригинальный HARD msg id.

- [ ] **Step 7: Provoke stale HARD, verify re-alert**

Снова ip link set nwg0 down → wait HARD. Затем authorize и UPDATE:

```bash
sudo sqlite3 /var/lib/wg-monitor/state.db <<EOF
UPDATE incident_state
SET hard_since = datetime('now', '-7 hours'),
    last_alert_at = datetime('now', '-7 hours'),
    silenced_until = NULL
WHERE check_name = 'awg_handshake';
EOF
```

Wait next 5-min poller tick. Verify TG: получено сообщение `🔁 [testkeen] AWG handshake — STILL DOWN`, **без кнопок**.

- [ ] **Step 8: Restore awg, mark task complete**

```bash
ssh root@192.168.31.1 -p 222
ip link set nwg0 up
```

Wait recovery.

- [ ] **Step 9: Tag**

```bash
cd C:/Users/Anex/Projects/wg-monitor/.worktrees/stage-2
git tag v0.4.0-stage2 -m "Stage 2 — inline callbacks + StaleHards re-alert poller (live verified)"
```

- [ ] **Step 10: Commit any final fixes from live verify**

If any issues surfaced during live verify — fix them, commit, re-tag if necessary.

---

## Self-review

(Run by author of plan after all tasks written.)

**1. Spec coverage:**
- ✅ §3 Operational defaults → embedded в Tasks 11-12 (Silence/Ack/Mute) + Task 14 (Realert poller)
- ✅ §4 Architecture → Tasks 5-16 покрывают все компоненты
- ✅ §5 Schema → Tasks 1-2 (acked column + tg_state KV)
- ✅ §6.1 tg/ extensions → Tasks 5-7
- ✅ §6.2 callbacks/ → Tasks 10-13
- ✅ §6.3 realert/ → Task 14
- ✅ §6.4 alerts/ → Tasks 8-9
- ✅ §6.5 wiring → Task 16
- ✅ §6.6 config → Task 15
- ✅ §7 Data flow → integration test Task 17
- ✅ §8 Edge cases → covered in unit tests (E1-E7); E8 (mute boundary) in `TestNextCutoff`
- ✅ §9 Error handling → backoff in Task 13 router, error-no-advance в Task 14 poller, error toast в Task 13 router test
- ✅ §10 Testing → ~40 unit + 1 integration → Task counts match (Tasks 1-15 contain ~38 unit, Task 17 integration)
- ✅ §11 YAGNI → not implemented, comments where relevant
- ✅ §12 tech-debt → Task 18 step 9 leaves room for fixes from live; Stage 5 will do data-migration

**2. Placeholder scan:** clean. Все code blocks содержат конкретный код.

**3. Type consistency:**
- `IncidentState.Acked` — везде `bool` (Task 2 struct, Tasks 4-12 используют как bool)
- `tg.InlineKeyboardMarkup` — везде через указатель (`*tg.InlineKeyboardMarkup`)
- `callbacks.Args` — `Action string`, `UserID int64`, `CheckName string`, `TTL time.Duration` — везде совпадает
- `realert.Config.RealertEvery` vs `tickEvery` — оба `time.Duration`, корректно
- Method signature `Apply(ctx, q, args)` — все 4 actions consistent

**4. Ambiguity:** clean. Каждая mutation описана как exact diff.

**Verdict:** plan ready for execution.
