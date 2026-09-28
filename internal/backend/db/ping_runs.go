package db

import (
	"fmt"
	"time"
)

// PingRunRow -- серия неудач пингчека awg-manager (wire.PingRun в базе).
type PingRunRow struct {
	TunnelID   string
	TunnelName string
	From       time.Time
	To         time.Time
	Fails      int
	WentDown   bool
	Recovered  bool
	Error      string
}

// UpsertPingRunSQL -- идемпотентная запись серии. Повтор той же серии
// (агент перечитал буфер после перезапуска) не плодит строк, а запоздалая
// копия открытой серии не откатывает закрытую: всё растёт только через MAX.
// Экспортирован, чтобы приём отчёта писал серии в своей транзакции.
const UpsertPingRunSQL = `
INSERT INTO awgm_ping_runs(user_id, tunnel_id, tunnel_name, from_ts, to_ts, fails, went_down, recovered, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user_id, tunnel_id, from_ts) DO UPDATE SET
  tunnel_name = CASE WHEN excluded.tunnel_name != '' THEN excluded.tunnel_name ELSE awgm_ping_runs.tunnel_name END,
  to_ts       = MAX(awgm_ping_runs.to_ts, excluded.to_ts),
  fails       = MAX(awgm_ping_runs.fails, excluded.fails),
  went_down   = MAX(awgm_ping_runs.went_down, excluded.went_down),
  recovered   = MAX(awgm_ping_runs.recovered, excluded.recovered),
  error       = CASE WHEN excluded.error != '' THEN excluded.error ELSE awgm_ping_runs.error END`

func pingRunFlag(b bool) int {
	if b {
		return 1
	}
	return 0
}

// PingRunArgs -- аргументы UpsertPingRunSQL в его порядке.
func PingRunArgs(userID int64, r PingRunRow) []any {
	return []any{userID, r.TunnelID, r.TunnelName, factTS(r.From), factTS(r.To), r.Fails,
		pingRunFlag(r.WentDown), pingRunFlag(r.Recovered), r.Error}
}

// PingRunsRepo -- серии пингчека по роутерам.
type PingRunsRepo struct{ d *DB }

func (d *DB) PingRuns() *PingRunsRepo { return &PingRunsRepo{d: d} }

func (r *PingRunsRepo) Upsert(userID int64, run PingRunRow) error {
	_, err := r.d.db.Exec(UpsertPingRunSQL, PingRunArgs(userID, run)...)
	return err
}

// pingRunsSinceLimit -- потолок выборки: неделя флаппинга -- сотни серий.
const pingRunsSinceLimit = 2000

// Since -- серии, закончившиеся не раньше since, по времени начала, по
// возрастанию. Строк больше лимита -- отдаём САМЫЕ НОВЫЕ (ORDER BY from_ts
// DESC ... LIMIT в SQL), а не первые попавшиеся под срез: иначе долгий
// флаппинг похоронил бы под лимитом именно последние, самые нужные серии.
// Разворот в возрастающий порядок делаем в Go после LIMIT.
func (r *PingRunsRepo) Since(userID int64, since time.Time) ([]PingRunRow, error) {
	rows, err := r.d.db.Query(`
SELECT tunnel_id, tunnel_name, from_ts, to_ts, fails, went_down, recovered, error
  FROM awgm_ping_runs
 WHERE user_id = ? AND to_ts >= ?
 ORDER BY from_ts DESC
 LIMIT ?`, userID, factTS(since), pingRunsSinceLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PingRunRow
	for rows.Next() {
		var p PingRunRow
		var from, to string
		var down, rec int
		if err := rows.Scan(&p.TunnelID, &p.TunnelName, &from, &to, &p.Fails, &down, &rec, &p.Error); err != nil {
			return nil, err
		}
		if p.From, err = parseFactTS(from); err != nil {
			return nil, fmt.Errorf("awgm_ping_runs.from_ts: %w", err)
		}
		if p.To, err = parseFactTS(to); err != nil {
			return nil, fmt.Errorf("awgm_ping_runs.to_ts: %w", err)
		}
		p.WentDown, p.Recovered = down == 1, rec == 1
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Разворот DESC -> ASC: срез набран по новизне, но потребителям (экран,
	// прунинг) удобен возрастающий порядок, как раньше.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// PruneBefore удаляет серии, закончившиеся раньше cutoff.
func (r *PingRunsRepo) PruneBefore(cutoff time.Time) (int64, error) {
	res, err := r.d.db.Exec(`DELETE FROM awgm_ping_runs WHERE to_ts < ?`, factTS(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
