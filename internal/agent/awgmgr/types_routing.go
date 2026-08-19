package awgmgr

import "encoding/json"

// DNSRouteEntry is one element of DNSRoute.Routes — explicit tunnel binding.
// `Interface` is the fresh kernel iface from /api/routing/tunnels (e.g.
// "nwg1", "eth3"). For managed tunnels `TunnelID` must use the stable
// AWG Manager routing/NDMS id when available; iface-only tunnelId values can
// be rejected by AWG Manager even when Interface itself is correct.
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
	Subnets            []string        `json:"subnets,omitempty"`
	ManualDomains      []string        `json:"manualDomains"`
	ManualText         string          `json:"manualText,omitempty"`
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

// AccessPolicy mirrors one entry of /api/routing/access-policies .data[].
//
// The interface chain here IS the binding for every DNS rule with
// hrRouteMode=="policy": awg-manager keeps it on the policy, not on the rule.
// A rule's own hrPolicyInterfaces field belongs to hrRouteMode=="interface"
// and is empty for policy-mode rules — reading it for them yields nothing,
// which is exactly the bug this type exists to fix.
type AccessPolicy struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Standalone  bool                    `json:"standalone"`
	Interfaces  []AccessPolicyInterface `json:"interfaces"`
	DeviceCount int                     `json:"deviceCount"`
	IsStandard  bool                    `json:"isStandard"`
}

// AccessPolicyInterface is one link of a policy's priority chain.
//
// Name is the NDMS/RCI interface name ("OpkgTun11", "Wireguard0",
// "GigabitEthernet1") — NOT the kernel iface ("opkgtun11", "nwg0", "eth3")
// that /api/routing/tunnels and DNS route bindings use. Label is the human
// name and equals the tunnel's name for managed tunnels.
//
// Order is the priority: lower wins, and the first link that is up carries the
// policy's traffic.
type AccessPolicyInterface struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Order int    `json:"order"`
}

// PolicyInterface mirrors /api/routing/policy-interfaces .data[] — the
// catalogue of interfaces a policy may reference, with live up/down state.
// It is also the only authoritative source of the exact interface name
// /api/access-policies/permit accepts.
type PolicyInterface struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Up    bool   `json:"up"`
}
