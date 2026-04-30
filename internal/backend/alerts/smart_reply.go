package alerts

import "time"

// SmartReplyState classifies a user's current state for the [📊 Что
// происходит?] smart reply (spec §5.2). Order is OK < DEGRADED < HARD <
// OFFLINE so callers can compare with `>` if desired.
type SmartReplyState int

const (
	StateOK SmartReplyState = iota
	StateDegraded
	StateHard
	StateOffline
)

func (s SmartReplyState) String() string {
	switch s {
	case StateOK:
		return "ok"
	case StateDegraded:
		return "degraded"
	case StateHard:
		return "hard"
	case StateOffline:
		return "offline"
	}
	return "?"
}

// TunnelView is the per-tunnel projection consumed by smart-reply formatting.
// Built by callbacks.Router from events.LatestEventsByPrefix(uid, "tunnel_").
type TunnelView struct {
	Name         string // pretty (e.g. "amnezia")
	CheckName    string // FSM key (e.g. "tunnel_awg11")
	Interface    string // "nwg1"
	HandshakeAge int    // seconds, 0 if unknown
	PingStatus   string // "ok"|"degraded"|"dead"|""
	Latency      int    // last latency ms (0 if unknown)
	FailCount    int    // ping_check fails right now
	FailThresh   int    // ping_check failure threshold
}

// IncidentView is the projection of an active hard incident, used to drive
// the HARD template.
type IncidentView struct {
	CheckName string
	HardSince time.Time
	FailCount int // consecutive_fails at HARD time
}

// SmartReplyArgs is everything FormatSmartReply needs to render a message.
// Built by callbacks.Router.dispatchSmartReply (Task 12).
type SmartReplyArgs struct {
	Nickname        string
	Tunnels         []TunnelView
	ActiveIncidents []IncidentView
	LastReportAge   time.Duration
	IsMobile        bool
	UserID          int64 // needed for callback_data on inline buttons
}

const (
	smartReplyOfflineThreshold        = 5 * time.Minute
	smartReplyDegradedHandshakeMinSec = 60
)

// ClassifyState applies the spec §5.2 decision tree:
//  1. report age > 5 min                                    → Offline
//  2. ≥1 active hard incident                                → Hard
//  3. any tunnel handshake_age ≥ 60 s                        → Degraded
//  4. any tunnel pingCheck has fail_count > 0 (below thresh) → Degraded
//  5. else                                                   → OK
//
// Rule 3's upper bound is intentionally open — the FSM converts age ≥ 180 s
// into a HARD only after fail_threshold consecutive observations, so during
// the gap we still want "Degraded" rather than misleading "OK".
func ClassifyState(a SmartReplyArgs) SmartReplyState {
	if a.LastReportAge > smartReplyOfflineThreshold {
		return StateOffline
	}
	if len(a.ActiveIncidents) > 0 {
		return StateHard
	}
	for _, t := range a.Tunnels {
		if t.HandshakeAge >= smartReplyDegradedHandshakeMinSec {
			return StateDegraded
		}
		if t.FailCount > 0 && (t.FailThresh == 0 || t.FailCount < t.FailThresh) {
			return StateDegraded
		}
	}
	return StateOK
}
