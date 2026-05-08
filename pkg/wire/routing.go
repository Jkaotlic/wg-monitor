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
	ID      string `json:"id"`
	Name    string `json:"name"`
	Iface   string `json:"iface"`
	Enabled bool   `json:"enabled"`
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
	Tunnels []TunnelMeta            `json:"tunnels"` // managed tunnels only
	Counts  map[string]TunnelCounts `json:"counts"`  // key = tunnel id
	Other   TunnelCounts            `json:"other"`   // sum across WAN/system/external
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
