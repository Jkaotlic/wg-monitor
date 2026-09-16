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

// ErrReviveURLConflict -- у роутера уже записан другой адрес панели.
var ErrReviveURLConflict = errors.New("revive: у роутера другой адрес панели")

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
	// Generation -- растёт на каждый Put/replace (Fix round 2, мандатное
	// ревью). Писать через MarkRunning/RecordProbe/BackToWaiting(+NoAttempt)/
	// Finish можно только с ЭТИМ значением (условие AND generation = ? в
	// каждом из них): если Schedule успел переставить намерение между чтением
	// (Get) и записью воркера, поколение уже другое, запись не находит строку
	// и тихо ничего не делает -- вместо того чтобы закрыть или переписать
	// чужую, свежую попытку под старым снимком.
	Generation int64
}

// ReviveRepo -- намерения оживления и их секреты.
type ReviveRepo struct{ d *DB }

func (d *DB) Revive() *ReviveRepo { return &ReviveRepo{d: d} }

const reviveColumns = `user_id, status, target_version, created_at, updated_at, expires_at, attempts,
	last_error, last_probe_at, last_probe_state, reachable_since, reachable_probes, requested_by, generation`

// Put ставит намерение с нуля (waiting, попыток 0, опросов нет) и кладёт
// секрет -- одной транзакцией. Поверх running -- ErrReviveRunning, и секрет
// не трогается. generation растёт на каждый успешный Put (и на первую
// INSERT она 0) -- это и есть точка, где старое поколение перестаёт быть
// действительным для условных записей воркера.
func (r *ReviveRepo) Put(in ReviveIntent, nonce, ciphertext []byte) error {
	return r.put(in, nonce, ciphertext, "")
}

// PutSettingURL -- Put и запись адреса панели одной транзакцией (финальное
// ревью 15.09, конкурентная постановка на роутере без адреса). Адрес
// записывается, если у роутера его нет; тот же адрес -- не конфликт; другой
// -- ErrReviveURLConflict, и ни намерение, ни секрет, ни адрес не меняются.
func (r *ReviveRepo) PutSettingURL(in ReviveIntent, nonce, ciphertext []byte, awgmURL string) error {
	return r.put(in, nonce, ciphertext, awgmURL)
}

