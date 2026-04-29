// Package callbacks handles Telegram inline-button callbacks for HARD alerts.
// Long-poll loop in router.go fetches callback_query updates, dispatches to actions.go.
package callbacks

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Args is the parsed shape of a callback_data string.
type Args struct {
	Action    string        // "silence" | "ack" | "mute" | "history"
	UserID    int64
	CheckName string
	TTL       time.Duration // only set for silence
}

var validActions = map[string]bool{
	"silence": true, "ack": true, "mute": true, "history": true,
	// command-channel actions: enqueue a wire.Command for the agent.
	"restart_tunnel": true, "diag_now": true, "pingcheck_now": true,
	"force_recheck": true, "opkg_upgrade": true,
}

// IsCommandAction reports whether action is dispatched via the cmd queue
// (vs. local DB-only actions like silence/ack/mute/history).
func IsCommandAction(a string) bool {
	switch a {
	case "restart_tunnel", "diag_now", "pingcheck_now", "force_recheck", "opkg_upgrade":
		return true
	}
	return false
}

func Parse(data string) (Args, error) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 {
		return Args{}, fmt.Errorf("malformed callback_data: %q", data)
	}
	action := parts[0]
	if !validActions[action] {
		return Args{}, fmt.Errorf("unknown action: %q", action)
	}
	uid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Args{}, fmt.Errorf("bad user_id %q: %w", parts[1], err)
	}
	a := Args{Action: action, UserID: uid, CheckName: parts[2]}
	if action == "silence" {
		if len(parts) != 4 {
			return Args{}, fmt.Errorf("silence requires ttl: %q", data)
		}
		ttl, err := parseTTL(parts[3])
		if err != nil {
			return Args{}, err
		}
		a.TTL = ttl
	}
	return a, nil
}

func parseTTL(s string) (time.Duration, error) {
	switch s {
	case "1h":
		return 1 * time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	case "24h":
		return 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("invalid ttl: %q (must be 1h|4h|24h)", s)
}
