package db

import "time"

// UnreachableTarget -- человек, которому бот не смог написать в личку.
type UnreachableTarget struct {
	TelegramUserID int64
	LastError      string
	UpdatedAt      time.Time
}

// UnreachableRepo -- учёт недоставленных личек.
type UnreachableRepo struct{ d *DB }

func (d *DB) Unreachable() *UnreachableRepo { return &UnreachableRepo{d: d} }

// Mark помечает человека недоступным. Повторный вызов обновляет причину и
// время, но не плодит строк.
func (r *UnreachableRepo) Mark(telegramUserID int64, reason string) error {
	_, err := r.d.db.Exec(
		`INSERT INTO telegram_unreachable (telegram_user_id, last_error, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(telegram_user_id) DO UPDATE SET
		   last_error = excluded.last_error,
		   updated_at = CURRENT_TIMESTAMP`,
		telegramUserID, reason)
	return err
}

// Clear снимает отметку: доставка прошла, человек подхватился.
func (r *UnreachableRepo) Clear(telegramUserID int64) error {
	_, err := r.d.db.Exec(
		`DELETE FROM telegram_unreachable WHERE telegram_user_id = ?`, telegramUserID)
	return err
}

// List -- все, кто сейчас не получает уведомления.
func (r *UnreachableRepo) List() ([]UnreachableTarget, error) {
	rows, err := r.d.db.Query(
		`SELECT telegram_user_id, last_error, updated_at FROM telegram_unreachable
		  ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UnreachableTarget, 0)
	for rows.Next() {
		var t UnreachableTarget
		if err := rows.Scan(&t.TelegramUserID, &t.LastError, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
