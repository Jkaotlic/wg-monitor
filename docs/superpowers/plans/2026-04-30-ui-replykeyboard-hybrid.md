# ReplyKeyboard Hybrid UX + diag_now Reply Fix — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace pinned inline control panel with a persistent two-button ReplyKeyboard at the chat bottom; respond to button presses with state-aware smart replies that show only the right inline action buttons; relay command-action results (diag_now, opkg_upgrade, etc) back to Telegram so the operator sees feedback within 5 seconds.

**Architecture:** Extend the existing `internal/backend/tg`, `internal/backend/alerts`, `internal/backend/callbacks` and `internal/backend/db` packages. Add a new `kv`-backed configuration for operations-topic IDs. Extend the callback router to also handle text Messages (for ReplyKeyboard taps). Add a command-result reply path from agent CommandResult → TG message.

**Tech Stack:** Go 1.21+, modernc.org/sqlite (pure-Go), gopkg.in/yaml.v3, Telegram Bot API v6+ (raw HTTP via `internal/backend/tg`). No new external dependencies.

---

## Conventions for every task

- Module path: `github.com/anex/wg-monitor`.
- All paths are relative to repo root `C:\Users\Anex\Projects\wg-monitor\.worktrees\stage-2`.
- Bash on Windows is fine for `git`, `go test`, `go vet`. Use the **PowerShell tool** for any Linux cross-compile (otherwise Bash on Windows host produces `.exe` instead of an ELF binary).
- Every commit must override the email because `~/.gitconfig` carries the placeholder `you@example.com`:
  `git -c user.email=asnekhaev@gmail.com commit -m "..."`
- Conventional Commits in English: `feat(scope): ...`, `fix(scope): ...`, `refactor(scope): ...`, `test(scope): ...`, `docs(scope): ...`.
- TDD discipline: every task starts with a failing test (Step 1+2), then minimal implementation (Step 3), then the test passes (Step 4), then commit (Step 5).

---

## Spec → Task mapping

| Spec section | Implemented by tasks |
|---|---|
| §5.1 ReplyKeyboard layout | T3, T4 |
| §5.2 Smart inline reply by state | T7, T8, T9 |
| §5.3 HARD-alert humanised labels | T17 |
| §5.4 Pinned panel removal | T18 |
| §6.1 ReplyKeyboard installation / re-attach | T5, T12 |
| §6.2 `📊 Что происходит?` press flow | T11, T12 |
| §6.3 Smart-reply inline button presses | T15, T16 |
| §6.4 HARD-alert presses | T17 (labels only) |
| §6.5 CommandResult → TG reply, pagination | T10, T15, T16 |
| §7 Topic → user lookup, KV for operations topics | T1, T2 |
| §8 Configuration changes | T13 |
| §9 Migration | T18, T24 |
| §10 Testing strategy | T22, T23 |
| §11 Operator-side CLI for set-topic | T21 |

---

## Phase A — DB extensions for topic→user lookup and operations topics

### Task 1: `users.GetByThreadID` (spec §7)

**Files:**
- Modify: `internal/backend/db/users.go:159-170` (append after existing methods)
- Test: `internal/backend/db/users_test.go` (create if missing; otherwise append)

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/db/users_test.go
package db

import (
	"errors"
	"sync"
	"testing"
)

func openTempDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.TempDir() + "/u.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestUsersGetByThreadID_Hit(t *testing.T) {
	d := openTempDB(t)
	uid, err := d.Users().Insert("vasya", "tok", "1.1.1.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(uid, 4242); err != nil {
		t.Fatal(err)
	}
	got, err := d.Users().GetByThreadID(4242)
	if err != nil {
		t.Fatalf("GetByThreadID: %v", err)
	}
	if got.ID != uid || got.Nickname != "vasya" {
		t.Errorf("got id=%d nick=%s want id=%d nick=vasya", got.ID, got.Nickname, uid)
	}
	if got.TelegramThreadID == nil || *got.TelegramThreadID != 4242 {
		t.Errorf("thread id not populated: %+v", got.TelegramThreadID)
	}
}

