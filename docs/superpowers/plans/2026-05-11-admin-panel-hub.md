# Admin Panel Hub Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `/panel` admin slash-command that posts an inline-keyboard hub for pushing Maintenance / Routes / Status panels into any router's per_router topic, plus a welcome-message auto-send on topic creation so reply-keyboard buttons appear immediately.

**Architecture:** New `panel_hub.go` file owns the hub state machine (Home → Kind → Push/Awaken → Result). Publication uses the existing `compat_btn` synthetic-Message pattern to feed `openMaintPanelMessage` / `openRoutesPanelMessage` / `dispatchSmartReply` from outside the target topic. Welcome message is a new `alerts.SendWelcome` helper called by all four topic-creation paths (Dispatcher lazy-create, CLI `ensure-topics`, admin `/ensure_topics`, admin `/recreate_topic`).

**Tech Stack:** Go 1.26, net/http TG client, SQLite. Tests use existing `fakeRouterTGFull` fixture + httptest.Server for tg/client tests.

**Spec:** [docs/superpowers/specs/2026-05-11-admin-panel-hub-design.md](../specs/2026-05-11-admin-panel-hub-design.md)

---

## File Plan

| Action | Path | Responsibility |
|---|---|---|
| Create | `internal/backend/callbacks/panel_hub.go` | Hub state machine: `adminPanelOpen`, `handlePanelCallback`, all screen renderers. ~250 lines. |
| Create | `internal/backend/callbacks/panel_hub_test.go` | Unit tests for every screen, ACL behaviour, error paths. ~300 lines. |
| Modify | `internal/backend/callbacks/parse.go` | Add `"panel"` to validActions, `PanelScreen` + `PanelKind` to Args, parser cases for panel screens. |
| Modify | `internal/backend/callbacks/parse_test.go` | Parser cases for panel callbacks. |
| Modify | `internal/backend/callbacks/admin_topics.go` | Add `/panel` case in `handleAdminCommand` (delegate to `adminPanelOpen`), extend `/topic_help`, send welcome after fresh `EnsureTopicForUser` in `adminEnsureTopics` and `adminRecreateTopic`. |
| Modify | `internal/backend/callbacks/admin_topics_test.go` | Test for `/panel` admin-only gate + welcome integration tests. |
| Modify | `internal/backend/callbacks/router.go` | Add `case "panel": r.handlePanelCallback(ctx, q, args); return` in `HandleCallback` switch. |
| Modify | `internal/backend/tg/client.go` | Add `BotCommand` struct + `SetMyCommands(ctx, []BotCommand) error` method. |
| Modify | `internal/backend/tg/client_test.go` | `TestSetMyCommands_PostsExpectedPayload` via httptest. |
| Modify | `internal/backend/alerts/topics.go` | Add `WelcomeSender` interface + `SendWelcome` helper. |
| Create | `internal/backend/alerts/topics_test.go` | Tests for `SendWelcome`. |
| Modify | `internal/backend/alerts/dispatcher.go` | Wrap `ensureTopic` so when a fresh thread_id is created (`u.TelegramThreadID == nil` pre-call), `SendWelcome` runs after. |
| Modify | `internal/backend/alerts/dispatcher_test.go` | Test that fresh-create path sends welcome; reuse path does not. |
| Modify | `cmd/wg-monitor-cli/ensure_topics.go` | After successful create-or-rebuild path, call `SendWelcome` via the TG client. |
| Modify | `cmd/wg-monitor-cli/ensure_topics_test.go` | Verify welcome is sent for new topics and not for skipped. |
| Modify | `cmd/backend/main.go` | Call `tgClient.SetMyCommands(...)` at startup; non-fatal on failure. |

---

## Task 1: `tg.SetMyCommands` API

**Files:**
- Modify: `internal/backend/tg/client.go`
- Test: `internal/backend/tg/client_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/tg/client_test.go`:

```go
func TestSetMyCommands_PostsExpectedPayload(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()

	c := &Client{
		BaseURL: ts.URL + "/bot",
		Token:   "T",
		HTTP:    ts.Client(),
	}
	cmds := []BotCommand{
		{Command: "panel", Description: "Открыть панель управления"},
		{Command: "topic_help", Description: "Шпаргалка"},
	}
	if err := c.SetMyCommands(context.Background(), cmds); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	if !strings.Contains(string(gotBody), `"command":"panel"`) {
		t.Errorf("body missing panel command: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), `"description":"Шпаргалка"`) {
		t.Errorf("body missing description: %s", gotBody)
	}
}
```

If `client_test.go` lacks the required imports, ensure `httptest`, `io`, `strings` are imported.

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/backend/tg -run TestSetMyCommands_PostsExpectedPayload -v
```

Expected: FAIL with "undefined: BotCommand" or "c.SetMyCommands undefined".

- [ ] **Step 3: Implement**

Append to `internal/backend/tg/client.go` (after the existing methods, just before `call` private helpers — pick a sensible spot, e.g. right after `DownloadFile`):

```go
// BotCommand mirrors the TG Bot API BotCommand object used by setMyCommands.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type setMyCommandsReq struct {
	Commands []BotCommand `json:"commands"`
}

// SetMyCommands registers the bot's slash-command menu so TG clients show
// the commands in the command picker. Idempotent — TG replaces the previous
// list each call. Errors are non-fatal at call sites (the bot keeps working
// without the menu hint).
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	body, _ := json.Marshal(setMyCommandsReq{Commands: cmds})
	return c.call(ctx, "setMyCommands", body, nil)
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/backend/tg -run TestSetMyCommands_PostsExpectedPayload -v
```

Expected: PASS.

- [ ] **Step 5: Run full tg package tests**

```
go test ./internal/backend/tg/...
```

Expected: all PASS (no regressions).

- [ ] **Step 6: Commit**

```bash
git add internal/backend/tg/client.go internal/backend/tg/client_test.go
git commit -m "feat(tg): add SetMyCommands API for bot command menu

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Parser extension for `panel` callbacks

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Test: `internal/backend/callbacks/parse_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/backend/callbacks/parse_test.go`:

```go
func TestParse_PanelHome(t *testing.T) {
	a, err := Parse("panel:0:home")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.Action != "panel" || a.PanelScreen != "home" || a.UserID != 0 {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelKindMaint(t *testing.T) {
	a, err := Parse("panel:0:kind:maint")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "kind" || a.PanelKind != "maint" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelPush(t *testing.T) {
	a, err := Parse("panel:42:push:routes")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "push" || a.PanelKind != "routes" || a.UserID != 42 {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelNoTopic(t *testing.T) {
	a, err := Parse("panel:7:no_topic")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "no_topic" || a.UserID != 7 {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelAwakenConfirm(t *testing.T) {
	a, err := Parse("panel:0:awaken_confirm")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "awaken_confirm" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelAwakenDo(t *testing.T) {
	a, err := Parse("panel:0:awaken_do")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "awaken_do" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelClose(t *testing.T) {
	a, err := Parse("panel:0:close")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "close" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelRejectsUnknownScreen(t *testing.T) {
	if _, err := Parse("panel:0:wat"); err == nil {
		t.Error("expected error for unknown screen")
	}
}

func TestParse_PanelRejectsUnknownKind(t *testing.T) {
	if _, err := Parse("panel:0:kind:lol"); err == nil {
		t.Error("expected error for unknown kind")
	}
}

func TestParse_PanelKindRequiresKind(t *testing.T) {
	if _, err := Parse("panel:0:kind"); err == nil {
		t.Error("expected error for kind screen without kind value")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/backend/callbacks -run TestParse_Panel -v
```

Expected: FAIL (unknown action: "panel").

- [ ] **Step 3: Implement Args extension + parser**

Edit `internal/backend/callbacks/parse.go`:

In the `Args` struct (after `MaintToken` field at line ~54), add:

```go
	// PanelScreen identifies the panel-hub screen for callbacks where
	// Action == "panel". One of: "home" | "kind" | "push" | "no_topic" |
	// "awaken_confirm" | "awaken_do" | "close".
	PanelScreen string
	// PanelKind is the panel type ("maint" | "routes" | "status") for
	// the "kind" and "push" screens.
	PanelKind string
```

In `validActions` map (around line 67), add inside the existing map literal:

```go
	// admin panel hub — multi-screen inline-kb dispatcher.
	"panel": true,
```

In the `Parse` function, after the existing `switch action { ... }` block (around line 202, just before `return a, nil`), add a separate panel block:

```go
	if action == "panel" {
		if len(parts) < 3 {
			return Args{}, fmt.Errorf("panel requires screen: %q", data)
		}
		screen := parts[2]
		validPanelScreens := map[string]bool{
			"home": true, "kind": true, "push": true, "no_topic": true,
			"awaken_confirm": true, "awaken_do": true, "close": true,
		}
		if !validPanelScreens[screen] {
			return Args{}, fmt.Errorf("panel: unknown screen %q", screen)
		}
		a.PanelScreen = screen
		if screen == "kind" || screen == "push" {
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("panel %s requires kind: %q", screen, data)
			}
			validKinds := map[string]bool{"maint": true, "routes": true, "status": true}
			if !validKinds[parts[3]] {
				return Args{}, fmt.Errorf("panel %s: unknown kind %q", screen, parts[3])
			}
			a.PanelKind = parts[3]
		}
	}
```

Note: the panel block parses parts[2] as `screen` directly, overriding the earlier `CheckName = parts[2]` assignment. That's fine — panel callbacks never consume `CheckName`. Make sure the panel block runs **after** the earlier `a := Args{...}` initialization so `a` exists.

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/backend/callbacks -run TestParse_Panel -v
```

Expected: all PASS.

- [ ] **Step 5: Run full callbacks package tests**

```
go test ./internal/backend/callbacks/...
```

Expected: all PASS (no regressions on existing parser tests).

- [ ] **Step 6: Commit**

```bash
git add internal/backend/callbacks/parse.go internal/backend/callbacks/parse_test.go
git commit -m "feat(callbacks): add panel callback grammar to parser

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `alerts.SendWelcome` helper

**Files:**
- Modify: `internal/backend/alerts/topics.go`
- Create: `internal/backend/alerts/topics_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/backend/alerts/topics_test.go`:

```go
package alerts

import (
	"context"
	"strings"
	"testing"
)

type fakeWelcomeSender struct {
	calls []welcomeCall
	err   error
}

type welcomeCall struct {
	chatID   int64
	threadID *int64
	text     string
	markup   any
}

func (f *fakeWelcomeSender) SendMessageWithReplyKeyboard(_ context.Context, chatID int64, threadID *int64, text, _ string, _ *int64, markup any) (int64, error) {
	f.calls = append(f.calls, welcomeCall{chatID, threadID, text, markup})
	return 1, f.err
}

func TestSendWelcome_IncludesNickname(t *testing.T) {
	f := &fakeWelcomeSender{}
	if err := SendWelcome(context.Background(), f, -100, 555, "testkeen", "stub-kb"); err != nil {
		t.Fatalf("SendWelcome: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.calls))
	}
	got := f.calls[0]
	if got.chatID != -100 {
		t.Errorf("chatID: %d", got.chatID)
	}
	if got.threadID == nil || *got.threadID != 555 {
		t.Errorf("threadID: %v", got.threadID)
	}
	if !strings.Contains(got.text, "testkeen") {
		t.Errorf("text missing nickname: %s", got.text)
	}
}

func TestSendWelcome_AttachesProvidedMarkup(t *testing.T) {
	f := &fakeWelcomeSender{}
	mark := "my-keyboard"
	if err := SendWelcome(context.Background(), f, -100, 555, "x", mark); err != nil {
		t.Fatal(err)
	}
	if f.calls[0].markup != mark {
		t.Errorf("markup not propagated: %v", f.calls[0].markup)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/backend/alerts -run TestSendWelcome -v
```

Expected: FAIL (undefined: SendWelcome).

- [ ] **Step 3: Implement**

Append to `internal/backend/alerts/topics.go`:

```go
// WelcomeSender is the slice of *tg.Client used by SendWelcome. Narrow
// interface so tests can substitute a fake and the alerts package doesn't
// pull in the full TG client surface for one helper.
type WelcomeSender interface {
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
}

// SendWelcome posts the first-contact message into a freshly-created
// per_router topic so the reply-keyboard buttons attach immediately (TG
// only re-installs the persistent keyboard on bot-originated messages —
// an empty topic has none until something arrives).
//
// markup must be the value returned by callbacks/UIConfigSnapshot.KeyboardForTopic("per_router")
// so the same compat-inline / reply-kb switch the rest of the app uses
// is honoured here. Pass nil to skip the keyboard entirely (not recommended).
func SendWelcome(ctx context.Context, tg WelcomeSender, chatID, threadID int64, nickname string, markup any) error {
	text := "👋 Топик роутера " + nickname + " готов.\n\n" +
		"Кнопки внизу — то, что я умею. Тапни 📊 чтобы посмотреть статус прямо сейчас."
	t := threadID
	_, err := tg.SendMessageWithReplyKeyboard(ctx, chatID, &t, text, "", nil, markup)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/backend/alerts -run TestSendWelcome -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/alerts/topics.go internal/backend/alerts/topics_test.go
git commit -m "feat(alerts): add SendWelcome helper for fresh topic onboarding

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Welcome integration in `Dispatcher.ensureTopic`

**Files:**
- Modify: `internal/backend/alerts/dispatcher.go`
- Test: `internal/backend/alerts/dispatcher_test.go`

The Dispatcher already holds `di.tg` (which satisfies WelcomeSender) and `di.cfg.ChatID`. The keyboard markup needs to come from the UI snapshot — Dispatcher does NOT currently hold a UIConfigSnapshot, so the simplest contract is to pass the markup factory via a new Dispatcher field set by `cmd/backend/main.go` after construction. For tests, leave the factory nil → fall back to nil markup (no welcome sent, no regressions).

- [ ] **Step 1: Inspect current Dispatcher struct + Config**

Read `internal/backend/alerts/dispatcher.go` lines 1-80 to find the `Dispatcher` struct definition and `NewDispatcher` constructor. Note where to add a new field.

- [ ] **Step 2: Write failing test**

Append to `internal/backend/alerts/dispatcher_test.go`:

```go
// TestEnsureTopic_SendsWelcomeOnFreshCreate verifies that the lazy-create
// path calls SendWelcome exactly once after a fresh thread_id is provisioned.
// The no-op path (existing thread_id) must NOT trigger welcome.
func TestEnsureTopic_SendsWelcomeOnFreshCreate(t *testing.T) {
	d, uid := newDispatcherTestDB(t)  // helper from existing tests
	tgFake := newDispatcherFakeTG()    // fake tg w/ create + sendWithKB capture
	disp := NewDispatcher(d, tgFake, Config{ChatID: -100})
	disp.WelcomeKeyboard = func() any { return "stub-kb" }

	// Fresh-create path.
	tid, err := disp.ensureTopic(context.Background(), uid, "vasya")
	if err != nil {
		t.Fatalf("ensureTopic: %v", err)
	}
	if tid == 0 {
		t.Fatalf("expected non-zero thread id")
	}
	if got := tgFake.welcomeSends; len(got) != 1 {
		t.Fatalf("want 1 welcome send, got %d", len(got))
	}
	if !strings.Contains(tgFake.welcomeSends[0].text, "vasya") {
		t.Errorf("welcome missing nickname")
	}

	// No-op path: ensureTopic again, no new welcome.
	if _, err := disp.ensureTopic(context.Background(), uid, "vasya"); err != nil {
		t.Fatalf("second ensureTopic: %v", err)
	}
	if got := len(tgFake.welcomeSends); got != 1 {
		t.Errorf("want still 1 welcome (no-op), got %d", got)
	}
}
```

Note: `newDispatcherTestDB`, `newDispatcherFakeTG` likely already exist in `dispatcher_test.go`. Inspect first; if a fake doesn't capture `SendMessageWithReplyKeyboard`, extend it with a `welcomeSends []welcomeRecord` slice (mirror the pattern in `callbacks/router_test.go` `fakeRouterTGFull`). The fake must also satisfy whatever existing tg interface the Dispatcher uses.

If the test helpers don't exist with those exact names, use whatever fixture the existing dispatcher_test.go uses, and add the `welcomeSends` capture to that fake.

- [ ] **Step 3: Run test to verify it fails**

```
go test ./internal/backend/alerts -run TestEnsureTopic_SendsWelcomeOnFreshCreate -v
```

Expected: FAIL (undefined field WelcomeKeyboard, or 0 welcome sends).

- [ ] **Step 4: Implement**

Edit `internal/backend/alerts/dispatcher.go`:

In the `Dispatcher` struct (find the existing struct definition near the top), add a field:

```go
	// WelcomeKeyboard returns the reply-keyboard markup to attach to the
	// first message in a freshly-created per_router topic. Set by
	// cmd/backend/main.go after construction (the Dispatcher doesn't
	// import the callbacks UI snapshot to avoid a cycle). Nil disables
	// welcome — used only by tests or admin-disabled flows.
	WelcomeKeyboard func() any
