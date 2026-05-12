# Router Operators Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-router whitelist of additional Telegram users (`router_operators`) managed through a new `👥 Доступ` screen in the `/panel` admin hub. Operators get full owner-equivalent access; only the global `cfg.AdminUserID` manages bindings.

**Architecture:** New SQLite table `router_operators(user_id, telegram_user_id, granted_by, granted_at)` with `ON DELETE CASCADE` to `users(id)`. ACL extension in `Router.aclAllow()` is a single-line addition checking `HasAccess`. UI in `internal/backend/callbacks/access_panel.go` mirrors the inline-router-methods pattern used by `panel_hub.go` (no separate Action interface). Add-operator FSM (`pendingAddOperatorStore`) lives in `Router` and intercepts messages in the admin's private DM with the bot.

**Tech Stack:** Go 1.23, SQLite (modernc.org/sqlite), existing wg-monitor callback/parse/db patterns.

---

## File Structure

**Created:**

| File | Purpose |
|---|---|
| `internal/backend/db/router_operators.go` | `Operator` type + `RouterOperators` repo (`Add`/`Remove`/`List`/`HasAccess`) |
| `internal/backend/db/router_operators_test.go` | Repo unit tests including FK CASCADE |
| `internal/backend/callbacks/access_panel.go` | `pendingAddOperatorStore`, render helpers (`accessHomeMessage`, `accessRouterMessage`), router methods (`handleAccessCallback` + sub-screens), `processAddOperatorMessage` |
| `internal/backend/callbacks/access_panel_test.go` | Renderer + FSM + dispatch tests |

**Modified:**

| File | Change |
|---|---|
| `internal/backend/db/migrations.sql` | Add `router_operators` table DDL |
| `internal/backend/db/db.go` | Add `RouterOperators() *RouterOperatorsRepo` accessor on `*DB` |
| `internal/backend/callbacks/parse.go` | Add `access` to `validActions`, parse `access:*` shape, add `AccessScreen`/`AccessRouterID`/`AccessOperatorTGID` fields to `Args` |
| `internal/backend/callbacks/parse_test.go` | Cover access grammar |
| `internal/backend/callbacks/router.go` | Add `pendingAddOperator` field, init in `NewRouterWithSink`, extend `aclAllow` (one line), add `access:*` dispatch in `HandleCallback` with admin-only gate, add FSM hook at top of `HandleMessage` |
| `internal/backend/callbacks/router_test.go` | Cover `aclAllow` operator path + admin gate on `access:*` |
| `internal/backend/callbacks/panel_hub.go` | Add `👥 Доступ` row to `panelHomeMessage` (always shown — admin gate enforced at dispatch, not in render) |
| `README.md` | One-line entry in feature table |

---

## Task 1: Add `router_operators` table to migrations

**Files:**
- Modify: `internal/backend/db/migrations.sql`

- [ ] **Step 1: Add DDL**

Append to `internal/backend/db/migrations.sql` (after the existing `tg_state` table at the bottom):

```sql
CREATE TABLE IF NOT EXISTS router_operators (
    user_id          INTEGER NOT NULL,
    telegram_user_id INTEGER NOT NULL,
    granted_by       INTEGER NOT NULL,
    granted_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, telegram_user_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
-- HasAccess fires on every callback to a router-scoped action; needs an
-- index on the lookup pair. Composite PK already gives the right key order
-- (user_id, telegram_user_id) so no extra index needed.
```

- [ ] **Step 2: Verify build + migration applies on a fresh DB**

```
go build ./...
```

(No test runs yet — migration is exercised by the repo tests in Task 2.)

- [ ] **Step 3: Commit**

```
git add internal/backend/db/migrations.sql
git commit -m "@'
feat(db): router_operators table — per-router TG user whitelist

ON DELETE CASCADE keeps the table tidy when a router record is deleted.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

(PowerShell single-quoted here-string; the `git commit -m` value is the
whole `@'...'@` block.)

---

## Task 2: `RouterOperators` repository

**Files:**
- Create: `internal/backend/db/router_operators.go`
- Create: `internal/backend/db/router_operators_test.go`
- Modify: `internal/backend/db/db.go` (add accessor)

- [ ] **Step 1: Write the failing tests**

Create `internal/backend/db/router_operators_test.go`:

```go
package db

import (
	"path/filepath"
	"testing"
)

// newTestDBForOps opens a fresh DB in a temp dir and inserts two router
// users (ids 1 and 2). Returns the DB and the two ids.
func newTestDBForOps(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id1, err := d.Users().Insert("router-a", "tok-a", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatalf("insert a: %v", err)
	}
	id2, err := d.Users().Insert("router-b", "tok-b", "2.2.2.2", "awg11")
	if err != nil {
		t.Fatalf("insert b: %v", err)
	}
	return d, id1, id2
}

func TestRouterOperators_AddListRoundTrip(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	if err := d.RouterOperators().Add(routerA, 1001, 999); err != nil {
		t.Fatalf("add op1: %v", err)
	}
	if err := d.RouterOperators().Add(routerA, 1002, 999); err != nil {
		t.Fatalf("add op2: %v", err)
	}
	got, err := d.RouterOperators().List(routerA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 operators, got %d", len(got))
	}
	if got[0].TelegramUserID != 1001 || got[1].TelegramUserID != 1002 {
		t.Errorf("order wrong: %+v", got)
	}
	if got[0].GrantedBy != 999 {
		t.Errorf("granted_by=%d, want 999", got[0].GrantedBy)
	}
}

func TestRouterOperators_AddIdempotent(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	if err := d.RouterOperators().Add(routerA, 1001, 999); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := d.RouterOperators().Add(routerA, 1001, 888); err != nil {
		t.Fatalf("second add should not error: %v", err)
	}
	got, _ := d.RouterOperators().List(routerA)
	if len(got) != 1 {
		t.Errorf("expected 1 row after dup add, got %d", len(got))
	}
	if got[0].GrantedBy != 999 {
		t.Errorf("original granted_by must be preserved, got %d", got[0].GrantedBy)
	}
}

func TestRouterOperators_Remove(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	_ = d.RouterOperators().Add(routerA, 1001, 999)
	_ = d.RouterOperators().Add(routerA, 1002, 999)
	if err := d.RouterOperators().Remove(routerA, 1001); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, _ := d.RouterOperators().List(routerA)
	if len(got) != 1 || got[0].TelegramUserID != 1002 {
		t.Errorf("expected only 1002 to remain, got %+v", got)
	}
}

func TestRouterOperators_HasAccess(t *testing.T) {
	d, routerA, routerB := newTestDBForOps(t)
	_ = d.RouterOperators().Add(routerA, 1001, 999)
	if !d.RouterOperators().HasAccess(routerA, 1001) {
		t.Error("HasAccess should be true for added pair")
	}
	if d.RouterOperators().HasAccess(routerA, 9999) {
		t.Error("HasAccess should be false for unknown tg id")
	}
	if d.RouterOperators().HasAccess(routerB, 1001) {
		t.Error("HasAccess should be false for different router")
	}
}