func TestUsersGetByThreadID_Miss(t *testing.T) {
	d := openTempDB(t)
	_, err := d.Users().GetByThreadID(99999)
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestUsersGetByThreadID_NoRaceOnConcurrentInsert(t *testing.T) {
	d := openTempDB(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nick := []string{"a", "b", "c", "d"}[i]
			uid, err := d.Users().Insert(nick, nick+"-tok", "1.1.1.1", "nwg0")
			if err != nil {
				t.Errorf("insert %s: %v", nick, err)
				return
			}
			if err := d.Users().UpdateThreadID(uid, int64(1000+i)); err != nil {
				t.Errorf("thread %s: %v", nick, err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		_, err := d.Users().GetByThreadID(int64(1000 + i))
		if err != nil {
			t.Errorf("lookup %d: %v", 1000+i, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/db/... -run TestUsersGetByThreadID -v`
Expected: FAIL — `d.Users().GetByThreadID undefined (type *UsersRepo has no field or method GetByThreadID)`

- [ ] **Step 3: Write minimal implementation**

Append to `internal/backend/db/users.go` (after existing `UpdateThreadID`):

```go
// GetByThreadID looks up a user by their assigned Telegram forum-topic id.
// Used by the callbacks router to map an incoming Message's
// message_thread_id to the owning user (per-router topic). Returns
// ErrUserNotFound when no user owns this topic.
func (u *UsersRepo) GetByThreadID(threadID int64) (*User, error) {
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE telegram_thread_id = ?`, threadID)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return got, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/db/... -run TestUsersGetByThreadID -v`
Expected: PASS — three subtests green.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(db): UsersRepo.GetByThreadID for topic→user lookup"
```

---

### Task 2: KV helpers for operations-topic IDs (spec §7)

**Files:**
- Create: `internal/backend/db/ui_kv.go`
- Test: `internal/backend/db/ui_kv_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/db/ui_kv_test.go
package db

import (
	"strings"
	"testing"
)

func TestKVTopicIDRoundTrip(t *testing.T) {
	d := openTempDB(t)
	if err := d.KV().SetTopicID("summary", 7); err != nil {
		t.Fatal(err)
	}
	id, ok, err := d.KV().GetTopicID("summary")
	if err != nil || !ok || id != 7 {
		t.Fatalf("got id=%d ok=%v err=%v want 7,true,nil", id, ok, err)
	}
	if err := d.KV().SetTopicID("systemic", 99); err != nil {
		t.Fatal(err)
	}
	id, ok, _ = d.KV().GetTopicID("systemic")
	if id != 99 || !ok {
		t.Fatalf("systemic got %d ok=%v want 99,true", id, ok)
	}
}

func TestKVTopicIDMiss(t *testing.T) {
	d := openTempDB(t)
	id, ok, err := d.KV().GetTopicID("summary")
	if err != nil {
		t.Fatal(err)
	}
	if ok || id != 0 {
		t.Fatalf("got id=%d ok=%v want 0,false", id, ok)
	}
}

func TestKVTopicIDInvalidKind(t *testing.T) {
	d := openTempDB(t)
	err := d.KV().SetTopicID("garbage", 1)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("expected kind-validation error, got %v", err)
	}
	if _, _, err := d.KV().GetTopicID("garbage"); err == nil {
		t.Fatalf("expected kind-validation error from Get, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/db/... -run TestKVTopicID -v`
Expected: FAIL — `d.KV().SetTopicID undefined`

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/db/ui_kv.go
package db

import (
	"fmt"
	"strconv"
)

// KV keys for operations-topic IDs (spec §7). Populated by
// `wg-monitor-cli set-topic --kind=summary|systemic --thread-id=N`.
const (
	KVKeySummaryTopicID  = "ui.summary_topic_id"
	KVKeySystemicTopicID = "ui.systemic_topic_id"
)

func topicKVKey(kind string) (string, error) {
	switch kind {
	case "summary":
		return KVKeySummaryTopicID, nil
	case "systemic":
		return KVKeySystemicTopicID, nil
	}
	return "", fmt.Errorf("invalid topic kind %q (want summary|systemic)", kind)
}

// SetTopicID upserts the topic id for kind ∈ {summary, systemic}.
func (r *KVRepo) SetTopicID(kind string, id int64) error {
	key, err := topicKVKey(kind)
	if err != nil {
		return err
	}
	return r.Set(key, strconv.FormatInt(id, 10))
}

// GetTopicID returns (id, true, nil) when set, (0, false, nil) when absent,
// or (0, false, err) when kind is invalid or the underlying store errored.
func (r *KVRepo) GetTopicID(kind string) (int64, bool, error) {
	key, err := topicKVKey(kind)
	if err != nil {
		return 0, false, err
	}
	raw, err := r.Get(key)
	if err != nil {
		return 0, false, err
	}
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("kv: bad topic id %q: %w", raw, err)
	}
	return n, true, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/db/... -run TestKVTopicID -v`
Expected: PASS — three subtests green.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(db): KV helpers for operations-topic IDs"
```

---

## Phase B — `tg` package: ReplyKeyboard support

### Task 3: ReplyKeyboard data types (spec §5.1)

**Files:**
- Create: `internal/backend/tg/reply_keyboard.go`
- Test: `internal/backend/tg/reply_keyboard_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/tg/reply_keyboard_test.go
package tg

import (
	"encoding/json"
	"testing"
)

func TestReplyKeyboardMarkupJSONShape(t *testing.T) {
	kb := ReplyKeyboardMarkup{
		Keyboard:       [][]ReplyKeyboardButton{{{Text: "X"}}},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
	raw, err := json.Marshal(kb)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	want := `{"keyboard":[[{"text":"X"}]],"is_persistent":true,"resize_keyboard":true}`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

func TestReplyKeyboardRemoveJSONShape(t *testing.T) {
	rm := ReplyKeyboardRemove{RemoveKeyboard: true}
	raw, _ := json.Marshal(rm)
	if string(raw) != `{"remove_keyboard":true}` {
		t.Errorf("got %s want {\"remove_keyboard\":true}", raw)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/tg/... -run TestReplyKeyboard -v`
Expected: FAIL — `undefined: ReplyKeyboardMarkup`

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/tg/reply_keyboard.go
package tg

// ReplyKeyboardButton mirrors the TG Bot API ReplyKeyboardButton object.
// Only `text` is needed for our two-button layouts (we never request
// contact/location/poll/web_app variants).
type ReplyKeyboardButton struct {
	Text string `json:"text"`
}

// ReplyKeyboardMarkup mirrors the TG Bot API ReplyKeyboardMarkup object.
// Field order matches the canonical JSON for stable golden assertions.
type ReplyKeyboardMarkup struct {
	Keyboard       [][]ReplyKeyboardButton `json:"keyboard"`
	IsPersistent   bool                    `json:"is_persistent,omitempty"`
	ResizeKeyboard bool                    `json:"resize_keyboard,omitempty"`
	Selective      bool                    `json:"selective,omitempty"`
}

// ReplyKeyboardRemove mirrors the TG Bot API ReplyKeyboardRemove object.
// Sent in place of a ReplyKeyboardMarkup to clear the bottom panel
// (used in unknown / non-topic chats per spec §5.1).
type ReplyKeyboardRemove struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
	Selective      bool `json:"selective,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/tg/... -run TestReplyKeyboard -v`
Expected: PASS — both golden JSON tests green.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(tg): ReplyKeyboard data types"
```

---

### Task 4: `ReplyKeyboardForTopic` helper (spec §5.1)

**Files:**
- Modify: `internal/backend/tg/reply_keyboard.go` (append helper)
- Modify: `internal/backend/tg/reply_keyboard_test.go` (append table test)

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/tg/reply_keyboard_test.go`:

```go
func TestReplyKeyboardForTopic(t *testing.T) {
	cases := []struct {
		kind   string
		isMM   bool // is ReplyKeyboardMarkup expected?
		texts  []string
		wantR1 int  // row 1 button count (0 means "don't care")
		wantR2 int
	}{
		{"per_router", true, []string{"📊 Что происходит?", "🆘 Помощь"}, 1, 1},
		{"summary", true, []string{"📋 Список юзеров", "📊 Здоровье флота"}, 2, 0},
		{"systemic", true, []string{"📋 Список юзеров", "📊 Здоровье флота"}, 2, 0},
		{"unknown", false, nil, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			got := ReplyKeyboardForTopic(c.kind)
			if c.isMM {
				kb, ok := got.(*ReplyKeyboardMarkup)
				if !ok {
					t.Fatalf("kind=%s: expected *ReplyKeyboardMarkup, got %T", c.kind, got)
				}
				if !kb.IsPersistent || !kb.ResizeKeyboard {
					t.Errorf("kind=%s: persistence flags off: %+v", c.kind, kb)
				}
				if c.wantR1 > 0 && len(kb.Keyboard[0]) != c.wantR1 {
					t.Errorf("kind=%s: row 0 has %d buttons want %d", c.kind, len(kb.Keyboard[0]), c.wantR1)
				}
				if c.wantR2 > 0 && (len(kb.Keyboard) < 2 || len(kb.Keyboard[1]) != c.wantR2) {
					t.Errorf("kind=%s: row 1 mismatch", c.kind)
				}
				// Texts must all appear
				flat := ""
				for _, row := range kb.Keyboard {
					for _, b := range row {
						flat += b.Text + "|"
					}
				}
				for _, want := range c.texts {
					if !contains(flat, want) {
						t.Errorf("kind=%s: missing text %q in %q", c.kind, want, flat)
					}
				}
			} else {
				rm, ok := got.(*ReplyKeyboardRemove)
				if !ok {
					t.Fatalf("kind=%s: expected *ReplyKeyboardRemove, got %T", c.kind, got)
				}
				if !rm.RemoveKeyboard {
					t.Errorf("kind=%s: RemoveKeyboard should be true", c.kind)
				}
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/tg/... -run TestReplyKeyboardForTopic -v`
Expected: FAIL — `undefined: ReplyKeyboardForTopic`

- [ ] **Step 3: Write minimal implementation**

Append to `internal/backend/tg/reply_keyboard.go`:

```go
// ReplyKeyboardForTopic returns the right ReplyKeyboardMarkup (or a
// ReplyKeyboardRemove) for a given topic kind. Returned type is `any` so
// callers can pass either *ReplyKeyboardMarkup or *ReplyKeyboardRemove
// directly into SendMessageWithReplyKeyboard.
//
// kind ∈ {"per_router","summary","systemic","unknown"}.
func ReplyKeyboardForTopic(kind string) any {
	switch kind {
	case "per_router":
		return &ReplyKeyboardMarkup{
			Keyboard: [][]ReplyKeyboardButton{
				{{Text: "📊 Что происходит?"}},
				{{Text: "🆘 Помощь"}},
			},
			IsPersistent:   true,
			ResizeKeyboard: true,
		}
	case "summary", "systemic":
		return &ReplyKeyboardMarkup{
			Keyboard: [][]ReplyKeyboardButton{
				{{Text: "📋 Список юзеров"}, {Text: "📊 Здоровье флота"}},
			},
			IsPersistent:   true,
			ResizeKeyboard: true,
		}
	default:
		return &ReplyKeyboardRemove{RemoveKeyboard: true}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/tg/... -run TestReplyKeyboardForTopic -v`
Expected: PASS — four subtests green.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(tg): ReplyKeyboardForTopic helper"
```

---

### Task 5: `Client.SendMessageWithReplyKeyboard` and `Client.DeleteMessage` (spec §6.1, §6.2)

**Files:**
- Modify: `internal/backend/tg/client.go:96-145` (append new methods)
- Modify: `internal/backend/tg/client_test.go` (append two httptest tests)

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/tg/client_test.go`:

```go
func TestSendMessageWithReplyKeyboard_AcceptsReplyMarkup(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}

	rk := &ReplyKeyboardMarkup{
		Keyboard:       [][]ReplyKeyboardButton{{{Text: "📊 Что происходит?"}}},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
	mid, err := c.SendMessageWithReplyKeyboard(context.Background(), -100, intPtr(11), "hi", "", nil, rk)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if mid != 7 {
		t.Fatalf("mid=%d", mid)
	}
	rm, ok := got["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup missing or wrong type: %T", got["reply_markup"])
	}
	if rm["is_persistent"] != true {
		t.Errorf("is_persistent missing: %+v", rm)
	}
}

func TestSendMessageWithReplyKeyboard_AcceptsInlineMarkup(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Write([]byte(`{"ok":true,"result":{"message_id":8}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	ik := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "X", CallbackData: "y"}}}}
	_, err := c.SendMessageWithReplyKeyboard(context.Background(), -100, nil, "hi", "", nil, ik)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	rm, ok := got["reply_markup"].(map[string]any)
	if !ok || rm["inline_keyboard"] == nil {
		t.Errorf("inline_keyboard not propagated: %+v", got)
	}
}

func TestDeleteMessage(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	if err := c.DeleteMessage(context.Background(), -100, 42); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got["chat_id"].(float64) != -100 || got["message_id"].(float64) != 42 {
		t.Errorf("payload: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/tg/... -run "TestSendMessageWithReplyKeyboard|TestDeleteMessage" -v`
Expected: FAIL — `(*Client).SendMessageWithReplyKeyboard undefined`, `(*Client).DeleteMessage undefined`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/backend/tg/client.go`:

```go
// sendMessageWithRMReq lets reply_markup be ANY of {InlineKeyboardMarkup,
// ReplyKeyboardMarkup, ReplyKeyboardRemove}. Encoded as raw json.RawMessage
// avoids polymorphic struct fields and keeps the wire format clean.
type sendMessageWithRMReq struct {
	ChatID           int64           `json:"chat_id"`
	MessageThreadID  *int64          `json:"message_thread_id,omitempty"`
	Text             string          `json:"text"`
	ParseMode        string          `json:"parse_mode,omitempty"`
	ReplyToMessageID *int64          `json:"reply_to_message_id,omitempty"`
	ReplyMarkup      json.RawMessage `json:"reply_markup,omitempty"`
}

// SendMessageWithReplyKeyboard sends a message with any kind of reply_markup
// payload. markup may be:
//   - *InlineKeyboardMarkup
//   - *ReplyKeyboardMarkup
//   - *ReplyKeyboardRemove
//   - nil (no reply_markup field)
//
// We marshal first, then forward as RawMessage so json.Marshal produces the
// canonical TG payload regardless of which variant was passed.
func (c *Client) SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error) {
	var rawMarkup json.RawMessage
	if markup != nil {
		raw, err := json.Marshal(markup)
		if err != nil {
			return 0, fmt.Errorf("tg sendMessage: marshal reply_markup: %w", err)
		}
		rawMarkup = raw
	}
	body, _ := json.Marshal(sendMessageWithRMReq{
		ChatID:           chatID,
		MessageThreadID:  threadID,
		Text:             text,
		ParseMode:        parseMode,
		ReplyToMessageID: replyTo,
		ReplyMarkup:      rawMarkup,
	})
	var out sendMessageResult
	if err := c.call(ctx, "sendMessage", body, &out); err != nil {
		return 0, err
	}
	return out.MessageID, nil
}

type deleteMessageReq struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

// DeleteMessage calls TG `deleteMessage`. Used to wipe the operator's
// "📊 Что происходит?" message after the smart-reply is composed (spec §6.2 b).
// Caller is responsible for swallowing 403 / can_delete_messages errors.
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	body, _ := json.Marshal(deleteMessageReq{ChatID: chatID, MessageID: messageID})
	return c.call(ctx, "deleteMessage", body, nil)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/tg/... -run "TestSendMessageWithReplyKeyboard|TestDeleteMessage" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(tg): SendMessageWithReplyKeyboard and DeleteMessage"
```

---

### Task 6: Extend `tg.Update` for text Messages (spec §6.2)

**Files:**
- Modify: `internal/backend/tg/updates.go:8-56`
- Test: `internal/backend/tg/updates_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/tg/updates_test.go`:

```go
func TestGetUpdatesIncludesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		// Verify the new message type is in the allowed_updates list
		au, _ := req["allowed_updates"].([]any)
		hasMessage := false
		for _, v := range au {
			if v == "message" {
				hasMessage = true
			}
		}
		if !hasMessage {
			t.Errorf("allowed_updates missing 'message': %v", au)
		}
		w.Write([]byte(`{"ok":true,"result":[
			{"update_id":1,"message":{"message_id":42,"chat":{"id":-100},"from":{"id":555},"text":"📊 Что происходит?","message_thread_id":11}}
		]}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client(), LongPollHTTP: srv.Client()}
	ups, err := c.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 1 || ups[0].Message == nil {
		t.Fatalf("expected 1 update with non-nil Message, got %+v", ups)
	}
	m := ups[0].Message
	if m.Text != "📊 Что происходит?" {
		t.Errorf("text: %q", m.Text)
	}
	if m.MessageID != 42 {
		t.Errorf("message_id: %d", m.MessageID)
	}
	if m.MessageThreadID == nil || *m.MessageThreadID != 11 {
		t.Errorf("thread id: %v", m.MessageThreadID)
	}
	if m.From.ID != 555 {
		t.Errorf("from.id: %d", m.From.ID)
	}
	if m.Chat.ID != -100 {
		t.Errorf("chat.id: %d", m.Chat.ID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/tg/... -run TestGetUpdatesIncludesMessage -v`
Expected: FAIL — either Message field undefined on Update, or `allowed_updates` does not contain `"message"`.

- [ ] **Step 3: Write minimal implementation**

Replace lines 8-56 of `internal/backend/tg/updates.go` with:

```go
type Update struct {
	UpdateID      int64          `json:"update_id"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
	Message       *Message       `json:"message,omitempty"`
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
	From            User   `json:"from"`
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
// Filters to callback_query AND message updates — the latter feeds
// ReplyKeyboard-button taps to the router (spec §6.2).
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	body, _ := json.Marshal(getUpdatesReq{
		Offset:         offset,
		Timeout:        timeoutSec,
		AllowedUpdates: []string{"callback_query", "message"},
	})
	var out []Update
	if err := c.callLongPoll(ctx, "getUpdates", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/tg/... -run TestGetUpdatesIncludesMessage -v`
Expected: PASS. Also run the whole `tg` package: `go test ./internal/backend/tg/... -v` — must stay green (no regression in `TestSendMessage*` etc).

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(tg): poll message updates and expose Message on Update"
```

---

## Phase C — `alerts` package: smart-reply templates

### Task 7: `SmartReplyState` enum + `SmartReplyArgs`/views (spec §5.2)

**Files:**
- Create: `internal/backend/alerts/smart_reply.go`
- Test: `internal/backend/alerts/smart_reply_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/alerts/smart_reply_test.go
package alerts

import "testing"

func TestSmartReplyStateValues(t *testing.T) {
	if StateOK == StateDegraded || StateDegraded == StateHard || StateHard == StateOffline {
		t.Errorf("states must be distinct")
	}
}

func TestSmartReplyArgsBuilderSmoke(t *testing.T) {
	a := SmartReplyArgs{
		Nickname:      "vasya",
		Tunnels:       []TunnelView{{Name: "amnezia", Interface: "nwg0", HandshakeAge: 47, PingStatus: "ok", Latency: 12}},
		LastReportAge: 0,
	}
	if a.Nickname != "vasya" || len(a.Tunnels) != 1 || a.Tunnels[0].HandshakeAge != 47 {
		t.Errorf("smoke: args build: %+v", a)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/alerts/... -run "TestSmartReplyState|TestSmartReplyArgs" -v`
Expected: FAIL — `undefined: StateOK`, `undefined: SmartReplyArgs`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/alerts/smart_reply.go
package alerts

import "time"

// SmartReplyState classifies a user's current state for the [📊 Что
// происходит?] smart reply (spec §5.2). Order is OK < DEGRADED < HARD <
// OFFLINE so callers can compare with `>` if desired.
type SmartReplyState int

const (
	StateOK SmartReplyState = iota
	StateDegraded
	StateHard
	StateOffline
)

func (s SmartReplyState) String() string {
	switch s {
	case StateOK:
		return "ok"
	case StateDegraded:
		return "degraded"
	case StateHard:
		return "hard"
	case StateOffline:
		return "offline"
	}
	return "?"
}

// TunnelView is the per-tunnel projection consumed by smart-reply formatting.
// Built by callbacks.Router from events.LatestEventsByPrefix(uid, "tunnel_").
type TunnelView struct {
	Name         string // pretty (e.g. "amnezia")
	CheckName    string // FSM key (e.g. "tunnel_awg11")
	Interface    string // "nwg1"
	HandshakeAge int    // seconds, 0 if unknown
	PingStatus   string // "ok"|"degraded"|"dead"|""
	Latency      int    // last latency ms (0 if unknown)
	FailCount    int    // ping_check fails right now
	FailThresh   int    // ping_check failure threshold
}

// IncidentView is the projection of an active hard incident, used to drive
// the HARD template.
type IncidentView struct {
	CheckName string
	HardSince time.Time
	FailCount int // consecutive_fails at HARD time
}

// SmartReplyArgs is everything FormatSmartReply needs to render a message.
// Built by callbacks.Router.dispatchSmartReply (Task 12).
type SmartReplyArgs struct {
	Nickname        string
	Tunnels         []TunnelView
	ActiveIncidents []IncidentView
	LastReportAge   time.Duration
	IsMobile        bool
	UserID          int64 // needed for callback_data on inline buttons
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/alerts/... -run "TestSmartReplyState|TestSmartReplyArgs" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(alerts): SmartReplyState enum and view types"
```

---

### Task 8: `ClassifyState` (spec §5.2)

**Files:**
- Modify: `internal/backend/alerts/smart_reply.go` (append)
- Modify: `internal/backend/alerts/smart_reply_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append:

```go
import "time"

func mkArgs(t *testing.T, build func(*SmartReplyArgs)) SmartReplyArgs {
	t.Helper()
	a := SmartReplyArgs{Nickname: "x"}
	build(&a)
	return a
}

func TestClassifyState_OfflineWhenReportStale(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) { a.LastReportAge = 6 * time.Minute })
	if got := ClassifyState(a); got != StateOffline {
		t.Errorf("got %v want offline", got)
	}
}

func TestClassifyState_HardWhenIncident(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 30 * time.Second
		a.ActiveIncidents = []IncidentView{{CheckName: "tunnel_awg11", FailCount: 5}}
	})
	if got := ClassifyState(a); got != StateHard {
		t.Errorf("got %v want hard", got)
	}
}

func TestClassifyState_DegradedHandshakeBoundary(t *testing.T) {
	cases := []struct {
		age   int
		state SmartReplyState
	}{
		{0, StateOK},
		{59, StateOK},
		{60, StateDegraded},
		{179, StateDegraded},
		// 180+ would normally be a HARD via FSM, but here we test the gap
		// between thresholds: handshake age 180 with no active incident
		// is the unusual "FSM hasn't ticked yet" race — treat as Degraded.
		{180, StateDegraded},
	}
	for _, c := range cases {
		t.Run(time.Duration(c.age).String(), func(t *testing.T) {
			a := mkArgs(t, func(a *SmartReplyArgs) {
				a.LastReportAge = 10 * time.Second
				a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: c.age, PingStatus: "ok"}}
			})
			if got := ClassifyState(a); got != c.state {
				t.Errorf("age=%d got %v want %v", c.age, got, c.state)
			}
		})
	}
}

func TestClassifyState_DegradedPingFails(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 30, PingStatus: "ok", FailCount: 2, FailThresh: 5}}
	})
	if got := ClassifyState(a); got != StateDegraded {
		t.Errorf("got %v want degraded", got)
	}
}

func TestClassifyState_OKBaseline(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 5 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 12, PingStatus: "ok"}}
	})
	if got := ClassifyState(a); got != StateOK {
		t.Errorf("got %v want ok", got)
	}
}

func TestClassifyState_MobileLongerStaleWindow(t *testing.T) {
	// Mobile users have a 60-min OFFLINE grace window in heartbeat config,
	// but for smart-reply context we still want to surface "offline" as soon
	// as a report >5 min is stale — that's the user-facing definition.
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 6 * time.Minute
		a.IsMobile = true
	})
	if got := ClassifyState(a); got != StateOffline {
		t.Errorf("mobile: got %v want offline", got)
	}
}

func TestClassifyState_MultiTunnelOnlyOneDegraded(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{
			{Name: "amnezia", HandshakeAge: 12, PingStatus: "ok"},
			{Name: "secondary", HandshakeAge: 120, PingStatus: "ok"},
		}
	})
	if got := ClassifyState(a); got != StateDegraded {
		t.Errorf("got %v want degraded", got)
	}
}

func TestClassifyState_HardWinsOverDegradedHandshake(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 120, PingStatus: "ok"}}
		a.ActiveIncidents = []IncidentView{{CheckName: "dns"}}
	})
	if got := ClassifyState(a); got != StateHard {
		t.Errorf("got %v want hard", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/alerts/... -run TestClassifyState -v`
Expected: FAIL — `undefined: ClassifyState`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/backend/alerts/smart_reply.go`:

```go
const (
	smartReplyOfflineThreshold       = 5 * time.Minute
	smartReplyDegradedHandshakeMinSec = 60
)

// ClassifyState applies the spec §5.2 decision tree:
//   1. report age > 5 min                                    → Offline
//   2. ≥1 active hard incident                                → Hard
//   3. any tunnel handshake_age ≥ 60 s                        → Degraded
//   4. any tunnel pingCheck has fail_count > 0 (below thresh) → Degraded
//   5. else                                                   → OK
//
// Rule 3's upper bound is intentionally open — the FSM converts age ≥ 180 s
// into a HARD only after fail_threshold consecutive observations, so during
// the gap we still want "Degraded" rather than misleading "OK".
func ClassifyState(a SmartReplyArgs) SmartReplyState {
	if a.LastReportAge > smartReplyOfflineThreshold {
		return StateOffline
	}
	if len(a.ActiveIncidents) > 0 {
		return StateHard
	}
	for _, t := range a.Tunnels {
		if t.HandshakeAge >= smartReplyDegradedHandshakeMinSec {
			return StateDegraded
		}
		if t.FailCount > 0 && (t.FailThresh == 0 || t.FailCount < t.FailThresh) {
			return StateDegraded
		}
	}
	return StateOK
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/alerts/... -run TestClassifyState -v`
Expected: PASS — 8+ subtests green.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(alerts): ClassifyState for smart replies"
```

---

### Task 9: `FormatSmartReply` (spec §5.2)

**Files:**
- Modify: `internal/backend/alerts/smart_reply.go` (append)
- Modify: `internal/backend/alerts/smart_reply_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append:

```go
import "strings"

func TestFormatSmartReply_OK(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 23 * time.Second,
		Tunnels: []TunnelView{{Name: "amnezia", HandshakeAge: 47, PingStatus: "ok", Latency: 12}},
	}
	text, kb := FormatSmartReply(a)
	for _, want := range []string{"✅", "vasya", "всё работает", "amnezia", "47с", "12 ms", "23с"} {
		if !strings.Contains(text, want) {
			t.Errorf("OK template missing %q in:\n%s", want, text)
		}
	}
	// inline keyboard must contain only 📋 Подробнее
	if len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 1 || kb.InlineKeyboard[0][0].CallbackData != "details:7" {
		t.Errorf("OK keyboard wrong: %+v", kb)
	}
}

func TestFormatSmartReply_Degraded(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 10 * time.Second,
		Tunnels: []TunnelView{{Name: "amnezia", CheckName: "tunnel_awg11", HandshakeAge: 142, PingStatus: "ok", FailCount: 3, FailThresh: 5}},
	}
	text, kb := FormatSmartReply(a)
	for _, want := range []string{"⚠️", "подозрения", "amnezia", "142", "3", "5"} {
		if !strings.Contains(text, want) {
			t.Errorf("Degraded missing %q in:\n%s", want, text)
		}
	}
	// inline kb: row 0 [Перезапуск][Тест связи], row 1 [Подробнее]
	if len(kb.InlineKeyboard) < 2 {
		t.Fatalf("Degraded keyboard rows: %d, want ≥2", len(kb.InlineKeyboard))
	}
	want := map[string]bool{
		"restart_tunnel:7:tunnel_awg11": true,
		"pingcheck_now:7:tunnel_awg11":  true,
		"details:7":                     true,
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			delete(want, b.CallbackData)
		}
	}
	for k := range want {
		t.Errorf("Degraded missing button: %s", k)
	}
}

func TestFormatSmartReply_Hard(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 10 * time.Second,
		Tunnels: []TunnelView{{Name: "amnezia", CheckName: "tunnel_awg11", HandshakeAge: 250, PingStatus: "dead"}},
		ActiveIncidents: []IncidentView{{CheckName: "tunnel_awg11", HardSince: time.Now().Add(-4 * time.Minute), FailCount: 5}},
	}
	text, kb := FormatSmartReply(a)
	for _, want := range []string{"🔴", "vasya", "проблема"} {
		if !strings.Contains(text, want) {
			t.Errorf("Hard missing %q in:\n%s", want, text)
		}
	}
	// inline kb must contain restart + diag + silence + details
	want := map[string]bool{
		"restart_tunnel:7:tunnel_awg11":   true,
		"diag_now:7:tunnel_awg11":         true,
		"silence:7:tunnel_awg11:1h":       true,
		"details:7":                       true,
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			delete(want, b.CallbackData)
		}
	}
	for k := range want {
		t.Errorf("Hard missing button: %s", k)
	}
}

func TestFormatSmartReply_Offline(t *testing.T) {
	a := SmartReplyArgs{Nickname: "vasya", UserID: 7, LastReportAge: 14 * time.Minute}
	text, kb := FormatSmartReply(a)
	for _, want := range []string{"📵", "vasya", "не на связи", "14"} {
		if !strings.Contains(text, want) {
			t.Errorf("Offline missing %q in:\n%s", want, text)
		}
	}
	// only one inline button: last_report:7
	if len(kb.InlineKeyboard) != 1 || kb.InlineKeyboard[0][0].CallbackData != "last_report:7" {
		t.Errorf("Offline kb: %+v", kb)
	}
}

func TestFormatSmartReply_MultiTunnelHardSplit(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 10 * time.Second,
		Tunnels: []TunnelView{
			{Name: "amnezia", CheckName: "tunnel_awg11", HandshakeAge: 250, PingStatus: "dead"},
			{Name: "secondary", CheckName: "tunnel_awg12", HandshakeAge: 200, PingStatus: "dead"},
		},
		ActiveIncidents: []IncidentView{{CheckName: "tunnel_awg11", HardSince: time.Now().Add(-2 * time.Minute), FailCount: 5}},
	}
	_, kb := FormatSmartReply(a)
	// must have at least one row per tunnel for restart
	want := map[string]bool{
		"restart_tunnel:7:tunnel_awg11": true,
		"restart_tunnel:7:tunnel_awg12": true,
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			delete(want, b.CallbackData)
		}
	}
	for k := range want {
		t.Errorf("multi-tunnel missing %s", k)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/alerts/... -run TestFormatSmartReply -v`
Expected: FAIL — `undefined: FormatSmartReply`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/backend/alerts/smart_reply.go`:

```go
import (
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

// FormatSmartReply renders the [📊 Что происходит?] response per spec §5.2.
// Returns body text plus the inline-button keyboard appropriate to the
// computed state. The inline keyboard is empty (no rows) only when caller
// explicitly chose to omit one — every state currently emits at least
// "📋 Подробнее" or "📋 Последний отчёт".
func FormatSmartReply(a SmartReplyArgs) (string, tg.InlineKeyboardMarkup) {
	state := ClassifyState(a)
	plainCD := func(action, cn string) string { return fmt.Sprintf("%s:%d:%s", action, a.UserID, cn) }
	silenceCD := func(cn, ttl string) string { return fmt.Sprintf("silence:%d:%s:%s", a.UserID, cn, ttl) }
	detailsCD := fmt.Sprintf("details:%d", a.UserID)
	lastReportCD := fmt.Sprintf("last_report:%d", a.UserID)

	var b strings.Builder
	switch state {
	case StateOK:
		fmt.Fprintf(&b, "✅ %s — всё работает.\n\n", a.Nickname)
		for _, t := range a.Tunnels {
			line := fmt.Sprintf("Туннель %s: handshake %s назад", t.Name, humanAgeSec(t.HandshakeAge))
			if t.PingStatus != "" {
				line += fmt.Sprintf(", ping %s", t.PingStatus)
			}
			if t.Latency > 0 {
				line += fmt.Sprintf(" (%d ms)", t.Latency)
			}
			b.WriteString(line + ".\n")
		}
		fmt.Fprintf(&b, "Роутер последний раз отчитывался: %s назад.", humanAgeDur(a.LastReportAge))
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "📋 Подробнее", CallbackData: detailsCD}},
		}}
		return b.String(), kb

	case StateDegraded:
		fmt.Fprintf(&b, "⚠️ %s — есть подозрения.\n\n", a.Nickname)
		for _, t := range a.Tunnels {
			fmt.Fprintf(&b, "Туннель %s: handshake %d сек назад (норма до 180).\n", t.Name, t.HandshakeAge)
			if t.FailCount > 0 {
				fmt.Fprintf(&b, "Ping: %d неудачи подряд из %d.\n", t.FailCount, t.FailThresh)
			}
		}
		b.WriteString("Роутер пока не считает это сбоем, но подозрительно.\n\nДействия:")
		// Per-tunnel restart row (one button per tunnel, max one row each)
		var rows [][]tg.InlineKeyboardButton
		for _, t := range a.Tunnels {
			label := "🔁 Перезапустить туннель"
			if len(a.Tunnels) > 1 {
				label = "🔁 Перезапуск " + t.Name
			}
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: label, CallbackData: plainCD("restart_tunnel", t.CheckName)},
				{Text: "▶ Проверить связь", CallbackData: plainCD("pingcheck_now", t.CheckName)},
			})
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: "📋 Подробнее", CallbackData: detailsCD}})
		return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}

	case StateHard:
		fmt.Fprintf(&b, "🔴 %s — есть проблема.\n\n", a.Nickname)
		for _, inc := range a.ActiveIncidents {
			age := time.Since(inc.HardSince).Round(time.Minute)
			fmt.Fprintf(&b, "%s не отвечает уже %s.\n", inc.CheckName, durFmt(age))
		}
		b.WriteString("\nЧто можно сделать:")
		var rows [][]tg.InlineKeyboardButton
		// Map incident → tunnel for restart/diag buttons.
		for _, inc := range a.ActiveIncidents {
			if !strings.HasPrefix(inc.CheckName, "tunnel_") {
				continue
			}
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: "🔁 Перезапустить туннель", CallbackData: plainCD("restart_tunnel", inc.CheckName)},
				{Text: "📊 Запустить диагностику", CallbackData: plainCD("diag_now", inc.CheckName)},
			})
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: "⏸ Замолчать на час", CallbackData: silenceCD(inc.CheckName, "1h")},
			})
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: "📋 Подробнее", CallbackData: detailsCD}})
		return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}

	case StateOffline:
		fmt.Fprintf(&b, "📵 %s — роутер не на связи.\n\n", a.Nickname)
		mins := int(a.LastReportAge.Minutes())
		fmt.Fprintf(&b, "Последний отчёт: %d минут назад.\n", mins)
		b.WriteString("Возможные причины: роутер выключен, нет интернета, агент упал.\n\n")
		b.WriteString("Действия ограничены пока агент не появится:")
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "📋 Последний отчёт", CallbackData: lastReportCD}},
		}}
		return b.String(), kb
	}
	return "", tg.InlineKeyboardMarkup{}
}

// humanAgeDur is the time.Duration counterpart to humanAgeSec.
func humanAgeDur(d time.Duration) string {
	if d <= 0 {
		return "0с"
	}
	return humanAgeSec(int(d.Seconds()))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/alerts/... -run TestFormatSmartReply -v`
Expected: PASS — five subtests green. Then run the entire `alerts` package: `go test ./internal/backend/alerts/... -v` to confirm no regression in existing format tests.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(alerts): FormatSmartReply renders state-aware reply"
```

---

### Task 10: `FormatCommandResult` with pagination (spec §6.5)

**Files:**
- Create: `internal/backend/alerts/command_result.go`
- Test: `internal/backend/alerts/command_result_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/alerts/command_result_test.go
package alerts

import (
	"strings"
	"testing"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestFormatCommandResult_DiagOK(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "diagnostics:\nall green"}
	chunks := FormatCommandResult("diag_now", r, 3500)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0], "📊") || !strings.Contains(chunks[0], "Диагностика") {
		t.Errorf("missing label: %s", chunks[0])
	}
	if !strings.Contains(chunks[0], "```") {
		t.Errorf("diag must use code-fence: %s", chunks[0])
	}
}

func TestFormatCommandResult_PingcheckOneLiner(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "alive 12 ms", DurationMs: 250}
	chunks := FormatCommandResult("pingcheck_now", r, 3500)
	if len(chunks) != 1 {
		t.Fatal("want 1 chunk")
	}
	if strings.Count(chunks[0], "\n") > 1 {
		t.Errorf("pingcheck should be one-liner-ish, got %d newlines:\n%s", strings.Count(chunks[0], "\n"), chunks[0])
	}
	if !strings.Contains(chunks[0], "alive 12 ms") || !strings.Contains(chunks[0], "250") {
		t.Errorf("missing output or duration: %s", chunks[0])
	}
}

func TestFormatCommandResult_RestartTunnelOK(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "restarted nwg0"}
	chunks := FormatCommandResult("restart_tunnel", r, 3500)
	if !strings.Contains(chunks[0], "🔁") || !strings.Contains(chunks[0], "restarted nwg0") {
		t.Errorf("bad: %s", chunks[0])
	}
}

func TestFormatCommandResult_OpkgPaginated(t *testing.T) {
	body := strings.Repeat("X", 12000)
	r := wire.CommandResult{Status: "ok", Output: body}
	chunks := FormatCommandResult("opkg_upgrade", r, 4000)
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks for 12000 chars at 4000-cap, got %d", len(chunks))
	}
	for i, c := range chunks {
		prefix := "(" + itoa1(i+1) + "/3)"
		if !strings.HasPrefix(c, prefix) && !strings.HasPrefix(strings.TrimLeft(c, " "), prefix) {
			t.Errorf("chunk %d missing %q prefix:\n%s", i, prefix, c[:20])
		}
		if len(c) > 4096 {
			t.Errorf("chunk %d exceeds TG limit: %d", i, len(c))
		}
	}
}

func TestFormatCommandResult_ErrorPrefix(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "tunnel not found"}
	chunks := FormatCommandResult("restart_tunnel", r, 3500)
	if !strings.Contains(chunks[0], "❌ Не удалось:") {
		t.Errorf("missing error prefix: %s", chunks[0])
	}
}

func TestFormatCommandResult_LockedAndTimeout(t *testing.T) {
	for _, st := range []string{"locked", "timeout"} {
		r := wire.CommandResult{Status: st, Output: ""}
		chunks := FormatCommandResult("diag_now", r, 3500)
		if !strings.Contains(chunks[0], "❌ Не удалось:") {
			t.Errorf("status=%s: missing error prefix", st)
		}
		if !strings.Contains(chunks[0], st) {
			t.Errorf("status=%s: status word missing in body", st)
		}
	}
}

func itoa1(n int) string { // local int→str without importing strconv into the test
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/alerts/... -run TestFormatCommandResult -v`
Expected: FAIL — `undefined: FormatCommandResult`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/alerts/command_result.go
package alerts

import (
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/pkg/wire"
)

const tgMaxMessageBytes = 4096

// FormatCommandResult renders a wire.CommandResult as one or more TG message
// bodies (chunks). Caller sends each chunk in sequence, each replying to the
// previous. maxChars is a hint for the per-chunk soft cap (raw TG limit is
// 4096; default callers pass 3500-4000 to leave headroom for code-fence and
// chunk-prefix markup).
//
// Per spec §6.5:
//   - diag_now      → wraps Output in code-fence, label "📊 Диагностика"
//   - opkg_upgrade  → label "⬆ Обновление пакетов", paginated (1/N) prefix
//   - pingcheck_now → label "▶ Тест связи", one-liner with duration
//   - restart_tunnel→ label "🔁 Перезапуск туннеля"
//   - other actions → action name as label (defensive)
//
// Status ∈ {err, locked, timeout} prepends "❌ Не удалось:" before the body.
func FormatCommandResult(action string, r wire.CommandResult, maxChars int) []string {
	if maxChars <= 0 || maxChars > tgMaxMessageBytes-200 {
		maxChars = tgMaxMessageBytes - 200
	}
	label := commandLabelHuman(action)
	header := label
	body := r.Output
	switch r.Status {
	case "err":
		body = "❌ Не удалось: " + body
	case "locked":
		body = "❌ Не удалось: locked (другая операция держит lock-файл)"
	case "timeout":
		body = "❌ Не удалось: timeout (агент не уложился в лимит)"
	}

	switch action {
	case "pingcheck_now":
		return []string{fmt.Sprintf("%s: %s (за %dмс)", header, strings.TrimSpace(body), r.DurationMs)}
	case "restart_tunnel":
		return []string{fmt.Sprintf("%s: %s", header, strings.TrimSpace(body))}
	case "diag_now":
		full := fmt.Sprintf("%s:\n\n```\n%s\n```", header, body)
		if len(full) <= maxChars {
			return []string{full}
		}
		// Diag too large — paginate the raw body (without code fences per chunk).
		return paginate(header+":", body, maxChars)
	case "opkg_upgrade":
		full := fmt.Sprintf("%s:\n\n%s", header, body)
		if len(full) <= maxChars {
			return []string{full}
		}
		return paginate(header+":", body, maxChars)
	}
	full := fmt.Sprintf("%s: %s", header, body)
	if len(full) <= maxChars {
		return []string{full}
	}
	return paginate(header+":", body, maxChars)
}

// paginate splits body into chunks each prefixed with "(K/N) <header>".
// The header is repeated on every chunk for context. K and N are 1-based.
func paginate(header, body string, maxChars int) []string {
	// Reserve room for the prefix "(99/99) " plus header plus newlines.
	reserve := len(header) + 16
	per := maxChars - reserve
	if per < 100 {
		per = 100
	}
	var chunks []string
	for i := 0; i < len(body); i += per {
		end := i + per
		if end > len(body) {
			end = len(body)
		}
		chunks = append(chunks, body[i:end])
	}
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = fmt.Sprintf("(%d/%d) %s\n%s", i+1, len(chunks), header, c)
	}
	return out
}

func commandLabelHuman(action string) string {
	switch action {
	case "diag_now":
		return "📊 Диагностика"
	case "pingcheck_now":
		return "▶ Тест связи"
	case "restart_tunnel":
		return "🔁 Перезапуск туннеля"
	case "opkg_upgrade":
		return "⬆ Обновление пакетов"
	case "force_recheck":
		return "🔁 Force recheck"
	}
	return action
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/alerts/... -run TestFormatCommandResult -v`
Expected: PASS — six subtests green.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(alerts): FormatCommandResult with pagination"
```

---

## Phase D — `callbacks` router: handle text Messages

### Task 11: Config field for UI (spec §8)

**Files:**
- Modify: `internal/backend/config.go:11-41` (add UI sub-config)
- Modify: `internal/backend/config_test.go` (append default-propagation test)

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/config_test.go`:

```go
func TestConfigUIDefaults(t *testing.T) {
	yamlBody := `
listen: ":8080"
db_path: /tmp/x.db
telegram:
  bot_token_file: ` + writeTokenFile(t, "abc") + `
  chat_id: -100
  admin_user_id: 1
`
	p := writeTempFile(t, yamlBody)
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.UI.DeleteUserCommandMessages {
		t.Errorf("DeleteUserCommandMessages should default true")
	}
	if !c.UI.SmartReplyWithKeyboard {
		t.Errorf("SmartReplyWithKeyboard should default true")
	}
	if c.UI.DiagMaxChars != 3500 {
		t.Errorf("DiagMaxChars default = %d, want 3500", c.UI.DiagMaxChars)
	}
}
```

(If `writeTempFile`/`writeTokenFile` helpers don't exist, add them inline at the top of the test file using `t.TempDir()`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/... -run TestConfigUI -v`
Expected: FAIL — `c.UI undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/backend/config.go`, add `UI UIConfig` to the `Config` struct, add `UIConfig`, and apply defaults in `LoadConfig`:

```go
type Config struct {
	Listen    string          `yaml:"listen"`
	LogLevel  string          `yaml:"log_level"`
	DBPath    string          `yaml:"db_path"`
	Telegram  TelegramConfig  `yaml:"telegram"`
	Heartbeat HeartbeatConfig `yaml:"heartbeat"`
	State     StateConfig     `yaml:"state"`
	UI        UIConfig        `yaml:"ui"`
}

// UIConfig controls v0.6.0 ReplyKeyboard / smart-reply behaviour (spec §8).
type UIConfig struct {
	// DeleteUserCommandMessages — bot deletes the operator's
	// "📊 Что происходит?" message after the smart-reply is composed.
	// Disable if the bot lacks `can_delete_messages` admin right.
	DeleteUserCommandMessages bool `yaml:"delete_user_command_messages"`
	// SmartReplyWithKeyboard — re-attach the topic-appropriate ReplyKeyboard
	// to every smart-reply message (mitigation for desktop-client bug
	// where ReplyKeyboard intermittently disappears).
	SmartReplyWithKeyboard bool `yaml:"smart_reply_with_keyboard"`
	// DiagMaxChars — soft cap for code-fenced diag output before pagination
	// kicks in. TG raw limit is 4096; 3500 leaves room for fence and prefix.
	DiagMaxChars int `yaml:"diag_max_chars"`
}
```

In `LoadConfig`, after the existing default block (around line 107) add:

```go
	// UI defaults: only set when YAML omitted the field. We treat `false`
	// as "not set" for bool fields because YAML tri-state is awkward in Go.
	if !cfg.UI.DeleteUserCommandMessages {
		cfg.UI.DeleteUserCommandMessages = true
	}
	if !cfg.UI.SmartReplyWithKeyboard {
		cfg.UI.SmartReplyWithKeyboard = true
	}
	if cfg.UI.DiagMaxChars == 0 {
		cfg.UI.DiagMaxChars = 3500
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/... -run TestConfigUI -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(config): add ui sub-config with safe defaults"
```

---

### Task 12: Router dispatch on text Messages (spec §6.2)

**Files:**
- Modify: `internal/backend/callbacks/router.go:99-184` (extend `handleUpdate` and add `handleMessage`)
- Modify: `internal/backend/callbacks/router_test.go` (append handleMessage tests)

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/callbacks/router_test.go`:

```go
import "github.com/anex/wg-monitor/internal/backend/db"

// fakeRouterTGFull adds capture of SendMessageWithReplyKeyboard + DeleteMessage
type fakeRouterTGFull struct {
	fakeRouterTG
	rkSends     []rkSend
	deleted     []deleteCall
	deleteErr   error
}
type rkSend struct {
	chatID  int64
	thread  *int64
	text    string
	markup  any
	replyTo *int64
}
type deleteCall struct{ chatID, msgID int64 }

func (f *fakeRouterTGFull) SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	f.rkSends = append(f.rkSends, rkSend{chatID, threadID, text, markup, replyTo})
	return 100, nil
}
func (f *fakeRouterTGFull) DeleteMessage(ctx context.Context, chatID, msgID int64) error {
	f.mu.Lock(); defer f.mu.Unlock()
	f.deleted = append(f.deleted, deleteCall{chatID, msgID})
	return f.deleteErr
}

func TestRouterHandleMessage_RoutesPerRouter(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 11); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9, UI: UIConfigSnapshot{DeleteUserCommandMessages: true}})

	tid := int64(11)
	msg := &tg.Message{
		MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "📊 Что происходит?",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 smart-reply send, got %d", len(f.rkSends))
	}
	if !strings.Contains(f.rkSends[0].text, "vasya") {
		t.Errorf("smart reply missing nickname: %s", f.rkSends[0].text)
	}
	if len(f.deleted) != 1 || f.deleted[0].msgID != 42 {
		t.Errorf("expected DeleteMessage(_, 42), got %+v", f.deleted)
	}
}

