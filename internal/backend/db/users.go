package db

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrUserNotFound = errors.New("user not found")

type User struct {
	ID               int64
	Nickname         string
	TokenHash        string
	ExpectedExitIP   string
	AWGIface         string
	TelegramThreadID *int64
	CreatedAt        time.Time
	LastSeenAt       *time.Time
}

type UsersRepo struct{ d *DB }

func (d *DB) Users() *UsersRepo { return &UsersRepo{d: d} }

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (u *UsersRepo) Insert(nickname, rawToken, expectedExitIP, awgIface string) (int64, error) {
	res, err := u.d.db.Exec(
		`INSERT INTO users(nickname, token_hash, expected_exit_ip, awg_iface) VALUES (?, ?, ?, ?)`,
		nickname, hashToken(rawToken), expectedExitIP, awgIface,
	)
	if err != nil {
		return 0, fmt.Errorf("users.Insert: %w", err)
	}
	return res.LastInsertId()
}

func (u *UsersRepo) GetByToken(rawToken string) (*User, error) {
	target := hashToken(rawToken)
	row := u.d.db.QueryRow(
		`SELECT id, nickname, token_hash, expected_exit_ip, awg_iface, telegram_thread_id, created_at, last_seen_at FROM users WHERE token_hash = ?`,
		target,
	)
	var got User
	var threadID sql.NullInt64
	var lastSeen sql.NullTime
	if err := row.Scan(&got.ID, &got.Nickname, &got.TokenHash, &got.ExpectedExitIP, &got.AWGIface, &threadID, &got.CreatedAt, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(got.TokenHash), []byte(target)) != 1 {
		// SHA-256 collision is astronomically unlikely; this branch is paranoia.
		return nil, ErrUserNotFound
	}
	if threadID.Valid {
		v := threadID.Int64
		got.TelegramThreadID = &v
	}
	if lastSeen.Valid {
		v := lastSeen.Time
		got.LastSeenAt = &v
	}
	return &got, nil
}

func (u *UsersRepo) GetByNickname(nickname string) (*User, error) {
	row := u.d.db.QueryRow(
		`SELECT id, nickname, token_hash, expected_exit_ip, awg_iface, telegram_thread_id, created_at, last_seen_at FROM users WHERE nickname = ?`,
		nickname,
	)
	var got User
	var threadID sql.NullInt64
	var lastSeen sql.NullTime
	if err := row.Scan(&got.ID, &got.Nickname, &got.TokenHash, &got.ExpectedExitIP, &got.AWGIface, &threadID, &got.CreatedAt, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if threadID.Valid {
		v := threadID.Int64
		got.TelegramThreadID = &v
	}
	if lastSeen.Valid {
		v := lastSeen.Time
		got.LastSeenAt = &v
	}
	return &got, nil
}

func (u *UsersRepo) GetAll() ([]User, error) {
	rows, err := u.d.db.Query(`SELECT id, nickname, expected_exit_ip, awg_iface, telegram_thread_id, last_seen_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var got User
		var threadID sql.NullInt64
		var lastSeen sql.NullTime
		if err := rows.Scan(&got.ID, &got.Nickname, &got.ExpectedExitIP, &got.AWGIface, &threadID, &lastSeen); err != nil {
			return nil, err
		}
		if threadID.Valid {
			v := threadID.Int64
			got.TelegramThreadID = &v
		}
		if lastSeen.Valid {
			v := lastSeen.Time
			got.LastSeenAt = &v
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

func (u *UsersRepo) UpdateLastSeen(id int64) error {
	_, err := u.d.db.Exec(`UPDATE users SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func (u *UsersRepo) UpdateThreadID(id, threadID int64) error {
	_, err := u.d.db.Exec(`UPDATE users SET telegram_thread_id = ? WHERE id = ?`, threadID, id)
	return err
}
