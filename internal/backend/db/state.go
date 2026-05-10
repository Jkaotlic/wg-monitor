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
	Acked            bool
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
		`SELECT consecutive_fails, consecutive_oks, current_status, hard_since, last_alert_msg_id, last_alert_at, silenced_until, acked_until, acked
		   FROM incident_state WHERE user_id = ? AND check_name = ?`,
		userID, checkName,
	)
	var hardSince, lastAlertAt, silenced, ackedUntil sql.NullTime
	var lastMsgID sql.NullInt64
	var ackedFlag int
	err := row.Scan(&got.ConsecutiveFails, &got.ConsecutiveOKs, &got.CurrentStatus,
		&hardSince, &lastMsgID, &lastAlertAt, &silenced, &ackedUntil, &ackedFlag)
	if errors.Is(err, sql.ErrNoRows) {
		return got, nil
	}
	if err != nil {
		return got, err
	}
	got.HardSince = nullTime(hardSince)
	got.LastAlertAt = nullTime(lastAlertAt)
	got.SilencedUntil = nullTime(silenced)
	got.AckedUntil = nullTime(ackedUntil)
	if lastMsgID.Valid {
		v := lastMsgID.Int64
		got.LastAlertMsgID = &v
	}
	got.Acked = ackedFlag == 1
	return got, nil
}

func (s *StateRepo) Save(userID int64, checkName string, st IncidentState) error {
	ackedInt := 0
	if st.Acked {
		ackedInt = 1
	}
	_, err := s.d.db.Exec(
		`INSERT INTO incident_state(user_id, check_name, consecutive_fails, consecutive_oks, current_status,
		    hard_since, last_alert_msg_id, last_alert_at, silenced_until, acked_until, acked)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(user_id, check_name) DO UPDATE SET
		    consecutive_fails = excluded.consecutive_fails,
		    consecutive_oks   = excluded.consecutive_oks,
		    current_status    = excluded.current_status,
		    hard_since        = excluded.hard_since,
		    last_alert_msg_id = excluded.last_alert_msg_id,
		    last_alert_at     = excluded.last_alert_at,
		    silenced_until    = excluded.silenced_until,
		    acked_until       = excluded.acked_until,
		    acked             = excluded.acked`,
		userID, checkName, st.ConsecutiveFails, st.ConsecutiveOKs, st.CurrentStatus,
		utcPtr(st.HardSince), st.LastAlertMsgID, utcPtr(st.LastAlertAt),
		utcPtr(st.SilencedUntil), utcPtr(st.AckedUntil), ackedInt,
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

// BumpLastAlertAt updates only the last_alert_at timestamp for an existing HARD incident.
// No-op if the incident has been recovered (current_status != 'hard').
// Use this from the realert poller to avoid race-overwriting an FSM-Recovery that
// happened between StaleHards() and the poller's read-modify-write.
func (s *StateRepo) BumpLastAlertAt(userID int64, checkName string, ts time.Time) error {
	_, err := s.d.db.Exec(
		`UPDATE incident_state SET last_alert_at = ?
		 WHERE user_id = ? AND check_name = ? AND current_status = 'hard'`,
		ts.UTC(), userID, checkName)
	return err
}

// ActiveIncidentRow is one currently-HARD incident as seen by Fleet-Health.
// Includes silenced/acked rows so the operator sees the full picture; UI may
// filter or annotate them later.
type ActiveIncidentRow struct {
	UserID    int64
	CheckName string
	HardSince time.Time
	FailCount int
}

// AllActiveHard returns every incident_state row with current_status='hard',
// regardless of silence/ack — used by the operator-only Fleet-Health reply.
func (s *StateRepo) AllActiveHard() ([]ActiveIncidentRow, error) {
	rows, err := s.d.db.Query(
		`SELECT user_id, check_name, hard_since, consecutive_fails
		   FROM incident_state
		  WHERE current_status = 'hard'
		  ORDER BY hard_since`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveIncidentRow
	for rows.Next() {
		var r ActiveIncidentRow
		var hs sql.NullTime
		if err := rows.Scan(&r.UserID, &r.CheckName, &hs, &r.FailCount); err != nil {
			return nil, err
		}
		if hs.Valid {
			r.HardSince = hs.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveHardForUser returns hard, non-silenced, non-acked incidents for a
// single user — feeds the smart-reply panel. Mirrors StaleHards' silenced_until
// handling: pass time.Now().UTC() so SQL serialisation matches the column
// format (see StaleHards comment for the lexicographic-compare gotcha).
func (s *StateRepo) ActiveHardForUser(userID int64, now time.Time) ([]ActiveIncidentRow, error) {
	cutoff := now.UTC()
	rows, err := s.d.db.Query(
		`SELECT user_id, check_name, hard_since, consecutive_fails
		   FROM incident_state
		  WHERE user_id = ? AND current_status = 'hard'
		    AND (silenced_until IS NULL OR silenced_until < ?)
		    AND acked = 0
		  ORDER BY hard_since`,
		userID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveIncidentRow
	for rows.Next() {
		var r ActiveIncidentRow
		var hs sql.NullTime
		if err := rows.Scan(&r.UserID, &r.CheckName, &hs, &r.FailCount); err != nil {
			return nil, err
		}
		if hs.Valid {
			r.HardSince = hs.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StaleHards returns hard incidents whose last_alert_at is older than `cutoff`
// and which are not currently silenced, and have not been acked.
//
// silenced_until is compared against the SAME `cutoff` parameter rather than
// SQL CURRENT_TIMESTAMP. modernc.org/sqlite serialises Go time.Time as
// `2026-05-10T14:30:00Z` (RFC3339, capital T) while SQL CURRENT_TIMESTAMP
// returns `2026-05-10 14:30:00` (space). Lexicographic comparison treats
// 'T' (0x54) > ' ' (0x20), so `silenced_until < CURRENT_TIMESTAMP` was
// always false → silenced incidents NEVER re-surface after the silence
// window expires. Bug surfaced in 2026-05 audit (BUG-18). Using a Go-side
// parameter via database/sql gives consistent serialisation.
func (s *StateRepo) StaleHards(cutoff time.Time) ([]StaleHard, error) {
	now := cutoff.UTC()
	rows, err := s.d.db.Query(
		`SELECT user_id, check_name, hard_since FROM incident_state
		 WHERE current_status = 'hard'
		   AND last_alert_at < ?
		   AND (silenced_until IS NULL OR silenced_until < ?)
		   AND acked = 0`,
		now, now)
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

func utcPtr(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return v
}