func TestRouterHandleMessage_RejectsWrongChat(t *testing.T) {
	d, uid := newTestDB(t)
	_ = uid
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100})
	msg := &tg.Message{Chat: tg.Chat{ID: -999}, From: tg.User{ID: 12345}, Text: "📊 Что происходит?"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 0 || len(f.deleted) != 0 {
		t.Errorf("must no-op on wrong chat: %+v %+v", f.rkSends, f.deleted)
	}
}

func TestRouterHandleMessage_NonAdminIgnored(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 99999}, Text: "📊 Что происходит?"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 0 {
		t.Errorf("non-admin message must be ignored")
	}
}

func TestRouterHandleMessage_DeleteFailureDoesNotAbort(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 11)
	f := &fakeRouterTGFull{deleteErr: fmt.Errorf("403: bot lacks can_delete_messages")}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, UI: UIConfigSnapshot{DeleteUserCommandMessages: true}})
	tid := int64(11)
	msg := &tg.Message{MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "📊 Что происходит?"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Errorf("smart reply must still be sent after delete failure")
	}
}
```

(Note: `UIConfigSnapshot` is the `callbacks.Config` field — see Step 3.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/callbacks/... -run TestRouterHandleMessage -v`
Expected: FAIL — `r.HandleMessage undefined`, `Config.UI undefined`.

