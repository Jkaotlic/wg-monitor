package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Operator is one whitelist row: a Telegram user authorised to control a
// specific router (in addition to the router's owner stored in
// users.telegram_user_id).
type Operator struct {
	UserID         int64
	TelegramUserID int64
	GrantedBy      int64 // TG ID of the admin who granted access
	GrantedAt      time.Time
}

type RouterOperatorsRepo struct{ d *DB }

// RouterOperators returns the typed accessor for the router_operators table.
func (d *DB) RouterOperators() *RouterOperatorsRepo { return &RouterOperatorsRepo{d: d} }

// Add inserts a whitelist row. INSERT OR IGNORE makes the call idempotent —
// re-adding an existing (user_id, telegram_user_id) pair is a no-op that
// preserves the original granted_by / granted_at.
func (r *RouterOperatorsRepo) Add(userID, telegramUserID, grantedBy int64) error {
	_, err := r.d.db.Exec(
		`INSERT OR IGNORE INTO router_operators(user_id, telegram_user_id, granted_by) VALUES (?, ?, ?)`,
		userID, telegramUserID, grantedBy,
	)
	if err != nil {
		return fmt.Errorf("router_operators.Add: %w", err)
	}
	return nil
}

// Remove deletes one operator from one router. Missing rows are not an
// error — DELETE is idempotent.
func (r *RouterOperatorsRepo) Remove(userID, telegramUserID int64) error {
	_, err := r.d.db.Exec(
		`DELETE FROM router_operators WHERE user_id = ? AND telegram_user_id = ?`,
		userID, telegramUserID,
	)
	if err != nil {
		return fmt.Errorf("router_operators.Remove: %w", err)
	}
	return nil
}

// List returns operators for a router ordered by GrantedAt ASC (oldest
// first), which is the rendering order on the access screen.
func (r *RouterOperatorsRepo) List(userID int64) ([]Operator, error) {
	rows, err := r.d.db.Query(
		`SELECT user_id, telegram_user_id, granted_by, granted_at
		 FROM router_operators WHERE user_id = ? ORDER BY granted_at ASC, telegram_user_id ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("router_operators.List: %w", err)
	}
	defer rows.Close()
	var out []Operator
	for rows.Next() {
		var op Operator
		if err := rows.Scan(&op.UserID, &op.TelegramUserID, &op.GrantedBy, &op.GrantedAt); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// HasAccess is the hot path for ACL: called from Router.aclAllow on every
// non-admin, non-owner callback. Single indexed-lookup query.
func (r *RouterOperatorsRepo) HasAccess(userID, telegramUserID int64) bool {
	var one int
	err := r.d.db.QueryRow(
		`SELECT 1 FROM router_operators WHERE user_id = ? AND telegram_user_id = ?`,
		userID, telegramUserID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		// Defensive: log via slog from caller; here we just deny.
		return false
	}
	return one == 1
}
