package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestProvisionLastSeen_UnknownNicknameNotFound(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	lastSeen := provisionLastSeen(database)
	if ts, ok := lastSeen("ghost"); ok {
		t.Fatalf("unknown nickname: found=true (ts=%v), want found=false", ts)
	}
}

func TestProvisionLastSeen_EnrolledButNeverReportedNotFound(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Users().UpsertEnrollment("freshrouter", "tok-freshrouter-00000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}

	lastSeen := provisionLastSeen(database)
	if ts, ok := lastSeen("freshrouter"); ok {
		t.Fatalf("enrolled-but-never-reported: found=true (ts=%v), want found=false", ts)
	}
}

// TestProvisionLastSeen_ReadsTheSameColumnReportHandlerUpdates pins the
// canonical source: UpdateLastSeen is the exact same users.last_seen_at
// column reportHandler's `UPDATE users SET last_seen_at = ...` writes on
// every POST /v1/report (see internal/backend/handler.go). If a future
// change swapped provisionLastSeen to read from a different/cached source,
// this test would stop passing.
func TestProvisionLastSeen_ReadsTheSameColumnReportHandlerUpdates(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	uid, err := database.Users().UpsertEnrollment("reportedrouter", "tok-reportedrouter-0000000", db.KindStatic, 0)
	if err != nil {
		t.Fatal(err)
	}

	lastSeen := provisionLastSeen(database)
	if _, ok := lastSeen("reportedrouter"); ok {
		t.Fatal("found=true before any report — should still be false")
	}

	before := time.Now().Add(-time.Minute)
	if err := database.Users().UpdateLastSeen(uid); err != nil {
		t.Fatal(err)
	}

	ts, ok := lastSeen("reportedrouter")
	if !ok {
		t.Fatal("found=false after UpdateLastSeen, want true")
	}
	if ts.IsZero() {
		t.Fatal("want a non-zero timestamp")
	}
	if ts.Before(before) {
		t.Fatalf("timestamp %v looks stale (before %v)", ts, before)
	}
	if now := time.Now(); ts.After(now.Add(time.Minute)) {
		t.Fatalf("timestamp %v is implausibly far in the future (now=%v)", ts, now)
	}
}
