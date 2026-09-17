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
	Action    string // "silence" | "ack" | "mute" | "history" | command-channel actions
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
	// 4th colon-segment of pingcheck_toggle callbacks. Empty for any other
	// action — the agent's runner needs it to call ndmc.
	NDMSName string
	// IsPanel marks callbacks whose CheckName is the "_panel_" sentinel
	// (PingCheck panel, close_panel). The router answers such taps with a
	// toast and leaves the message in place.
	IsPanel bool
	// DiagRawToken is the 8-hex token of a cached diag JSON body retrieved
	// by the "📄 Полный отчёт" button under a diag result.
	DiagRawToken string
	// PingCheckTunnelID is the awg-mgr tunnel id ("awg10") in
	// pingcheck_toggle callbacks. Empty for any other action.
	PingCheckTunnelID string
	// PingCheckEnable is the bool transported in the 5th colon-segment
	// of pingcheck_toggle ("0" → false, "1" → true).
	PingCheckEnable bool
	// DiagTestID is the short slug ("mtu", "dns_leak", ...) identifying
	// which test was tapped on a diag drill-down. Set for diag_test action.
	DiagTestID string
	// PanelScreen -- для Action == "panel" всегда "help": от хаба /panel
	// осталась одна справка под панелями роутера (help_callback.go).
	PanelScreen string
	// PanelKind -- экран справки ("tunnels", "routes", "pingcheck", ...).
	PanelKind string
}

// menuSuffix is appended to CheckName in control-panel callback_data so the
// router can distinguish menu taps from HARD-alert taps without inventing
// a new top-level callback_data namespace. Choosing "_menu" because real
// FSM check names never carry this suffix (synthetic + reserved).
const menuSuffix = "_menu"

// panelSentinel is the CheckName placeholder used by panel-global buttons
// (PingCheck refresh, close) where there is no per-check target.
const panelSentinel = "_panel_"

var validActions = map[string]bool{
	"silence": true, "ack": true, "mute": true, "history": true,
	// command-channel actions: enqueue a wire.Command for the agent. Панели
	// туннелей, маршрутов и перезапуска служб ушли в приложение (цикл 4);
	// их старые кнопки отвечают тостом (moved_to_app.go).
	"diag_now": true, "pingcheck_now": true,
	"force_recheck":    true,
	"router_doctor":    true,
	"check_via_tunnel": true, "check_direct": true,
	// закрыть справку.
	"close_panel": true,
	// diag_raw: fetch cached raw diag JSON body for "📄 Полный отчёт" button.
	"diag_raw": true,
	// diag_back: re-render parsed diag summary inline ("« К сводке" button).
	"diag_back": true,
	// pingcheck panel: monitor + per-tunnel watchdog toggle.
	"pingcheck_open": true, "pingcheck_toggle": true,
	// diag drill-down: tap a failing test in a diag summary.
	"diag_test": true,
	// compat-mode inline button: encodes the per-topic reply-keyboard label
	// as an inline-keyboard tap (TG Desktop forum-topic workaround). The
	// short code lives in CheckName and is mapped back to the original
	// label by tg.CompatBtnTextByCode.
	"compat_btn": true,
	// справка «ℹ Помощь» под панелями роутера (help_callback.go).
	"panel": true,
}

var callbackCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,16}$`)

// IsCommandAction reports whether action is dispatched via the cmd queue
// (vs. local DB-only actions like silence/ack/mute/history).
func IsCommandAction(a string) bool {
	switch a {
	case "diag_now", "pingcheck_now", "force_recheck",
		"opkg_upgrade", "check_via_tunnel", "check_direct", "router_doctor":
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
	switch action {
	case "diag_raw":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("diag_raw requires token: %q", data)
		}
		if err := requireCallbackCode(action, "token", parts[3]); err != nil {
			return Args{}, err
		}
		a.DiagRawToken = parts[3]
	case "diag_back":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("diag_back requires cache_token: %q", data)
		}
		if err := requireCallbackCode(action, "cache token", parts[3]); err != nil {
			return Args{}, err
		}
		a.DiagRawToken = parts[3]
	case "pingcheck_toggle":
		if len(parts) < 5 {
			return Args{}, fmt.Errorf("pingcheck_toggle requires tunnel_id, ndms_name, enable: %q", data)
		}
		// parts[2] is CheckName (already set above); for this action it
		// carries the awg-mgr tunnel id.
		a.PingCheckTunnelID = parts[2]
		if !ndmsNameRe.MatchString(parts[3]) {
			return Args{}, fmt.Errorf("pingcheck_toggle: ndms_name %q must match ^[A-Za-z0-9_-]{1,32}$", parts[3])
		}
		a.NDMSName = parts[3]
		switch parts[4] {
		case "0":
			a.PingCheckEnable = false
		case "1":
			a.PingCheckEnable = true
		default:
			return Args{}, fmt.Errorf("pingcheck_toggle: enable must be 0 or 1, got %q", parts[4])
		}
	case "diag_test":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("diag_test requires cache_token and test_id: %q", data)
		}
		if err := requireCallbackCode(action, "cache token", parts[2]); err != nil {
			return Args{}, err
		}
		a.DiagRawToken = parts[2]
		a.DiagTestID = parts[3]
	}
	if action == "panel" {
		// От хаба /panel осталась одна справка: её кнопки стоят на панелях
		// роутера до цикла 4. Остальные экраны уехали в приложение.
		if parts[2] != "help" {
			return Args{}, fmt.Errorf("panel: unknown screen %q", parts[2])
		}
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("panel help requires screen: %q", data)
		}
		validHelpScreens := map[string]bool{
			"operator": true, "alerts": true, "fleet": true, "premium": true, "mobile": true,
			"access": true, "diag": true, "status": true, "pingcheck": true, "doctor": true,
		}
		if !validHelpScreens[parts[3]] {
			return Args{}, fmt.Errorf("panel help: unknown screen %q", parts[3])
		}
		a.PanelScreen = "help"
		a.PanelKind = parts[3]
	}
	return a, nil
}

func requireCallbackCode(action, field, value string) error {
	if !callbackCodeRe.MatchString(value) {
		return fmt.Errorf("%s: bad %s %q", action, field, value)
	}
	return nil
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
