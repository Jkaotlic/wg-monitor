package db

import (
	"path/filepath"
	"testing"
)

// newTestDBForOps opens a fresh DB in a temp dir and inserts two router
// users (ids 1 and 2). Returns the DB and the two ids.
func newTestDBForOps(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id1, err := d.Users().Insert("router-a", "tok-a", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatalf("insert a: %v", err)
	}
	id2, err := d.Users().Insert("router-b", "tok-b", "2.2.2.2", "awg11")
	if err != nil {
		t.Fatalf("insert b: %v", err)
	}
	return d, id1, id2
}

func TestRouterOperators_AddListRoundTrip(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	if err := d.RouterOperators().Add(routerA, 1001, 999); err != nil {
		t.Fatalf("add op1: %v", err)
	}
	if err := d.RouterOperators().Add(routerA, 1002, 999); err != nil {
		t.Fatalf("add op2: %v", err)
	}
	got, err := d.RouterOperators().List(routerA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 operators, got %d", len(got))
	}
	if got[0].TelegramUserID != 1001 || got[1].TelegramUserID != 1002 {
		t.Errorf("order wrong: %+v", got)
	}
	if got[0].GrantedBy != 999 {
		t.Errorf("granted_by=%d, want 999", got[0].GrantedBy)
	}
}

func TestRouterOperators_AddIdempotent(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	if err := d.RouterOperators().Add(routerA, 1001, 999); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := d.RouterOperators().Add(routerA, 1001, 888); err != nil {
		t.Fatalf("second add should not error: %v", err)
	}
	got, _ := d.RouterOperators().List(routerA)
	if len(got) != 1 {
		t.Errorf("expected 1 row after dup add, got %d", len(got))
	}
	if got[0].GrantedBy != 999 {
		t.Errorf("original granted_by must be preserved, got %d", got[0].GrantedBy)
	}
}

func TestRouterOperators_Remove(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	_ = d.RouterOperators().Add(routerA, 1001, 999)
	_ = d.RouterOperators().Add(routerA, 1002, 999)
	if err := d.RouterOperators().Remove(routerA, 1001); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, _ := d.RouterOperators().List(routerA)
	if len(got) != 1 || got[0].TelegramUserID != 1002 {
		t.Errorf("expected only 1002 to remain, got %+v", got)
	}
}

func TestRouterOperators_HasAccess(t *testing.T) {
	d, routerA, routerB := newTestDBForOps(t)
	_ = d.RouterOperators().Add(routerA, 1001, 999)
	if !d.RouterOperators().HasAccess(routerA, 1001) {
		t.Error("HasAccess should be true for added pair")
	}
	if d.RouterOperators().HasAccess(routerA, 9999) {
		t.Error("HasAccess should be false for unknown tg id")
	}
	if d.RouterOperators().HasAccess(routerB, 1001) {
		t.Error("HasAccess should be false for different router")
	}
}

func TestRouterOperators_CascadeOnUserDelete(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	_ = d.RouterOperators().Add(routerA, 1001, 999)
	if _, err := d.SQL().Exec(`DELETE FROM users WHERE id = ?`, routerA); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	got, err := d.RouterOperators().List(routerA)
	if err != nil {
		t.Fatalf("list after cascade: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cascade should have emptied operators, got %d rows", len(got))
	}
}
