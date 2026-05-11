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

const (
	KindStatic = "static"
	KindMobile = "mobile"
)

func IsValidKind(k string) bool { return k == KindStatic || k == KindMobile }

type User struct {
	ID               int64
	Nickname         string
	TokenHash        string
	ExpectedExitIP   string
	AWGIface         string
	Kind             string
	TelegramThreadID *int64
	// TelegramUserID is the numeric Telegram user id of the router's owner,
	// captured either via CLI bind-tg-user or via TOFU on the first action
	// in the owner's per-router topic. NULL until bound; callbacks router
	// uses it to gate non-admin button taps to the rightful owner.
	TelegramUserID *int64
	CreatedAt      time.Time
	LastSeenAt     *time.Time
}

// IsMobile reports whether this user is a mobile (4G in-vehicle) router.
// Used by the heartbeat watcher to apply a longer grace window.
func (u User) IsMobile() bool { return u.Kind == KindMobile }

type UsersRepo struct{ d *DB }

func (d *DB) Users() *UsersRepo { return &UsersRepo{d: d} }

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Insert creates a static-kind user (default for fixed home/office routers).
// Use InsertWithKind to create a mobile-kind user.
func (u *UsersRepo) Insert(nickname, rawToken, expectedExitIP, awgIface string) (int64, error) {
	return u.InsertWithKind(nickname, rawToken, expectedExitIP, awgIface, KindStatic)
}

func (u *UsersRepo) InsertWithKind(nickname, rawToken, expectedExitIP, awgIface, kind string) (int64, error) {
	if !IsValidKind(kind) {
		return 0, fmt.Errorf("users.Insert: invalid kind %q (want static|mobile)", kind)
	}
	res, err := u.d.db.Exec(
		`INSERT INTO users(nickname, token_hash, expected_exit_ip, awg_iface, kind) VALUES (?, ?, ?, ?, ?)`,
		nickname, hashToken(rawToken), expectedExitIP, awgIface, kind,
	)
	if err != nil {
		return 0, fmt.Errorf("users.Insert: %w", err)
	}
	return res.LastInsertId()
}

// userColsFull lists every column read by single-row Get*. GetAll uses a
// shorter projection (no token_hash, no created_at) and has its own scanner.
const userColsFull = `id, nickname, token_hash, expected_exit_ip, awg_iface, kind, telegram_thread_id, telegram_user_id, created_at, last_seen_at`

type userScanner interface {
	Scan(dest ...any) error
}

func scanUserFull(s userScanner) (*User, error) {
	var got User
	var threadID sql.NullInt64
	var tgUserID sql.NullInt64
	var lastSeen sql.NullTime
	if err := s.Scan(&got.ID, &got.Nickname, &got.TokenHash, &got.ExpectedExitIP, &got.AWGIface, &got.Kind, &threadID, &tgUserID, &got.CreatedAt, &lastSeen); err != nil {
		return nil, err
	}
	if threadID.Valid {
		v := threadID.Int64
		got.TelegramThreadID = &v
	}
	if tgUserID.Valid {
		v := tgUserID.Int64
		got.TelegramUserID = &v
	}
	if lastSeen.Valid {
		v := lastSeen.Time
		got.LastSeenAt = &v
	}
	return &got, nil
}

func (u *UsersRepo) GetByToken(rawToken string) (*User, error) {
	target := hashToken(rawToken)
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE token_hash = ?`, target)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(got.TokenHash), []byte(target)) != 1 {
		// SHA-256 collision is astronomically unlikely; this branch is paranoia.
		return nil, ErrUserNotFound
	}
	return got, nil
}

func (u *UsersRepo) GetByID(id int64) (*User, error) {
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE id = ?`, id)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return got, nil
}

func (u *UsersRepo) GetByNickname(nickname string) (*User, error) {
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE nickname = ?`, nickname)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return got, nil
}

func (u *UsersRepo) GetAll() ([]User, error) {
	rows, err := u.d.db.Query(`SELECT id, nickname, expected_exit_ip, awg_iface, kind, telegram_thread_id, telegram_user_id, last_seen_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var got User
		var threadID sql.NullInt64
		var tgUserID sql.NullInt64
		var lastSeen sql.NullTime
		if err := rows.Scan(&got.ID, &got.Nickname, &got.ExpectedExitIP, &got.AWGIface, &got.Kind, &threadID, &tgUserID, &lastSeen); err != nil {
			return nil, err
		}
		if threadID.Valid {
			v := threadID.Int64
			got.TelegramThreadID = &v
		}
		if tgUserID.Valid {
			v := tgUserID.Int64
			got.TelegramUserID = &v
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

// SetTelegramUserID binds the router to a specific Telegram user id.
// Used by the callbacks router (TOFU on first owner action) and the
// `bind-tg-user` CLI. Pass 0 to clear the binding.
func (u *UsersRepo) SetTelegramUserID(id, tgUserID int64) error {
	if tgUserID == 0 {
		_, err := u.d.db.Exec(`UPDATE users SET telegram_user_id = NULL WHERE id = ?`, id)
		return err
	}
	_, err := u.d.db.Exec(`UPDATE users SET telegram_user_id = ? WHERE id = ?`, tgUserID, id)
	return err
}

// ClearThreadID nulls out telegram_thread_id for a user. Used by the
// dispatcher's self-heal path: when sendMessage reports the cached topic
// no longer exists in TG, we clear the id so the next ensureTopic call
// invokes createForumTopic and persists a fresh id.
func (u *UsersRepo) ClearThreadID(id int64) error {
	_, err := u.d.db.Exec(`UPDATE users SET telegram_thread_id = NULL WHERE id = ?`, id)
	return err
}

// GetByThreadID looks up a user by their assigned Telegram forum-topic id.
// Used by the callbacks router to map an incoming Message's
// message_thread_id to the owning user (per-router topic). Returns
// ErrUserNotFound when no user owns this topic.
func (u *UsersRepo) GetByThreadID(threadID int64) (*User, error) {
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE telegram_thread_id = ?`, threadID)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return got, nil
}
