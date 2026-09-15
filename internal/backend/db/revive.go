package db

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Статусы намерения оживления.
const (
	ReviveWaiting   = "waiting"
	ReviveRunning   = "running"
	ReviveDone      = "done"
	ReviveFailed    = "failed"
	ReviveCancelled = "cancelled"
	ReviveExpired   = "expired"
)

// ErrReviveRunning -- переставить намерение нельзя: переустановка уже идёт с
// прежним паролем, и подмена секрета посреди неё ничего бы не дала.
var ErrReviveRunning = errors.New("revive: переустановка уже идёт")

// ReviveIntent -- строка revive_intents. Нулевое время означает NULL.
type ReviveIntent struct {
	RouterID        int64
	Status          string
	TargetVersion   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ExpiresAt       time.Time
	Attempts        int
	LastError       string
	LastProbeAt     time.Time
	LastProbeState  string
	ReachableSince  time.Time
	ReachableProbes int
	RequestedBy     int64
}

// ReviveRepo -- намерения оживления и их секреты.
type ReviveRepo struct{ d *DB }

func (d *DB) Revive() *ReviveRepo { return &ReviveRepo{d: d} }

const reviveColumns = `user_id, status, target_version, created_at, updated_at, expires_at, attempts,
	last_error, last_probe_at, last_probe_state, reachable_since, reachable_probes, requested_by`

// Put ставит намерение с нуля (waiting, попыток 0, опросов нет) и кладёт
// секрет -- одной транзакцией. Поверх running -- ErrReviveRunning, и секрет
// не трогается.
func (r *ReviveRepo) Put(in ReviveIntent, nonce, ciphertext []byte) error {
	tx, err := r.d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`
INSERT INTO revive_intents (user_id, status, target_version, created_at, updated_at, expires_at,
                            attempts, last_error, last_probe_at, last_probe_state,
                            reachable_since, reachable_probes, requested_by)
VALUES (?, 'waiting', ?, ?, ?, ?, 0, '', NULL, '', NULL, 0, ?)
ON CONFLICT(user_id) DO UPDATE SET
    status = 'waiting', target_version = excluded.target_version,
    created_at = excluded.created_at, updated_at = excluded.updated_at,
    expires_at = excluded.expires_at, attempts = 0, last_error = '',
    last_probe_at = NULL, last_probe_state = '', reachable_since = NULL,
    reachable_probes = 0, requested_by = excluded.requested_by
WHERE revive_intents.status <> 'running'`,
		in.RouterID, in.TargetVersion, in.CreatedAt.UTC(), in.CreatedAt.UTC(), in.ExpiresAt.UTC(), in.RequestedBy)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrReviveRunning
	}
	if _, err := tx.Exec(`
INSERT INTO revive_secrets (user_id, nonce, ciphertext) VALUES (?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET nonce = excluded.nonce, ciphertext = excluded.ciphertext`,
		in.RouterID, nonce, ciphertext); err != nil {
		return err
	}
	return tx.Commit()
}

