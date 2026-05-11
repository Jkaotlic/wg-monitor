package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
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

func TestRunBindTopic_ValidatesArgs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bt.db")
	for _, tc := range []struct {
		name string
		opts bindTopicOpts
		want string
	}{
		{"missing nickname", bindTopicOpts{DBPath: dbPath, ThreadID: 1}, "--nickname is required"},
		{"negative thread", bindTopicOpts{DBPath: dbPath, Nickname: "x", ThreadID: -5}, "--thread-id must be"},
		{"zero thread no clear", bindTopicOpts{DBPath: dbPath, Nickname: "x"}, "--thread-id must be"},
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