func TestRouterOperators_CascadeOnUserDelete(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	_ = d.RouterOperators().Add(routerA, 1001, 999)
	if _, err := d.SQL().Exec(`DELETE FROM users WHERE id = ?`, routerA); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	got, err := d.RouterOperators().List(routerA)
	if err != nil {
		t.Fatalf("list after cascade: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cascade should have emptied operators, got %d rows", len(got))
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/backend/db/ -run TestRouterOperators -v`
Expected: FAIL — `d.RouterOperators undefined`.

- [ ] **Step 3: Implement the repo**

Create `internal/backend/db/router_operators.go`:

```go
package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Operator is one whitelist row: a Telegram user authorised to control a
// specific router (in addition to the router's owner stored in
// users.telegram_user_id).
type Operator struct {
	UserID         int64
	TelegramUserID int64
	GrantedBy      int64 // TG ID of the admin who granted access
	GrantedAt      time.Time
}

type RouterOperatorsRepo struct{ d *DB }

// RouterOperators returns the typed accessor for the router_operators table.
func (d *DB) RouterOperators() *RouterOperatorsRepo { return &RouterOperatorsRepo{d: d} }

// Add inserts a whitelist row. INSERT OR IGNORE makes the call idempotent —
// re-adding an existing (user_id, telegram_user_id) pair is a no-op that
// preserves the original granted_by / granted_at.
func (r *RouterOperatorsRepo) Add(userID, telegramUserID, grantedBy int64) error {
	_, err := r.d.db.Exec(
		`INSERT OR IGNORE INTO router_operators(user_id, telegram_user_id, granted_by) VALUES (?, ?, ?)`,
		userID, telegramUserID, grantedBy,
	)
	if err != nil {
		return fmt.Errorf("router_operators.Add: %w", err)
	}
	return nil
}

// Remove deletes one operator from one router. Missing rows are not an
// error — DELETE is idempotent.
func (r *RouterOperatorsRepo) Remove(userID, telegramUserID int64) error {
	_, err := r.d.db.Exec(
		`DELETE FROM router_operators WHERE user_id = ? AND telegram_user_id = ?`,
		userID, telegramUserID,
	)
	if err != nil {
		return fmt.Errorf("router_operators.Remove: %w", err)
	}
	return nil
}

// List returns operators for a router ordered by GrantedAt ASC (oldest
// first), which is the rendering order on the access screen.
func (r *RouterOperatorsRepo) List(userID int64) ([]Operator, error) {
	rows, err := r.d.db.Query(
		`SELECT user_id, telegram_user_id, granted_by, granted_at
		 FROM router_operators WHERE user_id = ? ORDER BY granted_at ASC, telegram_user_id ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("router_operators.List: %w", err)
	}
	defer rows.Close()
	var out []Operator
	for rows.Next() {
		var op Operator
		if err := rows.Scan(&op.UserID, &op.TelegramUserID, &op.GrantedBy, &op.GrantedAt); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// HasAccess is the hot path for ACL: called from Router.aclAllow on every
// non-admin, non-owner callback. Single indexed-lookup query.
func (r *RouterOperatorsRepo) HasAccess(userID, telegramUserID int64) bool {
	var one int
	err := r.d.db.QueryRow(
		`SELECT 1 FROM router_operators WHERE user_id = ? AND telegram_user_id = ?`,
		userID, telegramUserID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		// Defensive: log via slog from caller; here we just deny.
		return false
	}
	return one == 1
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/backend/db/ -run TestRouterOperators -v`
Expected: PASS — all 5 tests green.

Run the whole package too: `go test ./internal/backend/db/ -v`. Expected: green.

- [ ] **Step 5: Commit**

```
git add internal/backend/db/router_operators.go internal/backend/db/router_operators_test.go
git commit -m "@'
feat(db): RouterOperators repo — Add/Remove/List/HasAccess

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

---

## Task 3: ACL extension in `Router.aclAllow`

**Files:**
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/router_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/backend/callbacks/router_test.go`:

```go
func TestAclAllow_OperatorAllowed(t *testing.T) {
	d := newTestDB(t)
	uid, err := d.Users().Insert("router-x", "tok-x", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Owner is bound to TG user 100; we test that TG user 200 (operator)
	// is also allowed.
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 999)

	r := NewRouterWithSink(d, &fakeRouterTG{}, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	q := &tg.CallbackQuery{ID: "q", From: &tg.User{ID: 200}, Message: &tg.Message{Chat: tg.Chat{ID: 7}}}

	if !r.aclAllow(context.Background(), q, Args{UserID: uid}) {
		t.Error("operator (TG 200) should be allowed for router uid")
	}
}

func TestAclAllow_FormerOperatorDenied(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("router-y", "tok-y", "1.1.1.1", "awg11")
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 999)
	_ = d.RouterOperators().Remove(uid, 200)

	r := NewRouterWithSink(d, &fakeRouterTG{}, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	q := &tg.CallbackQuery{ID: "q", From: &tg.User{ID: 200}, Message: &tg.Message{Chat: tg.Chat{ID: 7}}}

	if r.aclAllow(context.Background(), q, Args{UserID: uid}) {
		t.Error("removed operator (TG 200) must not be allowed")
	}
}
```

(The helpers `newTestDB`, `fakeRouterTG`, `fakeEnqueuer` already exist in
`router_test.go` from prior tasks. Use them as-is. If `aclAllow` has a
slightly different signature, adapt the call accordingly — read the
existing function in router.go first.)

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/backend/callbacks/ -run "TestAclAllow_(Operator|FormerOperator)" -v`
Expected: FAIL — the operator path is not yet wired into `aclAllow`.

(The "FormerOperator" test may pass coincidentally because the existing
`aclAllow` already returns false for non-owner non-admin. Don't celebrate
— it doesn't test the new code path. The "Operator" test should fail.)

- [ ] **Step 3: Extend `aclAllow`**

In `internal/backend/callbacks/router.go`, find the `aclAllow` function
(currently around line 438). The existing flow is roughly:

```go
// admin bypass
// args.UserID == 0 bypass
// look up user by args.UserID
// if user.TelegramUserID != nil:
//     if *user.TelegramUserID == q.From.ID: allow
//     else: reject with toast
// TOFU fallback
```

Insert the operator check **after the owner-match logic but before TOFU
fallback**. Specifically: after the existing `if user.TelegramUserID != nil
{ ... }` block (which either returns true on match or returns false with
a toast on mismatch), and before the TOFU block.

Wait — the current logic returns from `aclAllow` on owner mismatch with
`«это не твой роутер»`. We need the operator check to run BEFORE that
mismatch rejection, otherwise operators get rejected as "not your router".

Read the function carefully. The right placement is:

- Replace this snippet (the mismatch branch inside the
  `if user.TelegramUserID != nil` block):

```go
if user.TelegramUserID != nil {
    if *user.TelegramUserID == q.From.ID {
        return true
    }
    _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "это не твой роутер")
    return false
}
```

- With this:

```go
if user.TelegramUserID != nil {
    if *user.TelegramUserID == q.From.ID {
        return true
    }
    // Owner mismatch — try the operator whitelist before rejecting.
    if r.d.RouterOperators().HasAccess(user.ID, q.From.ID) {
        return true
    }
    _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "это не твой роутер")
    return false
}
```

The TOFU block (when owner is nil) does NOT change — TOFU is owner-only.

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/backend/callbacks/ -run TestAclAllow -v`
Expected: PASS — both new tests plus all existing aclAllow tests.

Run full package: `go test ./internal/backend/callbacks/ -v`. Expected:
green.

- [ ] **Step 5: Commit**

```
git add internal/backend/callbacks/router.go internal/backend/callbacks/router_test.go
git commit -m "@'
feat(callbacks): aclAllow consults router_operators after owner check

Owners still match first; if the caller is not the owner, the
router_operators whitelist is consulted before the
\"это не твой роутер\" rejection.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

---

## Task 4: Parse grammar for `access:*`

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/parse_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/backend/callbacks/parse_test.go`:

```go
func TestParse_Access_Home(t *testing.T) {
	a, err := Parse("access:0:home")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.Action != "access" || a.AccessScreen != "home" {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_Router(t *testing.T) {
	a, err := Parse("access:0:router:42")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.AccessScreen != "router" || a.AccessRouterID != 42 {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_Add(t *testing.T) {
	a, err := Parse("access:0:add:42")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.AccessScreen != "add" || a.AccessRouterID != 42 {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_RemoveOp(t *testing.T) {
	a, err := Parse("access:0:remove_op:42:1234567890")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.AccessScreen != "remove_op" || a.AccessRouterID != 42 || a.AccessOperatorTGID != 1234567890 {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_UnbindOwner(t *testing.T) {
	a, err := Parse("access:0:unbind_owner:42")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.AccessScreen != "unbind_owner" || a.AccessRouterID != 42 {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_BackCancel(t *testing.T) {
	for _, s := range []string{"access:0:back", "access:0:cancel_add"} {
		a, err := Parse(s)
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		if a.Action != "access" {
			t.Errorf("%s: a=%+v", s, a)
		}
	}
}

func TestParse_Access_Errors(t *testing.T) {
	for _, bad := range []string{
		"access:0:bogus",            // unknown screen
		"access:0:router",           // missing router id
		"access:0:router:",          // empty router id
		"access:0:router:abc",       // non-numeric
		"access:0:remove_op:42",     // missing tg id
		"access:0:remove_op:42:abc", // non-numeric tg id
		"access:0:add",              // missing router id
		"access:0:unbind_owner",     // missing router id
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should have errored", bad)
		}
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/backend/callbacks/ -run "TestParse_Access" -v`
Expected: FAIL — `Args` lacks `AccessScreen` etc., `access` not in
`validActions`.

- [ ] **Step 3: Extend `parse.go`**

In `internal/backend/callbacks/parse.go`:

(a) Add fields to `Args` struct (near other screen/token fields):

```go
	// AccessScreen identifies the access:* admin-panel screen for callbacks
	// where Action == "access". One of: "home" | "router" | "add" |
	// "remove_op" | "unbind_owner" | "back" | "cancel_add".
	AccessScreen string
	// AccessRouterID is the users.id of the router whose access list is
	// being viewed/modified. Set for "router" / "add" / "remove_op" /
	// "unbind_owner" screens.
	AccessRouterID int64
	// AccessOperatorTGID is the target operator's TG user ID for remove_op.
	AccessOperatorTGID int64
```

(b) Add `"access": true,` to the `validActions` map (place near `panel`):

```go
	// admin panel hub — multi-screen inline-kb dispatcher.
	"panel": true,
	// admin access-control panel — per-router operator whitelist.
	"access": true,
```

(c) Add parsing case in the existing `switch action` block (place after
the `panel` case at the end):

```go
	if action == "access" {
		screen := parts[2]
		validAccessScreens := map[string]bool{
			"home": true, "router": true, "add": true,
			"remove_op": true, "unbind_owner": true,
			"back": true, "cancel_add": true,
		}
		if !validAccessScreens[screen] {
			return Args{}, fmt.Errorf("access: unknown screen %q", screen)
		}
		a.AccessScreen = screen
		switch screen {
		case "router", "add", "unbind_owner":
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("access %s requires router id: %q", screen, data)
			}
			rid, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access %s: bad router id %q", screen, parts[3])
			}
			a.AccessRouterID = rid
		case "remove_op":
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("access remove_op requires router id and tg id: %q", data)
			}
			rid, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access remove_op: bad router id %q", parts[3])
			}
			tgid, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access remove_op: bad tg id %q", parts[4])
			}
			a.AccessRouterID = rid
			a.AccessOperatorTGID = tgid
		}
	}
```

`strconv` should already be imported (used by the user-id parse at line ~125).

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/backend/callbacks/ -run "TestParse_Access" -v`
Expected: PASS — all 7 access parse tests.

Run full package: `go test ./internal/backend/callbacks/ -v`. Expected:
green.

- [ ] **Step 5: Commit**

```
git add internal/backend/callbacks/parse.go internal/backend/callbacks/parse_test.go
git commit -m "@'
feat(callbacks): parse access:* callback grammar

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

---

## Task 5: `pendingAddOperatorStore`

**Files:**
- Create: `internal/backend/callbacks/access_panel.go` (new file, starts with the store)
- Create: `internal/backend/callbacks/access_panel_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/backend/callbacks/access_panel_test.go`:

```go
package callbacks

import (
	"testing"
	"time"
)

func TestPendingAddOperator_PutGetClear(t *testing.T) {
	s := newPendingAddOperatorStore()
	s.put(42, 100, 5*time.Minute)
	got, ok := s.get(42)
	if !ok {
		t.Fatal("get should succeed")
	}
	if got.RouterID != 100 {
		t.Errorf("RouterID=%d", got.RouterID)
	}
	s.clear(42)
	if _, ok := s.get(42); ok {
		t.Error("after clear, get should fail")
	}
}

func TestPendingAddOperator_Expired(t *testing.T) {
	s := newPendingAddOperatorStore()
	s.put(42, 100, -time.Minute) // already expired
	if _, ok := s.get(42); ok {
		t.Error("expired entry must not be returned")
	}
	// Expired entries get evicted on get attempt.
	s.mu.Lock()
	_, present := s.m[42]
	s.mu.Unlock()
	if present {
		t.Error("expired entry should have been evicted")
	}
}

func TestPendingAddOperator_PutReplacesOld(t *testing.T) {
	s := newPendingAddOperatorStore()
	s.put(42, 100, 5*time.Minute)
	s.put(42, 200, 5*time.Minute) // same admin, different router
	got, ok := s.get(42)
	if !ok {
		t.Fatal("get should succeed")
	}
	if got.RouterID != 200 {
		t.Errorf("RouterID=%d, want 200 (replacement)", got.RouterID)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/backend/callbacks/ -run TestPendingAddOperator -v`
Expected: FAIL — file doesn't exist.

- [ ] **Step 3: Create the store**

Create `internal/backend/callbacks/access_panel.go`:

```go
package callbacks

import (
	"sync"
	"time"
)

// pendingAddOperator is one in-progress FSM: the admin tapped "Добавить
// оператора" and we're waiting for them to either forward a message from
// the new operator or type a numeric TG ID. Single FSM per admin at a
// time (keyed by admin user id); a fresh put replaces any prior entry.
type pendingAddOperator struct {
	AdminUserID int64
	RouterID    int64
	ExpiresAt   time.Time
}

// pendingAddOperatorStore is a goroutine-safe map keyed by admin's TG
// user id. Lifetime mirrors pendingMaintStore — short TTL (5 min), in-
// memory only, evicted on expired-get.
type pendingAddOperatorStore struct {
	mu sync.Mutex
	m  map[int64]*pendingAddOperator
}

func newPendingAddOperatorStore() *pendingAddOperatorStore {
	return &pendingAddOperatorStore{m: make(map[int64]*pendingAddOperator)}
}

// put stores an FSM for `adminID`, replacing any prior pending entry for
// the same admin. ttl is added to time.Now() as the expiry.
func (s *pendingAddOperatorStore) put(adminID, routerID int64, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[adminID] = &pendingAddOperator{
		AdminUserID: adminID,
		RouterID:    routerID,
		ExpiresAt:   time.Now().Add(ttl),
	}
}

// get returns the unexpired FSM for `adminID` or (nil, false). Expired
// entries are evicted as a side effect.
func (s *pendingAddOperatorStore) get(adminID int64) (*pendingAddOperator, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[adminID]
	if !ok {
		return nil, false
	}
	if time.Now().After(p.ExpiresAt) {
		delete(s.m, adminID)
		return nil, false
	}
	return p, true
}

// clear removes the FSM for `adminID` (no-op if absent).
func (s *pendingAddOperatorStore) clear(adminID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, adminID)
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/backend/callbacks/ -run TestPendingAddOperator -v`
Expected: PASS — three tests.

- [ ] **Step 5: Commit**

```
git add internal/backend/callbacks/access_panel.go internal/backend/callbacks/access_panel_test.go
git commit -m "@'
feat(callbacks): pendingAddOperator FSM store

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

---

## Task 6: Render functions for access screens

**Files:**
- Modify: `internal/backend/callbacks/access_panel.go`
- Modify: `internal/backend/callbacks/access_panel_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/backend/callbacks/access_panel_test.go`:

```go
import (
	"strings"
	// keep "testing" / "time" already imported
)

func TestAccessHomeMessage_TwoRouters(t *testing.T) {
	d := newTestDB(t)
	uidA, _ := d.Users().Insert("alpha", "tok-a", "1.1.1.1", "awg11")
	uidB, _ := d.Users().Insert("beta", "tok-b", "2.2.2.2", "awg11")
	_ = d.Users().SetTelegramUserID(uidA, 100)
	_ = d.RouterOperators().Add(uidA, 200, 999)
	_ = d.RouterOperators().Add(uidA, 201, 999)
	// uidB: no owner, no operators

	text, kb := accessHomeMessage(d)
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "beta") {
		t.Errorf("text should list both routers: %q", text)
	}
	if !strings.Contains(text, "owner: 100") || !strings.Contains(text, "2 операт") {
		t.Errorf("alpha line should show owner + 2 operators: %q", text)
	}
	if !strings.Contains(text, "0 операт") {
		t.Errorf("beta line should show 0 operators: %q", text)
	}
	// One button per router + one "back" button row at the bottom.
	if len(kb.InlineKeyboard) < 3 {
		t.Errorf("expected ≥3 rows (2 routers + back), got %d", len(kb.InlineKeyboard))
	}
	_ = uidB
}

func TestAccessHomeMessage_NoRouters(t *testing.T) {
	d := newTestDB(t)
	text, _ := accessHomeMessage(d)
	if !strings.Contains(text, "Роутеров нет") && !strings.Contains(text, "пуст") {
		t.Errorf("empty state text expected, got %q", text)
	}
}

func TestAccessRouterMessage(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("gamma", "tok-g", "3.3.3.3", "awg11")
	_ = d.Users().SetTelegramUserID(uid, 300)
	_ = d.RouterOperators().Add(uid, 301, 999)

	text, kb, err := accessRouterMessage(d, uid)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(text, "gamma") {
		t.Errorf("text should name the router: %q", text)
	}
	if !strings.Contains(text, "300") {
		t.Errorf("text should show owner 300: %q", text)
	}
	if !strings.Contains(text, "301") {
		t.Errorf("text should show operator 301: %q", text)
	}
	// Each operator row contains a Remove button with the right callback.
	found := false
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.CallbackData, "access:0:remove_op:") && strings.HasSuffix(btn.CallbackData, ":301") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected a remove_op button for op 301, got kb=%+v", kb)
	}
}

