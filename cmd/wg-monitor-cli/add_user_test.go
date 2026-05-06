// cmd/wg-monitor-cli/add_user_test.go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestAddUserHappyPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	var out bytes.Buffer
	err = runAddUser(addUserOpts{
		DBPath:         dbPath,
		Nickname:       "vasya",
		AWGIface:       "awg0",
		ExpectedExitIP: "198.51.100.21",
		BackendURL:     "https://wgmonitor.example.com",
		Out:            &out,
	})
	if err != nil {
		t.Fatalf("add-user: %v", err)
	}
	got := out.String()
	for _, want := range []string{"vasya", "Token (raw,", "config.yaml", "https://wgmonitor.example.com"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}

	// Re-open DB and confirm the user is there
	d, _ = db.Open(dbPath)
	defer d.Close()
	u, err := d.Users().GetByNickname("vasya")
	if err != nil {
		t.Fatalf("user not found: %v", err)
	}
	if u.AWGIface != "awg0" || u.ExpectedExitIP != "198.51.100.21" {
		t.Fatalf("user fields: %+v", u)
	}
}

func TestAddUserRejectsBadNickname(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	err := runAddUser(addUserOpts{
		DBPath: dbPath, Nickname: "Vasya!", AWGIface: "awg0",
		ExpectedExitIP: "1.1.1.1", BackendURL: "https://x", Out: &out,
	})
	if err == nil {
		t.Fatal("expected nickname validation error")
	}
}

func TestAddUserMobileKind(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	if err := runAddUser(addUserOpts{
		DBPath: dbPath, Nickname: "client-h", AWGIface: "nwg0",
		ExpectedExitIP: "1.1.1.1", BackendURL: "https://x",
		Kind: db.KindMobile, Out: &out,
	}); err != nil {
		t.Fatalf("add-user mobile: %v", err)
	}
	if !strings.Contains(out.String(), "kind=mobile") {
		t.Fatalf("output missing kind=mobile: %s", out.String())
	}
	d, _ = db.Open(dbPath)
	defer d.Close()
	u, _ := d.Users().GetByNickname("client-h")
	if !u.IsMobile() {
		t.Fatalf("expected mobile, got kind=%q", u.Kind)
	}
}

func TestAddUserRejectsBadKind(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	err := runAddUser(addUserOpts{
		DBPath: dbPath, Nickname: "vasya", AWGIface: "awg0",
		ExpectedExitIP: "1.1.1.1", BackendURL: "https://x",
		Kind: "drone", Out: &out,
	})
	if err == nil {
		t.Fatal("expected kind validation error")
	}
}

func TestAddUserRejectsDuplicate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	opts := addUserOpts{
		DBPath: dbPath, Nickname: "vasya", AWGIface: "awg0",
		ExpectedExitIP: "1.1.1.1", BackendURL: "https://x", Out: &out,
	}
	if err := runAddUser(opts); err != nil {
		t.Fatal(err)
	}
	if err := runAddUser(opts); err == nil {
		t.Fatal("expected duplicate error on second add")
	}
}
