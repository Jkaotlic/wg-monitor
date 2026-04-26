package db

import (
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestInsertAndGetUser(t *testing.T) {
	d := newTestDB(t)
	rawToken := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	id, err := d.Users().Insert("vasya", rawToken, "1.2.3.4", "awg0")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("got id 0")
	}

	u, err := d.Users().GetByToken(rawToken)
	if err != nil {
		t.Fatalf("getbytoken: %v", err)
	}
	if u.Nickname != "vasya" || u.AWGIface != "awg0" || u.ExpectedExitIP != "1.2.3.4" {
		t.Fatalf("user: %+v", u)
	}

	if _, err := d.Users().GetByToken("wrongtoken"); err != ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestInsertDuplicateNickname(t *testing.T) {
	d := newTestDB(t)
	tok1 := "11111111111111111111111111111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tok2 := "22222222222222222222222222222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := d.Users().Insert("vasya", tok1, "1.1.1.1", "awg0"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("vasya", tok2, "2.2.2.2", "awg1"); err == nil {
		t.Fatal("expected duplicate-nickname error")
	}
}

func TestUpdateLastSeenAndThreadID(t *testing.T) {
	d := newTestDB(t)
	tok := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	id, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	if err := d.Users().UpdateThreadID(id, 42); err != nil {
		t.Fatalf("thread: %v", err)
	}
	if err := d.Users().UpdateLastSeen(id); err != nil {
		t.Fatalf("seen: %v", err)
	}
	u, _ := d.Users().GetByToken(tok)
	if u.TelegramThreadID == nil || *u.TelegramThreadID != 42 {
		t.Fatalf("thread id: %+v", u.TelegramThreadID)
	}
	if u.LastSeenAt == nil {
		t.Fatal("last_seen_at not set")
	}
}

func TestGetAllUsers(t *testing.T) {
	d := newTestDB(t)
	for i, n := range []string{"a", "b", "c"} {
		tok := "0000000000000000000000000000000000000000000000000000000000000000"
		// build a unique 64-char token per user
		tok = string([]byte(tok)[:63]) + string('0'+rune(i))
		if _, err := d.Users().Insert(n, tok, "1.1.1.1", "awg0"); err != nil {
			t.Fatal(err)
		}
	}
	all, err := d.Users().GetAll()
	if err != nil || len(all) != 3 {
		t.Fatalf("getall: n=%d err=%v", len(all), err)
	}
}