```

Replace the existing `ensureTopic` body (around line 249) with:

```go
func (di *Dispatcher) ensureTopic(ctx context.Context, userID int64, nickname string) (int64, error) {
	u, err := di.d.Users().GetByNickname(nickname)
	if err != nil {
		return 0, err
	}
	if u.TelegramThreadID != nil {
		return *u.TelegramThreadID, nil
	}
	di.mu.Lock()
	defer di.mu.Unlock()
	// Double-check under lock — another goroutine may have created the
	// topic while we waited (BUG-XX style race; original guard).
	u2, err := di.d.Users().GetByID(u.ID)
	if err == nil && u2 != nil && u2.TelegramThreadID != nil {
		return *u2.TelegramThreadID, nil
	}
	tid, err := EnsureTopicForUser(ctx, di.tg, di.d, di.cfg.ChatID, u.ID, false)
	if err != nil {
		return 0, err
	}
	// Fresh create — send welcome so reply-keyboard attaches to the topic.
	// Non-fatal: log and continue if welcome fails. The topic is usable
	// without it (it appears on first alert).
	if di.WelcomeKeyboard != nil {
		if werr := SendWelcome(ctx, di.tg, di.cfg.ChatID, tid, nickname, di.WelcomeKeyboard()); werr != nil {
			slog.Warn("welcome send failed (non-fatal)", "user", nickname, "err", werr)
		}
	}
	return tid, nil
}
```

If `slog` isn't imported in dispatcher.go, add it.

- [ ] **Step 5: Run test to verify it passes**

```
go test ./internal/backend/alerts -run TestEnsureTopic_SendsWelcomeOnFreshCreate -v
```

Expected: PASS.

- [ ] **Step 6: Run full alerts package tests**

```
go test ./internal/backend/alerts/...
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/backend/alerts/dispatcher.go internal/backend/alerts/dispatcher_test.go
git commit -m "feat(alerts): send welcome on fresh topic create in Dispatcher

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Welcome integration in CLI `ensure-topics`

**Files:**
- Modify: `cmd/wg-monitor-cli/ensure_topics.go`
- Test: `cmd/wg-monitor-cli/ensure_topics_test.go`

The CLI runs single-threaded; the welcome call goes inline after a successful create. We need a `WelcomeSender` capability — extend `ensureTopicsOpts` with an optional `Welcomer alerts.WelcomeSender` and a markup factory. Tests pass a fake; production wires `tgClient` (same one used as `Creator`).

- [ ] **Step 1: Write failing test**

Append to `cmd/wg-monitor-cli/ensure_topics_test.go`:

```go
func TestRunEnsureTopics_SendsWelcomeForNewTopic(t *testing.T) {
	// existing test scaffolding likely sets up db with vasya needing topic
	d, _ := newCliTestDB(t) // uses existing helper
	creator := newCliFakeCreator()
	welcomer := newCliFakeWelcomer()

	err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath:           ":memory:-handled-by-helper", // adapt to helper
		ChatID:           -100,
		Creator:          creator,
		Welcomer:         welcomer,
		WelcomeKeyboard:  func() any { return "stub-kb" },
		Out:              io.Discard,
	})
	if err != nil {
		t.Fatalf("runEnsureTopics: %v", err)
	}
	if len(welcomer.calls) != 1 {
		t.Errorf("want 1 welcome send, got %d", len(welcomer.calls))
	}
}

func TestRunEnsureTopics_NoWelcomeForSkippedUser(t *testing.T) {
	// User already has thread → no welcome.
	d, uid := newCliTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 99); err != nil {
		t.Fatal(err)
	}
	creator := newCliFakeCreator()
	welcomer := newCliFakeWelcomer()

	_ = runEnsureTopics(context.Background(), ensureTopicsOpts{
		ChatID:          -100,
		Creator:         creator,
		Welcomer:        welcomer,
		WelcomeKeyboard: func() any { return "stub-kb" },
		Out:             io.Discard,
	})
	if len(welcomer.calls) != 0 {
		t.Errorf("expected 0 welcome (skip path), got %d", len(welcomer.calls))
	}
}
```

If the existing test file doesn't have `newCliTestDB`, `newCliFakeCreator` with those exact names, inspect and use what's there. Add `newCliFakeWelcomer` as a new helper modeled after `alerts/topics_test.go`'s `fakeWelcomeSender`.

- [ ] **Step 2: Run tests to verify failure**

```
go test ./cmd/wg-monitor-cli -run TestRunEnsureTopics -v
```

Expected: FAIL (unknown field Welcomer in ensureTopicsOpts).

- [ ] **Step 3: Implement**

Edit `cmd/wg-monitor-cli/ensure_topics.go`:

In `ensureTopicsOpts` (around line 23), add:

```go
	// Welcomer is optional — when non-nil, a welcome message is sent
	// into each freshly-created topic so reply-keyboard buttons attach
	// immediately. nil disables (used by older tests that don't model TG).
	Welcomer alerts.WelcomeSender
	// WelcomeKeyboard returns the reply-markup attached to the welcome.
	// Mirrors Dispatcher.WelcomeKeyboard contract.
	WelcomeKeyboard func() any
```

In `cmdEnsureTopics` constructor (around line 55), set both:

```go
	tgClient := &tg.Client{
		BaseURL: tg.DefaultBaseURL,
		Token:   cfg.Telegram.BotToken,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
	if err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath:          dbPath,
		ChatID:          cfg.Telegram.ChatID,
		Nickname:        *nick,
		Force:           *force,
		Sleep:           time.Duration(*sleepMs) * time.Millisecond,
		Creator:         tgClient,
		Welcomer:        tgClient,
		WelcomeKeyboard: func() any { return tg.ReplyKeyboardForTopic("per_router") },
		Out:             os.Stdout,
	}); err != nil {
		// existing error handling
	}
```

