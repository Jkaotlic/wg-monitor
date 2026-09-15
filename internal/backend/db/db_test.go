package db

import (
	"database/sql"
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

// Fix round 2 (мандатное ревью): фехтование записей revive_intents по
// поколению нуждается в колонке generation. Фреш-база получает её через
// CREATE TABLE; эта проверка -- что addColumnIfMissing тоже отработал.
func TestMigrateReviveGenerationAddsColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var notNull int
	var dflt string
	if err := d.SQL().QueryRow(
		`SELECT "notnull", dflt_value FROM pragma_table_info('revive_intents') WHERE name='generation'`,
	).Scan(&notNull, &dflt); err != nil {
		t.Fatalf("нет колонки generation: %v", err)
	}
	if notNull != 1 || dflt != "0" {
		t.Fatalf("generation: notnull=%d default=%q, ждали NOT NULL DEFAULT 0", notNull, dflt)
	}
}

// (d) Fix round 2, мандатное ревью: миграция обязана добавить колонку к УЖЕ
// СУЩЕСТВУЮЩИМ строкам старой базы (revive_intents была создана в Task 3, до
// этой колонки) -- собираем такую базу руками, минуя embed-схему, и
// проверяем, что Open() не теряет данные и проставляет generation=0.
func TestMigrateReviveGenerationPreservesExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    nickname TEXT UNIQUE NOT NULL,
    token_hash TEXT NOT NULL,
    expected_exit_ip TEXT NOT NULL,
    awg_iface TEXT NOT NULL,
    telegram_thread_id INTEGER,
    telegram_chat_id INTEGER,
    telegram_user_id INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP
);
CREATE TABLE revive_intents (
    user_id          INTEGER   PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    target_version   TEXT      NOT NULL DEFAULT '',
    status           TEXT      NOT NULL CHECK (status IN ('waiting','running','done','failed','cancelled','expired')),
    created_at       TIMESTAMP NOT NULL,
    updated_at       TIMESTAMP NOT NULL,
    expires_at       TIMESTAMP NOT NULL,
    attempts         INTEGER   NOT NULL DEFAULT 0,
    last_error       TEXT      NOT NULL DEFAULT '',
    last_probe_at    TIMESTAMP,
    last_probe_state TEXT      NOT NULL DEFAULT '',
    reachable_since  TIMESTAMP,
    reachable_probes INTEGER   NOT NULL DEFAULT 0,
    requested_by     INTEGER   NOT NULL DEFAULT 0
);
INSERT INTO users (id, nickname, token_hash, expected_exit_ip, awg_iface) VALUES (1, 'bronya', 'hash', '198.51.100.1', 'awg0');
INSERT INTO revive_intents (user_id, status, created_at, updated_at, expires_at, attempts, requested_by)
VALUES (1, 'waiting', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', '2026-10-01T00:00:00Z', 2, 42);
`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open старой базы: %v", err)
	}
	defer d.Close()

	in, err := d.Revive().Get(1)
	if err != nil || in == nil {
		t.Fatalf("строка не пережила миграцию: %+v %v", in, err)
	}
	if in.Status != ReviveWaiting || in.Attempts != 2 || in.RequestedBy != 42 {
		t.Fatalf("данные старой строки обязаны сохраниться: %+v", in)
	}
	if in.Generation != 0 {
		t.Fatalf("у старой строки generation обязан стать 0 по умолчанию: %+v", in)
	}
}
