package db

import (
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// InsertUnstickEventSQL -- запись события сторожа зависаний. Повтор того же
// события (журнал пришёл ещё раз) ничего не пишет: RowsAffected 0.
// Экспортирован, чтобы приём отчёта писал в своей транзакции.
const InsertUnstickEventSQL = `
INSERT INTO awgm_unstick_events(user_id, event_id, tunnel_id, tunnel_name, from_status, steps, result, to_status, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user_id, event_id) DO NOTHING`

// UnstickEventArgs -- аргументы InsertUnstickEventSQL в его порядке.
func UnstickEventArgs(userID int64, e wire.UnstickEvent) []any {
	return []any{userID, e.ID, e.TunnelID, wire.ClipText(e.TunnelName), wire.ClipText(e.From),
		strings.Join(e.Steps, ","), e.Result, wire.ClipText(e.To), factTS(e.At)}
}

type UnstickEventsRepo struct{ d *DB }

func (d *DB) UnstickEvents() *UnstickEventsRepo { return &UnstickEventsRepo{d: d} }

// PruneBefore удаляет события старше cutoff.
func (r *UnstickEventsRepo) PruneBefore(cutoff time.Time) (int64, error) {
	res, err := r.d.db.Exec(`DELETE FROM awgm_unstick_events WHERE at < ?`, factTS(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Count -- число событий роутера (тесты, диагностика).
func (r *UnstickEventsRepo) Count(userID int64) (int, error) {
	var n int
	err := r.d.db.QueryRow(`SELECT COUNT(*) FROM awgm_unstick_events WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}