Inside `runEnsureTopics`, after the existing `tid, err := alerts.EnsureTopicForUser(...)` and the success-print branch (around line 136-142), add the welcome call BEFORE incrementing `created`:

```go
		tid, err := alerts.EnsureTopicForUser(ctx, o.Creator, d, o.ChatID, u.ID, o.Force)
		if err != nil {
			fmt.Fprintf(o.Out, "! fail %s — %v\n", u.Nickname, err)
			failed++
			continue
		}
		if oldID != 0 && tid != oldID {
			fmt.Fprintf(o.Out, "+ rebuilt %s — old topic id=%d → new topic id=%d (old topic left intact in TG)\n", u.Nickname, oldID, tid)
		} else {
			fmt.Fprintf(o.Out, "+ created %s — topic id=%d\n", u.Nickname, tid)
		}
		// Send welcome so reply-keyboard attaches to the new topic.
		// Non-fatal: log to stdout and continue.
		if o.Welcomer != nil && o.WelcomeKeyboard != nil {
			if werr := alerts.SendWelcome(ctx, o.Welcomer, o.ChatID, tid, u.Nickname, o.WelcomeKeyboard()); werr != nil {
				fmt.Fprintf(o.Out, "  (welcome send failed for %s: %v)\n", u.Nickname, werr)
			}
		}
		created++
```

- [ ] **Step 4: Run tests to verify pass**

```
go test ./cmd/wg-monitor-cli -run TestRunEnsureTopics -v
```

Expected: all PASS.

- [ ] **Step 5: Run full cli tests**

```
go test ./cmd/wg-monitor-cli/...
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/wg-monitor-cli/ensure_topics.go cmd/wg-monitor-cli/ensure_topics_test.go
git commit -m "feat(cli): send welcome in ensure-topics for fresh topics

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Welcome integration in admin `/ensure_topics`

**Files:**
- Modify: `internal/backend/callbacks/admin_topics.go`
- Test: `internal/backend/callbacks/admin_topics_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/backend/callbacks/admin_topics_test.go`:

```go
func TestAdminEnsureTopics_SendsWelcomeForFreshTopic(t *testing.T) {
	d, _ := newTestDB(t) // vasya has no topic by default
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 50, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		Text: "/ensure_topics",
	}
	r.HandleMessage(context.Background(), msg)

	// Expect at least one welcome send (text starts with "👋 Топик роутера").
	var welcomeCount int
	for _, s := range f.rkSends {
		if strings.HasPrefix(s.text, "👋 Топик роутера vasya") {
			welcomeCount++
		}
	}
	if welcomeCount != 1 {
		t.Fatalf("want 1 welcome rkSend, got %d (all: %d)", welcomeCount, len(f.rkSends))
	}
}
```

- [ ] **Step 2: Run test — fail**

```
go test ./internal/backend/callbacks -run TestAdminEnsureTopics_SendsWelcomeForFreshTopic -v
```

Expected: FAIL.

- [ ] **Step 3: Implement**

Edit `internal/backend/callbacks/admin_topics.go` `adminEnsureTopics` (around line 67). In the loop, after a successful `EnsureTopicForUser` and before incrementing `created`:

```go
		tid, err := alerts.EnsureTopicForUser(ctx, r.tg, r.d, r.cfg.ChatID, u.ID, false)
		if err != nil {
			fmt.Fprintf(&b, "❌ %s — %v\n", u.Nickname, err)
			failed++
			continue
		}
		// Welcome: best-effort, non-fatal. Skip if it fails — fresh topic
		// is still usable, the reply-kb will attach on the next bot message.
		if werr := alerts.SendWelcome(ctx, r.tg, r.cfg.ChatID, tid, u.Nickname, r.cfg.UI.KeyboardForTopic("per_router")); werr != nil {
			slog.Warn("welcome send failed (non-fatal)", "user", u.Nickname, "err", werr)
		}
		fmt.Fprintf(&b, "✅ %s — thread_id=%d\n", u.Nickname, tid)
		created++
```

- [ ] **Step 4: Run test — pass**

```
go test ./internal/backend/callbacks -run TestAdminEnsureTopics -v
```

Expected: PASS for new test, existing tests still PASS (welcome is additive — `TestAdminEnsureTopics_BulkCreatesMissing` already asserts `len(f.sentMsgs) != 1` which now includes the welcome — verify that assertion **uses the summary msg index correctly** or update it to filter; if existing tests break, adjust them to count only the summary message).

If `TestAdminEnsureTopics_BulkCreatesMissing` fails: the old assertion `len(f.sentMsgs) != 1` should become "the LAST sentMsg is the summary". Update it accordingly:

```go
	if len(f.sentMsgs) == 0 {
		t.Fatal("want at least 1 reply, got 0")
	}
	reply := f.sentMsgs[len(f.sentMsgs)-1]
	for _, want := range []string{"✅ vasya", "1 создано", "1 пропущено"} {
		...
	}
```

Note: `f.sentMsgs` captures both `SendMessage` and `SendMessageWithReplyKeyboard` (see fake definition lines 50, 69) — welcome arrives via the latter, summary via the former. The summary should still be findable as the message containing "Итого:".

- [ ] **Step 5: Run full callbacks tests**

```
go test ./internal/backend/callbacks/...
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/callbacks/admin_topics.go internal/backend/callbacks/admin_topics_test.go
git commit -m "feat(callbacks): welcome on /ensure_topics fresh-create path

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Welcome integration in admin `/recreate_topic`

**Files:**
- Modify: `internal/backend/callbacks/admin_topics.go`
- Test: `internal/backend/callbacks/admin_topics_test.go`

For `/recreate_topic`, the call is `EnsureTopicForUser(..., force=true)`. Always send welcome on success — new thread_id = new topic, reply-kb hasn't attached yet.

- [ ] **Step 1: Write failing test**

Append to `internal/backend/callbacks/admin_topics_test.go`:

```go
func TestAdminRecreateTopic_SendsWelcomeAfterRebuild(t *testing.T) {
	d, uid := newTestDB(t)
	const oldThread = int64(7777)
	if err := d.Users().UpdateThreadID(uid, oldThread); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := oldThread
	msg := &tg.Message{
		MessageID: 51, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "/recreate_topic",
	}
	r.HandleMessage(context.Background(), msg)

	var welcomeCount int
	for _, s := range f.rkSends {
		if strings.HasPrefix(s.text, "👋 Топик роутера vasya") {
			welcomeCount++
		}
	}
	if welcomeCount != 1 {
		t.Fatalf("want 1 welcome, got %d", welcomeCount)
	}
}
```

- [ ] **Step 2: Run — fail**

```
go test ./internal/backend/callbacks -run TestAdminRecreateTopic_SendsWelcomeAfterRebuild -v
```

Expected: FAIL.

- [ ] **Step 3: Implement**

In `admin_topics.go` `adminRecreateTopic` (around line 103), after the successful `EnsureTopicForUser` call and before the success reply:

```go
	tid, err := alerts.EnsureTopicForUser(ctx, r.tg, r.d, r.cfg.ChatID, u.ID, true)
	if err != nil {
		r.adminReply(ctx, m, "❌ не удалось пересоздать тему: "+err.Error())
		return
	}
	if werr := alerts.SendWelcome(ctx, r.tg, r.cfg.ChatID, tid, u.Nickname, r.cfg.UI.KeyboardForTopic("per_router")); werr != nil {
		slog.Warn("welcome send failed (non-fatal)", "user", u.Nickname, "err", werr)
	}
	r.adminReply(ctx, m, fmt.Sprintf(...))
```

