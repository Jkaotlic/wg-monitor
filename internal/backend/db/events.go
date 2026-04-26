package db

import (
	"database/sql"
	"errors"
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
	// SQLite stores time.Time as its .String() representation (e.g., "2026-04-26 18:04:05.123456789 +0000 UTC")
	// Try to parse using time.UnixNano if it's a number, otherwise use Go's time format
	ts, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", tsStr.String)
	if err != nil {
		return time.Time{}, err
	}
	return ts, nil
}

func (e *EventsRepo) PruneBefore(cutoff time.Time) (int64, error) {
	res, err := e.d.db.Exec(`DELETE FROM events WHERE ts < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