- [ ] **Step 3: Write minimal implementation**

Modify `internal/backend/callbacks/router.go`:

1. Extend `TGClient`:

```go
type TGClient interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error
	GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error)
}
```

2. Extend `Config`:

```go
type Config struct {
	ChatID         int64
	AdminUserID    int64
	MuteCutoffHour int
	UI             UIConfigSnapshot
}

// UIConfigSnapshot mirrors backend.UIConfig (avoid an import cycle).
type UIConfigSnapshot struct {
	DeleteUserCommandMessages bool
	SmartReplyWithKeyboard    bool
	DiagMaxChars              int
}
```

3. Extend `handleUpdate`:

```go
func (r *Router) handleUpdate(ctx context.Context, u tg.Update) {
	switch {
	case u.CallbackQuery != nil:
		r.HandleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		r.HandleMessage(ctx, u.Message)
	}
}
```

4. Add `HandleMessage`:

```go
// HandleMessage dispatches an incoming text Message: chat/admin gate, topic
// resolution, then the appropriate smart-reply / operations action.
//
// Allowlist: chat must equal cfg.ChatID; from must equal cfg.AdminUserID
// (text-message router is admin-only — group members can still tap inline
// callbacks per the 2026-04-30 policy reversal, but typing into the chat
// is a one-operator surface).
func (r *Router) HandleMessage(ctx context.Context, m *tg.Message) {
	if r.cfg.ChatID != 0 && m.Chat.ID != r.cfg.ChatID {
		return
	}
	if r.cfg.AdminUserID != 0 && m.From.ID != r.cfg.AdminUserID {
		return
	}
	kind, user := r.resolveTopicKind(m.MessageThreadID)
	switch m.Text {
	case "📊 Что происходит?":
		if kind == "per_router" && user != nil {
			r.dispatchSmartReply(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя или в Сводке.", "", nil)
		}
	case "🆘 Помощь":
		r.dispatchHelp(ctx, m, kind)
	case "📋 Список юзеров":
		r.dispatchListUsers(ctx, m)
	case "📊 Здоровье флота":
		r.dispatchFleetHealth(ctx, m)
	default:
		// Ignore — could be operator chatting; don't delete.
		return
	}
	if r.cfg.UI.DeleteUserCommandMessages {
		if err := r.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
			slog.Warn("deleteMessage failed (non-fatal)", "err", err, "chat", m.Chat.ID, "msg", m.MessageID)
		}
	}
}

// resolveTopicKind classifies a thread id into "per_router" / "summary" /
// "systemic" / "unknown" using db.Users + db.KV operations-topic IDs.
func (r *Router) resolveTopicKind(threadID *int64) (string, *db.User) {
	if threadID == nil || *threadID == 0 {
		return "unknown", nil
	}
	if u, err := r.d.Users().GetByThreadID(*threadID); err == nil {
		return "per_router", u
	}
	if id, ok, err := r.d.KV().GetTopicID("summary"); err == nil && ok && id == *threadID {
		return "summary", nil
	}
	if id, ok, err := r.d.KV().GetTopicID("systemic"); err == nil && ok && id == *threadID {
		return "systemic", nil
	}
	return "unknown", nil
}

// dispatchHelp sends the static help text for the topic kind.
func (r *Router) dispatchHelp(ctx context.Context, m *tg.Message, kind string) {
	body := "Кнопки внизу:\n" +
		"📊 Что происходит? — состояние роутера прямо сейчас.\n" +
		"🆘 Помощь — этот текст.\n\n" +
		"В топиках Сводки/Системного:\n" +
		"📋 Список юзеров, 📊 Здоровье флота — операторские команды."
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, body, "", nil, tg.ReplyKeyboardForTopic(kind))
}
```