// Get -- намерение роутера или nil, если его не ставили.
func (r *ReviveRepo) Get(routerID int64) (*ReviveIntent, error) {
	row := r.d.db.QueryRow(`SELECT `+reviveColumns+` FROM revive_intents WHERE user_id = ?`, routerID)
	in, err := scanReviveIntent(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}

// ListByStatus -- намерения в одном статусе, по возрастанию user_id.
func (r *ReviveRepo) ListByStatus(status string) ([]ReviveIntent, error) {
	rows, err := r.d.db.Query(`SELECT `+reviveColumns+` FROM revive_intents WHERE status = ? ORDER BY user_id`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviveIntent
	for rows.Next() {
		in, err := scanReviveIntent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// Secret -- nonce и шифртекст. ok=false, если секрета нет (стёрт или не ставили).
func (r *ReviveRepo) Secret(routerID int64) (nonce, ciphertext []byte, ok bool, err error) {
	err = r.d.db.QueryRow(`SELECT nonce, ciphertext FROM revive_secrets WHERE user_id = ?`, routerID).
		Scan(&nonce, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	return nonce, ciphertext, true, nil
}

// RecordProbe записывает итог опроса. Только для waiting: опрос, вернувшийся
// после отмены или запуска, не имеет права переписать строку.
func (r *ReviveRepo) RecordProbe(routerID int64, at time.Time, state string, reachableProbes int, reachableSince time.Time) error {
	_, err := r.d.db.Exec(`
UPDATE revive_intents
   SET last_probe_at = ?, last_probe_state = ?, reachable_probes = ?, reachable_since = ?, updated_at = ?
 WHERE user_id = ? AND status = 'waiting'`,
		at.UTC(), state, reachableProbes, nullableTime(reachableSince), at.UTC(), routerID)
	return err
}

// MarkRunning -- waiting→running с засчитанной попыткой. false -- кто-то
// успел раньше (второй запуск на тот же роутер не пройдёт).
func (r *ReviveRepo) MarkRunning(routerID int64, at time.Time) (bool, error) {
	res, err := r.d.db.Exec(`
UPDATE revive_intents SET status = 'running', attempts = attempts + 1, updated_at = ?
 WHERE user_id = ? AND status = 'waiting'`, at.UTC(), routerID)
	return affectedOne(res, err)
}

// BackToWaiting -- running→waiting после неудачной, но не окончательной
// попытки. Серия «панель отвечает» обнуляется: следующий запуск снова ждёт
// двух опросов подряд.
func (r *ReviveRepo) BackToWaiting(routerID int64, lastError string, at time.Time) (bool, error) {
	res, err := r.d.db.Exec(`
UPDATE revive_intents
   SET status = 'waiting', last_error = ?, reachable_probes = 0, reachable_since = NULL, updated_at = ?
 WHERE user_id = ? AND status = 'running'`, lastError, at.UTC(), routerID)
	return affectedOne(res, err)
}

// BackToWaitingNoAttempt -- как BackToWaiting, но дополнительно отменяет
// попытку, которую MarkRunning уже засчитал в БД (attempts = attempts + 1
// -- вплоть до вызова, до которого дело даже не успело толком дойти). Для
// случая, когда запуск отказал не по вине оживления: движок переустановки
// занят чужим заданием на этом же роутере (дашборд уже чинит/ставит) --
// тратить на это одну из пяти попыток нечестно (Fix round 1, Minor #3).
// MAX(attempts-1, 0) -- защита от ухода в минус при гонке/повторном вызове.
func (r *ReviveRepo) BackToWaitingNoAttempt(routerID int64, lastError string, at time.Time) (bool, error) {
	res, err := r.d.db.Exec(`
UPDATE revive_intents
   SET status = 'waiting', last_error = ?, reachable_probes = 0, reachable_since = NULL,
       attempts = MAX(attempts - 1, 0), updated_at = ?
 WHERE user_id = ? AND status = 'running'`, lastError, at.UTC(), routerID)
	return affectedOne(res, err)
}

// Finish переводит намерение из одного из from в конечный статус to и в той
// же транзакции стирает секрет. false -- статус уже не тот, ничего не тронуто.
func (r *ReviveRepo) Finish(routerID int64, from []string, to, lastError string, at time.Time) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("revive: пустой список исходных статусов")
	}
	tx, err := r.d.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	args := []any{to, lastError, at.UTC(), routerID}
	for _, s := range from {
		args = append(args, s)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")
	res, err := tx.Exec(`
UPDATE revive_intents SET status = ?, last_error = ?, updated_at = ?
 WHERE user_id = ? AND status IN (`+placeholders+`)`, args...)
	ok, err := affectedOne(res, err)
	if err != nil || !ok {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM revive_secrets WHERE user_id = ?`, routerID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ResetRunning -- после рестарта бэкенда заданий в памяти нет: все running
// возвращаются в waiting, попытка остаётся засчитанной.
func (r *ReviveRepo) ResetRunning(lastError string, at time.Time) (int64, error) {
	res, err := r.d.db.Exec(`
UPDATE revive_intents
   SET status = 'waiting', last_error = ?, reachable_probes = 0, reachable_since = NULL, updated_at = ?
 WHERE status = 'running'`, lastError, at.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ExpireOverdue -- беспарольный сторож (Fix round 1, Important #2,
// мандатное ревью). Решение оператора «затем стирается» обязано работать,
// даже когда revive.key_file потерян или негоден и Service вообще не
// собран: тут нет расшифровки, только перевод просроченных намерений
// (waiting ИЛИ running -- запись не смотрит, идёт ли ещё сама
// переустановка) в expired и удаление зашифрованного секрета. Строки, чей
// срок ещё не истёк, не трогает -- если ключ вернётся, они доедут своим
// чередом. Один проход -- одна транзакция: список ID, затем по каждому
// UPDATE+DELETE, чтобы DELETE FROM revive_secrets бил точно по тем строкам,
// что действительно истекли этим проходом, а не по формуле, которую было бы
// легко рассинхронизировать с условием UPDATE.
func (r *ReviveRepo) ExpireOverdue(now time.Time, lastError string) (int64, error) {
	tx, err := r.d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`
SELECT user_id FROM revive_intents WHERE status IN ('waiting', 'running') AND expires_at < ?`, now.UTC())
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, tx.Commit()
	}

	for _, id := range ids {
		if _, err := tx.Exec(`
UPDATE revive_intents SET status = 'expired', last_error = ?, updated_at = ?
 WHERE user_id = ? AND status IN ('waiting', 'running')`, lastError, now.UTC(), id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM revive_secrets WHERE user_id = ?`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

func scanReviveIntent(scan func(...any) error) (ReviveIntent, error) {
	var (
		in             ReviveIntent
		probeAt, since sql.NullTime
	)
	err := scan(&in.RouterID, &in.Status, &in.TargetVersion, &in.CreatedAt, &in.UpdatedAt, &in.ExpiresAt,
		&in.Attempts, &in.LastError, &probeAt, &in.LastProbeState, &since, &in.ReachableProbes, &in.RequestedBy)
	if err != nil {
		return ReviveIntent{}, err
	}
	if probeAt.Valid {
		in.LastProbeAt = probeAt.Time.UTC()
	}
	if since.Valid {
		in.ReachableSince = since.Time.UTC()
	}
	in.CreatedAt = in.CreatedAt.UTC()
	in.UpdatedAt = in.UpdatedAt.UTC()
	in.ExpiresAt = in.ExpiresAt.UTC()
	return in, nil
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func affectedOne(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
