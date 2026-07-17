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

// Traffic modes. These answer the operator's daily question -- "does traffic go
// direct or through the VPN" -- and the honest fourth answer, "we cannot tell".
const (
	miniappTrafficVPN     = "vpn"
	miniappTrafficDirect  = "direct"
	miniappTrafficSingbox = "singbox"
	miniappTrafficUnknown = "unknown"
)

// miniappTraffic is the screen's headline answer.
type miniappTraffic struct {
	Mode             string `json:"mode"`
	EgressTunnelID   string `json:"egress_tunnel_id,omitempty"`
	EgressTunnelName string `json:"egress_tunnel_name,omitempty"`
	// ContestedDefault: more than one tunnel claims defaultRoute=true. Real and
	// common (the operator's own router does it). Worth surfacing either way: when
	// we know the egress it explains why the other tunnel looks idle; when we
	// don't, it is precisely why.
	ContestedDefault bool `json:"contested_default"`
}

// miniappHydraDetails decodes only the sing-box flag out of the hydraroute check.
type miniappHydraDetails struct {
	SingboxRouterActive bool `json:"singbox_router_active"`
}

// miniappDeriveTraffic answers "direct or via VPN" from stored state alone.
//
// Order matters. sing-box wins first: when its tproxy router is active it picks a
// route per destination, so "the default route" is not the question anymore and
// any single answer would be a lie (the external_reach probe doesn't even run on
// those routers -- cmd/agent/main.go:304-318).
//
// Otherwise the answer is only as good as the agent: is_active_default comes from
// settings.download.routeTag and is the sole authority. Without it (agent older
// than the routeTag change, or /api/settings/get down) we return "unknown" rather
// than falling back to default_route_intent -- several tunnels can each claim it,
// so that fallback is a coin flip, and the operator's own router is exactly that
// case.
func miniappDeriveTraffic(tunnels []miniappTunnel, byCheck map[string]db.EventRow) miniappTraffic {
	out := miniappTraffic{Mode: miniappTrafficUnknown}

	claimed := 0
	for _, t := range tunnels {
		if t.DefaultRouteIntent {
			claimed++
		}
	}
	out.ContestedDefault = claimed > 1

	if row, ok := byCheck["hydraroute"]; ok {
		var hd miniappHydraDetails
		if json.Unmarshal([]byte(row.DetailsJSON), &hd) == nil && hd.SingboxRouterActive {
			out.Mode = miniappTrafficSingbox
			return out
		}
	}

	if len(tunnels) == 0 {
		return out
	}

	known := false
	for _, t := range tunnels {
		if !t.ActiveDefaultKnown {
			continue
		}
		known = true
		if t.IsActiveDefault {
			out.Mode = miniappTrafficVPN
			out.EgressTunnelID = t.TunnelID
			out.EgressTunnelName = t.Name
			return out
		}
	}
	if known {
		// awg-manager answered, and no tunnel is the egress: traffic leaves
		// through the WAN.
		out.Mode = miniappTrafficDirect
	}
	return out
}