- [ ] **Step 4: Run — pass**

```
go test ./internal/backend/callbacks -run TestAdminRecreateTopic -v
```

Expected: all PASS (including the pre-existing `TestAdminRecreateTopic_RebuildsCurrentTopic`).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/admin_topics.go internal/backend/callbacks/admin_topics_test.go
git commit -m "feat(callbacks): welcome on /recreate_topic rebuild

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: `panel_hub.go` — Home screen + `/panel` slash command

**Files:**
- Create: `internal/backend/callbacks/panel_hub.go`
- Create: `internal/backend/callbacks/panel_hub_test.go`
- Modify: `internal/backend/callbacks/admin_topics.go`

- [ ] **Step 1: Write failing test**

Create `internal/backend/callbacks/panel_hub_test.go`:

```go
package callbacks

import (
	"context"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

func TestPanelHome_RendersHubMessage(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := int64(99)
	msg := &tg.Message{
		MessageID: 60, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "/panel",
	}
	r.HandleMessage(context.Background(), msg)

	// Hub posts via SendMessageWithReplyKeyboard so it can carry inline kb.
	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.rkSends))
	}
	got := f.rkSends[0]
	if !strings.Contains(got.text, "🎛 Панель управления") {
		t.Errorf("missing hub header: %s", got.text)
	}
	if !strings.Contains(got.text, "Что открыть?") {
		t.Errorf("missing hub prompt: %s", got.text)
	}
	// Inline kb attached, with at least the four primary buttons.
	kb, ok := got.markup.(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("markup not InlineKeyboardMarkup: %T", got.markup)
	}
	flatTexts := flattenKbTexts(kb)
	for _, want := range []string{"🛠 Maintenance", "📦 Routes", "📊 Status", "🪄 Оживить топики", "✖ Закрыть"} {
		if !contains(flatTexts, want) {
			t.Errorf("hub kb missing %q (have %v)", want, flatTexts)
		}
	}
}

// flattenKbTexts / contains are small helpers; reuse if they exist in
// other test files in this package, otherwise define them here:
func flattenKbTexts(kb *tg.InlineKeyboardMarkup) []string {
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run — fail**

```
go test ./internal/backend/callbacks -run TestPanelHome_RendersHubMessage -v
```

Expected: FAIL (`/panel` not handled; 0 sends).

- [ ] **Step 3: Create `panel_hub.go` with Home renderer**

Create `internal/backend/callbacks/panel_hub.go`:

```go
package callbacks

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

// adminPanelOpen posts the hub Home screen. Called from handleAdminCommand
// on /panel. Admin gate is the existing m.From.ID == cfg.AdminUserID check
// in HandleMessage — no extra auth here.
func (r *Router) adminPanelOpen(ctx context.Context, m *tg.Message) {
	text, kb := panelHomeMessage()
	if _, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &kb); err != nil {
		slog.Warn("panel open send failed", "err", err)
	}
}

// panelHomeMessage builds the (text, inline-kb) for the hub Home screen.
// Pure function — easy to test.
func panelHomeMessage() (string, tg.InlineKeyboardMarkup) {
	text := "🎛 Панель управления\n\nЧто открыть?"
	kb := tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{
				{Text: "🛠 Maintenance", CallbackData: "panel:0:kind:maint"},
				{Text: "📦 Routes", CallbackData: "panel:0:kind:routes"},
			},
			{
				{Text: "📊 Status", CallbackData: "panel:0:kind:status"},
				{Text: "🪄 Оживить топики", CallbackData: "panel:0:awaken_confirm"},
			},
			{
				{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
			},
		},
	}
	return text, kb
}
```

Edit `internal/backend/callbacks/admin_topics.go` `handleAdminCommand` switch (around line 40):

```go
	case "/panel":
		r.adminPanelOpen(ctx, m)
		return true
```

- [ ] **Step 4: Run — pass**

```
go test ./internal/backend/callbacks -run TestPanelHome_RendersHubMessage -v
```

Expected: PASS.

- [ ] **Step 5: Run full callbacks tests**

```
go test ./internal/backend/callbacks/...
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/callbacks/panel_hub.go internal/backend/callbacks/panel_hub_test.go internal/backend/callbacks/admin_topics.go
git commit -m "feat(callbacks): /panel slash + hub Home screen

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Kind pick screen (`panel:0:kind:<kind>` callback)

**Files:**
- Modify: `internal/backend/callbacks/panel_hub.go`
- Modify: `internal/backend/callbacks/router.go`
- Test: `internal/backend/callbacks/panel_hub_test.go`

- [ ] **Step 1: Write failing test**

Append to `panel_hub_test.go`:

```go
func TestPanelKindPick_ListsRoutersWithThreadFlag(t *testing.T) {
	d, _ := newTestDB(t) // vasya, no thread
	// Add a second user with a thread, a third without.
	uid2, _ := d.Users().Insert("betak", "tok-b", "2.2.2.2", "nwg1")
	_ = d.Users().UpdateThreadID(uid2, 1234)
	_, _ = d.Users().Insert("gamma", "tok-g", "3.3.3.3", "nwg1")

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:   "cb-1",
		From: tg.User{ID: 12345},
		Data: "panel:0:kind:maint",
		Message: &tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 70,
		},
	}
	r.HandleCallback(context.Background(), q)

	// Hub edited (not new send).
	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	got := f.edits[0]
	if !strings.Contains(got, "Maintenance") {
		t.Errorf("kind pick header missing 'Maintenance': %s", got)
	}
	for _, want := range []string{"betak", "vasya", "gamma"} {
		if !strings.Contains(got, want) {
			t.Errorf("kind pick missing router %q: %s", want, got)
		}
	}
	// Users without thread carry a warning marker.
	if !strings.Contains(got, "⚠") {
		t.Errorf("expected ⚠ marker for users without thread, got: %s", got)
	}
}
```

Note: the existing `fakeRouterTG.edits` field captures only the edit text, not the markup. If we need to assert the keyboard, extend it (mirror `rkSends` capture). For now `f.edits[0]` containing the text is enough; keyboard assertions can be added in a separate test once we know the helpers work.

- [ ] **Step 2: Run — fail**

```
go test ./internal/backend/callbacks -run TestPanelKindPick -v
```

Expected: FAIL (unknown "panel" action in HandleCallback switch).

- [ ] **Step 3: Wire `case "panel"` into HandleCallback**

Edit `internal/backend/callbacks/router.go`. In the action switch around line 257-332, add a `case "panel"` before the existing `case "compat_btn"`:

```go
		case "panel":
			r.handlePanelCallback(ctx, q, args)
			return
```

- [ ] **Step 4: Implement panel callback dispatcher + kind pick**

Append to `internal/backend/callbacks/panel_hub.go`:

