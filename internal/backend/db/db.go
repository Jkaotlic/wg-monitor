// Package db wraps modernc.org/sqlite with our schema migrations and typed queries.
// modernc.org/sqlite is a pure-Go translation of SQLite — no cgo, cross-compiles cleanly.
package db

import (
	_ "embed"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed migrations.sql
var migrationsSQL string

type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := d.Exec(migrationsSQL); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{db: d}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// SQL exposes the underlying *sql.DB for tests and ad-hoc queries.
// Production code should use the typed methods (Users(), Events(), etc.).
func (d *DB) SQL() *sql.DB { return d.db }
