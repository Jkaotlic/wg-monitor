package awgmgr

import (
	"encoding/json"
	"testing"
)

// Real shape from client-c's /api/settings/get .data (sing-box router active,
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