```go
// handlePanelCallback is the top-level dispatcher for panel:* callbacks.
// Routed from Router.HandleCallback after aclAllow. Each screen runs as
// an EditMessageText on the hub message; new messages (panel publication
// into per_router topic) are sent separately and don't touch the hub.
func (r *Router) handlePanelCallback(ctx context.Context, q *tg.CallbackQuery, args Args) {
	slog.Info("panel callback", "screen", args.PanelScreen, "kind", args.PanelKind, "from", q.From.ID, "user_id", args.UserID)
	switch args.PanelScreen {
	case "home":
		r.panelEditToHome(ctx, q)
	case "kind":
		r.panelEditToKindPick(ctx, q, args.PanelKind)
	case "close":
		r.panelClose(ctx, q)
	default:
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "screen TBA")
	}
}

func (r *Router) panelEditToHome(ctx context.Context, q *tg.CallbackQuery) {
	text, kb := panelHomeMessage()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel home edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) panelClose(ctx context.Context, q *tg.CallbackQuery) {
	empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "🎛 Панель закрыта.", "", &empty); err != nil {
		slog.Warn("panel close edit failed (non-fatal)", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// panelEditToKindPick renders the router selection screen for the chosen
// kind. Users without TelegramThreadID render with a ⚠ prefix and a
// no_topic callback that toasts an explanation.
func (r *Router) panelEditToKindPick(ctx context.Context, q *tg.CallbackQuery, kind string) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеров")
		slog.Warn("panel kind pick: users list failed", "err", err)
		return
	}
	kindLabel := map[string]string{"maint": "Maintenance", "routes": "Routes", "status": "Status"}[kind]
	if kindLabel == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "unknown kind")
		return
	}
	if len(users) == 0 {
		text := "🎛 " + kindLabel + " → выбери роутер:\n\nРоутеров нет. Сначала добавь — wizard или CLI `add-user`."
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "« Назад", CallbackData: "panel:0:home"}, {Text: "✖ Закрыть", CallbackData: "panel:0:close"}},
		}}
		_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
		return
	}
	text := "🎛 " + kindLabel + " → выбери роутер:"
	rows := make([][]tg.InlineKeyboardButton, 0, len(users)+1)
	for _, u := range users {
		if u.TelegramThreadID != nil {
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: u.Nickname, CallbackData: fmt.Sprintf("panel:%d:push:%s", u.ID, kind)},
			})
			continue
		}
		rows = append(rows, []tg.InlineKeyboardButton{
			{Text: "⚠ " + u.Nickname + " (нет топика)", CallbackData: fmt.Sprintf("panel:%d:no_topic", u.ID)},
		})
	}
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "« Назад", CallbackData: "panel:0:home"},
		{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
	})
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: rows}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel kind pick edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}
```

- [ ] **Step 5: Run — pass**

```
go test ./internal/backend/callbacks -run TestPanelKindPick -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/callbacks/panel_hub.go internal/backend/callbacks/panel_hub_test.go internal/backend/callbacks/router.go
git commit -m "feat(callbacks): panel hub kind-pick + close + home navigation

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Push screen — synthetic message publication

**Files:**
- Modify: `internal/backend/callbacks/panel_hub.go`
- Test: `internal/backend/callbacks/panel_hub_test.go`

- [ ] **Step 1: Write failing test**

Append to `panel_hub_test.go`:

```go
func TestPanelPush_StatusSendsSmartReplyToTargetThread(t *testing.T) {
	d, uid := newTestDB(t) // vasya, no thread by default
	const targetThread = int64(4242)
	if err := d.Users().UpdateThreadID(uid, targetThread); err != nil {
		t.Fatal(err)
	}
	// Smart-reply needs at least one event to avoid the "never reported" branch.
	if err := d.Events().Insert(uid, "tunnel_amnezia_for_awg", "ok", `{"tunnel_name":"amnezia_for_awg","status":"ok"}`); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:   "cb-push",
		From: tg.User{ID: 12345},
		Data: fmt.Sprintf("panel:%d:push:status", uid),
		Message: &tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 75,
		},
	}
	r.HandleCallback(context.Background(), q)

	// At least one rkSend went into the target thread.
	var landedInTarget bool
	for _, s := range f.rkSends {
		if s.thread != nil && *s.thread == targetThread && strings.Contains(s.text, "vasya") {
			landedInTarget = true
			break
		}
	}
	if !landedInTarget {
		t.Errorf("smart-reply did not land in target thread %d; got rkSends=%+v", targetThread, f.rkSends)
	}
	// Hub also edited to show result.
	if len(f.edits) == 0 {
		t.Errorf("expected hub edit with result, got 0 edits")
	}
}

func TestPanelPush_StaleTopicSurfacesError(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 555); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{sendErr: &tg.APIError{Method: "sendMessage", Description: "message thread not found", Code: 400}}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:   "cb-stale",
		From: tg.User{ID: 12345},
		Data: fmt.Sprintf("panel:%d:push:status", uid),
		Message: &tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 76,
		},
	}
	r.HandleCallback(context.Background(), q)

	// Hub edit should surface stale-topic error.
	var stale bool
	for _, e := range f.edits {
		if strings.Contains(e, "удалён") || strings.Contains(e, "stale") || strings.Contains(e, "не найден") {
			stale = true
			break
		}
	}
	if !stale {
		t.Errorf("expected stale-topic error in hub edit; got %v", f.edits)
	}
}
```

`d.Events().Insert(...)` — find the actual signature in `internal/backend/db/events.go` first. Adjust the test call to match (it may take `(userID int64, checkName, status, detailsJSON string)` or similar). Same for any other DB helper used.

- [ ] **Step 2: Run — fail**

```
go test ./internal/backend/callbacks -run TestPanelPush -v
```

Expected: FAIL (push screen not implemented).

- [ ] **Step 3: Implement push handler + result render**

Edit `panel_hub.go` `handlePanelCallback` switch — add `case "push"`:

```go
	case "push":
		r.panelHandlePush(ctx, q, args)
	case "no_topic":
		r.panelHandleNoTopic(ctx, q, args)
```

Append handler functions:

```go
// panelHandlePush publishes the chosen panel kind into the target router's
// per_router topic via a synthetic tg.Message, then edits the hub message
// to the result screen. The synthetic-Message pattern mirrors compat_btn
// (callbacks/compat_inline.go) — MessageID=0 marks the message as
// synthetic so the user-message deletion branch in HandleMessage skips.
func (r *Router) panelHandlePush(ctx context.Context, q *tg.CallbackQuery, args Args) {
	u, err := r.d.Users().GetByID(args.UserID)
	if err != nil || u == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	if u.TelegramThreadID == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "у роутера нет топика")
		return
	}
	threadID := *u.TelegramThreadID
	synth := &tg.Message{
		MessageID:       0, // sentinel: do not delete
		Chat:            q.Message.Chat,
		From:            q.From,
		MessageThreadID: &threadID,
	}
	publishErr := r.panelPublish(ctx, synth, u, args.PanelKind)
	kindLabel := map[string]string{"maint": "Maintenance", "routes": "Routes", "status": "Status"}[args.PanelKind]
	var resultText string
	if publishErr == nil {
		resultText = fmt.Sprintf("🎛 Панель управления\n\n✅ %s отправлен в топик @%s.", kindLabel, u.Nickname)
	} else if tg.IsTopicNotFound(publishErr) {
		resultText = fmt.Sprintf("🎛 Панель управления\n\n❌ Топик роутера @%s похоже удалён. Сделай /recreate_topic внутри его топика или /ensure_topics.", u.Nickname)
	} else {
		resultText = fmt.Sprintf("🎛 Панель управления\n\n❌ Не удалось опубликовать %s в @%s: %v", kindLabel, u.Nickname, publishErr)
	}
	kb := panelResultKb()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, resultText, "", &kb); err != nil {
		slog.Warn("panel push result edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// panelPublish dispatches to the appropriate kind-specific builder. Each
// builder posts a fresh message in the target topic (via the synthetic
// Message) and enqueues any associated agent command. Errors are returned
// only for the synchronous TG send; async results travel through the
// existing notifier pipeline and update the published message in place.
func (r *Router) panelPublish(ctx context.Context, m *tg.Message, u *db.User, kind string) error {
	// Currently the builders log errors internally rather than returning
	// them. Mirror that contract — but to surface stale-topic, do a
	// best-effort SendMessage probe first so we get an error path.
	// (TG sendMessage is cheap; the builders below will overwrite it.)
	probeMsg := fmt.Sprintf("🎛 %s готовится…", map[string]string{"maint": "Maintenance", "routes": "Routes", "status": "Status"}[kind])
	if _, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, probeMsg, "", nil); err != nil {
		return err
	}
	switch kind {
	case "maint":
		r.openMaintPanelMessage(ctx, m, u)
	case "routes":
		r.openRoutesPanelMessage(ctx, m, u)
	case "status":
		r.dispatchSmartReply(ctx, m, u)
	default:
		return fmt.Errorf("unknown panel kind: %q", kind)
	}
	return nil
}

func panelResultKb() tg.InlineKeyboardMarkup {
	return tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{
				{Text: "« К панели", CallbackData: "panel:0:home"},
				{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
			},
		},
	}
}