func TestAccessRouterMessage_NoOwner(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("delta", "tok-d", "4.4.4.4", "awg11")
	text, _, err := accessRouterMessage(d, uid)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(text, "не привязан") && !strings.Contains(text, "TOFU") {
		t.Errorf("unbound-owner label expected, got %q", text)
	}
}

func TestAccessRouterMessage_UnknownRouter(t *testing.T) {
	d := newTestDB(t)
	_, _, err := accessRouterMessage(d, 9999)
	if err == nil {
		t.Error("expected error for unknown router id")
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/backend/callbacks/ -run TestAccess(Home|Router)Message -v`
Expected: FAIL — functions undefined.

- [ ] **Step 3: Add render functions**

Append to `internal/backend/callbacks/access_panel.go`:

```go
import block at the top of the file gets:
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
```

Then append:

```go
// accessHomeMessage renders the "👥 Доступ" screen: list of all routers with
// owner + operator-count summary. One button per router plus a "back" row.
func accessHomeMessage(d *db.DB) (string, tg.InlineKeyboardMarkup) {
	users, err := d.Users().GetAll()
	if err != nil {
		return "👥 Управление доступом\n\nНе удалось прочитать роутеров: " + err.Error(),
			tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
				{{Text: "« Назад", CallbackData: "panel:0:home"}},
			}}
	}
	var b strings.Builder
	b.WriteString("👥 Управление доступом\n\nВыбери роутер:")
	rows := make([][]tg.InlineKeyboardButton, 0, len(users)+1)
	if len(users) == 0 {
		b.WriteString("\n\nРоутеров нет.")
	}
	for _, u := range users {
		ops, _ := d.RouterOperators().List(u.ID)
		ownerLabel := "?"
		if u.TelegramUserID != nil {
			ownerLabel = fmt.Sprintf("%d", *u.TelegramUserID)
		}
		btnLabel := fmt.Sprintf("%s — owner: %s | %s",
			u.Nickname, ownerLabel, pluralOperators(len(ops)))
		rows = append(rows, []tg.InlineKeyboardButton{
			{Text: btnLabel, CallbackData: fmt.Sprintf("access:0:router:%d", u.ID)},
		})
		b.WriteString("\n  • ")
		b.WriteString(btnLabel)
	}
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "« Назад", CallbackData: "panel:0:home"},
	})
	return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// accessRouterMessage renders the per-router access screen: owner + each
