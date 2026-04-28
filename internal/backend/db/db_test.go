package db

import (
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	for _, table := range []string{"users", "events", "incident_state", "daily_soft_flaps"} {
		var name string
		err := d.SQL().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
}

func TestOpenIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	d.Close()
}

func TestMigrateAckedAddsColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var n int
	err = d.SQL().QueryRow(`SELECT count(*) FROM pragma_table_info('incident_state') WHERE name='acked'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected acked column, got count=%d", n)
	}
}

func TestMigrateTGStateTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var name string
	err = d.SQL().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='tg_state'`).Scan(&name)
	if err != nil {
		t.Fatalf("tg_state table missing: %v", err)
	}
}
