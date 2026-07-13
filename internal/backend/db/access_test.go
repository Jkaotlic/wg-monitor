package db

import (
	"path/filepath"
	"testing"
)

func TestRouterAccessRole(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	routerID, err := d.Users().Insert("router-a", "tok-a", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := d.Users().SetTelegramUserID(routerID, 100); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	if err := d.RouterOperators().Add(routerID, 200, 999); err != nil {
		t.Fatalf("add operator: %v", err)
	}

	if role, err := d.RouterAccessRole(routerID, 100); err != nil || role != "owner" {
		t.Errorf("owner: role=%q err=%v, want owner/nil", role, err)
	}
	if role, err := d.RouterAccessRole(routerID, 200); err != nil || role != "operator" {
		t.Errorf("operator: role=%q err=%v, want operator/nil", role, err)
	}
	if role, err := d.RouterAccessRole(routerID, 300); err != nil || role != "" {
		t.Errorf("stranger: role=%q err=%v, want empty/nil", role, err)
	}
	if role, err := d.RouterAccessRole(999999, 100); err != nil || role != "" {
		t.Errorf("unknown router: role=%q err=%v, want empty/nil", role, err)
	}
}

func TestAccessibleRouterIDs(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ownedID, err := d.Users().Insert("router-owned", "tok-1", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatalf("insert owned: %v", err)
	}
	if err := d.Users().SetTelegramUserID(ownedID, 100); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	operatedID, err := d.Users().Insert("router-operated", "tok-2", "1.1.1.2", "awg11")
	if err != nil {
		t.Fatalf("insert operated: %v", err)
	}
	if err := d.RouterOperators().Add(operatedID, 100, 999); err != nil {
		t.Fatalf("add operator: %v", err)
	}
	if _, err := d.Users().Insert("router-unrelated", "tok-3", "1.1.1.3", "awg11"); err != nil {
		t.Fatalf("insert unrelated: %v", err)
	}

	ids, err := d.AccessibleRouterIDs(100)
	if err != nil {
		t.Fatalf("AccessibleRouterIDs: %v", err)
	}
	got := map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got[ownedID] || !got[operatedID] || len(got) != 2 {
		t.Errorf("ids = %v, want exactly {%d, %d}", ids, ownedID, operatedID)
	}
}
