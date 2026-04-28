// Package state implements the alert FSM described in spec §5.3.
// Pure: no I/O, no time.Now() — caller passes `now`. This makes the FSM
// trivially testable across millions of synthetic transitions.
package state

import (
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type Thresholds struct {
	Fail     int // consecutive fails to harden — spec default 3
	Recovery int // consecutive oks while hard to recover — spec default 2
}

type Kind int

const (
	Noop     Kind = iota // nothing observable to the operator
	Soft                 // failure but below threshold (just bumps counter)
	SoftFlap             // recovered before hard — increments daily_soft_flaps
	Hard                 // crossed threshold; HARD alert must be sent
	Recovery             // 2nd consecutive OK after hard; RECOVERY alert must be sent
)

func (k Kind) String() string {
	return [...]string{"noop", "soft", "soft_flap", "hard", "recovery"}[k]
}

type Transition struct {
	Kind Kind
	Next db.IncidentState
}

func Apply(prev db.IncidentState, incoming string, now time.Time, th Thresholds) Transition {
	next := prev
	switch {
	case prev.CurrentStatus == "ok" && incoming == "ok":
		next.ConsecutiveOKs = prev.ConsecutiveOKs + 1
		return Transition{Kind: Noop, Next: next}

	case prev.CurrentStatus == "ok" && incoming == "fail":
		next.ConsecutiveFails = 1
		next.ConsecutiveOKs = 0
		next.CurrentStatus = "fail"
		return Transition{Kind: Soft, Next: next}

	case prev.CurrentStatus == "fail" && incoming == "fail":
		next.ConsecutiveFails = prev.ConsecutiveFails + 1
		next.ConsecutiveOKs = 0
		if next.ConsecutiveFails >= th.Fail {
			next.CurrentStatus = "hard"
			t := now
			next.HardSince = &t
			next.LastAlertAt = &t
			return Transition{Kind: Hard, Next: next}
		}
		return Transition{Kind: Soft, Next: next}

	case prev.CurrentStatus == "fail" && incoming == "ok":
		next.ConsecutiveFails = 0
		next.ConsecutiveOKs = 1
		next.CurrentStatus = "ok"
		return Transition{Kind: SoftFlap, Next: next}

	case prev.CurrentStatus == "hard" && incoming == "fail":
		next.ConsecutiveOKs = 0
		next.ConsecutiveFails = prev.ConsecutiveFails + 1
		return Transition{Kind: Noop, Next: next}

	case prev.CurrentStatus == "hard" && incoming == "ok":
		next.ConsecutiveOKs = prev.ConsecutiveOKs + 1
		if next.ConsecutiveOKs >= th.Recovery {
			next.CurrentStatus = "ok"
			next.ConsecutiveFails = 0
			next.HardSince = nil
			next.Acked = false
			return Transition{Kind: Recovery, Next: next}
		}
		return Transition{Kind: Noop, Next: next}
	}
	return Transition{Kind: Noop, Next: next}
}