// operator with its own ✖ button, plus add / back rows.
func accessRouterMessage(d *db.DB, routerID int64) (string, tg.InlineKeyboardMarkup, error) {
	u, err := d.Users().GetByID(routerID)
	if err != nil {
		return "", tg.InlineKeyboardMarkup{}, err
	}
	ops, err := d.RouterOperators().List(routerID)
	if err != nil {
		return "", tg.InlineKeyboardMarkup{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "👥 %s\n\n", u.Nickname)
	rows := make([][]tg.InlineKeyboardButton, 0, len(ops)+3)
	if u.TelegramUserID == nil {
		b.WriteString("Owner: (не привязан, TOFU)\n")
	} else {
		fmt.Fprintf(&b, "Owner: %d\n", *u.TelegramUserID)
		rows = append(rows, []tg.InlineKeyboardButton{
			{Text: "✖ Отвязать owner'a", CallbackData: fmt.Sprintf("access:0:unbind_owner:%d", routerID)},
		})
	}
	b.WriteString("\nОператоры:")
	if len(ops) == 0 {
		b.WriteString(" (нет)")
	}
	for _, op := range ops {
		fmt.Fprintf(&b, "\n  • %d", op.TelegramUserID)
		rows = append(rows, []tg.InlineKeyboardButton{
			{Text: fmt.Sprintf("✖ %d", op.TelegramUserID),
				CallbackData: fmt.Sprintf("access:0:remove_op:%d:%d", routerID, op.TelegramUserID)},
		})
	}
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "➕ Добавить оператора", CallbackData: fmt.Sprintf("access:0:add:%d", routerID)},
	})
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "« К списку роутеров", CallbackData: "access:0:home"},
	})
	return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func pluralOperators(n int) string {
	// Simple Russian plural: 0 операторов, 1 оператор, 2-4 оператора, 5+ операторов.
	mod10 := n % 10
	mod100 := n % 100
	switch {
	case mod100 >= 11 && mod100 <= 14:
		return fmt.Sprintf("%d операторов", n)
	case mod10 == 1:
		return fmt.Sprintf("%d оператор", n)
	case mod10 >= 2 && mod10 <= 4:
		return fmt.Sprintf("%d оператора", n)
	default:
		return fmt.Sprintf("%d операторов", n)
	}
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/backend/callbacks/ -run TestAccess -v`
Expected: PASS — all access rendering tests.

- [ ] **Step 5: Commit**

```
git add internal/backend/callbacks/access_panel.go internal/backend/callbacks/access_panel_test.go
git commit -m "@'
feat(callbacks): accessHomeMessage + accessRouterMessage renderers

Pure functions — easy to test. Home lists routers with owner + operator
count; per-router screen shows owner and each operator with its own
remove button.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

---

## Task 7: `handleAccessCallback` + sub-screen methods + admin gate

**Files:**
- Modify: `internal/backend/callbacks/access_panel.go`
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/access_panel_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/backend/callbacks/access_panel_test.go`:

```go
import "context"  // ensure imported

func TestHandleAccessCallback_NonAdminToast(t *testing.T) {
	d := newTestDB(t)
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID: "q1", From: &tg.User{ID: 999}, // not admin
		Data:    "access:0:home",
		Message: &tg.Message{Chat: tg.Chat{ID: 7}, MessageID: 1},
	}
	r.HandleCallback(context.Background(), q)
	// Expect: toast says "доступ только у админа", no EditMessageText invoked.
	if len(tgFake.answers) != 1 || !strings.Contains(tgFake.answers[0].Text, "только у админа") {
		t.Errorf("expected admin-only toast, got answers=%+v", tgFake.answers)
	}
	if len(tgFake.edits) != 0 {
		t.Errorf("non-admin must not edit message, got %d edits", len(tgFake.edits))
	}
}

