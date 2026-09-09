package db

import (
	"database/sql"
	"errors"
)

// NotifyMutesRepo -- личные выключатели уведомлений: пара «человек + роутер».
type NotifyMutesRepo struct{ d *DB }

func (d *DB) NotifyMutes() *NotifyMutesRepo { return &NotifyMutesRepo{d: d} }

// IsMuted сообщает, заглушил ли этот человек этот роутер. Отсутствие строки --
// «получает»: дефолт держится сам собой, без переноса данных при выкатке.
func (r *NotifyMutesRepo) IsMuted(telegramUserID, userID int64) (bool, error) {
	var one int
	err := r.d.db.QueryRow(
		`SELECT 1 FROM router_notify_mutes WHERE telegram_user_id = ? AND user_id = ?`,
		telegramUserID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetMuted записывает решение человека. Повторный вызов с тем же значением
// безвреден.
func (r *NotifyMutesRepo) SetMuted(telegramUserID, userID int64, muted bool) error {
	if !muted {
		_, err := r.d.db.Exec(
			`DELETE FROM router_notify_mutes WHERE telegram_user_id = ? AND user_id = ?`,
			telegramUserID, userID)
		return err
	}
	_, err := r.d.db.Exec(
		`INSERT OR IGNORE INTO router_notify_mutes (telegram_user_id, user_id) VALUES (?, ?)`,
		telegramUserID, userID)
	return err
}

// MutedBy -- все, кто заглушил этот роутер. Один запрос вместо N проверок на
// каждой рассылке: получателей у роутера немного, но тревоги идут пачками.
func (r *NotifyMutesRepo) MutedBy(userID int64) (map[int64]bool, error) {
	rows, err := r.d.db.Query(
		`SELECT telegram_user_id FROM router_notify_mutes WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
