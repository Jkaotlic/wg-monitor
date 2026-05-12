// Package callbacks handles Telegram inline-button callbacks for HARD alerts.
// Long-poll loop in router.go fetches callback_query updates, dispatches to actions.go.
package callbacks

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ndmsNameRe whitelists Keenetic interface ids passed in callback_data.
// Source values come from events JSON returned by awg-manager → agent →
// backend; downstream they are concatenated into a `ndmc -c "interface
// <name> <state>"` invocation on the router, where the internal ndmc
// tokenizer can split on whitespace and let an attacker reshape the
// command. Whitelist matches all real Keenetic interface ids
// (Wireguard0..N, AmneziaWG0..N, Tunnel0..N).
var ndmsNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

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
	// ImportToken is set for tunnel_import_replace / tunnel_import_add callbacks.
	// It is an 8-hex-char random token that ties the callback to the pending upload.
	ImportToken string
	// RebindToken is set for routes_confirm callbacks. 8 hex chars, 5-min TTL.
	RebindToken string
	// RebindSrcID / RebindDstID parsed from routes_pick / routes_confirm.
	RebindSrcID string
	RebindDstID string
	// MaintName is the target of a maint_restart / maint_confirm callback:
	// "hrneo" | "awgmgr" | "router" | "firmware". Set by Parse for those actions.
	MaintName string
	// MaintToken is the 8-hex confirm token for maint_confirm / maint_fw_confirm.
	MaintToken string
	// OpkgRepairToken is the 8-hex confirm token for opkg_disable callbacks
	// originating from the "🔧 Отключить мёртвый фид" inline button.
	OpkgRepairToken string
	// PanelScreen identifies the panel-hub screen for callbacks where
	// Action == "panel". One of: "home" | "kind" | "push" | "no_topic" |
	// "awaken_confirm" | "awaken_do" | "close".
	PanelScreen string
	// PanelKind is the panel type ("maint" | "routes" | "status") for
	// the "kind" and "push" screens.
	PanelKind string
	// AccessScreen identifies the access:* admin-panel screen for callbacks
	// where Action == "access". One of: "home" | "router" | "add" |
	// "remove_op" | "unbind_owner" | "cancel_add".
	AccessScreen string
	// AccessRouterID is the users.id of the router whose access list is
	// being viewed/modified. Set for "router" / "add" / "remove_op" /
	// "unbind_owner" screens.
	AccessRouterID int64
	// AccessOperatorTGID is the target operator's TG user ID for remove_op.
	AccessOperatorTGID int64
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
	"force_recheck": true, "opkg_upgrade": true, "opkg_disable": true,
	"tunnel_enable": true, "tunnel_disable": true,
	"check_via_tunnel": true, "check_direct": true,
	// backend-only callback (no agent action): re-render Tunnels-panel inline.
	"tunnels_refresh": true,
	// tunnel import confirmation buttons (sent after conf upload review).
	"tunnel_import_replace": true,
	"tunnel_import_add":     true,
	// routes panel actions: browse, rebind, and confirm route changes.
	"routes_open": true, "routes_router": true, "routes_rebind": true,
	"routes_pick": true, "routes_confirm": true, "routes_refresh": true,
	"routes_back": true, "routes_close": true,
	// maintenance panel actions: open/close panel, restart services, firmware update.
	"maint_open": true, "maint_close": true,
	"maint_restart": true, "maint_confirm": true,
	"maint_fw_open": true, "maint_fw_check": true,
	"maint_fw_install": true, "maint_fw_confirm": true,
	// compat-mode inline button: encodes the per-topic reply-keyboard label
	// as an inline-keyboard tap (TG Desktop forum-topic workaround). The
	// short code lives in CheckName and is mapped back to the original
	// label by tg.CompatBtnTextByCode.
	"compat_btn": true,
	// admin panel hub — multi-screen inline-kb dispatcher.
	"panel": true,
	// admin access-control panel — per-router operator whitelist.
	"access": true,
}