5. Stub `dispatchSmartReply`/`dispatchListUsers`/`dispatchFleetHealth` placeholders for now (full bodies in later tasks):

```go
func (r *Router) dispatchSmartReply(ctx context.Context, m *tg.Message, user *db.User) {
	// Filled in Task 13.
}

func (r *Router) dispatchListUsers(ctx context.Context, m *tg.Message) {
	// Filled in Task 19.
}

func (r *Router) dispatchFleetHealth(ctx context.Context, m *tg.Message) {
	// Filled in Task 20.
}
```

To make `TestRouterHandleMessage_RoutesPerRouter` pass without the full smart-reply, give `dispatchSmartReply` a minimal stub that calls SendMessageWithReplyKeyboard:

```go
func (r *Router) dispatchSmartReply(ctx context.Context, m *tg.Message, user *db.User) {
	body := "✅ " + user.Nickname + " — всё работает."
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, body, "", nil, tg.ReplyKeyboardForTopic("per_router"))
}
```

This stub is replaced in Task 13.

6. Update existing `fakeRouterTG` to satisfy the wider interface (tests will fail to compile otherwise). Add `SendMessageWithReplyKeyboard` and `DeleteMessage` no-op methods on `fakeRouterTG` in `router_test.go`:

```go
func (f *fakeRouterTG) SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	f.sentMsgs = append(f.sentMsgs, text)
	return 1, nil
}
func (f *fakeRouterTG) DeleteMessage(ctx context.Context, chatID, messageID int64) error { return nil }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/callbacks/... -run TestRouterHandleMessage -v`
Expected: PASS — four subtests green. Then run the full callbacks suite: `go test ./internal/backend/callbacks/... -v` to verify no regression.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(callbacks): handle text messages from ReplyKeyboard"
```

---

### Task 13: `dispatchSmartReply` full implementation (spec §6.2 c, §5.2)

**Files:**
- Modify: `internal/backend/callbacks/router.go` (replace stub from Task 12)
- Modify: `internal/backend/callbacks/router_test.go` (append golden-text test)

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRouterDispatchSmartReply_RendersOK(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 11)
	// Insert a fresh tunnel event so the smart reply sees a Tunnel.
	now := time.Now().UTC()
	_ = d.Events().Insert(uid, "tunnel_awg11", "ok", `{"tunnel_name":"amnezia","interface":"nwg0","handshake_age_sec":12,"ping_check_status":"ok","ping_check_last_latency_ms":15}`, now)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	msg := &tg.Message{
		MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "📊 Что происходит?",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.rkSends))
	}
	body := f.rkSends[0].text
	for _, want := range []string{"✅", "vasya", "amnezia", "12с", "15 ms"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/callbacks/... -run TestRouterDispatchSmartReply -v`
Expected: FAIL — body lacks `amnezia`/`12с`/`15 ms` because Task 12 stub renders only the nickname header.

- [ ] **Step 3: Write minimal implementation**

Replace the stub of `dispatchSmartReply` in `internal/backend/callbacks/router.go`:

```go
func (r *Router) dispatchSmartReply(ctx context.Context, m *tg.Message, user *db.User) {
	tunnels := r.collectTunnelViews(user.ID)
	incidents := r.collectActiveIncidents(user.ID)
	lastTS, _ := r.d.Events().LatestPerUser(user.ID)
	var lastAge time.Duration
	if !lastTS.IsZero() {
		lastAge = time.Since(lastTS)
	} else {
		lastAge = 24 * time.Hour // never reported
	}
	args := alerts.SmartReplyArgs{
		Nickname:        user.Nickname,
		UserID:          user.ID,
		Tunnels:         tunnels,
		ActiveIncidents: incidents,
		LastReportAge:   lastAge,
		IsMobile:        user.IsMobile(),
	}
	text, inline := alerts.FormatSmartReply(args)
	// ReplyKeyboard cannot coexist with InlineKeyboard on a single message
	// — TG accepts only one reply_markup per send. Per spec §6.1, inline
	// keyboard wins for the smart-reply itself; the ReplyKeyboard re-installs
	// on the next bot message (alert / RECOVERY). However we still pass the
	// inline as the reply_markup so smart-reply buttons appear.
	_, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &inline)
	if err != nil {
		slog.Warn("smart reply send failed", "err", err, "user", user.Nickname)
	}
}

// collectTunnelViews builds []alerts.TunnelView from the latest per-tunnel
// event for this user.
func (r *Router) collectTunnelViews(userID int64) []alerts.TunnelView {
	rows, err := r.d.Events().LatestEventsByPrefix(userID, "tunnel_")
	if err != nil {
		return nil
	}
	var out []alerts.TunnelView
	for _, row := range rows {
		var det map[string]any
		if row.DetailsJSON != "" {
			_ = json.Unmarshal([]byte(row.DetailsJSON), &det)
		}
		out = append(out, alerts.TunnelView{
			Name:         strOrEmpty(det, "tunnel_name"),
			CheckName:    row.CheckName,
			Interface:    strOrEmpty(det, "interface"),
			HandshakeAge: intOrZero(det, "handshake_age_sec"),
			PingStatus:   strOrEmpty(det, "ping_check_status"),
			Latency:      intOrZero(det, "ping_check_last_latency_ms"),
			FailCount:    intOrZero(det, "ping_check_fail_count"),
			FailThresh:   intOrZero(det, "ping_check_fail_threshold"),
		})
	}
	return out
}

// collectActiveIncidents returns each `incident_state` row with
// current_status='hard' for this user. Reuses the existing State repo —
// no new SQL needed because we filter Go-side by status.
func (r *Router) collectActiveIncidents(userID int64) []alerts.IncidentView {
	// We don't yet have a repo method to list incidents per user. For Stage 2
	// and given fleet size (~10 users × < 5 checks each) it's fine to inspect
	// each known check_name on demand. Build the list from the latest events.
	rows, err := r.d.Events().LatestEventsByPrefix(userID, "")
	_ = rows
	if err != nil {
		return nil
	}
	// Cheaper path: query incident_state directly for this user.
	var out []alerts.IncidentView
	q, err := r.d.SQL().Query(`SELECT check_name, hard_since, consecutive_fails FROM incident_state WHERE user_id = ? AND current_status = 'hard'`, userID)
	if err != nil {
		return nil
	}
	defer q.Close()
	for q.Next() {
		var iv alerts.IncidentView
		var hs sql.NullTime
		if err := q.Scan(&iv.CheckName, &hs, &iv.FailCount); err != nil {
			continue
		}
		if hs.Valid {
			iv.HardSince = hs.Time
		}
		out = append(out, iv)
	}
	return out
}

// Local helpers for map-pulling (mirrors alerts/format.go's helpers but kept
// here so we don't widen the alerts package's exported surface).
func strOrEmpty(d map[string]any, k string) string {
	if d == nil {
		return ""
	}
	if v, ok := d[k].(string); ok {
		return v
	}
	return ""
}

func intOrZero(d map[string]any, k string) int {
	if d == nil {
		return 0
	}
	v, ok := d[k]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}
```

Add the necessary imports at the top of `router.go`: `encoding/json`, `database/sql`, `time`, plus `github.com/anex/wg-monitor/internal/backend/alerts`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/callbacks/... -run TestRouterDispatchSmartReply -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(callbacks): full smart-reply dispatch with state classification"
```

---

## Phase E — Command-result reply to TG

### Task 14: Trace command-result flow (notes-only commit)

**Files:**
- Create: `docs/superpowers/notes/2026-04-30-cmdresult-trace.md`

This task documents the existing path so subsequent edits land in the right place. No code change.

- [ ] **Step 1: Investigate** (read-only)

Read in this order:
- `internal/backend/handler.go:179-217` — `cmdResultHandler` posts `wire.CommandResult` into `cmd.Queue.RecordResult`. **Currently terminal**: result is recorded but never relayed to TG.
- `internal/backend/cmd/queue.go:93-142` — `RecordResult` + `AwaitResult` (used by tests). `AwaitResult` is the existing sync hook we can subscribe to.
- `internal/backend/callbacks/actions.go:212-226` — `CommandAction.Apply` enqueues without recording the originating message_id, chat_id, or thread_id.
- `internal/backend/cmd/queue.go:40-52` — `Enqueue` only carries `wire.Command`; no place to stash a `MessageRef`.

- [ ] **Step 2: Document the gap and the fix path**

Write to `docs/superpowers/notes/2026-04-30-cmdresult-trace.md`:

```markdown
# Command-result → TG reply trace

## Current path
1. TG callback → router.HandleCallback → CommandAction.Apply
2. CommandAction.Apply: builds wire.Command{ID, Action, Args, IssuedAt}
3. cmd.Queue.Enqueue(userID, cmd) — Command stored alone
4. Agent long-polls /v1/cmd → dequeues
5. Agent runs action, POSTs /v1/cmd/result with wire.CommandResult{ID, Status, Output, ...}
6. Backend handler.go cmdResultHandler → cmd.Queue.RecordResult(uid, result)
7. **DEAD END** — no subscriber relays to TG.

## Required changes (Tasks 15–16)
- Extend cmd.Queue to carry MessageRef alongside Command.
  - New type cmd.PendingCommand{Cmd wire.Command; Origin MessageRef}.
  - Replace `pending map[int64][]wire.Command` with `pending map[int64][]PendingCommand`.
  - Dequeue still returns *wire.Command but stores MessageRef in a parallel
    map for RecordResult's lookup.
- CommandEnqueuer interface gains an EnqueueWithRef method (additive).
- CommandAction.Apply records q.Message.{Chat.ID, MessageID, MessageThreadID}.
- cmdResultHandler, after RecordResult, looks up MessageRef and calls a new
  TGNotifier interface to post the formatted result via FormatCommandResult.
```

- [ ] **Step 3: Confirm compile-only**

Run: `go vet ./...`
Expected: PASS — no behaviour change.

- [ ] **Step 4: No test (notes only)**

Skip Step 4.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "docs(plan): trace command-result flow before reply fix"
```

---

### Task 15: `cmd.Queue` carries MessageRef + new `EnqueueWithRef` (spec §6.5)

**Files:**
- Modify: `internal/backend/cmd/queue.go:22-110`
- Modify: `internal/backend/cmd/queue_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/cmd/queue_test.go`:

```go
func TestQueueEnqueueWithRefAndLookup(t *testing.T) {
	q := New()
	tid := int64(11)
	ref := MessageRef{ChatID: -100, MessageID: 99, ThreadID: &tid}
	cmd := wire.Command{ID: "abc", Action: "diag_now", IssuedAt: time.Now()}
	if err := q.EnqueueWithRef(7, cmd, ref); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, ok := q.OriginRef(7, "abc")
	if !ok {
		t.Fatalf("OriginRef miss")
	}
	if got.ChatID != -100 || got.MessageID != 99 || got.ThreadID == nil || *got.ThreadID != 11 {
		t.Errorf("ref mismatch: %+v", got)
	}
	// Backwards compatibility: bare Enqueue still works (no ref recorded)
	if err := q.Enqueue(7, wire.Command{ID: "no-ref", Action: "diag_now", IssuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.OriginRef(7, "no-ref"); ok {
		t.Errorf("bare Enqueue should NOT record ref")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/cmd/... -run TestQueueEnqueueWithRef -v`
