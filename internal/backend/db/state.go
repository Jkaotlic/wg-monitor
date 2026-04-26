package db

import (
	"database/sql"
	"errors"
	"time"
)

type IncidentState struct {
	UserID           int64
	CheckName        string
	ConsecutiveFails int
	ConsecutiveOKs   int
	CurrentStatus    string // ok | fail | hard
	HardSince        *time.Time
	LastAlertMsgID   *int64
	LastAlertAt      *time.Time
	SilencedUntil    *time.Time
	AckedUntil       *time.Time
}

type StaleHard struct {
	UserID    int64
	CheckName string
	HardSince time.Time
}

type StateRepo struct{ d *DB }

func (d *DB) State() *StateRepo { return &StateRepo{d: d} }

func (s *StateRepo) Get(userID int64, checkName string) (IncidentState, error) {
	var got IncidentState
	got.UserID = userID
	got.CheckName = checkName
	got.CurrentStatus = "ok"

	row := s.d.db.QueryRow(
		`SELECT consecutive_fails, consecutive_oks, current_status, hard_since, last_alert_msg_id, last_alert_at, silenced_until, acked_until
		   FROM incident_state WHERE user_id = ? AND check_name = ?`,
		userID, checkName,
	)
	var hardSince, lastAlertAt, silenced, acked sql.NullTime
	var lastMsgID sql.NullInt64
	err := row.Scan(&got.ConsecutiveFails, &got.ConsecutiveOKs, &got.CurrentStatus,
		&hardSince, &lastMsgID, &lastAlertAt, &silenced, &acked)
	if errors.Is(err, sql.ErrNoRows) {
		return got, nil
	}
	if err != nil {
		return got, err
	}
	got.HardSince = nullTime(hardSince)
	got.LastAlertAt = nullTime(lastAlertAt)
	got.SilencedUntil = nullTime(silenced)
	got.AckedUntil = nullTime(acked)
	if lastMsgID.Valid {
		v := lastMsgID.Int64
		got.LastAlertMsgID = &v
	}
	return got, nil
}

func (s *StateRepo) Save(userID int64, checkName string, st IncidentState) error {
	_, err := s.d.db.Exec(
		`INSERT INTO incident_state(user_id, check_name, consecutive_fails, consecutive_oks, current_status,
		    hard_since, last_alert_msg_id, last_alert_at, silenced_until, acked_until)
		 VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(user_id, check_name) DO UPDATE SET
		    consecutive_fails = excluded.consecutive_fails,
		    consecutive_oks   = excluded.consecutive_oks,
		    current_status    = excluded.current_status,
		    hard_since        = excluded.hard_since,
		    last_alert_msg_id = excluded.last_alert_msg_id,
		    last_alert_at     = excluded.last_alert_at,
		    silenced_until    = excluded.silenced_until,
		    acked_until       = excluded.acked_until`,
		userID, checkName, st.ConsecutiveFails, st.ConsecutiveOKs, st.CurrentStatus,
		st.HardSince, st.LastAlertMsgID, st.LastAlertAt, st.SilencedUntil, st.AckedUntil,
	)
	return err
}

func (s *StateRepo) IncSoftFlap(userID int64, checkName, date string) error {
	_, err := s.d.db.Exec(
		`INSERT INTO daily_soft_flaps(user_id, check_name, date, flap_count) VALUES (?,?,?,1)
		 ON CONFLICT(user_id, check_name, date) DO UPDATE SET flap_count = flap_count + 1`,
		userID, checkName, date,
	)
	return err
}

func (s *StateRepo) GetSoftFlap(userID int64, checkName, date string) (int, error) {
	var n int
	err := s.d.db.QueryRow(
		`SELECT flap_count FROM daily_soft_flaps WHERE user_id=? AND check_name=? AND date=?`,
		userID, checkName, date).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// StaleHards returns hard incidents whose last_alert_at is older than `cutoff`
// and which are not currently silenced.
func (s *StateRepo) StaleHards(cutoff time.Time) ([]StaleHard, error) {
	rows, err := s.d.db.Query(
		`SELECT user_id, check_name, hard_since FROM incident_state
		 WHERE current_status = 'hard'
		   AND last_alert_at < ?
		   AND (silenced_until IS NULL OR silenced_until < CURRENT_TIMESTAMP)`,
		cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaleHard
	for rows.Next() {
		var sh StaleHard
		if err := rows.Scan(&sh.UserID, &sh.CheckName, &sh.HardSince); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

func nullTime(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	v := n.Time
	return &v
}