func TestHandleAccessCallback_AdminOpensHome(t *testing.T) {
	d := newTestDB(t)
	_, _ = d.Users().Insert("zoo", "tok", "5.5.5.5", "awg11")
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID: "q1", From: &tg.User{ID: 42},
		Data:    "access:0:home",
		Message: &tg.Message{Chat: tg.Chat{ID: 7}, MessageID: 1},
	}
	r.HandleCallback(context.Background(), q)
	if len(tgFake.edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(tgFake.edits))
	}
	if !strings.Contains(tgFake.edits[0].Text, "Управление доступом") {
		t.Errorf("edit text wrong: %q", tgFake.edits[0].Text)
	}
}

func TestHandleAccessCallback_RemoveOp(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("foo", "tok", "5.5.5.5", "awg11")
	_ = d.RouterOperators().Add(uid, 555, 999)
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID: "q", From: &tg.User{ID: 42},
		Data:    fmt.Sprintf("access:0:remove_op:%d:555", uid),
		Message: &tg.Message{Chat: tg.Chat{ID: 7}, MessageID: 1},
	}
	r.HandleCallback(context.Background(), q)

	ops, _ := d.RouterOperators().List(uid)
	if len(ops) != 0 {
		t.Errorf("operator should have been removed, got %+v", ops)
	}
	// Should have re-rendered the router screen.
	if len(tgFake.edits) != 1 {
		t.Fatalf("expected edit-in-place, got %d edits", len(tgFake.edits))
	}
}

func TestHandleAccessCallback_UnbindOwner(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("foo", "tok", "5.5.5.5", "awg11")
	_ = d.Users().SetTelegramUserID(uid, 777)
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID: "q", From: &tg.User{ID: 42},
		Data:    fmt.Sprintf("access:0:unbind_owner:%d", uid),
		Message: &tg.Message{Chat: tg.Chat{ID: 7}, MessageID: 1},
	}
	r.HandleCallback(context.Background(), q)

	u, _ := d.Users().GetByID(uid)
	if u.TelegramUserID != nil {
		t.Errorf("owner should be unbound, still %v", u.TelegramUserID)
	}
}

