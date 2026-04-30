// cmd/wg-monitor-cli/list_users_test.go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestListUsersEmpty(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	var out bytes.Buffer
	if err := runListUsers(listUsersOpts{DBPath: dbPath, Out: &out}); err != nil {
		t.Fatalf("list-users: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "NICKNAME") {
		t.Fatalf("missing header: %s", got)
	}
	if !strings.Contains(got, "0 user(s) total.") {
		t.Fatalf("missing total line: %s", got)
	}
}

func TestListUsersHappyPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("alpha", "tok-alpha", "1.2.3.4", "nwg0"); err != nil {
		t.Fatal(err)
	}
	bobID, err := d.Users().InsertWithKind("bobmobile", "tok-bob", "5.6.7.8", "nwg1", db.KindMobile)
	if err != nil {
		t.Fatal(err)
	}
	// alpha never reported; bob reported just now → exercise both branches.
	if err := d.Users().UpdateLastSeen(bobID); err != nil {
		t.Fatal(err)
	}
	d.Close()

	// Pin "now" 30s after bob's UpdateLastSeen so the gap is deterministic.
	now := time.Now().Add(30 * time.Second)

	var out bytes.Buffer
	if err := runListUsers(listUsersOpts{DBPath: dbPath, Now: now, Out: &out}); err != nil {
		t.Fatalf("list-users: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"alpha", "static", "1.2.3.4", "never",
		"bobmobile", "mobile", "5.6.7.8",
		"2 user(s) total.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\n---\n%s", want, got)
		}
	}
	// bob's last_seen was set ~30s before our pinned now; expect "Ns ago".
	if !strings.Contains(got, "s ago") {
		t.Fatalf("expected seconds-ago gap for bob, got: %s", got)
	}
}

func TestRenderLastSeen(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		ts   *time.Time
		want string
	}{
		{"nil", nil, "never"},
		{"future clock-skew", ptr(now.Add(5 * time.Second)), "future?"},
		{"seconds", ptr(now.Add(-23 * time.Second)), "23s ago"},
		{"minutes", ptr(now.Add(-14 * time.Minute)), "14m ago"},
		{"hours", ptr(now.Add(-3 * time.Hour)), "3h ago"},
		{"days", ptr(now.Add(-50 * time.Hour)), "2d ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderLastSeen(tc.ts, now)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }
