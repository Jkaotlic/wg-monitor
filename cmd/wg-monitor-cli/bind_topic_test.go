package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestRunBindTopic_BindsAndClears(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bt.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("vasya", "rawtok", "1.2.3.4", "nwg0"); err != nil {
		t.Fatal(err)
	}
	d.Close()

	var out bytes.Buffer
	if err := runBindTopic(bindTopicOpts{DBPath: dbPath, Nickname: "vasya", ThreadID: 4242, Out: &out}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !strings.Contains(out.String(), "bound vasya to topic thread_id=4242") {
		t.Errorf("output: %q", out.String())
	}
	d2, _ := db.Open(dbPath)
	u, _ := d2.Users().GetByNickname("vasya")
	if u.TelegramThreadID == nil || *u.TelegramThreadID != 4242 {
		t.Fatalf("thread_id not persisted: %+v", u.TelegramThreadID)
	}
	d2.Close()

	out.Reset()
	if err := runBindTopic(bindTopicOpts{DBPath: dbPath, Nickname: "vasya", Clear: true, Out: &out}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !strings.Contains(out.String(), "cleared topic binding") {
		t.Errorf("output: %q", out.String())
	}
	d3, _ := db.Open(dbPath)
	u2, _ := d3.Users().GetByNickname("vasya")
	if u2.TelegramThreadID != nil {
		t.Fatalf("thread_id should be NULL after clear, got %v", *u2.TelegramThreadID)
	}
	d3.Close()
}

func TestRunBindTopic_BindsTelegramChatID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bt-chat.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("tenant", "rawtok", "1.2.3.4", "nwg0"); err != nil {
		t.Fatal(err)
	}
	d.Close()

	var out bytes.Buffer
	if err := runBindTopic(bindTopicOpts{DBPath: dbPath, Nickname: "tenant", ChatID: -200, ThreadID: 4242, Out: &out}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !strings.Contains(out.String(), "chat_id=-200 thread_id=4242") {
		t.Errorf("output: %q", out.String())
	}
	d2, _ := db.Open(dbPath)
	defer d2.Close()
	u, _ := d2.Users().GetByNickname("tenant")
	if u.TelegramChatID == nil || *u.TelegramChatID != -200 {
		t.Fatalf("chat_id not persisted: %+v", u.TelegramChatID)
	}
	if u.TelegramThreadID == nil || *u.TelegramThreadID != 4242 {
		t.Fatalf("thread_id not persisted: %+v", u.TelegramThreadID)
	}
}

func TestRunBindTopic_AssignsTelegramChatWithoutThread(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bt-chat-only.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("tenant", "rawtok", "1.2.3.4", "nwg0"); err != nil {
		t.Fatal(err)
	}
	d.Close()

	var out bytes.Buffer
	if err := runBindTopic(bindTopicOpts{DBPath: dbPath, Nickname: "tenant", ChatID: -200, Out: &out}); err != nil {
		t.Fatalf("assign chat: %v", err)
	}
	if !strings.Contains(out.String(), "assigned tenant to chat_id=-200") {
		t.Errorf("output: %q", out.String())
	}
	d2, _ := db.Open(dbPath)
	defer d2.Close()
	u, _ := d2.Users().GetByNickname("tenant")
	if u.TelegramChatID == nil || *u.TelegramChatID != -200 {
		t.Fatalf("chat_id not persisted: %+v", u.TelegramChatID)
	}
	if u.TelegramThreadID != nil {
		t.Fatalf("thread_id should remain NULL, got %+v", u.TelegramThreadID)
	}
}

func TestRunBindTopic_ValidatesArgs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bt.db")
	for _, tc := range []struct {
		name string
		opts bindTopicOpts
		want string
	}{
		{"missing nickname", bindTopicOpts{DBPath: dbPath, ThreadID: 1}, "--nickname is required"},
		{"negative thread", bindTopicOpts{DBPath: dbPath, Nickname: "x", ThreadID: -5}, "--thread-id must be"},
		{"zero thread no chat no clear", bindTopicOpts{DBPath: dbPath, Nickname: "x"}, "--thread-id must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runBindTopic(tc.opts)
			if err == nil {
				t.Fatalf("want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %q", tc.want, err.Error())
			}
		})
	}
}
