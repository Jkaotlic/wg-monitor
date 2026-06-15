package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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

func TestInsertDuplicateTokenRejected(t *testing.T) {
	d := newTestDB(t)
	tok := "33333333333333333333333333333333cccccccccccccccccccccccccccccccc"
	if _, err := d.Users().Insert("alpha", tok, "1.1.1.1", "awg0"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("beta", tok, "2.2.2.2", "awg1"); err == nil {
		t.Fatal("expected duplicate-token error")
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
	var rawLastSeen string
	if err := d.SQL().QueryRow(`SELECT last_seen_at FROM users WHERE id = ?`, id).Scan(&rawLastSeen); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawLastSeen, "T") {
		t.Fatalf("last_seen_at must be RFC3339-like, got %q", rawLastSeen)
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

func TestUpsertEnrollmentCreatesUser(t *testing.T) {
	d := newTestDB(t)
	uid, err := d.Users().UpsertEnrollment("testkeen", "raw-token-1", KindStatic, 406)
	if err != nil {
		t.Fatalf("UpsertEnrollment: %v", err)
	}
	u, err := d.Users().GetByNickname("testkeen")
	if err != nil {
		t.Fatalf("GetByNickname: %v", err)
	}
	if u.ID != uid || u.Kind != KindStatic || u.TelegramThreadID == nil || *u.TelegramThreadID != 406 {
		t.Fatalf("bad user: %+v", u)
	}
	if _, err := d.Users().GetByToken("raw-token-1"); err != nil {
		t.Fatalf("new token rejected: %v", err)
	}
}

func TestUpsertEnrollmentRotatesExistingToken(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.Users().InsertWithKind("testkeen", "old-token", "0.0.0.0", "awg0", KindStatic); err != nil {
		t.Fatalf("InsertWithKind: %v", err)
	}
	if _, err := d.Users().UpsertEnrollment("testkeen", "new-token", KindMobile, 0); err != nil {
		t.Fatalf("UpsertEnrollment: %v", err)
	}
	if _, err := d.Users().GetByToken("new-token"); err != nil {
		t.Fatalf("new token rejected: %v", err)
	}
	if _, err := d.Users().GetByToken("old-token"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("old token still valid: %v", err)
	}
	u, _ := d.Users().GetByNickname("testkeen")
	if u.Kind != KindMobile {
		t.Fatalf("kind not updated: %s", u.Kind)
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

func TestOpenMigratesLegacyDBWithoutTelegramChatID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    nickname TEXT UNIQUE NOT NULL,
    token_hash TEXT NOT NULL,
    expected_exit_ip TEXT NOT NULL,
    awg_iface TEXT NOT NULL,
    telegram_thread_id INTEGER,
    telegram_user_id INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP
)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy DB: %v", err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("legacy", "tok", "1.1.1.1", "nwg0"); err != nil {
		t.Fatalf("insert after migration: %v", err)
	}
	var n int
	if err := d.SQL().QueryRow(`SELECT count(*) FROM pragma_table_info('users') WHERE name='telegram_chat_id'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("telegram_chat_id column count=%d, want 1", n)
	}
	if err := d.SQL().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_users_chat_thread_id'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("idx_users_chat_thread_id count=%d, want 1", n)
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

func TestUsersGetByChatThreadIDUsesDefaultChatForLegacyRows(t *testing.T) {
	d := openTempDB(t)
	id, err := d.Users().Insert("legacy", "tok", "1.1.1.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(id, 4242); err != nil {
		t.Fatal(err)
	}

	got, err := d.Users().GetByChatThreadID(-100, 4242, -100)
	if err != nil {
		t.Fatalf("GetByChatThreadID primary: %v", err)
	}
	if got.ID != id || got.Nickname != "legacy" {
		t.Fatalf("got id=%d nick=%s want id=%d nick=legacy", got.ID, got.Nickname, id)
	}
	if got.EffectiveTelegramChatID(-100) != -100 {
		t.Fatalf("effective chat=%d, want -100", got.EffectiveTelegramChatID(-100))
	}

	if _, err := d.Users().GetByChatThreadID(-200, 4242, -100); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("wrong chat lookup err=%v, want ErrUserNotFound", err)
	}
}

func TestUsersTopicBindingIncludesTelegramChatID(t *testing.T) {
	d := openTempDB(t)
	id, err := d.Users().Insert("tenant", "tok", "1.1.1.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateTelegramTopic(id, -200, 4242); err != nil {
		t.Fatal(err)
	}

	got, err := d.Users().GetByChatThreadID(-200, 4242, -100)
	if err != nil {
		t.Fatalf("GetByChatThreadID secondary: %v", err)
	}
	if got.ID != id || got.EffectiveTelegramChatID(-100) != -200 {
		t.Fatalf("got user=%+v effective_chat=%d", got, got.EffectiveTelegramChatID(-100))
	}
	if _, err := d.Users().GetByChatThreadID(-100, 4242, -100); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("primary chat lookup err=%v, want ErrUserNotFound", err)
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

func TestUsers_HasAnyOperatorOrOwnerBinding(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("alpha", "tok", "1.1.1.1", "awg11")

	if has, err := d.Users().HasAnyOperatorOrOwnerBinding(999); err != nil {
		t.Fatalf("err: %v", err)
	} else if has {
		t.Error("stranger should not have a binding")
	}

	// As owner.
	_ = d.Users().SetTelegramUserID(uid, 100)
	if has, _ := d.Users().HasAnyOperatorOrOwnerBinding(100); !has {
		t.Error("owner 100 should have a binding")
	}

	// As operator of a different router.
	uid2, _ := d.Users().Insert("beta", "tok2", "2.2.2.2", "awg11")
	_ = d.Users().SetTelegramUserID(uid2, 200)
	_ = d.RouterOperators().Add(uid2, 300, 42)
	if has, _ := d.Users().HasAnyOperatorOrOwnerBinding(300); !has {
		t.Error("operator 300 should have a binding")
	}
}

func TestUpdateLastSeenAgentVersionClearsMatchingPendingDeploy(t *testing.T) {
	d := newTestDB(t)
	id, err := d.Users().Insert("client-a", "tok", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("client-a", DeployInfo{
		SSHHost:             "192.168.0.1",
		SSHPort:             22,
		SSHUser:             "root",
		Arch:                "arm64",
		LastDeployedVersion: "v0.13.0-rc14",
		Ring:                "stable",
		PendingVersion:      "v0.13.0-rc31",
		PendingSince:        "2026-05-23T09:45:11Z",
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.Users().UpdateLastSeenAgentVersion(id, "v0.13.0-rc31"); err != nil {
		t.Fatal(err)
	}

	got, err := d.Users().GetByNickname("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastDeployedVersion == nil || *got.LastDeployedVersion != "v0.13.0-rc31" {
		t.Fatalf("last_deployed_version=%v", got.LastDeployedVersion)
	}
	if got.PendingVersion != nil || got.PendingSince != nil {
		t.Fatalf("pending not cleared: version=%v since=%v", got.PendingVersion, got.PendingSince)
	}
}

func TestUpdateLastSeenAgentVersionKeepsDifferentPendingDeploy(t *testing.T) {
	d := newTestDB(t)
	id, err := d.Users().Insert("client-a", "tok", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("client-a", DeployInfo{
		SSHHost:             "192.168.0.1",
		SSHPort:             22,
		SSHUser:             "root",
		Arch:                "arm64",
		LastDeployedVersion: "v0.13.0-rc31",
		Ring:                "stable",
		PendingVersion:      "v0.13.0-rc32",
		PendingSince:        "2026-05-23T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.Users().UpdateLastSeenAgentVersion(id, "v0.13.0-rc31"); err != nil {
		t.Fatal(err)
	}

	got, err := d.Users().GetByNickname("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.PendingVersion == nil || *got.PendingVersion != "v0.13.0-rc32" {
		t.Fatalf("pending_version=%v", got.PendingVersion)
	}
	if got.PendingSince == nil || *got.PendingSince != "2026-05-23T10:00:00Z" {
		t.Fatalf("pending_since=%v", got.PendingSince)
	}
}

func TestUpdateDeployInfoRoundTripsPortableWizardMetadata(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.Users().Insert("client-a", "tok", "1.1.1.1", "awg11"); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("client-a", DeployInfo{
		SSHHost:             "192.168.0.1",
		SSHPort:             22,
		SSHUser:             "root",
		Arch:                "arm64",
		LastDeployedVersion: "v0.13.0-rc36",
		LastDeploy:          "2026-05-24T10:20:30Z",
		DeployMode:          "awgm",
		AWGMURL:             "https://client-a.keenetic.example",
		AWGMAuth:            "api-key",
		ExpectedMAC:         "aabbccddeeff",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := d.Users().GetByNickname("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastDeploy == nil || *got.LastDeploy != "2026-05-24T10:20:30Z" ||
		got.DeployMode == nil || *got.DeployMode != "awgm" ||
		got.AWGMURL == nil || *got.AWGMURL != "https://client-a.keenetic.example" ||
		got.AWGMAuth == nil || *got.AWGMAuth != "api-key" ||
		got.ExpectedMAC == nil || *got.ExpectedMAC != "aabbccddeeff" {
		t.Fatalf("portable wizard metadata did not round-trip: %+v", got)
	}
}

func TestUpdateDeployInfoPreservesDeployIdentityWhenMissing(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.Users().Insert("client-b", "tok", "1.1.1.1", "awg11"); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("client-b", DeployInfo{
		SSHHost:             "192.168.1.1",
		SSHPort:             222,
		SSHUser:             "root",
		Arch:                "arm64",
		LastDeployedVersion: "v0.13.0-rc39",
		PendingVersion:      "v0.13.0-rc40",
		PendingSince:        "2026-05-24T12:30:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.Users().UpdateDeployInfo("client-b", DeployInfo{
		LastDeployedVersion: "v0.13.0-rc40",
		LastDeploy:          "2026-05-24T12:40:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := d.Users().GetByNickname("client-b")
	if err != nil {
		t.Fatal(err)
	}
	if got.SSHHost == nil || *got.SSHHost != "192.168.1.1" || got.SSHPort == nil || *got.SSHPort != 222 ||
		got.SSHUser == nil || *got.SSHUser != "root" || got.Arch == nil || *got.Arch != "arm64" {
		t.Fatalf("deploy identity not preserved: %+v", got)
	}
	if got.LastDeployedVersion == nil || *got.LastDeployedVersion != "v0.13.0-rc40" {
		t.Fatalf("last_deployed_version not updated: %+v", got)
	}
	if got.PendingVersion != nil || got.PendingSince != nil {
		t.Fatalf("empty pending fields should clear pending deploy: %+v", got)
	}
}