func TestHandleAccessCallback_Add_StartsFSM(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("foo", "tok", "5.5.5.5", "awg11")
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID: "q", From: &tg.User{ID: 42},
		Data:    fmt.Sprintf("access:0:add:%d", uid),
		Message: &tg.Message{Chat: tg.Chat{ID: 7}, MessageID: 1},
	}
	r.HandleCallback(context.Background(), q)

	got, ok := r.pendingAddOperator.get(42)
	if !ok {
		t.Fatal("FSM entry should exist for admin 42")
	}
	if got.RouterID != uid {
		t.Errorf("FSM RouterID=%d, want %d", got.RouterID, uid)
	}
}

func TestHandleAccessCallback_CancelAdd_ClearsFSM(t *testing.T) {
	d := newTestDB(t)
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	r.pendingAddOperator.put(42, 100, 5*time.Minute)

	q := &tg.CallbackQuery{
		ID: "q", From: &tg.User{ID: 42},
		Data:    "access:0:cancel_add",
		Message: &tg.Message{Chat: tg.Chat{ID: 7}, MessageID: 1},
	}
	r.HandleCallback(context.Background(), q)

	if _, ok := r.pendingAddOperator.get(42); ok {
		t.Error("cancel_add should clear FSM")
	}
}
```

(The fake `tgFake.answers` / `tgFake.edits` capture structures come from
existing `router_test.go` — verify their exact field names before using.
If the field is `Toasts` or `AnswerCalls`, adapt.)

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/backend/callbacks/ -run TestHandleAccessCallback -v`
Expected: FAIL — `r.pendingAddOperator` field missing, no
`handleAccessCallback` dispatch, no `access` case in `HandleCallback`.

- [ ] **Step 3: Add `pendingAddOperator` to Router + init**

In `internal/backend/callbacks/router.go`, add a field to the `Router`
struct (near other `pending*` fields):

```go
	pendingAddOperator *pendingAddOperatorStore
```

In `NewRouterWithSink`, init it (next to other pending-store initialisations):

```go
	r.pendingAddOperator = newPendingAddOperatorStore()
```

- [ ] **Step 4: Add the access dispatch + admin gate to `HandleCallback`**

Find `HandleCallback` in `router.go`. After `args, err := Parse(...)` and
before the action-switch, add the admin-only gate for `access`:

```go
	if args.Action == "access" {
		if r.cfg.AdminUserID == 0 || q.From.ID != r.cfg.AdminUserID {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "доступ только у админа")
			return
		}
		r.handleAccessCallback(ctx, q, args)
		return
	}
```

Place this *before* `aclAllow` is called for other actions, since access
callbacks don't have a per-router `args.UserID` in the regular sense.

- [ ] **Step 5: Add `handleAccessCallback` + sub-methods**

Append to `internal/backend/callbacks/access_panel.go`:

```go
import block also gets:
	"context"
	"log/slog"
```

Then append:

```go
// handleAccessCallback is the dispatcher for access:* callbacks. Admin gate
// is enforced in HandleCallback; this method assumes the caller is admin.
func (r *Router) handleAccessCallback(ctx context.Context, q *tg.CallbackQuery, args Args) {
	slog.Info("access callback", "screen", args.AccessScreen, "router_id", args.AccessRouterID, "op_tg_id", args.AccessOperatorTGID, "from", q.From.ID)
	switch args.AccessScreen {
	case "home":
		r.accessShowHome(ctx, q)
	case "router":
		r.accessShowRouter(ctx, q, args.AccessRouterID)
	case "add":
		r.accessStartAdd(ctx, q, args.AccessRouterID)
	case "remove_op":
		r.accessRemoveOp(ctx, q, args.AccessRouterID, args.AccessOperatorTGID)
	case "unbind_owner":
		r.accessUnbindOwner(ctx, q, args.AccessRouterID)
	case "back":
		r.accessBack(ctx, q)
	case "cancel_add":
		r.accessCancelAdd(ctx, q)
	default:
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "unknown screen")
	}
}

func (r *Router) accessShowHome(ctx context.Context, q *tg.CallbackQuery) {
	text, kb := accessHomeMessage(r.d)
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("access home edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) accessShowRouter(ctx context.Context, q *tg.CallbackQuery, routerID int64) {
	text, kb, err := accessRouterMessage(r.d, routerID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("access router edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) accessStartAdd(ctx context.Context, q *tg.CallbackQuery, routerID int64) {
	// Verify the router exists before opening the FSM.
	u, err := r.d.Users().GetByID(routerID)
	if err != nil || u == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	r.pendingAddOperator.put(q.From.ID, routerID, 5*time.Minute)
	hint := fmt.Sprintf("🆔 Добавление оператора для %s\n\nПерешли мне (в личку с ботом) любое сообщение от нужного человека ИЛИ напиши его числовой Telegram ID. Жду 5 минут.", u.Nickname)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{{Text: "✖ Отмена", CallbackData: "access:0:cancel_add"}},
	}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, hint, "", &kb); err != nil {
		slog.Warn("access add edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "жду forward или ID в личке")
}

func (r *Router) accessRemoveOp(ctx context.Context, q *tg.CallbackQuery, routerID, opTGID int64) {
	if err := r.d.RouterOperators().Remove(routerID, opTGID); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось удалить")
		slog.Warn("access remove op failed", "err", err, "router_id", routerID, "op_tg", opTGID)
		return
	}
	r.accessShowRouter(ctx, q, routerID) // re-render
}

func (r *Router) accessUnbindOwner(ctx context.Context, q *tg.CallbackQuery, routerID int64) {
	if err := r.d.Users().SetTelegramUserID(routerID, 0); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось отвязать owner'a")
		slog.Warn("access unbind owner failed", "err", err, "router_id", routerID)
		return
	}
	r.accessShowRouter(ctx, q, routerID)
}

func (r *Router) accessBack(ctx context.Context, q *tg.CallbackQuery) {
	text, kb := panelHomeMessage()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("access back edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) accessCancelAdd(ctx context.Context, q *tg.CallbackQuery) {
	r.pendingAddOperator.clear(q.From.ID)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "отменено")
	// Re-render the panel home (admin lands back on the access list).
	r.accessShowHome(ctx, q)
}
```

- [ ] **Step 6: Verify tests pass**

Run: `go test ./internal/backend/callbacks/ -run TestHandleAccessCallback -v`
Expected: PASS — six new tests.

Run full package: `go test ./internal/backend/callbacks/ -v`. Expected:
green.

- [ ] **Step 7: Commit**