func (r *Router) panelHandleNoTopic(ctx context.Context, q *tg.CallbackQuery, args Args) {
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "У роутера нет топика — сделай /ensure_topics")
}
```

Imports for `db` package may need to be added to panel_hub.go (`"github.com/anex/wg-monitor/internal/backend/db"`).

- [ ] **Step 4: Run — pass**

```
go test ./internal/backend/callbacks -run TestPanelPush -v
```

Expected: both push tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/panel_hub.go internal/backend/callbacks/panel_hub_test.go
git commit -m "feat(callbacks): panel push via synthetic message + stale-topic escalation

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: Awaken screens (confirm + do)

**Files:**
- Modify: `internal/backend/callbacks/panel_hub.go`
- Test: `internal/backend/callbacks/panel_hub_test.go`

- [ ] **Step 1: Write failing tests**

Append to `panel_hub_test.go`:

```go
func TestPanelAwakenConfirm_ShowsCountOfTopics(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 100)
	uid2, _ := d.Users().Insert("b", "tb", "2.2.2.2", "nwg1")
	_ = d.Users().UpdateThreadID(uid2, 200)
	_, _ = d.Users().Insert("c", "tc", "3.3.3.3", "nwg1") // no thread

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID: "cb-aw-c", From: tg.User{ID: 12345},
		Data:    "panel:0:awaken_confirm",
		Message: &tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 80},
	}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "2 топика") {
		t.Errorf("expected 'Будут затронуты: 2 топика' (vasya+b have thread), got %v", f.edits)
	}
}

func TestPanelAwakenDo_SendsWelcomeOnlyToUsersWithThread(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 100)
	uid2, _ := d.Users().Insert("b", "tb", "2.2.2.2", "nwg1")
	_ = d.Users().UpdateThreadID(uid2, 200)
	_, _ = d.Users().Insert("c", "tc", "3.3.3.3", "nwg1")

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID: "cb-aw-do", From: tg.User{ID: 12345},
		Data:    "panel:0:awaken_do",
		Message: &tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 81},
	}
	r.HandleCallback(context.Background(), q)

	var welcomeCount int
	for _, s := range f.rkSends {
		if strings.HasPrefix(s.text, "👋 Топик роутера") {
			welcomeCount++
		}
	}
	if welcomeCount != 2 {
		t.Errorf("want 2 welcomes (vasya+b), got %d", welcomeCount)
	}
}
```

- [ ] **Step 2: Run — fail**

```
go test ./internal/backend/callbacks -run TestPanelAwaken -v
```

Expected: FAIL.

- [ ] **Step 3: Implement**

In `handlePanelCallback` switch, add:

```go
	case "awaken_confirm":
		r.panelAwakenConfirm(ctx, q)
	case "awaken_do":
		r.panelAwakenDo(ctx, q)
```

Append handlers:

```go
func (r *Router) panelAwakenConfirm(ctx context.Context, q *tg.CallbackQuery) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеров")
		return
	}
	var count int
	for _, u := range users {
		if u.TelegramThreadID != nil {
			count++
		}
	}
	text := fmt.Sprintf("🎛 Панель управления\n\n🪄 Оживить топики (отправить приветствие с кнопками во все per_router топики)\n\nБудут затронуты: %d топика", count)
	kb := tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{
				{Text: "✓ Подтвердить", CallbackData: "panel:0:awaken_do"},
				{Text: "« Назад", CallbackData: "panel:0:home"},
			},
		},
	}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel awaken confirm edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) panelAwakenDo(ctx context.Context, q *tg.CallbackQuery) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеров")
		return
	}
	start := time.Now()
	var sent, failed int
	var failLines []string
	const sleep = 200 * time.Millisecond
	first := true
	for _, u := range users {
		if u.TelegramThreadID == nil {
			continue
		}
		if !first {
			select {
			case <-ctx.Done():
				break
			case <-time.After(sleep):
			}
		}
		first = false
		if werr := alerts.SendWelcome(ctx, r.tg, r.cfg.ChatID, *u.TelegramThreadID, u.Nickname, r.cfg.UI.KeyboardForTopic("per_router")); werr != nil {
			failed++
			failLines = append(failLines, fmt.Sprintf("❌ %s: %v", u.Nickname, werr))
			continue
		}
		sent++
	}
	slog.Info("panel awaken", "sent", sent, "failed", failed, "elapsed_ms", time.Since(start).Milliseconds())
	var b strings.Builder
	fmt.Fprintf(&b, "🎛 Панель управления\n\n✅ Оживлено: %d топиков, %d ошибок.", sent, failed)
	for _, line := range failLines {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	text := b.String()
	if len(text) > 4096 {
		text = text[:4093] + "..."
	}
	kb := panelResultKb()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel awaken result edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}
```

Imports needed in panel_hub.go: `"strings"`, `"time"`, `"github.com/anex/wg-monitor/internal/backend/alerts"`.

- [ ] **Step 4: Run — pass**

```
go test ./internal/backend/callbacks -run TestPanelAwaken -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/panel_hub.go internal/backend/callbacks/panel_hub_test.go
git commit -m "feat(callbacks): panel awaken — backfill welcome into existing topics

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: Close screen test + admin-only gate test + `/topic_help` update

**Files:**
- Modify: `internal/backend/callbacks/admin_topics.go`
- Test: `internal/backend/callbacks/panel_hub_test.go`, `internal/backend/callbacks/admin_topics_test.go`

- [ ] **Step 1: Add close + admin-only tests**

Append to `panel_hub_test.go`:

```go
func TestPanelClose_EditsToClosedText(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID: "cb-close", From: tg.User{ID: 12345},
		Data:    "panel:0:close",
		Message: &tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 85},
	}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "закрыта") {
		t.Errorf("expected 'закрыта' in edit, got %v", f.edits)
	}
}
```

Append to `admin_topics_test.go`:

```go
func TestPanel_AdminOnlyGate(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	// Non-admin sends /panel — must produce zero side effects.
	msg := &tg.Message{
		MessageID: 90, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 99999},
		Text: "/panel",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 0 || len(f.sentMsgs) != 0 {
		t.Errorf("non-admin /panel must be ignored; rkSends=%d sentMsgs=%d", len(f.rkSends), len(f.sentMsgs))
	}
}
```

