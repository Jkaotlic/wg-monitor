// Package wire — routing.go defines payload types for route_status and
// route_rebind. They are JSON-encoded into wire.CommandResult.Output;
// no wire envelope additions are required besides the action names.
package wire

const RouteOtherID = "__other__"

// DefaultEgressDirect is RouteSnapshot.DefaultEgress when the router routes
// unclaimed traffic past every tunnel, straight to the ISP. awg-manager spells
// it exactly this way in settings.download.routeTag, and it is a statement,
// not a tunnel id we failed to resolve.
const DefaultEgressDirect = "direct"

type HRStatus struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
}

// TunnelMeta is the subset of awgmgr.Tunnel the panel needs for rendering.
// `Iface` is the canonical bind value (matches awgmgr.Tunnel.InterfaceName
// for managed tunnels) and is used by the renderer to label rows.
type TunnelMeta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Iface     string `json:"iface"`
	NDMSName  string `json:"ndms_name,omitempty"`
	Type      string `json:"type,omitempty"`
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available,omitempty"`
	Status    string `json:"status,omitempty"`
	// HasHandshake distinguishes a real zero-second handshake from an older
	// agent/snapshot that did not report handshake data.
	HasHandshake bool   `json:"has_handshake,omitempty"`
	HandshakeAge int    `json:"handshake_age_sec,omitempty"`
	PingStatus   string `json:"ping_status,omitempty"`
	PingFails    int    `json:"ping_fails,omitempty"`
	PingFailMax  int    `json:"ping_fail_max,omitempty"`
	// DefaultRoute marks managed tunnels with `defaultRoute=true`. Used as
	// the heuristic for the global HR-Neo policy default during rebind
	// fall-through conversion.
	DefaultRoute bool `json:"default_route,omitempty"`
	// RestartMethod tells the screen whether a restart button can be offered
	// at all: "control" for awg-manager-managed tunnels (restartable by id via
	// /api/control/restart), "none" for system/WAN entries that only appear in
	// the routing catalogue. Empty means an older agent that did not report it.
	RestartMethod string `json:"restart_method,omitempty"`
}

// TunnelCounts tracks rules attached to a single tunnel by category.
// HRNeo is a sub-class of DNS — DNS rules with backend="hydraroute".
// Total rules = DNS + Static (HRNeo is INCLUDED in DNS, not added).
// Renderer derives "shown total" = DNS + Static; HRNeo shown separately
// only as informational sub-count.
type TunnelCounts struct {
	DNS    int `json:"dns"`
	Static int `json:"static"`
	HRNeo  int `json:"hr_neo"`
}

type RoutePolicyInterface struct {
	Bind      string `json:"bind"`
	Name      string `json:"name,omitempty"`
	Role      string `json:"role,omitempty"` // active | fallback | unavailable
	Available bool   `json:"available,omitempty"`
	// Order is the policy's own priority number (lower wins). The slice is
	// already sorted by it; the number is kept for the screen to show.
	Order int `json:"order,omitempty"`
	// TunnelID is our tunnel behind this interface. Empty means the interface
	// is not a tunnel of ours — WAN, bridge, guest network.
	TunnelID string `json:"tunnel_id,omitempty"`
	// ViaVPN mirrors TunnelID != "": traffic on this link is protected.
	ViaVPN bool `json:"via_vpn,omitempty"`
}

type RoutePolicySummary struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Interfaces  []RoutePolicyInterface `json:"interfaces,omitempty"`
	DNS         int                    `json:"dns"`
	HRNeo       int                    `json:"hr_neo"`
	// ActiveTunnelID is the tunnel carrying this policy's traffic right now —
	// the first chain link that is up. Empty when that link is not a tunnel of
	// ours, or when nothing in the chain is up. Consumers must attribute the
	// policy's rules to this tunnel and to no other: crediting every link of
	// the chain multiplies the same rules across all of them.
	ActiveTunnelID string `json:"active_tunnel_id,omitempty"`
	// ViaVPN reports whether the active link is a tunnel. False with a
	// non-empty chain means the policy's rules leave the router unprotected.
	ViaVPN bool `json:"via_vpn,omitempty"`
}

// SingboxRouterStatus reports awg-manager's sing-box router method (a third
// routing mechanism alongside NDMS default-route and HR-Neo). When Enabled,
// sing-box routes per its own policy/deviceMode using the tunnels as outbounds,
// so the NDMS/HR-Neo route accounting does not reflect the real client path.
type SingboxRouterStatus struct {
	Enabled    bool   `json:"enabled"`
	DeviceMode string `json:"device_mode,omitempty"`
	PolicyName string `json:"policy_name,omitempty"`
}

