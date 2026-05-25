// Package wire — routing.go defines payload types for route_status and
// route_rebind. They are JSON-encoded into wire.CommandResult.Output;
// no wire envelope additions are required besides the action names.
package wire

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
	Type      string `json:"type,omitempty"`
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available,omitempty"`
	// DefaultRoute marks managed tunnels with `defaultRoute=true`. Used as
	// the heuristic for the global HR-Neo policy default during rebind
	// fall-through conversion.
	DefaultRoute bool `json:"default_route,omitempty"`
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

// RouteSnapshot is the payload of a successful route_status CommandResult.
type RouteSnapshot struct {
	HRNeo   HRStatus                `json:"hr_neo"`
	Tunnels []TunnelMeta            `json:"tunnels"` // routable managed/NDMS targets
	Counts  map[string]TunnelCounts `json:"counts"`  // key = target id
	Other   TunnelCounts            `json:"other"`   // sum across unknown/unmatched binds
	Rules   []RouteRuleSummary      `json:"rules,omitempty"`
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
	Kind      string   `json:"kind"` // dns | static
	Name      string   `json:"name"`
	TunnelID  string   `json:"tunnel_id"`
	Targets   []string `json:"targets"`
	UseHRNeo  bool     `json:"use_hr_neo,omitempty"`
	DraftHash string   `json:"draft_hash,omitempty"`
}

type RouteAddPlan struct {
	Request  RouteAddRequest  `json:"request"`
	Route    RouteRuleSummary `json:"route"`
	Overlaps []RouteOverlap   `json:"overlaps,omitempty"`
	CanApply bool             `json:"can_apply"`
	Hash     string           `json:"hash"`
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

type RouteApplyResult struct {
	Action         string `json:"action"` // add | delete
	Kind           string `json:"kind"`
	RouteID        string `json:"route_id"`
	RouteName      string `json:"route_name"`
	HRNeoRestarted bool   `json:"hr_neo_restarted,omitempty"`
}
