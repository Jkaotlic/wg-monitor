package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestRunBindTGUser_BindsAndClears(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bind.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := d.Users().Insert("vasya", "rawtok", "1.2.3.4", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	_ = uid
	d.Close()

	// Bind.
	var out bytes.Buffer
	if err := runBindTGUser(bindTGUserOpts{DBPath: dbPath, Nickname: "vasya", TGUserID: 555, Out: &out}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !strings.Contains(out.String(), "bound vasya to Telegram user_id=555") {
		t.Errorf("unexpected output: %q", out.String())
	}
	d2, _ := db.Open(dbPath)
	u, _ := d2.Users().GetByNickname("vasya")
	if u.TelegramUserID == nil || *u.TelegramUserID != 555 {
		t.Fatalf("telegram_user_id not persisted: %+v", u.TelegramUserID)
	}
	d2.Close()

	// Clear.
	out.Reset()
	if err := runBindTGUser(bindTGUserOpts{DBPath: dbPath, Nickname: "vasya", Clear: true, Out: &out}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !strings.Contains(out.String(), "cleared owner binding") {
		t.Errorf("unexpected output: %q", out.String())
	}
	d3, _ := db.Open(dbPath)
	u2, _ := d3.Users().GetByNickname("vasya")
	if u2.TelegramUserID != nil {
		t.Fatalf("telegram_user_id must be cleared, got %v", *u2.TelegramUserID)
	}
	d3.Close()
}

func TestRunBindTGUser_ValidatesArgs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bind.db")
	for _, tc := range []struct {
		name string
		opts bindTGUserOpts
		want string
	}{
		{"missing nickname", bindTGUserOpts{DBPath: dbPath, TGUserID: 1}, "--nickname is required"},
		{"negative tg-id", bindTGUserOpts{DBPath: dbPath, Nickname: "x", TGUserID: -1}, "--tg-id must be"},
		{"zero tg-id no clear", bindTGUserOpts{DBPath: dbPath, Nickname: "x", TGUserID: 0}, "--tg-id must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runBindTGUser(tc.opts)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %q", tc.want, err.Error())
			}
		})
	}
}