```
git add internal/backend/callbacks/access_panel.go internal/backend/callbacks/access_panel_test.go internal/backend/callbacks/router.go
git commit -m "@'
feat(callbacks): handleAccessCallback + admin-only access:* dispatch

Adds Router.pendingAddOperator field, NewRouterWithSink wiring, the
access:* dispatch case (admin-only) in HandleCallback, and the
sub-screen methods (home/router/add/remove_op/unbind_owner/back/
cancel_add) on access_panel.go.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

---

## Task 8: `HandleMessage` FSM hook for add-operator

**Files:**
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/access_panel.go`
- Modify: `internal/backend/callbacks/access_panel_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/backend/callbacks/access_panel_test.go`:

```go
func TestProcessAddOperatorMessage_Forward(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("foo", "tok", "5.5.5.5", "awg11")
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	r.pendingAddOperator.put(42, uid, 5*time.Minute)

	m := &tg.Message{
		MessageID:   100,
		Chat:        tg.Chat{ID: 42}, // DM (chat.id == user.id)
		From:        &tg.User{ID: 42},
		ForwardFrom: &tg.User{ID: 555},
	}
	r.HandleMessage(context.Background(), m)

	if !r.d.RouterOperators().HasAccess(uid, 555) {
		t.Error("operator 555 should have been added")
	}
	if _, ok := r.pendingAddOperator.get(42); ok {
		t.Error("FSM should have been cleared")
	}
}

func TestProcessAddOperatorMessage_NumericText(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("foo", "tok", "5.5.5.5", "awg11")
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	r.pendingAddOperator.put(42, uid, 5*time.Minute)

	m := &tg.Message{
		MessageID: 100,
		Chat:      tg.Chat{ID: 42},
		From:      &tg.User{ID: 42},
		Text:      "777",
	}
	r.HandleMessage(context.Background(), m)

	if !r.d.RouterOperators().HasAccess(uid, 777) {
		t.Error("operator 777 should have been added")
	}
}

func TestProcessAddOperatorMessage_Garbage_FSMRemains(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("foo", "tok", "5.5.5.5", "awg11")
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	r.pendingAddOperator.put(42, uid, 5*time.Minute)

	m := &tg.Message{
		MessageID: 100,
		Chat:      tg.Chat{ID: 42},
		From:      &tg.User{ID: 42},
		Text:      "hello world",
	}
	r.HandleMessage(context.Background(), m)

	ops, _ := r.d.RouterOperators().List(uid)
	if len(ops) != 0 {
		t.Errorf("nothing should have been added, got %+v", ops)
	}
	if _, ok := r.pendingAddOperator.get(42); !ok {
		t.Error("FSM must remain after garbage input")
	}
}

func TestProcessAddOperatorMessage_NotInDM_NotConsumed(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("foo", "tok", "5.5.5.5", "awg11")
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	r.pendingAddOperator.put(42, uid, 5*time.Minute)

	// Message in the forum chat, not DM (chat.id != from.id).
	m := &tg.Message{
		MessageID:   100,
		Chat:        tg.Chat{ID: 7}, // forum chat
		From:        &tg.User{ID: 42},
		ForwardFrom: &tg.User{ID: 555},
	}
	r.HandleMessage(context.Background(), m)

	ops, _ := r.d.RouterOperators().List(uid)
	if len(ops) != 0 {
		t.Errorf("FSM must not consume forum-chat messages, got %+v", ops)
	}
	if _, ok := r.pendingAddOperator.get(42); !ok {
		t.Error("FSM must remain")
	}
}

func TestProcessAddOperatorMessage_NonAdmin_NotConsumed(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("foo", "tok", "5.5.5.5", "awg11")
	tgFake := &fakeRouterTG{}
	r := NewRouterWithSink(d, tgFake, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	r.pendingAddOperator.put(42, uid, 5*time.Minute) // admin 42 has pending

	// But message comes from a different user
	m := &tg.Message{
		MessageID:   100,
		Chat:        tg.Chat{ID: 999},
		From:        &tg.User{ID: 999},
		ForwardFrom: &tg.User{ID: 555},
	}
	r.HandleMessage(context.Background(), m)

	ops, _ := r.d.RouterOperators().List(uid)
	if len(ops) != 0 {
		t.Errorf("non-admin must not consume FSM, got %+v", ops)
	}
}
```

(The `tg.Message` struct must have `ForwardFrom *tg.User` for this. Verify
in `internal/backend/tg/types.go` or similar. If the field name is
different, adapt — read the struct first.)

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/backend/callbacks/ -run TestProcessAddOperator -v`
Expected: FAIL — HandleMessage doesn't have the FSM hook.

- [ ] **Step 3: Add `processAddOperatorMessage`**

Append to `internal/backend/callbacks/access_panel.go`:

```go
import:
	"strconv"