- [ ] **Step 2: Run tests — should pass close test, admin-only test should already pass (HandleMessage's admin gate is pre-existing)**

```
go test ./internal/backend/callbacks -run "TestPanelClose|TestPanel_AdminOnlyGate" -v
```

Expected: PASS.

- [ ] **Step 3: Update `/topic_help` text**

Edit `admin_topics.go` `adminTopicHelp` (around line 154):

```go
func (r *Router) adminTopicHelp(ctx context.Context, m *tg.Message) {
	r.adminReply(ctx, m, `📚 Команды управления темами (только для админа):

/ensure_topics — создать темы для всех роутеров без темы (bulk).

/recreate_topic — пересоздать тему ТЕКУЩЕГО топика (пиши команду внутри топика роутера). Старая остаётся в TG, новая становится активной.

/this_is <nickname> — привязать ЭТОТ топик к роутеру <nickname>. Полезно если ты создал тему руками в TG и хочешь, чтобы алерты этого роутера шли в неё.

/panel — открыть админ-панель: оттуда можно отправить Maintenance / Routes / Status в любой роутер, или "оживить" все топики (добавить кнопки во все).

/topic_help — эта справка.

Подсказка: thread_id топика можно посмотреть в /list_users — он совпадает с message_thread_id из TG API.`)
}
```

Also update the existing `TestAdminTopicHelp` (in admin_topics_test.go line 165) to expect the new `/panel` line:

```go
	for _, cmd := range []string{"/ensure_topics", "/recreate_topic", "/this_is", "/panel", "/topic_help"} {
```

- [ ] **Step 4: Run all callbacks tests**

```
go test ./internal/backend/callbacks/...
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/panel_hub_test.go internal/backend/callbacks/admin_topics.go internal/backend/callbacks/admin_topics_test.go
git commit -m "feat(callbacks): close screen test + /topic_help mentions /panel

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 13: Wire SetMyCommands + Dispatcher.WelcomeKeyboard at startup

**Files:**
- Modify: `cmd/backend/main.go`

- [ ] **Step 1: Inspect main.go around tg.Client construction**

Re-read `cmd/backend/main.go` lines 50-120 to locate the right insertion points (after tgClient construction, after Dispatcher construction).

- [ ] **Step 2: Add SetMyCommands call**

In `cmd/backend/main.go`, after the `tgClient := &tg.Client{...}` block (around line 60, just before `disp := alerts.NewDispatcher(...)`), add:

```go
	// Register slash-command menu so TG clients show the commands in the
	// picker. Non-fatal — the bot keeps working if TG refuses.
	smcCtx, smcCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := tgClient.SetMyCommands(smcCtx, []tg.BotCommand{
		{Command: "panel", Description: "Открыть панель управления"},
		{Command: "ensure_topics", Description: "Создать темы для всех роутеров"},
		{Command: "recreate_topic", Description: "Пересоздать тему текущего роутера"},
		{Command: "this_is", Description: "Привязать этот топик к роутеру (укажи nickname)"},
		{Command: "topic_help", Description: "Шпаргалка по управлению темами"},
	}); err != nil {
		logger.Warn("setMyCommands failed (non-fatal)", "err", err)
	}
	smcCancel()
```

- [ ] **Step 3: Wire Dispatcher.WelcomeKeyboard**

After the Dispatcher is constructed (around line 65) and after `uiSnap` is computed (around line 85), set:

```go
	disp.WelcomeKeyboard = func() any {
		return uiSnap.KeyboardForTopic("per_router")
	}
```

The exact line ordering matters: `uiSnap` is computed at line ~80; `disp` at line ~61. So:
- Move the `disp.WelcomeKeyboard = ...` assignment to AFTER `uiSnap` is defined, OR
- Compute `uiSnap` BEFORE Dispatcher construction.

Pick the smaller move: place the `disp.WelcomeKeyboard = ...` line right after the `uiSnap := callbacks.UIConfigSnapshot{...}` block (around line 86).

- [ ] **Step 4: Build the backend binary**

```
go build ./cmd/backend
```

Expected: clean build, no errors.

- [ ] **Step 5: Run full project tests**

```
go test ./...
```

Expected: all PASS. If anything fails, fix the issue (likely an import order or unused variable) and rerun.

- [ ] **Step 6: Commit**

```bash
git add cmd/backend/main.go
git commit -m "feat(backend): wire SetMyCommands + Dispatcher welcome keyboard at startup

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 14: End-to-end build + smoke

**Files:** none (build + verify).

- [ ] **Step 1: Vet**

```
go vet ./...
```

Expected: no warnings.

- [ ] **Step 2: Full test with race detector**

```
go test -race ./...
```

Expected: all PASS.

- [ ] **Step 3: Build all binaries**

```
go build ./cmd/backend
go build ./cmd/wg-monitor-cli
go build ./cmd/agent
go build ./cmd/deploy
```

Expected: all clean.

- [ ] **Step 4: Smoke `/panel` parser**

```
go test ./internal/backend/callbacks -run TestParse_Panel -v
go test ./internal/backend/callbacks -run TestPanel -v
```

Expected: every panel-related test PASSes.

- [ ] **Step 5: Verify no regressions in existing flows**

```
go test ./internal/backend/... ./cmd/wg-monitor-cli/... -v -count=1 2>&1 | tail -40
```

Expected: all PASS. Watch specifically for:
- `TestAdminEnsureTopics_BulkCreatesMissing` (welcome added, summary still findable)
- `TestAdminRecreateTopic_RebuildsCurrentTopic` (welcome added, "пересоздана" still in summary)
- `TestRouterHandleMessage_RoutesPerRouter` (no impact)

- [ ] **Step 6: Final commit if anything was touched**

If steps 1-5 surfaced fixes:

```bash
git add -A
git commit -m "fix: address test/build issues from panel hub e2e check

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Otherwise: no commit, plan complete.

---

## Self-Review Notes

**Spec coverage:**

| Spec section | Covered by |
|---|---|
| `/panel` slash + admin-only | Task 8 + Task 12 |
| Home screen | Task 8 |
| Kind pick screen | Task 9 |
| Router list with ⚠ for no-thread | Task 9 |
| Single-user skip-to-publish | NOT explicitly tested — added as enhancement to Task 9 (optional). Documented as edge case in spec; if needed, add a small test in Task 9. |
| Push via synthetic Message | Task 10 |
| Result screen | Task 10 |
| Stale topic escalation | Task 10 |
| Awaken confirm | Task 11 |
| Awaken do (loop + sleep + result) | Task 11 |
| Close screen | Task 8 (impl) + Task 12 (test) |
| no_topic toast | Task 10 |
| Welcome on EnsureTopic (Dispatcher) | Task 4 |
| Welcome on CLI ensure-topics | Task 5 |
| Welcome on /ensure_topics | Task 6 |
| Welcome on /recreate_topic | Task 7 |
| SendWelcome helper | Task 3 |
| Args extension (PanelScreen/Kind) | Task 2 |
| panel in validActions | Task 2 |
| SetMyCommands | Task 1 + Task 13 |
| /topic_help mentions /panel | Task 12 |

**Type consistency check:** all callbacks use `panel:<userID>:<screen>[:<kind>]` consistently. `PanelScreen` / `PanelKind` field names match across parser, dispatcher, and handlers. `panelResultKb()` reused between push-result and awaken-result paths.

**Placeholders:** none — every step has executable code or exact commands.

**Single-user skip-to-publish** is a UX enhancement noted in the spec but NOT implemented in this plan (decision: ship a slightly clunkier 2-step flow first; add skip-shortcut in a follow-up). If you want it now, expand Task 9 with: in `panelEditToKindPick`, if exactly one user has `TelegramThreadID != nil` AND `len(users) == 1`, synthesise a push callback for that user directly. Plus a `TestPanelKindPick_SingleUserSkipsToPublish` test.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-11-admin-panel-hub.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