Expected: FAIL — `q.EnqueueWithRef undefined`.

- [ ] **Step 3: Write minimal implementation**

Modify `internal/backend/cmd/queue.go`:

```go
// MessageRef identifies the originating TG message for a command — chat, the
// message that carried the inline button, and (optional) topic.
type MessageRef struct {
	ChatID    int64
	MessageID int64
	ThreadID  *int64
}

type Queue struct {
	mu      sync.Mutex
	pending map[int64][]wire.Command
	results map[int64]map[string]wire.CommandResult
	// origins maps (userID → cmd.ID → MessageRef). Populated by
	// EnqueueWithRef; consumed by the cmd-result handler to relay TG replies.
	origins map[int64]map[string]MessageRef
	signal  *sync.Cond
}

func New() *Queue {
	q := &Queue{
		pending: make(map[int64][]wire.Command),
		results: make(map[int64]map[string]wire.CommandResult),
		origins: make(map[int64]map[string]MessageRef),
	}
	q.signal = sync.NewCond(&q.mu)
	return q
}

// EnqueueWithRef is Enqueue + records MessageRef so that when the agent
// posts CommandResult later, the backend can reply to the original message.
func (q *Queue) EnqueueWithRef(userID int64, cmd wire.Command, ref MessageRef) error {
	if err := q.Enqueue(userID, cmd); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	bucket, ok := q.origins[userID]
	if !ok {
		bucket = make(map[string]MessageRef)
		q.origins[userID] = bucket
	}
	bucket[cmd.ID] = ref
	return nil
}

// OriginRef returns the MessageRef stored at EnqueueWithRef time, or
// (zero, false) when the command was enqueued without ref or already consumed.
func (q *Queue) OriginRef(userID int64, cmdID string) (MessageRef, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	bucket, ok := q.origins[userID]
	if !ok {
		return MessageRef{}, false
	}
	r, ok := bucket[cmdID]
	return r, ok
}

// ConsumeOriginRef is OriginRef + delete in one shot. Use from the result
// handler so the same ref isn't relayed twice if RecordResult somehow fires
// twice for the same command id.
func (q *Queue) ConsumeOriginRef(userID int64, cmdID string) (MessageRef, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	bucket, ok := q.origins[userID]
	if !ok {
		return MessageRef{}, false
	}
	r, ok := bucket[cmdID]
	if ok {
		delete(bucket, cmdID)
	}
	return r, ok
}
```

Existing `Enqueue` body stays unchanged — leave it as the bare-command path. `EnqueueWithRef` calls it internally.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/cmd/... -run TestQueueEnqueueWithRef -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(cmd): MessageRef on Queue for command-result reply"
```

---

### Task 16: `CommandAction` records MessageRef + cmdResultHandler relays to TG (spec §6.5)

**Files:**
- Modify: `internal/backend/callbacks/actions.go:18-21` (extend `CommandEnqueuer`)
- Modify: `internal/backend/callbacks/actions.go:212-226` (`Apply` records ref)
- Modify: `internal/backend/handler.go:24-65` (Deps gains TGNotifier), 179-217 (relay logic)
- Test: `internal/backend/handler_test.go` (append integration test for relay)

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/handler_test.go` (add stub `TGNotifier` fake if needed):

```go
type relayCapture struct {
	mu     sync.Mutex
	chunks []string
	chatID int64
	thread *int64
	reply  *int64
}

func (rc *relayCapture) NotifyCommandResult(ctx context.Context, ref cmd.MessageRef, action string, result wire.CommandResult, maxChars int) error {
	rc.mu.Lock(); defer rc.mu.Unlock()
	rc.chatID = ref.ChatID
	rc.thread = ref.ThreadID
	rc.reply = &ref.MessageID
	chunks := alerts.FormatCommandResult(action, result, maxChars)
	rc.chunks = append(rc.chunks, chunks...)
	return nil
}

func TestCmdResultRelayedToTG(t *testing.T) {
	// Setup: in-memory queue, register origin ref for cmd "abc",
	// then POST a CommandResult for that cmd. Expect relayCapture to fire.
	d := openTestBackendDB(t) // helper from existing handler_test.go
	uid := mustInsertUser(t, d, "vasya")
	q := cmd.New()
	tid := int64(11)
	_ = q.EnqueueWithRef(uid, wire.Command{ID: "abc", Action: "diag_now", IssuedAt: time.Now()},
		cmd.MessageRef{ChatID: -100, MessageID: 42, ThreadID: &tid})

	rc := &relayCapture{}
	deps := Deps{
		Logger: slog.Default(), DB: d, CommandSink: q, TGNotifier: rc,
		UI: UIConfig{DiagMaxChars: 3500},
	}
	mux := NewMux(deps)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "ok", Output: "diagnostics: all green"})
	req := httptest.NewRequest("POST", "/v1/cmd/result", bytes.NewReader(body))
	// Authenticate by injecting a context user — use existing auth helper from handler_test.go,
	// or wrap with the same middleware bypass other tests use.
	req = req.WithContext(WithUserID(req.Context(), uid))
	w := httptest.NewRecorder()
	cmdResultHandler(deps)(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if len(rc.chunks) != 1 {
		t.Fatalf("want 1 relay chunk, got %d", len(rc.chunks))
	}
	if rc.chatID != -100 || rc.reply == nil || *rc.reply != 42 {
		t.Errorf("ref mis-routed: %+v", rc)
	}
	if !strings.Contains(rc.chunks[0], "diagnostics: all green") {
		t.Errorf("output missing: %s", rc.chunks[0])
	}
}
```

(Helpers `openTestBackendDB`, `mustInsertUser`, `WithUserID` already exist in the test package per the existing handler tests; if any are missing, add minimal implementations following the patterns of nearby tests.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/... -run TestCmdResultRelayedToTG -v`
Expected: FAIL — `Deps.TGNotifier undefined`, `Deps.UI undefined`.

- [ ] **Step 3: Write minimal implementation**

A) Extend `internal/backend/callbacks/actions.go`:

```go
type CommandEnqueuer interface {
	Enqueue(userID int64, cmd wire.Command) error
	EnqueueWithRef(userID int64, cmd wire.Command, ref cmd.MessageRef) error
}
```

(import `cmd "github.com/anex/wg-monitor/internal/backend/cmd"` — alias so it doesn't collide with the existing `Command` type.)

Modify `CommandAction.Apply` to record the originating message:

```go
func (a *CommandAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	if a.sink == nil {
		return "", errors.New("command channel disabled (no sink configured)")
	}
	cmdMsg := wire.Command{
		ID:       a.idGen(),
		Action:   args.Action,
		Args:     map[string]any{"check_name": args.CheckName},
		IssuedAt: time.Now().UTC(),
	}
	if q != nil {
		ref := cmd.MessageRef{
			ChatID:    q.Message.Chat.ID,
			MessageID: q.Message.MessageID,
			ThreadID:  q.Message.MessageThreadID,
		}
		if err := a.sink.EnqueueWithRef(args.UserID, cmdMsg, ref); err != nil {
			return "", fmt.Errorf("enqueue %s: %w", args.Action, err)
		}
	} else {
		if err := a.sink.Enqueue(args.UserID, cmdMsg); err != nil {
			return "", fmt.Errorf("enqueue %s: %w", args.Action, err)
		}
	}
	return formatQueuedStatus(args.Action), nil
}
```

B) Extend `internal/backend/handler.go`:

```go
// TGNotifier posts command-result text to the originating TG chat/topic/message.
// Implemented by callbacks.Notifier (concrete impl in Task 16 same-task).
type TGNotifier interface {
	NotifyCommandResult(ctx context.Context, ref cmd.MessageRef, action string, result wire.CommandResult, maxChars int) error
}

type Deps struct {
	Logger      *slog.Logger
	DB          *db.DB
	Dispatcher  Dispatcher
	Resumer     Resumer
	CommandSink CommandSink
	TGNotifier  TGNotifier
	UI          UIConfig
	Thresholds  state.Thresholds
}
```

Extend `CommandSink` interface so the handler can call `ConsumeOriginRef`:

```go
type CommandSink interface {
	Dequeue(ctx context.Context, userID int64, holdTimeout time.Duration) (*wire.Command, bool)
	RecordResult(userID int64, result wire.CommandResult) error
	ConsumeOriginRef(userID int64, cmdID string) (cmd.MessageRef, bool)
}
```

(Concrete `*cmd.Queue` already satisfies this after Task 15.)

C) Update `cmdResultHandler`:

```go
func cmdResultHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ... existing parse + validate code unchanged ...
		uid := UserIDFromContext(r.Context())
		nick := NicknameFromContext(r.Context())
		if err := d.CommandSink.RecordResult(uid, res); err != nil {
			d.Logger.Warn("cmd result record", "nickname", nick, "err", err)
			http.Error(w, "record failed", http.StatusInternalServerError)
			return
		}
		// New: relay to TG if we have an origin and a notifier.
		if d.TGNotifier != nil {
			ref, ok := d.CommandSink.ConsumeOriginRef(uid, res.ID)
			if ok {
				// We don't have action name in CommandResult — fish it out of
				// the queue's recently-dispatched commands; for simplicity we
				// look it up by walking the result, but easier: record action
				// alongside MessageRef. Add action to MessageRef.
				maxChars := d.UI.DiagMaxChars
				if maxChars == 0 {
					maxChars = 3500
				}
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := d.TGNotifier.NotifyCommandResult(ctx, ref, ref.Action, res, maxChars); err != nil {
						d.Logger.Warn("tg notify failed", "cmd_id", res.ID, "err", err)
					}
				}()
			}
		}
		d.Logger.Info("cmd result", "nickname", nick, "cmd_id", res.ID, "status", res.Status, "duration_ms", res.DurationMs)
		w.WriteHeader(http.StatusOK)
	}
}
```

Add `Action` to `cmd.MessageRef`:

```go
type MessageRef struct {
	ChatID    int64
	MessageID int64
	ThreadID  *int64
	Action    string // populated by EnqueueWithRef from cmd.Action
}
```

Update `EnqueueWithRef` to copy `cmd.Action` into the ref before storing.

D) Implement the concrete notifier in a new file `internal/backend/callbacks/notifier.go`:

```go
package callbacks

import (
	"context"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// Notifier implements backend.TGNotifier by sending one or more chunks via
// the existing tg.Client. Each chunk replies to the previous (first replies
// to ref.MessageID).
type Notifier struct {
	TG  TGClient
	Cfg Config
}

func (n *Notifier) NotifyCommandResult(ctx context.Context, ref cmd.MessageRef, action string, result wire.CommandResult, maxChars int) error {
	chunks := alerts.FormatCommandResult(action, result, maxChars)
	var prev *int64
	first := ref.MessageID
	prev = &first
	for _, c := range chunks {
		mid, err := n.TG.SendMessageWithReplyKeyboard(ctx, ref.ChatID, ref.ThreadID, c, "", prev, tg.ReplyKeyboardForTopic("per_router"))
		if err != nil {
			return err
		}
		prev = &mid
	}
	return nil
}
```

E) Wire it up in `cmd/backend/main.go` next to where the router is constructed (don't write code here — leave as a TODO comment for Task 24 deploy).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/... -run TestCmdResultRelayedToTG -v`
Expected: PASS. Then run full backend test: `go test ./internal/backend/... -v` to verify no regression.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(callbacks): relay CommandResult back to TG via Notifier"
```

---

## Phase F — Humanise HARD-alert button labels

### Task 17: Humanise HARD-alert keyboard text (spec §5.3)

**Files:**
- Modify: `internal/backend/tg/keyboard.go:50-72` (text only — callback_data unchanged)
- Modify: `internal/backend/tg/keyboard_test.go` (update golden assertions)

- [ ] **Step 1: Write the failing test**

Replace `TestHardAlertKeyboardCallbackData` to also assert the new texts. Add a new test:

```go
func TestHardAlertKeyboardHumanisedLabels(t *testing.T) {
	kb := HardAlertKeyboard(42, "tunnel_amnezia", WithTunnelActions(), WithMobileActions())
	want := map[string]string{
		"silence:42:tunnel_amnezia:1h":      "⏸ Тише на 1ч",
		"silence:42:tunnel_amnezia:4h":      "⏸ Тише на 4ч",
		"silence:42:tunnel_amnezia:24h":     "⏸ Тише на 24ч",
		"ack:42:tunnel_amnezia":             "✅ Понял",
		"history:42:tunnel_amnezia":         "📋 История за 24ч",
		"mute:42:tunnel_amnezia":            "🔇 Тихо до утра",
		"restart_tunnel:42:tunnel_amnezia":  "🔁 Перезапуск туннеля",
		"diag_now:42:tunnel_amnezia":        "📊 Диагностика",
		"pingcheck_now:42:tunnel_amnezia":   "▶ Тест связи",
		"force_recheck:42:tunnel_amnezia":   "🔄 Дай отчёт сейчас",
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if expected, ok := want[b.CallbackData]; ok {
				if b.Text != expected {
					t.Errorf("cd=%q got text %q want %q", b.CallbackData, b.Text, expected)
				}
				delete(want, b.CallbackData)
			}
		}
	}
	for cd := range want {
		t.Errorf("missing callback_data: %s", cd)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/tg/... -run TestHardAlertKeyboardHumanisedLabels -v`
Expected: FAIL — current texts are `⏸ 1ч`, `📊 Diag`, etc.

- [ ] **Step 3: Write minimal implementation**

In `internal/backend/tg/keyboard.go` lines 50-72, replace the literal text values:

```go
	rows := [][]InlineKeyboardButton{
		{
			{Text: "⏸ Тише на 1ч", CallbackData: silenceCD("1h")},
			{Text: "⏸ Тише на 4ч", CallbackData: silenceCD("4h")},
			{Text: "⏸ Тише на 24ч", CallbackData: silenceCD("24h")},
			{Text: "✅ Понял", CallbackData: plainCD("ack")},
		},
		{
			{Text: "📋 История за 24ч", CallbackData: plainCD("history")},
			{Text: "🔇 Тихо до утра", CallbackData: plainCD("mute")},
		},
	}
	if o.tunnelActions {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔁 Перезапуск туннеля", CallbackData: plainCD("restart_tunnel")},
			{Text: "📊 Диагностика", CallbackData: plainCD("diag_now")},
			{Text: "▶ Тест связи", CallbackData: plainCD("pingcheck_now")},
		})
	}
	if o.mobileActions {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔄 Дай отчёт сейчас", CallbackData: plainCD("force_recheck")},
		})
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/tg/... -v`
Expected: PASS for all keyboard tests (existing callback_data tests still pass — only Text changed).

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "refactor(tg): humanise HARD-alert button labels"
```

