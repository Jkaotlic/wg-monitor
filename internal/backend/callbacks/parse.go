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
	// 4th colon-segment of tunnel_enable/disable callbacks. Empty for any
	// other action — the agent's runner needs it to call ndmc.
	NDMSName string
	// TunnelID is the awg-manager id transported by tunnel panel callbacks.
	TunnelID string
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
	// Route add/delete wizard tokens and short fields.
	RouteKind          string
	RouteUseHRNeo      bool
	RouteDraftToken    string
	RouteConfirmToken  string
	RouteToken         string
	RouteTemplateToken string
	RouteTemplatePage  int
	// MaintName is the target of a maint_restart / maint_confirm callback:
	// "hrneo" | "awgmgr" | "router" | "firmware". Set by Parse for those actions.
	MaintName string
	// MaintToken is the 8-hex confirm token for maint_confirm / maint_fw_confirm.
	MaintToken string
	// OpkgRepairToken is the 8-hex confirm token for opkg_disable callbacks
	// originating from the "🔧 Отключить мёртвый фид" inline button.
	OpkgRepairToken string
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
	// PanelScreen identifies the panel-hub screen for callbacks where
	// Action == "panel". One of: "home" | "kind" | "push" | "no_topic" |
	// "awaken_confirm" | "awaken_do" | "close".
	PanelScreen string
	// PanelKind is the panel type ("maint" | "routes" | "tunnels" | "status")
	// for the "kind" and "push" screens.
	PanelKind string
	// AccessScreen identifies the access:* admin-panel screen for callbacks
	// where Action == "access". One of: "home" | "router" | "add" |
	// "remove_op" | "remove_op_confirm" | "unbind_owner" |
	// "unbind_owner_confirm" | "cancel_add".
	AccessScreen string
	// AccessRouterID is the users.id of the router whose access list is
	// being viewed/modified. Set for "router" / "add" / "remove_op" /
	// "remove_op_confirm" / "unbind_owner" / "unbind_owner_confirm" screens.
	AccessRouterID int64
	// AccessOperatorTGID is the target operator's TG user ID for remove_op.
	AccessOperatorTGID int64
	// AmneziaKeyID identifies one stored Premium key inside the router topic.
	AmneziaKeyID string
	// AmneziaCountryCode is the lower-case country code for Amnezia Premium
	// config issuance callbacks.
	AmneziaCountryCode string
	// AmneziaPage is the zero-based country-list page.
	AmneziaPage int
	// SelfHostedAmneziaID identifies one backend-managed self-hosted VPS.
	SelfHostedAmneziaID string
	// SelfHostedAmneziaEnabled is the target enabled state for self-hosted VPS toggles.
	SelfHostedAmneziaEnabled bool
	// ConfirmToken is the short, actor-scoped token appended to dangerous
	// premium/self-hosted confirm callbacks.
	ConfirmToken string
	// HideMyCodeID identifies one stored HideMy.name access code in the topic.
	HideMyCodeID string
	// HideMyServerID identifies one server from HideMy.name serverlist.
	HideMyServerID string
	// HideMyPage is the zero-based server-list page.
	HideMyPage int
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
	"force_recheck": true, "opkg_upgrade": true, "opkg_disable": true, "opkg_disable_confirm": true,
	"router_doctor": true,
	"tunnel_enable": true, "tunnel_disable": true, "tunnel_restart": true,
	"tunnel_delete_ask": true, "tunnel_delete": true,
	"check_via_tunnel": true, "check_direct": true,
	// backend-only callback (no agent action): re-render Tunnels-panel inline.
	"tunnels_refresh": true,
	// tunnel import confirmation buttons (sent after conf upload review).
	"tunnel_import_replace": true,
	"tunnel_import_add":     true,
	// routes panel actions: browse, rebind, and confirm route changes.
	"routes_open": true, "routes_rebind": true,
	"routes_pick": true, "routes_confirm": true, "routes_rollback": true, "routes_refresh": true,
	"routes_back": true, "routes_close": true, "close_panel": true,
	"routes_add": true, "routes_add_type": true, "routes_add_tunnel": true,
	"routes_tpl_load": true, "routes_tpl_pick": true, "routes_tpl_page": true,
	"routes_add_confirm": true, "routes_add_cancel": true,
	"routes_del": true, "routes_del_confirm": true, "routes_del_cancel": true,
	"routes_hrneo": true, "routes_hrneo_doctor": true, "routes_snapshot": true,
	// maintenance panel actions: open/close panel, restart services, firmware update.
	"maint_open": true, "maint_close": true,
	"maint_restart": true, "maint_confirm": true,
	"maint_fw_open": true, "maint_fw_check": true,
	"maint_fw_install": true, "maint_fw_confirm": true,
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
	// admin panel hub — multi-screen inline-kb dispatcher.
	"panel": true,
	// admin access-control panel — per-router operator whitelist.
	"access": true,
	// Amnezia Premium cabinet.
	"amz_refresh": true, "amz_open": true, "amz_countries": true, "amz_delete": true,
	"amz_delete_confirm": true, "amz_dl": true, "amz_dl_confirm": true,
	"amz_revoke": true, "amz_revoke_confirm": true,
	"amz_selfhosted_issue": true, "amz_selfhosted_confirm": true,
	"amz_selfhosted_manage": true, "amz_selfhosted_add": true,
	"amz_selfhosted_edit": true, "amz_selfhosted_toggle": true,
	"amz_selfhosted_delete": true, "amz_selfhosted_delete_confirm": true,
	"amz_selfhosted_cancel": true,
	// HideMy.name access-code cabinet.
	"hmn_refresh": true, "hmn_open": true, "hmn_page": true,
	"hmn_delete": true, "hmn_delete_confirm": true, "hmn_dl": true,
	"hmn_dl_confirm": true,
}

var callbackCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,16}$`)

// IsCommandAction reports whether action is dispatched via the cmd queue
// (vs. local DB-only actions like silence/ack/mute/history).
func IsCommandAction(a string) bool {
	switch a {
	case "restart_tunnel", "diag_now", "pingcheck_now", "force_recheck",
		"opkg_upgrade", "tunnel_enable", "tunnel_disable", "tunnel_restart", "tunnel_delete",
		"check_via_tunnel", "check_direct", "router_doctor":
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
	if action == "tunnel_enable" || action == "tunnel_disable" || action == "tunnel_restart" || action == "tunnel_delete_ask" || action == "tunnel_delete" {
		if len(parts) >= 5 {
			a.TunnelID = strings.TrimSpace(parts[4])
		}
		if len(parts) < 4 || parts[3] == "" {
			if (action == "tunnel_delete_ask" || action == "tunnel_delete") && a.TunnelID != "" {
				a.IsPanel = true
				return a, nil
			}
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
		a.IsPanel = true
	}
	if action == "tunnel_import_replace" || action == "tunnel_import_add" {
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("%s requires token: %q", action, data)
		}
		if parts[2] != panelSentinel {
			return Args{}, fmt.Errorf("%s requires %q sentinel, not tunnel name: %q", action, panelSentinel, data)
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
	case "routes_rollback":
		if len(parts) < 4 {
			return Args{}, fmt.Errorf("routes_rollback requires src and dst: %q", data)
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
	case "routes_add_type":
		if len(parts) < 4 {
			return Args{}, fmt.Errorf("routes_add_type requires dns|dns_hr|static: %q", data)
		}
		switch parts[3] {
		case "dns":
			a.RouteKind = "dns"
		case "dns_hr":
			a.RouteKind = "dns"
			a.RouteUseHRNeo = true
		case "static":
			a.RouteKind = "static"
		default:
			return Args{}, fmt.Errorf("routes_add_type requires dns|dns_hr|static: %q", data)
		}
	case "routes_add_tunnel":
		if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
			return Args{}, fmt.Errorf("routes_add_tunnel requires draft token and tunnel id: %q", data)
		}
		a.RouteDraftToken = parts[3]
		a.RebindDstID = parts[4]
	case "routes_tpl_load":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("routes_tpl_load requires draft token: %q", data)
		}
		a.RouteDraftToken = parts[3]
	case "routes_tpl_pick":
		if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
			return Args{}, fmt.Errorf("routes_tpl_pick requires draft and template tokens: %q", data)
		}
		a.RouteDraftToken = parts[3]
		a.RouteTemplateToken = parts[4]
	case "routes_tpl_page":
		if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
			return Args{}, fmt.Errorf("routes_tpl_page requires draft token and page: %q", data)
		}
		page, err := strconv.Atoi(parts[4])
		if err != nil || page < 0 {
			return Args{}, fmt.Errorf("routes_tpl_page: bad page %q", parts[4])
		}
		a.RouteDraftToken = parts[3]
		a.RouteTemplatePage = page
	case "routes_add_confirm":
		if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
			return Args{}, fmt.Errorf("routes_add_confirm requires draft and confirm tokens: %q", data)
		}
		a.RouteDraftToken = parts[3]
		a.RouteConfirmToken = parts[4]
	case "routes_add_cancel":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("routes_add_cancel requires draft token: %q", data)
		}
		a.RouteDraftToken = parts[3]
	case "routes_del":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("routes_del requires route token: %q", data)
		}
		a.RouteToken = parts[3]
	case "routes_del_confirm":
		if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
			return Args{}, fmt.Errorf("routes_del_confirm requires draft and confirm tokens: %q", data)
		}
		a.RouteDraftToken = parts[3]
		a.RouteConfirmToken = parts[4]
	case "routes_del_cancel":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("routes_del_cancel requires draft token: %q", data)
		}
		a.RouteDraftToken = parts[3]
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
	case "opkg_disable", "opkg_disable_confirm":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("%s requires token: %q", action, data)
		}
		a.OpkgRepairToken = parts[3]
	case "diag_raw":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("diag_raw requires token: %q", data)
		}
		a.DiagRawToken = parts[3]
	case "diag_back":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("diag_back requires cache_token: %q", data)
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
		a.DiagRawToken = parts[2]
		a.DiagTestID = parts[3]
	}
	if action == "panel" {
		screen := parts[2]
		validPanelScreens := map[string]bool{
			"home": true, "kind": true, "push": true, "no_topic": true,
			"awaken_confirm": true, "awaken_do": true, "close": true,
			"help": true, "doctor_all": true, "audit_all": true,
			"update_all_confirm": true, "update_all_do": true, "mobile": true,
		}
		if !validPanelScreens[screen] {
			return Args{}, fmt.Errorf("panel: unknown screen %q", screen)
		}
		a.PanelScreen = screen
		if screen == "kind" || screen == "push" {
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("panel %s requires kind: %q", screen, data)
			}
			validKinds := map[string]bool{"maint": true, "routes": true, "tunnels": true, "status": true, "pingcheck": true, "doctor": true}
			if !validKinds[parts[3]] {
				return Args{}, fmt.Errorf("panel %s: unknown kind %q", screen, parts[3])
			}
			a.PanelKind = parts[3]
		}
		if screen == "help" {
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("panel help requires screen: %q", data)
			}
			validHelpScreens := map[string]bool{
				"operator": true, "alerts": true, "fleet": true, "premium": true, "mobile": true,
				"maint": true, "routes": true, "tunnels": true,
				"access": true, "diag": true, "status": true, "pingcheck": true, "doctor": true,
			}
			if !validHelpScreens[parts[3]] {
				return Args{}, fmt.Errorf("panel help: unknown screen %q", parts[3])
			}
			// Reuse PanelKind as transport for the help-target screen name.
			a.PanelKind = parts[3]
		}
	}
	if action == "access" {
		screen := parts[2]
		validAccessScreens := map[string]bool{
			"home": true, "router": true, "add": true,
			"remove_op": true, "remove_op_confirm": true,
			"unbind_owner": true, "unbind_owner_confirm": true,
			"cancel_add": true,
		}
		if !validAccessScreens[screen] {
			return Args{}, fmt.Errorf("access: unknown screen %q", screen)
		}
		a.AccessScreen = screen
		switch screen {
		case "router", "add", "unbind_owner", "unbind_owner_confirm":
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("access %s requires router id: %q", screen, data)
			}
			rid, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access %s: bad router id %q", screen, parts[3])
			}
			a.AccessRouterID = rid
		case "remove_op", "remove_op_confirm":
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("access %s requires router id and tg id: %q", screen, data)
			}
			rid, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access %s: bad router id %q", screen, parts[3])
			}
			tgid, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil {
				return Args{}, fmt.Errorf("access %s: bad tg id %q", screen, parts[4])
			}
			a.AccessRouterID = rid
			a.AccessOperatorTGID = tgid
		}
	}
	if strings.HasPrefix(action, "amz_") {
		switch action {
		case "amz_refresh":
			if len(parts) >= 4 && parts[3] != "" {
				if !callbackCodeRe.MatchString(parts[3]) {
					return Args{}, fmt.Errorf("%s: bad key id %q", action, parts[3])
				}
				a.AmneziaKeyID = parts[3]
			}
		case "amz_open", "amz_delete", "amz_delete_confirm":
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("%s requires key id: %q", action, data)
			}
			if !callbackCodeRe.MatchString(parts[3]) {
				return Args{}, fmt.Errorf("%s: bad key id %q", action, parts[3])
			}
			a.AmneziaKeyID = parts[3]
			if action == "amz_delete_confirm" {
				if err := setOptionalConfirmToken(&a, parts, 4, action); err != nil {
					return Args{}, err
				}
			}
		case "amz_countries":
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("%s requires key id and page: %q", action, data)
			}
			if !callbackCodeRe.MatchString(parts[3]) {
				return Args{}, fmt.Errorf("%s: bad key id %q", action, parts[3])
			}
			page, err := strconv.Atoi(parts[4])
			if err != nil || page < 0 {
				return Args{}, fmt.Errorf("%s: bad page %q", action, parts[4])
			}
			a.AmneziaKeyID = parts[3]
			a.AmneziaPage = page
		case "amz_dl", "amz_dl_confirm":
			if len(parts) == 4 && parts[3] != "" {
				if !callbackCodeRe.MatchString(parts[3]) {
					return Args{}, fmt.Errorf("%s: bad country code %q", action, parts[3])
				}
				a.AmneziaCountryCode = strings.ToLower(parts[3])
				return a, nil
			}
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("%s requires country code: %q", action, data)
			}
			if !callbackCodeRe.MatchString(parts[3]) {
				return Args{}, fmt.Errorf("%s: bad key id %q", action, parts[3])
			}
			if !callbackCodeRe.MatchString(parts[4]) {
				return Args{}, fmt.Errorf("%s: bad country code %q", action, parts[4])
			}
			a.AmneziaKeyID = parts[3]
			a.AmneziaCountryCode = strings.ToLower(parts[4])
			if action == "amz_dl_confirm" {
				if err := setOptionalConfirmToken(&a, parts, 5, action); err != nil {
					return Args{}, err
				}
			}
		case "amz_revoke", "amz_revoke_confirm":
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("%s requires key id and country code: %q", action, data)
			}
			if !callbackCodeRe.MatchString(parts[3]) {
				return Args{}, fmt.Errorf("%s: bad key id %q", action, parts[3])
			}
			if !callbackCodeRe.MatchString(parts[4]) {
				return Args{}, fmt.Errorf("%s: bad country code %q", action, parts[4])
			}
			a.AmneziaKeyID = parts[3]
			a.AmneziaCountryCode = strings.ToLower(parts[4])
			if action == "amz_revoke_confirm" {
				if err := setOptionalConfirmToken(&a, parts, 5, action); err != nil {
					return Args{}, err
				}
			}
		case "amz_selfhosted_manage", "amz_selfhosted_add", "amz_selfhosted_cancel":
			// No extra fields.
		case "amz_selfhosted_issue", "amz_selfhosted_confirm", "amz_selfhosted_edit", "amz_selfhosted_delete", "amz_selfhosted_delete_confirm":
			if len(parts) >= 4 && parts[3] != "" {
				if !callbackCodeRe.MatchString(parts[3]) {
					return Args{}, fmt.Errorf("%s: bad self-hosted id %q", action, parts[3])
				}
				a.SelfHostedAmneziaID = strings.ToLower(parts[3])
			}
			if action == "amz_selfhosted_confirm" || action == "amz_selfhosted_delete_confirm" {
				if err := setOptionalConfirmToken(&a, parts, 4, action); err != nil {
					return Args{}, err
				}
			}
		case "amz_selfhosted_toggle":
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("%s requires self-hosted id and target state: %q", action, data)
			}
			if !callbackCodeRe.MatchString(parts[3]) {
				return Args{}, fmt.Errorf("%s: bad self-hosted id %q", action, parts[3])
			}
			a.SelfHostedAmneziaID = strings.ToLower(parts[3])
			switch parts[4] {
			case "0":
				a.SelfHostedAmneziaEnabled = false
			case "1":
				a.SelfHostedAmneziaEnabled = true
			default:
				return Args{}, fmt.Errorf("%s: target state must be 0 or 1, got %q", action, parts[4])
			}
		}
	}
	if strings.HasPrefix(action, "hmn_") {
		switch action {
		case "hmn_refresh":
			// No extra fields: return to the stored-code list.
		case "hmn_open", "hmn_delete", "hmn_delete_confirm":
			if len(parts) < 4 || parts[3] == "" {
				return Args{}, fmt.Errorf("%s requires code id: %q", action, data)
			}
			if !callbackCodeRe.MatchString(parts[3]) {
				return Args{}, fmt.Errorf("%s: bad code id %q", action, parts[3])
			}
			a.HideMyCodeID = parts[3]
			if action == "hmn_delete_confirm" {
				if err := setOptionalConfirmToken(&a, parts, 4, action); err != nil {
					return Args{}, err
				}
			}
		case "hmn_page":
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("%s requires code id and page: %q", action, data)
			}
			if !callbackCodeRe.MatchString(parts[3]) {
				return Args{}, fmt.Errorf("%s: bad code id %q", action, parts[3])
			}
			page, err := strconv.Atoi(parts[4])
			if err != nil || page < 0 {
				return Args{}, fmt.Errorf("%s: bad page %q", action, parts[4])
			}
			a.HideMyCodeID = parts[3]
			a.HideMyPage = page
		case "hmn_dl", "hmn_dl_confirm":
			if len(parts) < 5 || parts[3] == "" || parts[4] == "" {
				return Args{}, fmt.Errorf("%s requires code id and server id: %q", action, data)
			}
			if !callbackCodeRe.MatchString(parts[3]) {
				return Args{}, fmt.Errorf("%s: bad code id %q", action, parts[3])
			}
			if !callbackCodeRe.MatchString(parts[4]) {
				return Args{}, fmt.Errorf("%s: bad server id %q", action, parts[4])
			}
			a.HideMyCodeID = parts[3]
			a.HideMyServerID = parts[4]
			if action == "hmn_dl_confirm" {
				if err := setOptionalConfirmToken(&a, parts, 5, action); err != nil {
					return Args{}, err
				}
			}
		}
	}
	return a, nil
}

func setOptionalConfirmToken(a *Args, parts []string, idx int, action string) error {
	if len(parts) <= idx {
		return nil
	}
	if parts[idx] == "" {
		return fmt.Errorf("%s: empty confirm token", action)
	}
	if !callbackCodeRe.MatchString(parts[idx]) {
		return fmt.Errorf("%s: bad confirm token %q", action, parts[idx])
	}
	a.ConfirmToken = parts[idx]
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
