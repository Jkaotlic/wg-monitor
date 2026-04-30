package db

import (
	"errors"
	"path/filepath"
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

func TestInsertDefaultsKindToStatic(t *testing.T) {
	d := newTestDB(t)
	tok := "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11"
	if _, err := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0"); err != nil {
		t.Fatal(err)
	}
	u, err := d.Users().GetByToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if u.Kind != KindStatic {
		t.Fatalf("kind=%q, want %q", u.Kind, KindStatic)
	}
	if u.IsMobile() {
		t.Fatal("static user reported IsMobile=true")
	}
}

func TestInsertWithKindMobile(t *testing.T) {
	d := newTestDB(t)
	tok := "bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22"
	if _, err := d.Users().InsertWithKind("petya", tok, "2.2.2.2", "awg1", KindMobile); err != nil {
		t.Fatal(err)
	}
	u, err := d.Users().GetByNickname("petya")
	if err != nil {
		t.Fatal(err)
	}
	if u.Kind != KindMobile || !u.IsMobile() {
		t.Fatalf("kind=%q IsMobile=%v, want mobile", u.Kind, u.IsMobile())
	}
}

func TestInsertWithKindRejectsUnknown(t *testing.T) {
	d := newTestDB(t)
	tok := "cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33"
	if _, err := d.Users().InsertWithKind("kola", tok, "3.3.3.3", "awg2", "fancy"); err == nil {
		t.Fatal("expected error for kind=fancy")
	}
}

func TestMigrateUserKindIdempotent(t *testing.T) {
	// Re-opening the same DB file must not error or duplicate-add the column.
	path := filepath.Join(t.TempDir(), "t.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d1.Close()
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	d2.Close()
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
