package awgmgr

import "encoding/json"

// DNSRouteEntry is one element of DNSRoute.Routes — explicit tunnel binding.
// `Interface` and `TunnelID` carry the same value (the iface from
// /api/routing/tunnels, e.g. "nwg1", "eth3"). awg-manager UI sets both.
type DNSRouteEntry struct {
	Interface string `json:"interface"`
	TunnelID  string `json:"tunnelId"`
	Fallback  string `json:"fallback,omitempty"`
}

// DNSRoute mirrors one entry of /api/dns-routes/list .data[]. A rule with
// Routes=nil falls through to the global engine policy (HRPolicyName for
// hydraroute). Setting an explicit Routes converts it to direct routing.
//
// All fields are preserved verbatim on update (awg-manager treats update
// as full-replace) — never drop unknown fields.
type DNSRoute struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Domains            []string        `json:"domains"`
	ManualDomains      []string        `json:"manualDomains"`
	Routes             []DNSRouteEntry `json:"routes"`
	Enabled            bool            `json:"enabled"`
	CreatedAt          string          `json:"createdAt"`
	UpdatedAt          string          `json:"updatedAt"`
	Backend            string          `json:"backend"` // "hydraroute" | "ndms" — engine, not tunnel
	HRRouteMode        string          `json:"hrRouteMode,omitempty"`
	HRPolicyName       string          `json:"hrPolicyName,omitempty"`
	HRPolicyInterfaces []string        `json:"hrPolicyInterfaces,omitempty"`
}

func (r *DNSRoute) UnmarshalJSON(data []byte) error {
	type alias DNSRoute
	var raw struct {
		alias
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = DNSRoute(raw.alias)
	r.Enabled = true
	if raw.Enabled != nil {
		r.Enabled = *raw.Enabled
	}
	return nil
}

// StaticRoute mirrors one entry of /api/static-routes/list .data[].
// CRITICAL: bind field is `tunnelID` with CAPITAL D (different from DNS).
type StaticRoute struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	TunnelID string   `json:"tunnelID"`
	Subnets  []string `json:"subnets"`
	Fallback string   `json:"fallback,omitempty"`
	Enabled  bool     `json:"enabled"`
}

func (r *StaticRoute) UnmarshalJSON(data []byte) error {
	type alias StaticRoute
	var raw struct {
		alias
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = StaticRoute(raw.alias)
	r.Enabled = true
	if raw.Enabled != nil {
		r.Enabled = *raw.Enabled
	}
	return nil
}

// RoutingTunnel mirrors one entry of /api/routing/tunnels .data[].
// `Iface` is the canonical bind value used in DNSRoute.Routes and StaticRoute.TunnelID.
type RoutingTunnel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Iface     string `json:"iface"`
	Type      string `json:"type"`   // "managed" | "system" | "wan"
	Status    string `json:"status"` // "running" | "up" | "down" | …
	Available bool   `json:"available"`
}

type PresetsListResponse struct {
	Presets []Preset `json:"presets"`
}

type Preset struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Category string        `json:"category"`
	Engines  PresetEngines `json:"engines"`
}

type PresetEngines struct {
	DNS        PresetDNSEngine        `json:"dns"`
	HydraRoute PresetHydraRouteEngine `json:"hydraroute"`
}

type PresetDNSEngine struct {
	Domains []string `json:"domains"`
}

type PresetHydraRouteEngine struct {
	GeoTags []string `json:"geoTags"`
}
