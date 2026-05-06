package db

import (
	"database/sql"
	"errors"
)

type KVRepo struct{ d *DB }

func (d *DB) KV() *KVRepo { return &KVRepo{d: d} }

// Get returns the value for key, or "" if the key does not exist.
func (r *KVRepo) Get(key string) (string, error) {
	var v string
	err := r.d.db.QueryRow(`SELECT value FROM tg_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// Set upserts the value for key.
func (r *KVRepo) Set(key, value string) error {
	_, err := r.d.db.Exec(
		`INSERT INTO tg_state(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}
