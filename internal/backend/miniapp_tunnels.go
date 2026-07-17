package backend

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// miniappTunnelPrefix mirrors checks.tunnelCheckPrefix ("tunnel_"): per-tunnel
// check names are "tunnel_<tunnelId>". Duplicated rather than imported because
// the backend does not depend on the agent's checks package.
const miniappTunnelPrefix = "tunnel_"

// miniappTunnel is the per-tunnel projection the mini app is allowed to see.
//
// It is a WHITELIST, not a passthrough of events.details_json. The agent's
// details map carries router topology (endpoint, address, isp_interface,
// ndms_name, interface) that is fine for the admin dashboard but must not reach
// the mini app: owners and operators read this screen too. Same reasoning that
// makes miniappRouterSummaryFromAgent drop expected_exit_ip / awg_iface / kind
// (miniapp_handler.go:94-114).
//
// Pointer fields are the ones whose ABSENCE is meaningful: an old agent, or an
// awg-manager that wouldn't answer, omits them entirely, and "unknown" must
// stay distinguishable from "zero".
type miniappTunnel struct {
	TunnelID string `json:"tunnel_id"`
	Name     string `json:"name,omitempty"`
	// Status is the check verdict ("ok"|"fail") -- the FSM's opinion.
	Status string `json:"status"`
	// RunState is the router's own word for the tunnel ("running"|"stopped"|...).
	RunState string `json:"run_state,omitempty"`
	// Enabled is a pointer for the same reason as HandshakeAgeSec/PingLatencyMs:
	// an unparseable details blob, or an agent older than this key, must render
	// as "unknown", not as a guessed true/false.
	Enabled         *bool  `json:"enabled,omitempty"`
	HandshakeAgeSec *int   `json:"handshake_age_sec,omitempty"`
	PingCheckStatus string `json:"ping_check_status,omitempty"`
	PingLatencyMs   *int   `json:"ping_latency_ms,omitempty"`
	// DefaultRouteIntent: this tunnel claims to be the default route. Several
	// tunnels can each claim it -- it is NOT the answer to "where does traffic go".
	DefaultRouteIntent bool `json:"default_route_intent"`
	// IsActiveDefault: this tunnel IS the live egress (settings.download.routeTag).
	// Only trustworthy when ActiveDefaultKnown is true.
	IsActiveDefault bool `json:"is_active_default"`
	// ActiveDefaultKnown: whether the egress question has an answer at all.
	// False for agents older than the routeTag change, or when awg-manager's
	// /api/settings/get failed. False means "say unknown", never "guess".
	ActiveDefaultKnown bool   `json:"active_default_known"`
	Note               string `json:"note,omitempty"`
	TS                 string `json:"ts,omitempty"`
}

// miniappTunnelDetails is the subset of the agent's details map we decode.
// Unknown keys are ignored by encoding/json -- that is the whitelist.
type miniappTunnelDetails struct {
	TunnelID           string `json:"tunnel_id"`
	TunnelName         string `json:"tunnel_name"`
	Status             string `json:"status"`
	Enabled            *bool  `json:"enabled"`
	HandshakeAgeSec    *int   `json:"handshake_age_sec"`
	PingCheckStatus    string `json:"ping_check_status"`
	PingLastLatencyMs  *int   `json:"ping_check_last_latency_ms"`
	DefaultRouteIntent bool   `json:"default_route_intent"`
	IsActiveDefault    bool   `json:"is_active_default"`
	ActiveDefaultKnown bool   `json:"active_default_known"`
	Note               string `json:"note"`
}

// miniappTunnelFromEvent projects one latest-event row into the mini app's
// tunnel view. Returns false for rows that are not per-tunnel checks (dns,
// external_reach, hydraroute, awg_manager, and the synthetic "tunnels").
//
// Details are best-effort: the agent/backend detail contract is an unversioned
// map, so a malformed or empty blob degrades to identity from the check name
// rather than dropping the tunnel off the screen.
func miniappTunnelFromEvent(row db.EventRow) (miniappTunnel, bool) {
	if !strings.HasPrefix(row.CheckName, miniappTunnelPrefix) {
		return miniappTunnel{}, false
	}
	id := strings.TrimPrefix(row.CheckName, miniappTunnelPrefix)
	if id == "" {
		return miniappTunnel{}, false
	}

	out := miniappTunnel{TunnelID: id, Status: row.Status}
	if !row.TS.IsZero() {
		out.TS = row.TS.UTC().Format(time.RFC3339)
	}

	var d miniappTunnelDetails
	if err := json.Unmarshal([]byte(row.DetailsJSON), &d); err != nil {
		return out, true
	}
	if d.TunnelID != "" {
		out.TunnelID = d.TunnelID
	}
	out.Name = d.TunnelName
	out.RunState = d.Status
	out.Enabled = d.Enabled
	out.HandshakeAgeSec = d.HandshakeAgeSec
	out.PingCheckStatus = d.PingCheckStatus
	out.PingLatencyMs = d.PingLastLatencyMs
	out.DefaultRouteIntent = d.DefaultRouteIntent
	out.ActiveDefaultKnown = d.ActiveDefaultKnown
	out.IsActiveDefault = d.ActiveDefaultKnown && d.IsActiveDefault
	out.Note = d.Note
	return out, true
}