```

Then append:

```go
// processAddOperatorMessage is invoked by HandleMessage when the admin
// sends a message in DM with the bot while an add-operator FSM is active.
// Determines the new operator's TG ID via forward_from (preferred) or
// numeric text. On success, persists the row and clears the FSM. On
// soft-failure (garbage input, channel forward), replies with a hint and
// keeps the FSM alive.
func (r *Router) processAddOperatorMessage(ctx context.Context, m *tg.Message, p *pendingAddOperator) {
	var opTGID int64
	switch {
	case m.ForwardFrom != nil:
		opTGID = m.ForwardFrom.ID
	case strings.TrimSpace(m.Text) != "":
		v, err := strconv.ParseInt(strings.TrimSpace(m.Text), 10, 64)
		if err == nil && v > 0 {
			opTGID = v
		}
	}
	if opTGID == 0 {
		// Could be channel-forward (forward_from nil, forward_from_chat set)
		// or garbage. Reply with hint, keep FSM.
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, nil,
			"Не вижу TG user ID — нужно либо forward от человека, либо положительное число. Жду дальше. ✖ Отмена доступна в исходном экране.", "", nil)
		return
	}
	u, err := r.d.Users().GetByID(p.RouterID)
	if err != nil || u == nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, nil,
			"Роутер для FSM уже не существует, отменяю.", "", nil)
		r.pendingAddOperator.clear(m.From.ID)
		return
	}
	if err := r.d.RouterOperators().Add(p.RouterID, opTGID, m.From.ID); err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, nil,
			fmt.Sprintf("Не удалось добавить оператора: %v", err), "", nil)
		return
	}
	r.pendingAddOperator.clear(m.From.ID)
	_, _ = r.tg.SendMessage(ctx, m.Chat.ID, nil,
		fmt.Sprintf("➕ Добавлен оператор %d для %s. Открой /panel чтобы продолжить.", opTGID, u.Nickname),
		"", nil)
}
```

- [ ] **Step 4: Wire the hook into `HandleMessage`**

In `internal/backend/callbacks/router.go`, find `HandleMessage`. At the
TOP of the function, before the existing admin-only gate:

```go
func (r *Router) HandleMessage(ctx context.Context, m *tg.Message) {
	// Add-operator FSM intercept: admin sends a qualifying message in DM
	// with the bot while a pending FSM exists. Falls through to normal
	// handlers otherwise.
	if r.cfg.AdminUserID != 0 && m.From != nil && m.From.ID == r.cfg.AdminUserID && m.Chat.ID == m.From.ID {
		if p, ok := r.pendingAddOperator.get(m.From.ID); ok {
			r.processAddOperatorMessage(ctx, m, p)
			return
		}
	}
	// ...existing handler body unchanged
```

- [ ] **Step 5: Verify tests pass**

Run: `go test ./internal/backend/callbacks/ -run TestProcessAddOperator -v`
Expected: PASS — five new tests.

Run full package: `go test ./internal/backend/callbacks/ -v`. Expected:
green.

- [ ] **Step 6: Commit**

```
git add internal/backend/callbacks/access_panel.go internal/backend/callbacks/router.go internal/backend/callbacks/access_panel_test.go
git commit -m "@'
feat(callbacks): processAddOperatorMessage — FSM consumes admin DM input

HandleMessage now intercepts admin private-DM messages when an
add-operator FSM is pending. forward_from picks the new operator's
TG ID; a numeric text body is the fallback. Garbage input keeps
the FSM alive and replies with a hint.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

---

## Task 9: Add `👥 Доступ` button to `/panel` home + admin-only gate for panel hub

**Files:**
- Modify: `internal/backend/callbacks/panel_hub.go`
- Modify: `internal/backend/callbacks/panel_hub.go` tests (if exist) or
  `internal/backend/callbacks/access_panel_test.go`

- [ ] **Step 1: Write the test**

Append to `internal/backend/callbacks/access_panel_test.go`:

```go
func TestPanelHomeMessage_HasAccessButton(t *testing.T) {
	_, kb := panelHomeMessage()
	found := false
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "access:0:home" {
				found = true
			}
		}
	}
	if !found {
		t.Error("panel home should expose 👥 Доступ button")
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/backend/callbacks/ -run TestPanelHomeMessage_HasAccess -v`
Expected: FAIL — button absent.

- [ ] **Step 3: Add the button**

In `internal/backend/callbacks/panel_hub.go`, modify `panelHomeMessage`:

Replace:
```go
		{
			{Text: "📊 Status", CallbackData: "panel:0:kind:status"},
			{Text: "🪄 Оживить топики", CallbackData: "panel:0:awaken_confirm"},
		},
		{
			{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
		},
```

With:
```go
		{
			{Text: "📊 Status", CallbackData: "panel:0:kind:status"},
			{Text: "🪄 Оживить топики", CallbackData: "panel:0:awaken_confirm"},
		},
		{
			{Text: "👥 Доступ", CallbackData: "access:0:home"},
		},
		{
			{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
		},
```

- [ ] **Step 4: Verify all tests pass**

Run: `go test ./internal/backend/callbacks/ -v`
Expected: green.

- [ ] **Step 5: Commit**

```
git add internal/backend/callbacks/panel_hub.go internal/backend/callbacks/access_panel_test.go
git commit -m "@'
feat(callbacks): panel home — add 👥 Доступ entry to admin hub

Tap routes to access:0:home, which is admin-gated in HandleCallback.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

---

## Task 10: Manual smoke + README feature line

**Files:**
- Modify: `README.md`

- [ ] **Step 1: README update**

In `README.md`, find the feature table (search for `| **Routes-панель** |`).
Add a new row after the existing `Maintenance-панель` row:

```
| **Доступ к роутеру** | `/panel → 👥 Доступ` — per-router whitelist дополнительных TG-операторов (helper, второй администратор и т.п.). Add: forward сообщения от человека ИЛИ числовой TG ID в личку с ботом. Remove: кнопка ✖ возле имени. Owner отвязывается отдельно (вернётся TOFU). Управляет только глобальный admin |
```

- [ ] **Step 2: Verify build + tests**

```
go build ./...
go test ./...
```

Both green.

- [ ] **Step 3: Commit README**

```
git add README.md
git commit -m "@'
docs(README): router operators feature line

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@"
```

- [ ] **Step 4: Manual smoke on testkeen (after RC is built & deployed)**

1. As admin: `/panel` → tap `👥 Доступ` → see list with `testkeen`.
2. Tap `testkeen` → see owner + 0 operators.
3. Tap `➕ Добавить оператора` → screen flips to "ждём forward или ID".
4. In private DM with bot, forward a message from a second test account.
   Bot replies `➕ Добавлен оператор <id> для testkeen`.
5. Re-open `/panel → 👥 Доступ → testkeen` → see the new operator.
6. From the second account, tap a button on the testkeen maintenance
   panel — should work (no `«это не твой роутер»`).
7. As admin, tap `✖ <id>` on the operator row — operator gone.
8. From second account, tap maintenance panel again — now denied.
9. As admin, tap `✖ Отвязать owner'a`. Owner taps a button in their own
   topic — TOFU re-binds them automatically.

---

## Self-Review

**1. Spec coverage check:**

| Spec section | Task(s) implementing |
|---|---|
| `router_operators` table DDL | Task 1 |
| `Operator` struct + repo (Add/Remove/List/HasAccess) | Task 2 |
| `db.RouterOperators()` accessor | Task 2 |
| ACL extension in `aclAllow` | Task 3 |
| Admin-only gate on `access:*` callbacks | Task 7 |
| `access:*` callback grammar | Task 4 |
| `pendingAddOperatorStore` FSM | Task 5 |
| Home + router screen renderers | Task 6 |
| Sub-screen dispatch (home/router/add/remove_op/unbind_owner/back/cancel_add) | Task 7 |
| HandleMessage FSM hook (forward / numeric / hint / non-admin / non-DM) | Task 8 |
| `👥 Доступ` button in panel hub | Task 9 |
| `Router.pendingAddOperator` field + init | Task 7 |
| README + smoke | Task 10 |

No gaps.

**2. Placeholder scan:**

No TBD / TODO / vague "handle edge cases" — every step has concrete code
or assertions. The one "verify field names before using" note in Task 7
and "verify the field is ForwardFrom" in Task 8 are explicit
verification asks, not placeholders.

**3. Type consistency:**

- `Operator` (Task 2) has fields `UserID, TelegramUserID, GrantedBy,
  GrantedAt` — referenced consistently in Task 6 rendering tests + Task 7
  remove handler.
- `pendingAddOperator{AdminUserID, RouterID, ExpiresAt}` (Task 5) —
  matches the Task 7/8 read paths (RouterID used by accessRemove and
  processAddOperatorMessage).
- Callback grammar (Task 4) consistent with renderer-emitted callback
  data (Task 6, Task 7).
- `r.pendingAddOperator` field name — same in Task 7 init and Task 8 hook.
- `accessHomeMessage(d)` / `accessRouterMessage(d, id)` signatures —
  consistent between Task 6 implementation and Task 7 invocations.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-05-12-router-operators.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session using executing-plans.

**Which approach?**