// IsCommandAction reports whether action is dispatched via the cmd queue
// (vs. local DB-only actions like silence/ack/mute/history).
func IsCommandAction(a string) bool {
	switch a {
	case "restart_tunnel", "diag_now", "pingcheck_now", "force_recheck",
		"opkg_upgrade", "tunnel_enable", "tunnel_disable",
		"check_via_tunnel", "check_direct":
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
		// Whitelist NDMS interface names: alphanumerics + underscore/hyphen.
		// Защищаемся от пробельных символов в значении, которые потом
		// передаются в `ndmc -c "interface <ndms> <state>"` на роутере —
		// внутренний токенизатор ndmc может разделить аргумент и переопределить
		// команду (SEC-02). Источник значения — events JSON от агента,
		// то есть от awg-manager API; формат шире чем нужно нам.
		if !ndmsNameRe.MatchString(parts[3]) {
			return Args{}, fmt.Errorf("%s: ndms_name %q must match ^[A-Za-z0-9_-]{1,32}$", action, parts[3])
		}
		a.NDMSName = parts[3]
	}
	if action == "tunnel_import_replace" || action == "tunnel_import_add" {
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("%s requires token: %q", action, data)
		}
		a.ImportToken = parts[3]
	}
	switch action {
	case "routes_rebind":
		if len(parts) >= 3 && parts[2] != "" && parts[2] != panelSentinel {
			a.RebindSrcID = parts[2]
		}
	case "routes_pick":
		if len(parts) < 4 {
			return Args{}, fmt.Errorf("routes_pick requires src and dst: %q", data)
		}
		a.RebindSrcID = parts[2]
		a.RebindDstID = parts[3]
	case "routes_confirm":
		if len(parts) < 5 || parts[4] == "" {
			return Args{}, fmt.Errorf("routes_confirm requires token: %q", data)
		}
		a.RebindSrcID = parts[2]
		a.RebindDstID = parts[3]
		a.RebindToken = parts[4]
	case "maint_restart":
		if len(parts) < 3 || parts[2] == "" || parts[2] == panelSentinel {
			return Args{}, fmt.Errorf("maint_restart requires name (hrneo|awgmgr|router): %q", data)
		}
		a.MaintName = parts[2]
	case "maint_confirm":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("maint_confirm requires token: %q", data)
		}
		a.MaintName = parts[2]
		a.MaintToken = parts[3]
	case "maint_fw_confirm":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("maint_fw_confirm requires token: %q", data)
		}
		a.MaintName = "firmware"
		a.MaintToken = parts[3]
	case "opkg_disable":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("opkg_disable requires token: %q", data)
		}
		a.OpkgRepairToken = parts[3]
	}
	if action == "panel" {
		screen := parts[2]
		validPanelScreens := map[string]bool{
			"home": true, "kind": true, "push": true, "no_topic": true,
			"awaken_confirm": true, "awaken_do": true, "close": true,
		}
		if !validPanelScreens[screen] {
			return Args{}, fmt.Errorf("panel: unknown screen %q", screen)
		}
		a.PanelScreen = screen
		if screen == "kind" || screen == "push" {
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("panel %s requires kind: %q", screen, data)
			}
			validKinds := map[string]bool{"maint": true, "routes": true, "status": true}
			if !validKinds[parts[3]] {
				return Args{}, fmt.Errorf("panel %s: unknown kind %q", screen, parts[3])
			}
			a.PanelKind = parts[3]
		}
	}
	if action == "access" {
		screen := parts[2]
		validAccessScreens := map[string]bool{
			"home": true, "router": true, "add": true,
			"remove_op": true, "unbind_owner": true,
			"cancel_add": true,
		}
		if !validAccessScreens[screen] {
			return Args{}, fmt.Errorf("access: unknown screen %q", screen)
		}
		a.AccessScreen = screen
		switch screen {
		case "router", "add", "unbind_owner":
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("access %s requires router id: %q", screen, data)
			}
			rid, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access %s: bad router id %q", screen, parts[3])
			}
			a.AccessRouterID = rid
		case "remove_op":
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("access remove_op requires router id and tg id: %q", data)
			}
			rid, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access remove_op: bad router id %q", parts[3])
			}
			tgid, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access remove_op: bad tg id %q", parts[4])
			}
			a.AccessRouterID = rid
			a.AccessOperatorTGID = tgid
		}
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
