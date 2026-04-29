package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EventsRepo struct{ d *DB }

func (d *DB) Events() *EventsRepo { return &EventsRepo{d: d} }

func (e *EventsRepo) Insert(userID int64, checkName, status, detailsJSON string, ts time.Time) error {
	_, err := e.d.db.Exec(
		`INSERT INTO events(user_id, check_name, status, details_json, ts) VALUES (?, ?, ?, ?, ?)`,
		userID, checkName, status, detailsJSON, ts.UTC(),
	)
	return err
}

// LatestPerUser returns the timestamp of the most recent event across all checks for one user.
// Returns zero time and nil error if the user has no events yet.
func (e *EventsRepo) LatestPerUser(userID int64) (time.Time, error) {
	var tsStr sql.NullString
	err := e.d.db.QueryRow(`SELECT MAX(ts) FROM events WHERE user_id = ?`, userID).Scan(&tsStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if !tsStr.Valid {
		return time.Time{}, nil
	}
	// modernc.org/sqlite returns MAX() result as a raw string (no declared column type → no driver time conversion).
	s := tsStr.String
	if i := strings.Index(s, " m="); i > 0 {
		s = s[:i]
	}
	ts, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", s)
	if err != nil {
		return time.Time{}, err
	}
	return ts, nil
}

// LatestEventsByPrefix returns the most recent event for each distinct
// check_name starting with `prefix`, for a single user. Used by the
// alerts formatter to render "neighbouring tunnels" context next to a
// failing tunnel — we want the latest known status of each sibling
// without doing a full per-name query in Go.
func (e *EventsRepo) LatestEventsByPrefix(userID int64, prefix string) ([]EventRow, error) {
	rows, err := e.d.db.Query(
		`SELECT e1.id, e1.user_id, e1.check_name, e1.status, e1.details_json, e1.ts
		   FROM events e1
		  WHERE e1.user_id = ? AND e1.check_name LIKE ?
		    AND e1.ts = (
		        SELECT MAX(e2.ts) FROM events e2
		         WHERE e2.user_id = e1.user_id AND e2.check_name = e1.check_name
		    )`,
		userID, prefix+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var r EventRow
		var tsStr string
		if err := rows.Scan(&r.ID, &r.UserID, &r.CheckName, &r.Status, &r.DetailsJSON, &tsStr); err != nil {
			return nil, err
		}
		t, err := parseEventTS(tsStr)
		if err != nil {
			return nil, err
		}
		r.TS = t
		out = append(out, r)
	}
	return out, rows.Err()
}

func (e *EventsRepo) PruneBefore(cutoff time.Time) (int64, error) {
	res, err := e.d.db.Exec(`DELETE FROM events WHERE ts < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// EventRow holds a single event from the events table.
type EventRow struct {
	ID          int64
	UserID      int64
	CheckName   string
	Status      string
	DetailsJSON string
	TS          time.Time
}

// ListSince returns all events for (userID, checkName) with ts >= since, ordered by ts ASC.
func (e *EventsRepo) ListSince(userID int64, checkName string, since time.Time) ([]EventRow, error) {
	rows, err := e.d.db.Query(
		`SELECT id, user_id, check_name, status, details_json, ts
		   FROM events
		  WHERE user_id = ? AND check_name = ? AND ts >= ?
		  ORDER BY ts ASC`,
		userID, checkName, since.UTC(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var r EventRow
		var tsStr string
		if err := rows.Scan(&r.ID, &r.UserID, &r.CheckName, &r.Status, &r.DetailsJSON, &tsStr); err != nil {
			return nil, err
		}
		// Parse timestamp stored by SQLite (UTC RFC3339 or Go time string).
		t, err := parseEventTS(tsStr)
		if err != nil {
			return nil, err
		}
		r.TS = t
		out = append(out, r)
	}
	return out, rows.Err()
}

// parseEventTS handles both "2006-01-02T15:04:05Z07:00" (RFC3339) and
// "2006-01-02 15:04:05.999999999 -0700 MST" (Go time.String) formats.
func parseEventTS(s string) (time.Time, error) {
	// Strip Go's monotonic clock suffix if present.
	if i := strings.Index(s, " m="); i > 0 {
		s = s[:i]
	}
	// Try RFC3339 / RFC3339Nano first (standard SQLite DATETIME stored via time.UTC()).
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02T15:04:05.999999999Z07:00",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("parseEventTS: unrecognised format %q", s)
}