func (r *ReviveRepo) put(in ReviveIntent, nonce, ciphertext []byte, awgmURL string) error {
	tx, err := r.d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if awgmURL != "" {
		res, err := tx.Exec(
			`UPDATE users SET awgm_url = ? WHERE id = ? AND (awgm_url IS NULL OR TRIM(awgm_url) = '')`,
			awgmURL, in.RouterID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var stored sql.NullString
			if err := tx.QueryRow(`SELECT awgm_url FROM users WHERE id = ?`, in.RouterID).Scan(&stored); err != nil {
				return err
			}
			if strings.TrimSpace(stored.String) != awgmURL {
				return ErrReviveURLConflict
			}
		}
	}

	res, err := tx.Exec(`
INSERT INTO revive_intents (user_id, status, target_version, created_at, updated_at, expires_at,
                            attempts, last_error, last_probe_at, last_probe_state,
                            reachable_since, reachable_probes, requested_by, generation)
VALUES (?, 'waiting', ?, ?, ?, ?, 0, '', NULL, '', NULL, 0, ?, 0)
ON CONFLICT(user_id) DO UPDATE SET
    status = 'waiting', target_version = excluded.target_version,
    created_at = excluded.created_at, updated_at = excluded.updated_at,
    expires_at = excluded.expires_at, attempts = 0, last_error = '',
    last_probe_at = NULL, last_probe_state = '', reachable_since = NULL,
    reachable_probes = 0, requested_by = excluded.requested_by,
    generation = revive_intents.generation + 1
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

// RecordProbe записывает итог опроса. Только для waiting и ровно того
// поколения, что видел вызывающий на своём Get (Fix round 2, мандатное
// ревью): опрос, вернувшийся после того, как Schedule успел переставить
// намерение, не имеет права переписать чужую, уже другую строку статистикой
// от старой.
func (r *ReviveRepo) RecordProbe(routerID int64, at time.Time, state string, reachableProbes int, reachableSince time.Time, generation int64) error {
	_, err := r.d.db.Exec(`
UPDATE revive_intents
   SET last_probe_at = ?, last_probe_state = ?, reachable_probes = ?, reachable_since = ?, updated_at = ?
 WHERE user_id = ? AND status = 'waiting' AND generation = ?`,
		at.UTC(), state, reachableProbes, nullableTime(reachableSince), at.UTC(), routerID, generation)
	return err
}

// MarkRunning -- waiting→running с засчитанной попыткой. false -- кто-то
// успел раньше (второй запуск на тот же роутер не пройдёт), ИЛИ поколение
// уже другое (Schedule переставил намерение между Get воркера и этим
// вызовом, Fix round 2, мандатное ревью) -- в обоих случаях запускать
// нечего, вызывающий обязан молча отступить.
func (r *ReviveRepo) MarkRunning(routerID int64, at time.Time, generation int64) (bool, error) {
	res, err := r.d.db.Exec(`
UPDATE revive_intents SET status = 'running', attempts = attempts + 1, updated_at = ?
 WHERE user_id = ? AND status = 'waiting' AND generation = ?`, at.UTC(), routerID, generation)
	return affectedOne(res, err)
}

// BackToWaiting -- running→waiting после неудачной, но не окончательной
// попытки. Серия «панель отвечает» обнуляется: следующий запуск снова ждёт
// двух опросов подряд. generation -- то же поколение, что видел вызывающий
// на своём Get (Fix round 2, мандатное ревью); MarkRunning его не меняет,
// так что это то же число, что было при запуске этой попытки.
func (r *ReviveRepo) BackToWaiting(routerID int64, lastError string, at time.Time, generation int64) (bool, error) {
	res, err := r.d.db.Exec(`
UPDATE revive_intents
   SET status = 'waiting', last_error = ?, reachable_probes = 0, reachable_since = NULL, updated_at = ?
 WHERE user_id = ? AND status = 'running' AND generation = ?`, lastError, at.UTC(), routerID, generation)
	return affectedOne(res, err)
}

// BackToWaitingNoAttempt -- как BackToWaiting, но дополнительно отменяет
// попытку, которую MarkRunning уже засчитал в БД (attempts = attempts + 1
// -- вплоть до вызова, до которого дело даже не успело толком дойти). Для
// случая, когда запуск отказал не по вине оживления: движок переустановки
// занят чужим заданием на этом же роутере (дашборд уже чинит/ставит) --
// тратить на это одну из пяти попыток нечестно (Fix round 1, Minor #3).
// MAX(attempts-1, 0) -- защита от ухода в минус при гонке/повторном вызове.
func (r *ReviveRepo) BackToWaitingNoAttempt(routerID int64, lastError string, at time.Time, generation int64) (bool, error) {
	res, err := r.d.db.Exec(`
UPDATE revive_intents
   SET status = 'waiting', last_error = ?, reachable_probes = 0, reachable_since = NULL,
       attempts = MAX(attempts - 1, 0), updated_at = ?
 WHERE user_id = ? AND status = 'running' AND generation = ?`, lastError, at.UTC(), routerID, generation)
	return affectedOne(res, err)
}

// Finish переводит намерение из одного из from в конечный статус to и в той
// же транзакции стирает секрет. false -- статус уже не тот или поколение уже
// другое (Fix round 2, мандатное ревью), ничего не тронуто.
//
// Поколение -- структурная защита вместо защиты временем удержания замка
// воркера: без неё Schedule, успевший переставить намерение в щель между
// Get воркера и этим вызовом (например, checkOne уже отпустила s.work перед
// ранним выходом finish -- expired/agentFresh/maxAttempts/no-url), создал
// бы СВЕЖУЮ строку с тем же статусом ('waiting'), и это Finish закрыл бы её
// (и стёр её НОВЫЙ секрет) как будто это была та, старая строка.
func (r *ReviveRepo) Finish(routerID int64, from []string, to, lastError string, at time.Time, generation int64) (bool, error) {
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
	args = append(args, generation)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")
	res, err := tx.Exec(`
UPDATE revive_intents SET status = ?, last_error = ?, updated_at = ?
 WHERE user_id = ? AND status IN (`+placeholders+`) AND generation = ?`, args...)
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
		&in.Attempts, &in.LastError, &probeAt, &in.LastProbeState, &since, &in.ReachableProbes, &in.RequestedBy,
		&in.Generation)
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
