package db

import (
	"path/filepath"
	"testing"
)

func newTestDBForRepair(t *testing.T) (*DB, int64) {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("router-a", "tok-a", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return d, id
}

// Умолчание -- включено. Строки в таблице у существующих роутеров нет, и
// отсутствие настройки обязано читаться как «да», иначе полуавтомат не
// включится ни у кого, кто завёлся до этой миграции.
func TestAutoRepair_DefaultsOn(t *testing.T) {
	d, userID := newTestDBForRepair(t)

	on, err := d.RepairSettings().AutoRepair(userID)
	if err != nil {
		t.Fatalf("AutoRepair: %v", err)
	}
	if !on {
		t.Fatal("без строки в таблице полуавтомат обязан быть включён")
	}
}

func TestAutoRepair_OffThenOn(t *testing.T) {
	d, userID := newTestDBForRepair(t)
	r := d.RepairSettings()

	if err := r.SetAutoRepair(userID, false); err != nil {
		t.Fatalf("SetAutoRepair(false): %v", err)
	}
	on, err := r.AutoRepair(userID)
	if err != nil {
		t.Fatalf("AutoRepair: %v", err)
	}
	if on {
		t.Fatal("после выключения обязан быть выключен")
	}

	if err := r.SetAutoRepair(userID, true); err != nil {
		t.Fatalf("SetAutoRepair(true): %v", err)
	}
	if on, _ = r.AutoRepair(userID); !on {
		t.Fatal("повторное включение обязано сработать")
	}
}

// Обкатка: парк, который настройку ни разу не трогал, идёт за решением
// оператора бэкенда. Явно выключивший её роутер остаётся выключенным, явно
// включивший -- включённым, что бы ни стояло дефолтом.
func TestAutoRepair_WithDefaultOff(t *testing.T) {
	d, userID := newTestDBForRepair(t)
	r := d.RepairSettings().WithDefault(false)

	on, err := r.AutoRepair(userID)
	if err != nil {
		t.Fatalf("AutoRepair: %v", err)
	}
	if on {
		t.Fatal("без строки полуавтомат обязан идти за дефолтом бэкенда")
	}

	if err := r.SetAutoRepair(userID, true); err != nil {
		t.Fatalf("SetAutoRepair: %v", err)
	}
	if on, _ = r.AutoRepair(userID); !on {
		t.Fatal("явно включённый роутер дефолту не подчиняется")
	}
}
