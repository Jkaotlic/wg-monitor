package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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
// (wizard_handler.go:605-608: router-local, recoverable, and confirmed in the
// UI before dispatch):
//
//   - check_via_tunnel / check_direct: read-only HTTP probes. They change nothing;
//     they report an exit IP and whether a handful of sites answer. That is data
//     the same person already sees in the bot via 🌍 Через туннель?.
//   - tunnel_restart: mutating, and the only entry here that is. Its blast radius
//     is provably one tunnel on one router the caller already administers: the
//     client sends a tunnel_id, never an ndms_name, and miniappCommandHandler
//     resolves that id to the router's NDM interface name from THIS router's own
//     tunnel_* event rows (see miniappResolveTunnelRestartArgs below). An interface
//     the backend has no tunnel_<id> event for is unreachable, not merely
//     rejected -- sanitizeWizardCommandArgs's regex check on ndms_name is a
//     wizard-side belt-and-braces, not what makes this safe for a mini-app
//     session. It re-establishes in seconds, and it is the single action that
//     actually FIXES the common failure. Without it the screen can only tell an
//     owner to go ask the admin.
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
		commandArgs := req.Args
		if req.Action == "tunnel_restart" {
			// The client sends tunnel_id, never ndms_name -- see the allowlist
			// comment above. Any ndms_name in req.Args is ignored outright (not
			// merely validated): building commandArgs fresh here, rather than
			// patching req.Args, is what makes the old {"ndms_name":"..."} shape
			// inert instead of just rejected.
			tunnelID, _ := req.Args["tunnel_id"].(string)
			resolved, ok := miniappResolveTunnelRestartArgs(d, routerID, tunnelID)
			if !ok {
				writeJSONError(w, http.StatusBadRequest, "unknown_tunnel", "tunnel_id does not match a known tunnel on this router")
				return
			}
			commandArgs = resolved
		}
		args, ok := sanitizeWizardCommandArgs(w, req.Action, commandArgs)
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

// miniappMaxCommandWaitSec caps the long-poll. Agents report on a ~68s median
// cadence (measured 2026-07-06), and the command channel is a separate long-poll,
// but a sleeping mobile router can take far longer -- so the client polls in
// bounded hops rather than holding one socket open forever.
const miniappMaxCommandWaitSec = 30

// miniappCommandResultHandler polls for the result of a command previously
// dispatched via miniappCommandHandler. It is gated by the same per-router
// ACL, checked before cmd_id is even looked at, for the same reason: a
// stranger must not be able to read another router's command output, nor
// tell an existing-but-forbidden router apart from a nonexistent one.
//
// A missing result is 404 result_not_ready, not an error -- the agent simply
// hasn't answered yet and the client is expected to poll again. That mirrors
// wizardCmdResultHandler's contract (wizard_handler.go:1284) exactly, so a
// later frontend task can rely on the same "keep polling" vs "something
// broke" distinction it already implements for the wizard.
func miniappCommandResultHandler(d Deps) http.HandlerFunc {
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
		cmdID := strings.TrimSpace(r.PathValue("cmd_id"))
		if cmdID == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "cmd_id required")
			return
		}
		wait := miniappMaxCommandWaitSec
		if q := r.URL.Query().Get("wait_sec"); q != "" {
			if n, err := strconv.Atoi(q); err == nil {
				wait = n
			}
		}
		if wait < 0 {
			wait = 0
		}
		if wait > miniappMaxCommandWaitSec {
			wait = miniappMaxCommandWaitSec
		}
		// routerID IS users.id in the mini app (see miniappRouterAllowed above),
		// so it goes straight to AwaitResult -- unlike wizardCmdResultHandler,
		// there is no nickname to resolve first.
		res, ok := d.CommandSink.AwaitResult(r.Context(), routerID, cmdID, time.Duration(wait)*time.Second)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "result_not_ready", "no result yet — poll again")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(res)
	}
}

// miniappTunnelNDMSNameDetails is the minimal decode of a tunnel_* event's
// details_json needed to resolve tunnel_restart's ndms_name. It is
// deliberately separate from miniappTunnelDetails/miniappTunnel: ndms_name is
// router topology that the mini-app whitelist (miniapp_tunnels.go) must never
// send to the client, so it is decoded here, server-side only, and never
// attached to any response type.
type miniappTunnelNDMSNameDetails struct {
	TunnelID string `json:"tunnel_id"`
	NDMSName string `json:"ndms_name"`
}

// miniappResolveTunnelRestartArgs maps a tunnel_id the mini-app client sent to
// the arguments the agent needs, using only routerID's own tunnel_* event rows —
// never the caller-supplied id itself, and never a row from another router.
// This is what makes tunnel_restart's blast radius provably one tunnel on one
// router: a tunnel the backend has no tunnel_<id> event for is unreachable,
// not merely rejected, and a real tunnel belonging to a different router does
// not resolve here either.
//
// tunnel_id is always returned — awg-manager restarts by id, which is the only
// path that works for opkg tunnels. ndms_name is added only when the router's
// own events carry one; the agent needs it solely as a fallback on builds that
// predate /api/control/restart. It is still server-derived: a client-supplied
// ndms_name never reaches the agent.
func miniappResolveTunnelRestartArgs(d Deps, routerID int64, tunnelID string) (map[string]any, bool) {
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		return nil, false
	}
	rows, err := d.DB.Events().LatestEventsByPrefixSince(routerID, miniappTunnelPrefix, time.Now().UTC().Add(-miniappEventsWindow))
	if err != nil {
		return nil, false
	}
	for _, row := range rows {
		tu, ok := miniappTunnelFromEvent(row)
		if !ok || tu.TunnelID != tunnelID {
			continue
		}
		args := map[string]any{"tunnel_id": tunnelID}
		var det miniappTunnelNDMSNameDetails
		if err := json.Unmarshal([]byte(row.DetailsJSON), &det); err == nil {
			if ndmsName := strings.TrimSpace(det.NDMSName); ndmsName != "" {
				args["ndms_name"] = ndmsName
			}
		}
		return args, true
	}
	return nil, false
}