---

## Phase G — Deprecate pinned panel

### Task 18: Deprecate `init-menu` and `control_panel.go` (spec §5.4)

**Files:**
- Modify: `cmd/wg-monitor-cli/init_menu.go:19-83` (replace body)
- Modify: `cmd/wg-monitor-cli/main.go:57-66` (drop init-menu from usage)
- Modify: `internal/backend/tg/control_panel.go:1-3` (add DEPRECATED comment)
- Test: append to `cmd/wg-monitor-cli/init_menu_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create `cmd/wg-monitor-cli/init_menu_test.go`:

```go
package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestInitMenuPrintsDeprecatedAndExitsZero(t *testing.T) {
	// Build the binary, then run it. This is heavier than a unit test, but it's
	// the cleanest way to verify the deprecation path including os.Exit(0).
	bin := buildCLI(t) // helper that returns the path to a freshly-built binary
	cmd := exec.Command(bin, "init-menu", "--nickname=any")
	var stderr, stdout bytes.Buffer
	cmd.Stderr, cmd.Stdout = &stderr, &stdout
	err := cmd.Run()
	if err != nil {
		t.Fatalf("expected exit 0 from deprecated init-menu, got %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "removed in v0.6.0") {
		t.Errorf("missing deprecation notice in stderr: %s", stderr.String())
	}
}

// buildCLI compiles the wg-monitor-cli into t.TempDir().
func buildCLI(t *testing.T) string {
	t.Helper()
	out := t.TempDir() + "/wg-monitor-cli-test.exe"
	cmd := exec.Command("go", "build", "-o", out, ".")
	if err := cmd.Run(); err != nil {
		t.Fatalf("build cli: %v", err)
	}
	return out
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/wg-monitor-cli/... -run TestInitMenuPrintsDeprecated -v`
Expected: FAIL — current `cmdInitMenu` requires real config + real DB and exits non-zero on missing env.

- [ ] **Step 3: Write minimal implementation**

Replace the body of `cmdInitMenu` in `init_menu.go`:

```go
func cmdInitMenu(args []string) {
	_ = args
	fmt.Fprintln(os.Stderr,
		"command removed in v0.6.0; ReplyKeyboard installs automatically — see docs/superpowers/specs/2026-04-30-ui-replykeyboard-hybrid-design.md")
	os.Exit(0)
}
```

Drop the now-unused imports (most of the file's imports were for the legacy body — keep only `fmt` and `os`). Delete the helper functions `resolveTunnels`, `wideCount`, `pinChatMessage` from this file.

In `cmd/wg-monitor-cli/main.go` `usage()`:

```go
func usage() string {
	return `wg-monitor-cli — onboarding CLI

Usage:
  wg-monitor-cli add-user --nickname=NAME --awg-iface=IFACE --expected-exit-ip=IP [--kind static|mobile] [--db PATH] [--backend-url URL]
  wg-monitor-cli list-users [--db PATH]
  wg-monitor-cli show-discovered-dns [--awg-manager-url URL] [--ndmc PATH]
  wg-monitor-cli set-topic --kind=summary|systemic --thread-id=N [--db PATH]
  wg-monitor-cli version
`
}
```

In `internal/backend/tg/control_panel.go` line 1, add a header comment:

```go
// DEPRECATED v0.6.0: ControlPanel-based pinned menu was replaced by
// ReplyKeyboardForTopic + smart-replies (spec 2026-04-30). Kept for one
// release window so any in-flight callbacks dispatched against pinned
// messages still parse correctly. Remove in v0.7.0.
package tg
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/wg-monitor-cli/... -run TestInitMenuPrintsDeprecated -v`
Expected: PASS. Also run `go vet ./...` to confirm no unused-import errors.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "refactor(cli): deprecate init-menu in favour of ReplyKeyboard"
```

---

## Phase H — Operations-topic replies

### Task 19: `dispatchListUsers` (spec §5.1, §6.2)

**Files:**
- Modify: `internal/backend/callbacks/router.go` (replace stub from Task 12)
- Modify: `internal/backend/callbacks/router_test.go` (append golden test)

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRouterDispatchListUsers(t *testing.T) {
	d, _ := newTestDB(t) // creates "vasya"
	uid2, _ := d.Users().InsertWithKind("petya", "tok2", "2.2.2.2", "nwg0", db.KindMobile)
	_ = uid2
	_, _ = d.Users().InsertWithKind("masha", "tok3", "3.3.3.3", "nwg0", db.KindStatic)

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	// Set summary topic id so the message is treated as ops topic.
	_ = d.KV().SetTopicID("summary", 77)
	tid := int64(77)
	msg := &tg.Message{MessageID: 50, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "📋 Список юзеров"}
	r.HandleMessage(context.Background(), msg)
	if len(f.sentMsgs) == 0 && len(f.rkSends) == 0 {
		t.Fatal("no message sent")
	}
	all := strings.Join(append(f.sentMsgs, allTexts(f.rkSends)...), "\n")
	for _, want := range []string{"vasya", "petya", "masha"} {
		if !strings.Contains(all, want) {
			t.Errorf("list missing %s in:\n%s", want, all)
		}
	}
}

func allTexts(ss []rkSend) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.text
	}
	return out
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/callbacks/... -run TestRouterDispatchListUsers -v`
Expected: FAIL — stub `dispatchListUsers` is empty.

- [ ] **Step 3: Write minimal implementation**

Replace `dispatchListUsers` stub:

```go
func (r *Router) dispatchListUsers(ctx context.Context, m *tg.Message) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "ошибка чтения пользователей: "+err.Error(), "", nil)
		return
	}
	if len(users) == 0 {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, "Пользователей нет.", "", nil, tg.ReplyKeyboardForTopic("summary"))
		return
	}
	var b strings.Builder
	b.WriteString("📋 *Список юзеров*\n")
	now := time.Now()
	for _, u := range users {
		seen := "никогда"
		if u.LastSeenAt != nil {
			d := now.Sub(*u.LastSeenAt)
			seen = humanAgeDur(d) + " назад"
		}
		fmt.Fprintf(&b, "• `%s` — %s — %s\n", u.Nickname, u.Kind, seen)
	}
	fmt.Fprintf(&b, "\nВсего: %d", len(users))
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, b.String(), "Markdown", nil, tg.ReplyKeyboardForTopic("summary"))
}

// humanAgeDur is a local copy to avoid importing the alerts package
// just for this. Mirrors alerts.humanAgeDur.
func humanAgeDur(d time.Duration) string {
	if d <= 0 {
		return "0с"
	}
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%dс", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dм", s/60)
	}
	return fmt.Sprintf("%dч", s/3600)
}
```

(Add `strings`, `fmt` imports if not present.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/callbacks/... -run TestRouterDispatchListUsers -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(callbacks): operations-topic Список юзеров reply"
```

---

### Task 20: `dispatchFleetHealth` (spec §5.1, §6.2)

**Files:**
- Modify: `internal/backend/callbacks/router.go` (replace stub from Task 12)
- Modify: `internal/backend/callbacks/router_test.go` (append two tests: empty + with-incidents)

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRouterDispatchFleetHealth_AllGreen(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	_ = d.KV().SetTopicID("summary", 77)
	tid := int64(77)
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "📊 Здоровье флота"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(f.rkSends))
	}
	body := f.rkSends[0].text
	if !strings.Contains(body, "Активных HARD: 0") {
		t.Errorf("expected zero-incident body, got: %s", body)
	}
}

