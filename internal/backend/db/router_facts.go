package db

import (
	"fmt"
	"time"
)

// Виды фактов роутера (v0.47). Совпадают с ключами wire.ReportFacts.
const (
	FactExit      = "exit"
	FactWAN       = "wan"
	FactNativeDNS = "native_dns"
	FactHooks     = "hooks"
	FactPingLog   = "ping_log"
)

// FactTSLayout -- фиксированная ширина: строки времени сравниваются как текст
// (MAX в upsert серий, выборка по to_ts). Всегда в UTC.
const FactTSLayout = "2006-01-02T15:04:05.000Z"

func factTS(t time.Time) string { return t.UTC().Format(FactTSLayout) }

func parseFactTS(s string) (time.Time, error) { return time.Parse(FactTSLayout, s) }

// RouterFact -- последний присланный блок одного вида.
type RouterFact struct {
	Kind       string
	Body       []byte
	At         time.Time
	ReceivedAt time.Time
}

// RouterFactsRepo -- факты роутера, строка на роутер и вид.
type RouterFactsRepo struct{ d *DB }

func (d *DB) RouterFacts() *RouterFactsRepo { return &RouterFactsRepo{d: d} }

// Upsert заменяет блок целиком: слияния полей нет намеренно.
func (r *RouterFactsRepo) Upsert(userID int64, kind string, body []byte, at, receivedAt time.Time) error {
	_, err := r.d.db.Exec(`
INSERT INTO router_facts(user_id, kind, body, at, received_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id, kind) DO UPDATE SET
  body = excluded.body, at = excluded.at, received_at = excluded.received_at`,
		userID, kind, string(body), factTS(at), factTS(receivedAt))
	return err
}

// All -- все блоки роутера по виду. Нет строк -- пустая карта, не ошибка.
func (r *RouterFactsRepo) All(userID int64) (map[string]RouterFact, error) {
	rows, err := r.d.db.Query(`SELECT kind, body, at, received_at FROM router_facts WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]RouterFact{}
	for rows.Next() {
		var f RouterFact
		var body, at, rcv string
		if err := rows.Scan(&f.Kind, &body, &at, &rcv); err != nil {
			return nil, err
		}
		f.Body = []byte(body)
		if f.At, err = parseFactTS(at); err != nil {
			return nil, fmt.Errorf("router_facts.at: %w", err)
		}
		if f.ReceivedAt, err = parseFactTS(rcv); err != nil {
			return nil, fmt.Errorf("router_facts.received_at: %w", err)
		}
		out[f.Kind] = f
	}
	return out, rows.Err()
}
