// Package alertaction holds the pure state-mutation and formatting logic
// behind the HARD-alert lifecycle actions (silence / ack / mute / history),
// shared by the Telegram callback handlers (internal/backend/callbacks) and
// the mini-app HTTP handlers (internal/backend) so the two surfaces cannot
// drift on suppression semantics or user-facing text.
package alertaction

import (
	"fmt"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// historyMaxTransitions bounds how many status transitions a history view
// shows (the most recent N). Matches the callback History behaviour.
const historyMaxTransitions = 30

// MoscowLoc returns Europe/Moscow, falling back to a fixed +3 zone if the
// tzdata lookup fails.
func MoscowLoc() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("MSK", 3*3600)
	}
	return loc
}

// NextCutoff returns the next hour:00 in loc strictly after now (today if the
// hour is still ahead, else tomorrow).
func NextCutoff(now time.Time, hour int, loc *time.Location) time.Time {
	nowLoc := now.In(loc)
	target := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), hour, 0, 0, 0, loc)
	if !target.After(nowLoc) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// ApplySilence returns st with SilencedUntil set to now+ttl, plus the status line.
func ApplySilence(st db.IncidentState, ttl time.Duration, now time.Time) (db.IncidentState, string) {
	until := now.Add(ttl)
	st.SilencedUntil = &until
	line := fmt.Sprintf("⏸ Уведомления скрыты до %s МСК (админ)", until.In(MoscowLoc()).Format("15:04"))
	return st, line
}

// ApplyAck returns st with Acked set, plus the status line. now is accepted
// for signature symmetry with the other actions (ack has no time component).
func ApplyAck(st db.IncidentState, now time.Time) (db.IncidentState, string) {
	_ = now
	st.Acked = true
	return st, "✅ Отмечено: вижу проблему, напомню после восстановления"
}

// ApplyMute returns st silenced until the next cutoffHour:00 MSK, plus the status line.
func ApplyMute(st db.IncidentState, cutoffHour int, now time.Time) (db.IncidentState, string) {
	loc := MoscowLoc()
	until := NextCutoff(now, cutoffHour, loc)
	untilUTC := until.UTC()
	st.SilencedUntil = &untilUTC
	line := fmt.Sprintf("🔇 Тихий режим до %02d:00 МСК (%s)", cutoffHour, until.Format("02 Jan"))
	return st, line
}

// Transition is one status change in an incident's history.
type Transition struct {
	TS     time.Time
	Status string // "ok" | "fail"
}

// Transitions compresses an event list (ordered by ts ASC) to status-change
// points and returns the most recent historyMaxTransitions of them; the bool
// reports whether older transitions were dropped.
func Transitions(events []db.EventRow) ([]Transition, bool) {
	var out []Transition
	var prev string
	for _, e := range events {
		if e.Status != prev {
			out = append(out, Transition{TS: e.TS, Status: e.Status})
			prev = e.Status
		}
	}
	if len(out) > historyMaxTransitions {
		return out[len(out)-historyMaxTransitions:], true
	}
	return out, false
}