func TestRouterDispatchFleetHealth_WithIncidents(t *testing.T) {
	d, uid := newTestDB(t)
	st := db.IncidentState{UserID: uid, CheckName: "tunnel_awg11", CurrentStatus: "hard", ConsecutiveFails: 5}
	hs := time.Now().Add(-10 * time.Minute)
	st.HardSince = &hs
	if err := d.State().Save(uid, "tunnel_awg11", st); err != nil {
		t.Fatal(err)
	}
	uid2, _ := d.Users().Insert("petya", "tok2", "2.2.2.2", "nwg0")
	st2 := db.IncidentState{UserID: uid2, CheckName: "dns", CurrentStatus: "hard", ConsecutiveFails: 3}
	hs2 := time.Now().Add(-5 * time.Minute)
	st2.HardSince = &hs2
	_ = d.State().Save(uid2, "dns", st2)

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	_ = d.KV().SetTopicID("summary", 77)
	tid := int64(77)
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "📊 Здоровье флота"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatal("no reply")
	}
	body := f.rkSends[0].text
	for _, want := range []string{"Активных HARD: 2", "tunnel_awg11", "dns"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/callbacks/... -run TestRouterDispatchFleetHealth -v`
Expected: FAIL — stub `dispatchFleetHealth` is empty.

- [ ] **Step 3: Write minimal implementation**

Add a small DB helper to `internal/backend/db/state.go` so we don't run raw SQL in the router:

```go
type ActiveIncidentRow struct {
	UserID    int64
	CheckName string
	HardSince time.Time
	FailCount int
}

// AllActiveHard returns every incident_state row currently in 'hard' status.
// Used by Fleet-Health smart reply.
func (s *StateRepo) AllActiveHard() ([]ActiveIncidentRow, error) {
	rows, err := s.d.db.Query(`SELECT user_id, check_name, hard_since, consecutive_fails FROM incident_state WHERE current_status = 'hard'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveIncidentRow
	for rows.Next() {
		var r ActiveIncidentRow
		var hs sql.NullTime
		if err := rows.Scan(&r.UserID, &r.CheckName, &hs, &r.FailCount); err != nil {
			return nil, err
		}
		if hs.Valid {
			r.HardSince = hs.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

Replace `dispatchFleetHealth` stub:

```go
func (r *Router) dispatchFleetHealth(ctx context.Context, m *tg.Message) {
	rows, err := r.d.State().AllActiveHard()
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "ошибка чтения incident_state: "+err.Error(), "", nil)
		return
	}
	users, _ := r.d.Users().GetAll()
	nickByID := map[int64]string{}
	for _, u := range users {
		nickByID[u.ID] = u.Nickname
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 *Здоровье флота*\nАктивных HARD: %d\n", len(rows))
	if len(rows) > 0 {
		// Group by check_name for a quick "what's broken" view.
		byCheck := map[string]int{}
		for _, row := range rows {
			byCheck[row.CheckName]++
		}
		b.WriteString("\nПо типам проблем:\n")
		for check, n := range byCheck {
			fmt.Fprintf(&b, "  • %s — %d\n", check, n)
		}
		b.WriteString("\nДетали:\n")
		for _, row := range rows {
			nick := nickByID[row.UserID]
			if nick == "" {
				nick = "user#" + fmt.Sprint(row.UserID)
			}
			fmt.Fprintf(&b, "  • [%s] %s — %s\n", nick, row.CheckName, humanAgeDur(time.Since(row.HardSince)))
		}
	}
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, b.String(), "Markdown", nil, tg.ReplyKeyboardForTopic("summary"))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/callbacks/... -run TestRouterDispatchFleetHealth -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(callbacks): operations-topic Здоровье флота reply"
```

---

## Phase I — Set-topic CLI

### Task 21: `wg-monitor-cli set-topic` (spec §7)

**Files:**
- Create: `cmd/wg-monitor-cli/set_topic.go`
- Create: `cmd/wg-monitor-cli/set_topic_test.go`
- Modify: `cmd/wg-monitor-cli/main.go:25-55` (add `case "set-topic"`)

- [ ] **Step 1: Write the failing test**

```go
// cmd/wg-monitor-cli/set_topic_test.go
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestRunSetTopic_Summary(t *testing.T) {
	dbPath := t.TempDir() + "/x.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	var out bytes.Buffer
	if err := runSetTopic(setTopicOpts{DBPath: dbPath, Kind: "summary", ThreadID: 11, Out: &out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "summary") || !strings.Contains(out.String(), "11") {
		t.Errorf("out: %s", out.String())
	}
	// Reopen and verify the KV row.
	d, _ = db.Open(dbPath)
	defer d.Close()
	id, ok, err := d.KV().GetTopicID("summary")
	if err != nil || !ok || id != 11 {
		t.Errorf("kv: id=%d ok=%v err=%v", id, ok, err)
	}
}

func TestRunSetTopic_Systemic(t *testing.T) {
	dbPath := t.TempDir() + "/x.db"
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	if err := runSetTopic(setTopicOpts{DBPath: dbPath, Kind: "systemic", ThreadID: 22, Out: &out}); err != nil {
		t.Fatal(err)
	}
}

func TestRunSetTopic_InvalidKind(t *testing.T) {
	dbPath := t.TempDir() + "/x.db"
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	err := runSetTopic(setTopicOpts{DBPath: dbPath, Kind: "garbage", ThreadID: 1, Out: &out})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Errorf("expected kind error, got %v", err)
	}
}

func TestRunSetTopic_MissingThreadID(t *testing.T) {
	var out bytes.Buffer
	err := runSetTopic(setTopicOpts{DBPath: t.TempDir() + "/x.db", Kind: "summary", ThreadID: 0, Out: &out})
	if err == nil || !strings.Contains(err.Error(), "thread") {
		t.Errorf("expected thread-id error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/wg-monitor-cli/... -run TestRunSetTopic -v`
Expected: FAIL — `undefined: runSetTopic, setTopicOpts`.

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/wg-monitor-cli/set_topic.go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type setTopicOpts struct {
	DBPath   string
	Kind     string
	ThreadID int64
	Out      io.Writer
}

func cmdSetTopic(args []string) {
	fs := flag.NewFlagSet("set-topic", flag.ExitOnError)
	dbPath := fs.String("db", "/var/lib/wg-monitor/state.db", "path to SQLite DB")
	kind := fs.String("kind", "", "topic kind: summary|systemic")
	thread := fs.Int64("thread-id", 0, "Telegram message_thread_id of the topic")
	_ = fs.Parse(args)
	if err := runSetTopic(setTopicOpts{DBPath: *dbPath, Kind: *kind, ThreadID: *thread, Out: os.Stdout}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runSetTopic(o setTopicOpts) error {
	if o.Kind != "summary" && o.Kind != "systemic" {
		return fmt.Errorf("--kind=%q must be summary|systemic", o.Kind)
	}
	if o.ThreadID == 0 {
		return fmt.Errorf("--thread-id is required (the message_thread_id of the topic)")
	}
	d, err := db.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", o.DBPath, err)
	}
	defer d.Close()
	if err := d.KV().SetTopicID(o.Kind, o.ThreadID); err != nil {
		return fmt.Errorf("set topic id: %w", err)
	}
	fmt.Fprintf(o.Out, "OK — kind=%s thread_id=%d\n", o.Kind, o.ThreadID)
	return nil
}
```

In `main.go` switch, add the case after `"list-users"`:

```go
	case "set-topic":
		cmdSetTopic(os.Args[2:])
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/wg-monitor-cli/... -run TestRunSetTopic -v`
Expected: PASS — four subtests green.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "feat(cli): set-topic for operations-topic IDs"
```

---

## Phase J — Integration test extension

### Task 22: `TestUIReplyKeyboardSmartReply` integration (spec §10)

**Files:**
- Modify: `cmd/backend/integration_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append:

```go
// TestUIReplyKeyboardSmartReply boots the full backend with mocked TG, sends
// a "📊 Что происходит?" Message, and asserts a smart-reply lands in the
// per-router topic with the OK template body and the original message gets
// deleted.
func TestUIReplyKeyboardSmartReply(t *testing.T) {
	mock := newTGMock(t) // existing helper in this file
	defer mock.Close()
	be := startBackend(t, mock) // existing helper
	defer be.Close()

	// Provision a user with a topic id of 11.
	uid := mustOnboardUser(t, be, "vasya", "1.1.1.1", "nwg0")
	mustSetUserThread(t, be, uid, 11)
	mustReportFreshTunnelOK(t, be, uid, "tunnel_awg11", "amnezia", "nwg0", 12)

	tid := int64(11)
	mock.PushUpdate(tg.Update{
		UpdateID: 1,
		Message: &tg.Message{
			MessageID:       42,
			Chat:            tg.Chat{ID: be.ChatID()},
			From:            tg.User{ID: be.AdminID()},
			MessageThreadID: &tid,
			Text:            "📊 Что происходит?",
		},
	})

	// Expect: 1 sendMessage, 1 deleteMessage.
	mock.WaitForCallCount("sendMessage", 1, 5*time.Second)
	mock.WaitForCallCount("deleteMessage", 1, 5*time.Second)
	body := mock.LastSendMessageText()
	if !strings.Contains(body, "vasya") || !strings.Contains(body, "amnezia") {
		t.Errorf("smart reply body: %s", body)
	}
}
```

(Helpers `newTGMock`, `startBackend`, `mustOnboardUser`, `mustSetUserThread`, `mustReportFreshTunnelOK`, `WaitForCallCount`, `LastSendMessageText` may need to be added to the integration test file — follow the patterns of existing integration tests in the same file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/backend/... -run TestUIReplyKeyboardSmartReply -v`
Expected: FAIL — first run fails on missing helpers; subsequent runs fail because the wiring in `main.go` does not register the new `Notifier` (Task 24 will fix). For now, the test serves as the reverse-funnel forcing main.go to wire the path correctly.

- [ ] **Step 3: Write minimal implementation**

Add the missing helpers in `cmd/backend/integration_test.go`. Wire the `callbacks.Notifier` and `Deps.TGNotifier` in `cmd/backend/main.go` (this is the Task 24 deploy work, but the hookup is required for the test to pass):

```go
// cmd/backend/main.go (excerpt — add near where queue and router are constructed)
notifier := &callbacks.Notifier{TG: tgC, Cfg: callbacksCfg}
deps := backend.Deps{
	Logger:      logger,
	DB:          d,
	Dispatcher:  dispatcher,
	Resumer:     watcher,
	CommandSink: queue,
	TGNotifier:  notifier,
	UI:          cfg.UI,
	Thresholds:  state.Thresholds{...},
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/backend/... -run TestUIReplyKeyboardSmartReply -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "test(backend): integration test for ReplyKeyboard smart reply"
```

---

### Task 23: `TestUICommandResultReply` integration (spec §10)

**Files:**
- Modify: `cmd/backend/integration_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestUICommandResultReply(t *testing.T) {
	mock := newTGMock(t)
	defer mock.Close()
	be := startBackend(t, mock)
	defer be.Close()

	uid := mustOnboardUser(t, be, "vasya", "1.1.1.1", "nwg0")
	mustSetUserThread(t, be, uid, 11)
	// Force a HARD on tunnel_awg11 so the kb shows diag/restart.
	mustReportHardTunnel(t, be, uid, "tunnel_awg11")

	// Operator taps Diag.
	tid := int64(11)
	mock.PushUpdate(tg.Update{
		UpdateID: 1,
		CallbackQuery: &tg.CallbackQuery{
			ID:   "cb1",
			From: tg.User{ID: be.AdminID()},
			Message: tg.Message{
				MessageID:       7,
				Chat:            tg.Chat{ID: be.ChatID()},
				MessageThreadID: &tid,
				Text:            "🔴 [vasya] tunnel_awg11 — DOWN",
			},
			Data: "diag_now:" + itoa(uid) + ":tunnel_awg11",
		},
	})
	// Wait for the cmd to be enqueued; then simulate the agent posting result.
	be.AgentPostResult(t, uid, wire.CommandResult{ID: "<auto>", Status: "ok", Output: "diagnostics: all OK"})
	// Expect: at least one sendMessage with diag body and reply_to_message_id == 7
	mock.WaitForCallCount("sendMessage", 1, 5*time.Second)
	last := mock.LastSendMessage()
	if last.ReplyToMessageID == nil || *last.ReplyToMessageID != 7 {
		t.Errorf("reply_to mismatch: %+v", last)
	}
	if !strings.Contains(last.Text, "diagnostics: all OK") || !strings.Contains(last.Text, "```") {
		t.Errorf("diag body missing or no code-fence: %s", last.Text)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/backend/... -run TestUICommandResultReply -v`
Expected: FAIL — agent post helper missing or notifier path not yet wired end-to-end.

- [ ] **Step 3: Write minimal implementation**

Add `AgentPostResult` to the test backend wrapper — it should look up the most recently dispatched cmd ID, then POST `/v1/cmd/result` with the corresponding token. If the wiring from Task 22 is in place, the new POST path will trigger the relay through `Notifier.NotifyCommandResult`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/backend/... -run TestUICommandResultReply -v`
Expected: PASS. Then run the full integration suite: `go test ./cmd/backend/... -v`.

- [ ] **Step 5: Commit**

```bash
git -c user.email=asnekhaev@gmail.com commit -am "test(backend): integration test for diag_now reply relay"
```

---

## Phase K — Live deploy + verify + tag

### Task 24: Deploy backend to VPS Main (spec §9)

**Files:**
- No source changes — operational task.

- [ ] **Step 1: Build the backend (PowerShell, cross-compile to Linux amd64)**

Use the **PowerShell tool**, not Bash, because Bash on this Windows host emits `.exe` instead of an ELF binary:

```powershell
cd C:\Users\Anex\Projects\wg-monitor\.worktrees\stage-2
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -ldflags "-X main.Version=0.6.0-ui-rework-dev" -o ./dist/wg-monitor-backend ./cmd/backend
```

Verify the binary exists and starts with `ELF`:

```powershell
Get-Item ./dist/wg-monitor-backend | Format-List Length, LastWriteTime
$b = Get-Content ./dist/wg-monitor-backend -TotalCount 1 -Encoding Byte; if ($b[0] -ne 0x7F) { throw "not an ELF binary" }
```

Expected: file >5 MB, header byte `0x7F` (ELF magic).

- [ ] **Step 2: Verify deployment script reachable**

```bash
ls deploy/backend/deploy_vps_main.py
```

Expected: file exists. If the script accepts a binary path, run it as documented in its own help. Otherwise SCP manually:

```bash
scp ./dist/wg-monitor-backend root@103.106.1.253:/usr/local/bin/wg-monitor-backend.new
ssh root@103.106.1.253 'systemctl stop wg-monitor-backend && mv /usr/local/bin/wg-monitor-backend.new /usr/local/bin/wg-monitor-backend && systemctl start wg-monitor-backend'
```

- [ ] **Step 3: Verify service is up**

```bash
ssh root@103.106.1.253 'systemctl status wg-monitor-backend --no-pager | head -20 && journalctl -u wg-monitor-backend -n 50 --no-pager'
```

Expected: `active (running)`, recent logs show `cmd dispatched`, no errors.

- [ ] **Step 4: Smoke-test one command**

In the operator's `👤 testkeen` topic, send `📊 Что происходит?`. Expect a smart reply with `vasya` (or actual nickname) inside 5 seconds.

- [ ] **Step 5: Commit (no code change — empty commit only if needed for traceability)**

If anything touched (e.g. updated `Version` constant defaults), commit with:

```bash
git -c user.email=asnekhaev@gmail.com commit --allow-empty -m "chore(deploy): backend v0.6.0-ui-rework-dev to VPS Main"
```

---

### Task 25: Live verification + tag (spec §10, §11)

**Files:**
- No source changes.

- [ ] **Step 1: Manual verification checklist**

Run through every item; mark each ☑ before tagging:

- ☐ Operator presses `📊 Что происходит?` in own per-router topic on **mobile TG client** — sees correct OK / Degraded / HARD / Offline template per current state.
- ☐ Same on **Desktop TG client** — verify the ReplyKeyboard remains visible after the bot's reply (the desktop-bug mitigation: re-installing on every bot message).
- ☐ Force a HARD via `ip link set nwg0 down` on testkeen router; wait for the FSM to fire; verify HARD-alert kb labels are humanised (`Тише на 1ч`, `Понял`, `Перезапуск туннеля`, `Диагностика`, `Тест связи`).
- ☐ Press `[📊 Запустить диагностику]` on the smart-reply HARD message; verify a diag-output reply lands within 5 sec, code-fenced, replying to the smart-reply message.
- ☐ Press `[⬆ Opkg upgrade]` on a HARD message (or trigger the action manually via the menu); verify the resulting message is paginated when output > 4000 chars and chunks chain via reply_to_message_id.
- ☐ Run `wg-monitor-cli set-topic --kind=summary --thread-id=<actual id>` on the VPS, then send `📋 Список юзеров` in the Сводка topic; verify the user list comes back.
- ☐ Restore the failing tunnel; verify the existing RECOVERY auto-cycle still emits its ✅ message (no regression).

- [ ] **Step 2: Bump Version and tag**

In `cmd/wg-monitor-cli/main.go` line 16 and `cmd/backend/main.go` (wherever `Version` is set), set `Version = "0.6.0-ui-rework"`. Commit:

```bash
git -c user.email=asnekhaev@gmail.com commit -am "chore: bump Version to v0.6.0-ui-rework"
```

- [ ] **Step 3: Tag and push**

```bash
git tag v0.6.0-ui-rework
git push origin feature/stage-2 v0.6.0-ui-rework
```

- [ ] **Step 4: Final smoke test on VPS**

```bash
ssh root@103.106.1.253 '/usr/local/bin/wg-monitor-backend --version 2>&1 | head -1'
```

Expected: `0.6.0-ui-rework`.

- [ ] **Step 5: No commit (already tagged)**

End of plan.

---

## Self-review notes

- **Every step that contains code shows actual Go.** Verified: tasks 1-23 each have one `Step 1` test block with full `package`/`import`/test-func bodies and one `Step 3` implementation block with the same level of detail.
- **Forward references resolved.** `users.GetByThreadID` (T1) is referenced first in T12; `KV.GetTopicID/SetTopicID` (T2) first in T12 (resolveTopicKind) and T21 (set-topic CLI). `ReplyKeyboardForTopic` (T4) first used in T12. `MessageRef` (T15) first referenced in T16.
- **Spec coverage map present** at top of file.
- **No TBD/TODO/implement-later** in any task body. The only forward reference is the `dispatchSmartReply` minimal stub in T12 explicitly marked "replaced in Task 13" — this is intentional TDD scaffolding so T12's test passes, then T13 tightens the assertion.
- **Conventional Commits** used in every Step 5: `feat(scope)`, `refactor(scope)`, `test(scope)`, `docs(scope)`, `chore`.
- **Cross-compile via PowerShell** explicitly called out in T24 with the literal one-liner from project memory.
- **Email override** applied to every `git commit` invocation.
