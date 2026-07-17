package backend

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// miniappCommandAllowlist is what a mini-app session may dispatch to an agent.
//
// It is deliberately NOT dashboardCommandAllowlist. The two have different
// trust models: the dashboard is one admin holding one token, while a mini-app
// session is any Telegram user resolved to a per-router role (admin / owner /
// operator). So this list is scoped to "things the person who owns THIS router
// should be able to do to THIS router", which both subtracts from the dashboard's
// list (no dns_reset, no agent config editing, no opkg/entware maintenance) and
// adds to it (tunnel probes and restart -- see below).
//
// Three entries widen the browser-session boundary that wizard_handler.go draws.
// Each is justified against the trust precedent dns_reset established
// (wizard_handler.go: router-local, recoverable, and confirmed in the UI before
// dispatch):
//
//   - check_via_tunnel / check_direct: read-only HTTP probes. They change nothing;
//     they report an exit IP and whether a handful of sites answer. That is data
//     the same person already sees in the bot via [ Через туннель?].
//   - tunnel_restart: mutating, and the only entry here that is. Blast radius is
//     one tunnel on one router the caller already administers, it re-establishes
//     in seconds, and it is the single action that actually FIXES the common
//     failure. Without it the screen can only tell an owner to go ask the admin.
//
// Everything else stays out on purpose; TestMiniappCommandAllowlistContents pins
// the denied set. In particular update_backend_url is fleet-takeover blast radius
// (same reasoning as the dashboard's hidden-update-url rejection), tunnel_delete
// is irreversible, tunnel_enable/disable are configuration changes rather than
// repairs, and dns_reset is router-global -- it stays with the admin's dashboard.
var miniappCommandAllowlist = map[string]bool{
	// Read-only, already trusted to the dashboard.
	"force_recheck":  true,
	"diag_now":       true,
	"tunnels_status": true,
	"route_status":   true,
	// Read-only probes; new for a browser session (wizard-only until now).
	"check_via_tunnel": true,
	"check_direct":     true,
	// Mutating; new for a browser session. Router-local, reversible, UI-confirmed.
	"tunnel_restart": true,
}

type miniappCommandReq struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
}

// miniappCommandHandler dispatches an allowlisted agent command on behalf of a
// mini-app user. Authorization is the same per-router ACL that gates viewing, and
// it is checked BEFORE the router is looked up so a stranger cannot probe which
// ids exist (same ordering as the Phase 3 access endpoints).
func miniappCommandHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.CommandSink == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "command sink not configured")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req miniappCommandReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.Action = strings.TrimSpace(req.Action)
		if !miniappCommandAllowlist[req.Action] || !wire.IsValidCommandAction(req.Action) {
			writeJSONError(w, http.StatusBadRequest, "unsupported_command", "action is not allowed from the mini app")
			return
		}
		args, ok := sanitizeWizardCommandArgs(w, req.Action, req.Args)
		if !ok {
			return
		}
		// miniappRouterAllowed's admin branch grants access without checking that
		// routerID actually exists (miniappIsAdmin short-circuits before
		// RouterAccessRole), so an admin hitting a stale/typo'd id can still reach
		// here. GetByID returns db.ErrUserNotFound (never a nil, nil-error User) in
		// that case; map it to 404 same as the Phase 3 access endpoints, not the
		// generic 500.
		u, err := d.DB.Users().GetByID(routerID)
		if errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
			return
		}
		enqueueAgentCommandForUser(w, d, u, req.Action, args)
	}
}
