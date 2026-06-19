package awgmgr

import (
	"encoding/json"
	"testing"
)

// Real shape from snekhaev's /api/settings/get .data (sing-box router active,
// routeTag "direct"). Guards the struct tags that drive sing-box detection.
func TestSettingsSingboxRouterActive(t *testing.T) {
	const data = `{"download":{"routeTag":"direct","routeKind":"direct"},` +
		`"singboxRouter":{"enabled":true,"policyName":"","deviceMode":"all","wanAutoDetect":true}}`
	var s Settings
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		t.Fatal(err)
	}
	if !s.SingboxRouterActive() {
		t.Fatal("singboxRouter.enabled=true should be active")
	}
	if s.SingboxRouter.DeviceMode != "all" {
		t.Fatalf("deviceMode = %q, want all", s.SingboxRouter.DeviceMode)
	}
	// routeTag "direct" (no "<kind>-<id>") is not a real tunnel id.
	if got := s.ActiveDefaultTunnelID(); got != "direct" {
		t.Fatalf("ActiveDefaultTunnelID = %q, want direct", got)
	}
}

// A DNS route with subnets + manualText (snekhaev shape) must survive a
// list→marshal→update round-trip; awg-manager update is full-replace, so a
// missing struct field silently erases data on the router.
func TestDNSRouteRoundTripPreservesSubnetsAndManualText(t *testing.T) {
	const raw = `{"id":"list_1","name":"Telegram","domains":["t.me"],` +
		`"subnets":["149.154.160.0/20"],"manualDomains":["t.me","149.154.160.0/20"],` +
		`"manualText":"t.me\n149.154.160.0/20",` +
		`"routes":[{"interface":"Wireguard1","tunnelId":"awg10"}],"enabled":false,"backend":"ndms"}`
	var r DNSRoute
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Subnets) != 1 || r.Subnets[0] != "149.154.160.0/20" {
		t.Fatalf("subnets not parsed: %v", r.Subnets)
	}
	if r.ManualText == "" {
		t.Fatal("manualText not parsed")
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var r2 DNSRoute
	if err := json.Unmarshal(out, &r2); err != nil {
		t.Fatal(err)
	}
	if len(r2.Subnets) != 1 || r2.ManualText != r.ManualText {
		t.Fatalf("round-trip dropped subnets/manualText: %s", out)
	}
}

func TestSettingsSingboxRouterInactive(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"singboxRouter":{"enabled":false,"deviceMode":"all"}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.SingboxRouterActive() {
		t.Fatal("disabled sing-box router must be inactive")
	}
	// nil receiver is safe.
	var nilS *Settings
	if nilS.SingboxRouterActive() {
		t.Fatal("nil settings must be inactive")
	}
}
