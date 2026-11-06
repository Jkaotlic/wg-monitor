package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOperationJournalMigrationAndTerminalAppendOnly(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("router", "token-journal", "", "")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	op := Operation{ID: "op-1", RequestID: "req-1", RouterID: uid, ActorID: 77, Action: "tunnel_restart", Source: "miniapp", StartedAt: started, Summary: "Перезапуск VPN-туннеля"}
	if err := d.Operations().Begin(op); err != nil {
		t.Fatal(err)
	}
	if err := d.Operations().Finish("op-1", OperationFinish{FinishedAt: started.Add(time.Minute), Outcome: "ok", ReasonCode: "command_completed", CommandID: "cmd-1"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Operations().Finish("op-1", OperationFinish{FinishedAt: started.Add(2 * time.Minute), Outcome: "failed", ReasonCode: "late_overwrite"}); err != ErrOperationAlreadyFinished {
		t.Fatalf("second finish err = %v", err)
	}
	got, err := d.Operations().List(uid, 10, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %+v err=%v", got, err)
	}
	if got[0].Outcome != "ok" || got[0].ReasonCode != "command_completed" || got[0].CommandID != "cmd-1" {
		t.Fatalf("operation overwritten or incomplete: %+v", got[0])
	}
}

func TestOperationJournalPaginationNewestFirst(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, _ := d.Users().Insert("router", "token-journal-page", "", "")
	for i, id := range []string{"op-1", "op-2", "op-3"} {
		if err := d.Operations().Begin(Operation{ID: id, RouterID: uid, Action: "test", Source: "test", StartedAt: time.Date(2026, 11, 6, 10, i, 0, 0, time.UTC)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := d.Operations().List(uid, 2, 0)
	if err != nil || len(first) != 2 || first[0].ID != "op-3" || first[1].ID != "op-2" {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	second, err := d.Operations().List(uid, 2, first[1].Seq)
	if err != nil || len(second) != 1 || second[0].ID != "op-1" {
		t.Fatalf("second = %+v err=%v", second, err)
	}
}
