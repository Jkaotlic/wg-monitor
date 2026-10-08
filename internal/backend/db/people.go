package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Откуда пришло имя человека в справочнике telegram_people.
const (
	PersonSourceMiniapp  = "miniapp"  // вход в мини-апп, initData
	PersonSourceBot      = "bot"      // личное сообщение или кнопка боту
	PersonSourceTelegram = "telegram" // дотянуто getChat Bot API
)

// PersonSeenThrottle -- не чаще этого last_seen_at пишется в базу: вход в
// мини-апп и сообщения боту случаются на каждом шагу, а «был сегодня»
// точнее десяти минут не нужен.
const PersonSeenThrottle = 10 * time.Minute

// Person -- строка справочника людей. Имена -- данные Telegram как есть,
// не разметка.
type Person struct {
	TelegramUserID int64
	FirstName      string
	LastName       string
	Username       string
	Source         string
	FirstSeenAt    time.Time
	LastSeenAt     *time.Time // nil -- не видели (имя дотянуто getChat)
	NameCheckedAt  *time.Time // последняя попытка getChat
}

type PeopleRepo struct{ d *DB }

// People -- справочник людей Telegram (v0.58).
func (d *DB) People() *PeopleRepo { return &PeopleRepo{d: d} }

func peopleTS(t time.Time) string { return t.UTC().Format(FactTSLayout) }

// Seen -- человек показался (вход в мини-апп, сообщение боту). Имя и ник
// обновляются свежими сразу; last_seen_at -- не чаще PersonSeenThrottle.
// Если имена те же и last_seen_at свежий, строка не трогается вовсе: условие
// стоит в самом UPSERT, чтобы горячий путь не писал в базу на каждый запрос.
// Номер 0 и отрицательные -- не люди, молча пропускаются.
func (r *PeopleRepo) Seen(telegramUserID int64, firstName, lastName, username, source string, now time.Time) error {
	if telegramUserID <= 0 {
		return nil
	}
	ts := peopleTS(now)
	_, err := r.d.db.Exec(
		`INSERT INTO telegram_people(telegram_user_id, first_name, last_name, username, first_seen_at, last_seen_at, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(telegram_user_id) DO UPDATE SET
		     first_name = excluded.first_name,
		     last_name = excluded.last_name,
		     username = excluded.username,
		     source = excluded.source,
		     last_seen_at = excluded.last_seen_at
		 WHERE telegram_people.last_seen_at IS NULL
		    OR telegram_people.last_seen_at < ?
		    OR telegram_people.first_name <> excluded.first_name
		    OR telegram_people.last_name <> excluded.last_name
		    OR telegram_people.username <> excluded.username`,
		telegramUserID, firstName, lastName, username, ts, ts, source,
		peopleTS(now.Add(-PersonSeenThrottle)),
	)
	if err != nil {
		return fmt.Errorf("telegram_people.Seen: %w", err)
	}
	return nil
}

// SetNameFromTelegram -- имя дотянуто getChat. last_seen_at не трогается:
// ответ Telegram не значит, что человек сам приходил.
func (r *PeopleRepo) SetNameFromTelegram(telegramUserID int64, firstName, lastName, username string, now time.Time) error {
	ts := peopleTS(now)
	_, err := r.d.db.Exec(
		`INSERT INTO telegram_people(telegram_user_id, first_name, last_name, username, first_seen_at, source, name_checked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(telegram_user_id) DO UPDATE SET
		     first_name = excluded.first_name,
		     last_name = excluded.last_name,
		     username = excluded.username,
		     source = excluded.source,
		     name_checked_at = excluded.name_checked_at`,
		telegramUserID, firstName, lastName, username, ts, PersonSourceTelegram, ts,
	)
	if err != nil {
		return fmt.Errorf("telegram_people.SetNameFromTelegram: %w", err)
	}
	return nil
}

// MarkNameChecked -- попытка getChat не дала имени (бот не знает чат).
// Отметка держит повтор для этого номера не чаще раза в сутки; имеющиеся
// имена не трогаются.
func (r *PeopleRepo) MarkNameChecked(telegramUserID int64, now time.Time) error {
	ts := peopleTS(now)
	_, err := r.d.db.Exec(
		`INSERT INTO telegram_people(telegram_user_id, first_seen_at, source, name_checked_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(telegram_user_id) DO UPDATE SET name_checked_at = excluded.name_checked_at`,
		telegramUserID, ts, PersonSourceTelegram, ts,
	)
	if err != nil {
		return fmt.Errorf("telegram_people.MarkNameChecked: %w", err)
	}
	return nil
}

// List -- весь справочник по номеру. Людей единицы-десятки: читается целиком.
func (r *PeopleRepo) List() ([]Person, error) {
	rows, err := r.d.db.Query(
		`SELECT telegram_user_id, first_name, last_name, username, source, first_seen_at, last_seen_at, name_checked_at
		 FROM telegram_people ORDER BY telegram_user_id`)
	if err != nil {
		return nil, fmt.Errorf("telegram_people.List: %w", err)
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		var firstSeen string
		var lastSeen, checked sql.NullString
		if err := rows.Scan(&p.TelegramUserID, &p.FirstName, &p.LastName, &p.Username, &p.Source, &firstSeen, &lastSeen, &checked); err != nil {
			return nil, fmt.Errorf("telegram_people.List scan: %w", err)
		}
		if t, err := time.Parse(FactTSLayout, firstSeen); err == nil {
			p.FirstSeenAt = t
		}
		p.LastSeenAt = parsePeopleTS(lastSeen)
		p.NameCheckedAt = parsePeopleTS(checked)
		out = append(out, p)
	}
	return out, rows.Err()
}

func parsePeopleTS(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(FactTSLayout, s.String)
	if err != nil {
		return nil
	}
	return &t
}
