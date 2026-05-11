// Package db wraps modernc.org/sqlite with our schema migrations and typed queries.
// modernc.org/sqlite is a pure-Go translation of SQLite — no cgo, cross-compiles cleanly.
package db

import (
	_ "embed"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	_ "modernc.org/sqlite"
)

//go:embed migrations.sql
var migrationsSQL string

type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, error) {
	// Pragmas:
	//   foreign_keys(1)  — enforce CASCADE rules.
	//   journal_mode(WAL) — concurrent reads with single writer; durability OK.
	//   synchronous(NORMAL) — WAL + NORMAL is the recommended pair: ~5–10×
	//                         faster small writes than FULL with no
	//                         meaningful corruption-risk increase under WAL.
	//   busy_timeout(5000) — block up to 5s for the writer lock instead of
	//                         returning SQLITE_BUSY immediately.
	existed := true
	if _, err := os.Stat(path); os.IsNotExist(err) {
		existed = false
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	// Single writer: SQLite serialises writes anyway, but the database/sql
	// pool can still hand out parallel connections that then collide on the
	// writer lock and produce SQLITE_BUSY errors under load. Pinning to one
	// connection makes ordering deterministic and matches SQLite's native
	// model. busy_timeout above absorbs the (now-rare) lock contention.
	d.SetMaxOpenConns(1)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := d.Exec(migrationsSQL); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrateAcked(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate acked: %w", err)
	}
	if err := migrateUserKind(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate users.kind: %w", err)
	}
	if err := migrateEventsUniqueIdx(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate events unique idx: %w", err)
	}
	if err := migrateTelegramUserID(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate users.telegram_user_id: %w", err)
	}
	// Surface where the DB lives and whether this is a fresh init — useful for
	// distinguishing "file vanished" from "first deploy" in journalctl (OBS-23).
	slog.Info("db opened", "path", path, "preexisting", existed)
	return &DB{db: d}, nil
}

// migrateEventsUniqueIdx is the API-01 idempotency migration. Without a UNIQUE
// index a TCP retry from the agent inserted a second copy of every event in
// the report, polluting daily_soft_flaps and skewing realert cadence.
//
// Dedup-then-create. The DELETE step keeps the lowest `id` per
// (user_id, check_name, ts) triple, which preserves the FSM-relevant ordering
// the dispatcher saw during the original ingest. If the index already exists
// (re-run on a clean DB) the function is a no-op.
func migrateEventsUniqueIdx(d *sql.DB) error {
	var exists int
	if err := d.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='uq_events_user_check_ts'`,
	).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	if _, err := d.Exec(
		`DELETE FROM events WHERE id NOT IN (
		    SELECT MIN(id) FROM events GROUP BY user_id, check_name, ts
		 )`); err != nil {
		return err
	}
	_, err := d.Exec(
		`CREATE UNIQUE INDEX uq_events_user_check_ts ON events(user_id, check_name, ts)`)
	return err
}

// migrateAcked is a one-shot ALTER TABLE for Stage 2.
// SQLite has no `ADD COLUMN IF NOT EXISTS`, so we probe pragma_table_info first.
func migrateAcked(d *sql.DB) error {
	return addColumnIfMissing(d, "incident_state", "acked",
		`ALTER TABLE incident_state ADD COLUMN acked INTEGER NOT NULL DEFAULT 0`)
}

// migrateUserKind adds users.kind ('static'|'mobile') for awg-manager pivot:
// mobile users (4G in-vehicle) get a longer heartbeat grace window and an
// online-resumed code path on rejoin.
func migrateUserKind(d *sql.DB) error {
	return addColumnIfMissing(d, "users", "kind",
		`ALTER TABLE users ADD COLUMN kind TEXT NOT NULL DEFAULT 'static'`)
}

// migrateTelegramUserID adds users.telegram_user_id (NULL by default) for
// callback ACL: the TG numeric user id of the router's owner. Used by
// HandleCallback to reject taps from members of the group chat who are
// not the owner of the targeted router (admin always passes).
func migrateTelegramUserID(d *sql.DB) error {
	return addColumnIfMissing(d, "users", "telegram_user_id",
		`ALTER TABLE users ADD COLUMN telegram_user_id INTEGER`)
}

func addColumnIfMissing(d *sql.DB, table, column, alter string) error {
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := d.Exec(alter)
		return err
	}
	return nil
}

func (d *DB) Close() error { return d.db.Close() }

// SQL exposes the underlying *sql.DB for tests and ad-hoc queries.
// Production code should use the typed methods (Users(), Events(), etc.).
func (d *DB) SQL() *sql.DB { return d.db }
