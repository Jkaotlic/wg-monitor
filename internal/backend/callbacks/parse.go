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
	Action    string        // "silence" | "ack" | "mute" | "history" | command-channel actions
	UserID    int64
	CheckName string
	TTL       time.Duration // only set for silence
	// IsMenu marks callbacks originating from the persistent control-panel
	// (pinned message). Detected via "_menu" suffix on CheckName, which the
	// parser strips. The router uses this to skip EditMessageText so menu
	// buttons stay visible after taps. HARD-alert callbacks have IsMenu=false
	// and continue to lose their keyboard on first tap.
	IsMenu bool
	// NDMSName is the Keenetic interface id ("Wireguard0") tucked into the
	// 4th colon-segment of tunnel_enable/disable callbacks. Empty for any
	// other action — the agent's runner needs it to call ndmc.
	NDMSName string
	// IsPanel marks callbacks originating from the Tunnels Panel (CheckName
	// ends in "_panel_"). The router uses this to refresh the panel inline
	// instead of editing the original alert message.
	IsPanel bool
}

// menuSuffix is appended to CheckName in control-panel callback_data so the
// router can distinguish menu taps from HARD-alert taps without inventing
// a new top-level callback_data namespace. Choosing "_menu" because real
// FSM check names never carry this suffix (synthetic + reserved).
const menuSuffix = "_menu"

// panelSentinel is the CheckName placeholder used by Tunnels-Panel global
// buttons (restart / refresh) where there is no per-tunnel target.
const panelSentinel = "_panel_"

var validActions = map[string]bool{
	"silence": true, "ack": true, "mute": true, "history": true,
	// command-channel actions: enqueue a wire.Command for the agent.
	"restart_tunnel": true, "diag_now": true, "pingcheck_now": true,
	"force_recheck": true, "opkg_upgrade": true,
	"tunnel_enable": true, "tunnel_disable": true,
	// backend-only callback (no agent action): re-render Tunnels-panel inline.
	"tunnels_refresh": true,
}

// IsCommandAction reports whether action is dispatched via the cmd queue
// (vs. local DB-only actions like silence/ack/mute/history).
func IsCommandAction(a string) bool {
	switch a {
	case "restart_tunnel", "diag_now", "pingcheck_now", "force_recheck",
		"opkg_upgrade", "tunnel_enable", "tunnel_disable":
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
	checkName := parts[2]
	isMenu := false
	if strings.HasSuffix(checkName, menuSuffix) {
		checkName = strings.TrimSuffix(checkName, menuSuffix)
		// "_menu" alone collapses to empty after strip — treat as global menu
		// op (opkg/force_recheck) where CheckName has no FSM meaning. We keep
		// it as "_menu" sentinel so action handlers can branch if they need to.
		if checkName == "" {
			checkName = menuSuffix
		}
		isMenu = true
	}
	a := Args{Action: action, UserID: uid, CheckName: checkName, IsMenu: isMenu}
	if checkName == panelSentinel {
		a.IsPanel = true
	}
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
	if action == "tunnel_enable" || action == "tunnel_disable" {
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("%s requires ndms_name: %q", action, data)
		}
		a.NDMSName = parts[3]
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
