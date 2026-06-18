// Package db wraps modernc.org/sqlite with our schema migrations and typed queries.
// modernc.org/sqlite is a pure-Go translation of SQLite — no cgo, cross-compiles cleanly.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"os"

	_ "modernc.org/sqlite"
)

//go:embed migrations.sql
var migrationsSQL string

type DB struct {
	db   *sql.DB
	path string
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
	if err := migrateTelegramChatID(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate users.telegram_chat_id: %w", err)
	}
	if err := migrateWizardSync(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate users wizard sync: %w", err)
	}
	if err := migrateMobileRollout(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate mobile rollout: %w", err)
	}
	if err := migrateWizardPortableMetadata(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate users wizard portable metadata: %w", err)
	}
	// Surface where the DB lives and whether this is a fresh init — useful for
	// distinguishing "file vanished" from "first deploy" in journalctl (OBS-23).
	slog.Info("db opened", "path", path, "preexisting", existed)
	return &DB{db: d, path: path}, nil
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

// migrateTelegramChatID adds users.telegram_chat_id for multi-operator-group
// routing. NULL preserves the historic primary telegram.chat_id behaviour.
func migrateTelegramChatID(d *sql.DB) error {
	if err := addColumnIfMissing(d, "users", "telegram_chat_id",
		`ALTER TABLE users ADD COLUMN telegram_chat_id INTEGER`); err != nil {
		return err
	}
	_, err := d.Exec(`CREATE INDEX IF NOT EXISTS idx_users_chat_thread_id ON users(telegram_chat_id, telegram_thread_id) WHERE telegram_thread_id IS NOT NULL`)
	return err
}

// migrateWizardSync adds five nullable columns used by the wizard sync
// feature (v0.12.0). All NULL for pre-existing rows; wizard fills them on
// the first push after deploy. Reverse-compatible: older backend versions
// ignore unknown columns (SQLite never drops on schema reload).
func migrateWizardSync(d *sql.DB) error {
	if err := addColumnIfMissing(d, "users", "ssh_host",
		`ALTER TABLE users ADD COLUMN ssh_host TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "ssh_port",
		`ALTER TABLE users ADD COLUMN ssh_port INTEGER`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "ssh_user",
		`ALTER TABLE users ADD COLUMN ssh_user TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "arch",
		`ALTER TABLE users ADD COLUMN arch TEXT`); err != nil {
		return err
	}
	return addColumnIfMissing(d, "users", "last_deployed_version",
		`ALTER TABLE users ADD COLUMN last_deployed_version TEXT`)
}

func migrateMobileRollout(d *sql.DB) error {
	if err := addColumnIfMissing(d, "users", "deploy_ring",
		`ALTER TABLE users ADD COLUMN deploy_ring TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "pending_version",
		`ALTER TABLE users ADD COLUMN pending_version TEXT`); err != nil {
		return err
	}
	return addColumnIfMissing(d, "users", "pending_since",
		`ALTER TABLE users ADD COLUMN pending_since TEXT`)
}

func migrateWizardPortableMetadata(d *sql.DB) error {
	if err := addColumnIfMissing(d, "users", "last_deploy",
		`ALTER TABLE users ADD COLUMN last_deploy TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "deploy_mode",
		`ALTER TABLE users ADD COLUMN deploy_mode TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "awgm_url",
		`ALTER TABLE users ADD COLUMN awgm_url TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "awgm_auth",
		`ALTER TABLE users ADD COLUMN awgm_auth TEXT`); err != nil {
		return err
	}
	return addColumnIfMissing(d, "users", "expected_mac",
		`ALTER TABLE users ADD COLUMN expected_mac TEXT`)
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

func (d *DB) Path() string { return d.path }

// HealthCheck verifies the database is actually readable, not merely
// connectable. PingContext only proves the connection is open; a SELECT against
// a real table also catches SQLite-corruption / disk-error classes where the
// connection is alive but every read silently fails — exactly the "listener up,
// alerts dropped" mode an external uptime probe must be able to detect. The
// users table is tiny, so this stays cheap to run on every /readyz poll.
func (d *DB) HealthCheck(ctx context.Context) error {
	var n int
	return d.db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n)
}