// RouteSnapshot is the payload of a successful route_status CommandResult.
type RouteSnapshot struct {
	HRNeo    HRStatus                `json:"hr_neo"`
	Tunnels  []TunnelMeta            `json:"tunnels"` // routable managed/NDMS targets
	Counts   map[string]TunnelCounts `json:"counts"`  // key = target id
	Other    TunnelCounts            `json:"other"`   // sum across unknown/unmatched binds
	Policies []RoutePolicySummary    `json:"policies,omitempty"`
	Rules    []RouteRuleSummary      `json:"rules,omitempty"`
	// SingboxRouter is set when awg-manager's sing-box router is the active
	// routing method; nil otherwise (NDMS/HR-Neo routing).
	SingboxRouter *SingboxRouterStatus `json:"singbox_router,omitempty"`
	// Warnings names non-fatal data source failures. UI must treat the
	// snapshot as partial when present.
	Warnings []string `json:"warnings,omitempty"`
	// DefaultEgress names where traffic goes when no rule claims it — the
	// authoritative answer from awg-manager's settings.download.routeTag,
	// which is the ONLY place that fact lives.
	//
	// It cannot be derived from the per-tunnel DefaultRoute flags: on a live
	// router all three tunnels carry the flag while traffic actually leaves
	// direct, so a consumer computing the default from flags states the exact
	// opposite of the truth. Values: DefaultEgressDirect, a tunnel id present
	// in Tunnels, or "" — the router did not say, which is an answer and must
	// not be replaced by a guess.
	DefaultEgress string `json:"default_egress,omitempty"`
	// PolicyModel marks a snapshot built by an agent that reads awg-manager's
	// access policies. It cannot be inferred from the policy data: a policy
	// whose whole chain leaves the VPN has no tunnel ids anywhere, so a router
	// carrying only such a policy would be indistinguishable from an agent too
	// old to resolve policies at all.
	PolicyModel bool `json:"policy_model,omitempty"`
}

type HRNeoRule struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	Bind          string   `json:"bind,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	PolicyName    string   `json:"policy_name,omitempty"`
	Domains       []string `json:"domains,omitempty"`
	ManualDomains []string `json:"manual_domains,omitempty"`
	Routes        []string `json:"routes,omitempty"`
}

type HRNeoInventory struct {
	Status HRStatus    `json:"status"`
	Rules  []HRNeoRule `json:"rules,omitempty"`
}

type CategoryResult struct {
	OK     int      `json:"ok"`
	Failed int      `json:"failed"`
	Errors []string `json:"errors,omitempty"`
}

// RouteRebindResult is the payload of a route_rebind CommandResult.
// Static is reported separately. HRNeo is the subset of DNS results where
// backend=="hydraroute"; the count of pure-NDMS DNS results = DNS - HRNeo.
type RouteRebindResult struct {
	SrcTunnelID string         `json:"src_tunnel_id"`
	DstTunnelID string         `json:"dst_tunnel_id"`
	DNS         CategoryResult `json:"dns"`
	Static      CategoryResult `json:"static"`
	HRNeo       CategoryResult `json:"hr_neo"`
}

type RouteTarget struct {
	Type  string `json:"type"`  // domain | cidr | opaque
	Value string `json:"value"` // canonical domain or netip prefix
}

type RouteRuleSummary struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Kind    string        `json:"kind"` // dns | static
	Backend string        `json:"backend,omitempty"`
	Enabled bool          `json:"enabled"`
	Bind    string        `json:"bind,omitempty"`
	Targets []RouteTarget `json:"targets,omitempty"`
}

type RouteOverlap struct {
	Severity string           `json:"severity"` // block | warn | info
	Reason   string           `json:"reason"`
	Existing RouteRuleSummary `json:"existing"`
	Target   RouteTarget      `json:"target"`
}

type RouteAddRequest struct {
	Kind       string   `json:"kind"` // dns | static
	Name       string   `json:"name"`
	TunnelID   string   `json:"tunnel_id"`
	Targets    []string `json:"targets"`
	UseHRNeo   bool     `json:"use_hr_neo,omitempty"`
	TemplateID string   `json:"template_id,omitempty"`
	DraftHash  string   `json:"draft_hash,omitempty"`
}

type RouteAddPlan struct {
	Request  RouteAddRequest  `json:"request"`
	Route    RouteRuleSummary `json:"route"`
	Overlaps []RouteOverlap   `json:"overlaps,omitempty"`
	CanApply bool             `json:"can_apply"`
	Hash     string           `json:"hash"`
}

type RouteTemplate struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Category string   `json:"category,omitempty"`
	DNS      []string `json:"dns,omitempty"`
	HRNeo    []string `json:"hr_neo,omitempty"`
}

type RouteTemplates struct {
	Templates []RouteTemplate `json:"templates"`
}

type RouteDeleteRequest struct {
	Kind        string `json:"kind"` // dns | static
	RouteID     string `json:"route_id"`
	PreviewHash string `json:"preview_hash,omitempty"`
}

type RouteDeletePlan struct {
	Route    RouteRuleSummary `json:"route"`
	Warnings []RouteOverlap   `json:"warnings,omitempty"`
	CanApply bool             `json:"can_apply"`
	Hash     string           `json:"hash"`
}

// RoutePolicyPromoteRequest makes a tunnel the first link of a policy's chain.
//
// Reordering only: the tunnel must already be in the chain. awg-manager has no
// reorder endpoint — promotion is `permit` with order 0 for an interface that
// is already a member, which is exactly what the router's own UI does. Adding
// a NEW interface to a policy changes what the policy can fall back to, not
// merely the order, and belongs to the config-replacement wizard.
type RoutePolicyPromoteRequest struct {
	PolicyName string `json:"policy_name"`
	TunnelID   string `json:"tunnel_id"`
}

type RouteApplyResult struct {
	Action         string `json:"action"` // add | delete
	Kind           string `json:"kind"`
	RouteID        string `json:"route_id"`
	RouteName      string `json:"route_name"`
	HRNeoRestarted bool   `json:"hr_neo_restarted,omitempty"`
	Warning        string `json:"warning,omitempty"`
}
