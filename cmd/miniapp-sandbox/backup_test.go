package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

func TestSeedBackupStatusModes(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "sandbox.db")
	if err := seedBackupStatus(db, "good", now); err != nil {
		t.Fatal(err)
	}
	s, err := backup.LoadStatus(sandboxBackupPath(db))
	if err != nil || !s.Small.OK || !s.Full.OK || !s.Verify.OK || s.Small.LastOKAt != "2026-10-07T09:00:00Z" || s.Full.Offsite != "ok" {
		t.Fatalf("good: %+v %v", s, err)
	}

	db2 := filepath.Join(t.TempDir(), "sandbox.db")
	if err := seedBackupStatus(db2, "failed", now); err != nil {
		t.Fatal(err)
	}
	f, err := backup.LoadStatus(sandboxBackupPath(db2))
	if err != nil || f.Small.OK || f.Full.OK || f.Verify.OK || f.Small.Telegram != "error" || f.Full.Offsite != "error" {
		t.Fatalf("failed: %+v %v", f, err)
	}

	db3 := filepath.Join(t.TempDir(), "sandbox.db")
	for _, mode := range []string{"unknown", "off"} {
		if err := seedBackupStatus(db3, mode, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backup.LoadStatus(sandboxBackupPath(db3)); err == nil {
		t.Fatal("unknown/off не должны класть файл")
	}
	if err := seedBackupStatus(db3, "nonsense", now); err == nil {
		t.Fatal("неизвестный режим принят")
	}
}
