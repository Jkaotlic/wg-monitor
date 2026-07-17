package backend

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func TestMiniappTunnelFromEventProjectsWhitelistedFields(t *testing.T) {
	row := db.EventRow{
		CheckName:   "tunnel_awg12",
		Status:      "ok",
		TS:          time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		DetailsJSON: `{"tunnel_id":"awg12","tunnel_name":"amst","status":"running","enabled":true,"handshake_age_sec":45,"ping_check_status":"alive","ping_check_last_latency_ms":12,"default_route_intent":true,"is_active_default":true,"active_default_known":true,"endpoint":"1.2.3.4:51820","address":"10.0.0.2/32","isp_interface":"ISP"}`,
	}

	got, ok := miniappTunnelFromEvent(row)
	if !ok {
		t.Fatal("tunnel_ row must project")
	}
	if got.TunnelID != "awg12" || got.Name != "amst" {
		t.Fatalf("identity: got %+v", got)
	}
	if got.HandshakeAgeSec == nil || *got.HandshakeAgeSec != 45 {
		t.Fatalf("handshake_age_sec not projected: %+v", got.HandshakeAgeSec)
	}
	if got.PingCheckStatus != "alive" || !got.IsActiveDefault || !got.ActiveDefaultKnown {
		t.Fatalf("state: got %+v", got)
	}
	if got.Enabled == nil || !*got.Enabled {
		t.Fatalf("enabled: details carried the key as true, got %+v", got.Enabled)
	}
}

// An agent that hasn't shipped the enabled key yet -- or a details blob that
// simply omits it -- must not be read as "disabled" or, worse, defaulted to
// "enabled" and presented as fact. Absence has to survive as nil.
func TestMiniappTunnelFromEventEnabledUnknownWhenKeyAbsent(t *testing.T) {
	row := db.EventRow{
		CheckName:   "tunnel_awg13",
		Status:      "ok",
		DetailsJSON: `{"tunnel_id":"awg13","tunnel_name":"riga"}`,
	}
	got, ok := miniappTunnelFromEvent(row)
	if !ok {
		t.Fatal("must project")
	}
	if got.Enabled != nil {
		t.Fatalf("enabled must be nil (unknown) when the details omit the key, got %v", *got.Enabled)
	}
}

// The mini app is read by owners and operators, not only the admin. Router
// topology must not ride along just because it happens to sit in the same
// details map.
func TestMiniappTunnelFromEventDropsTopology(t *testing.T) {
	row := db.EventRow{
		CheckName:   "tunnel_awg12",
		Status:      "ok",
		DetailsJSON: `{"tunnel_id":"awg12","endpoint":"1.2.3.4:51820","address":"10.0.0.2/32","isp_interface":"ISP","ndms_name":"Wireguard0","interface":"nwg3"}`,
	}
	got, ok := miniappTunnelFromEvent(row)
	if !ok {
		t.Fatal("must project")
	}
	blob := mustJSON(t, got)
	for _, leak := range []string{"1.2.3.4", "51820", "10.0.0.2", "ISP", "Wireguard0", "nwg3"} {
		if contains(blob, leak) {
			t.Fatalf("topology %q leaked into mini-app JSON: %s", leak, blob)
		}
	}
}

func TestMiniappTunnelFromEventIgnoresNonTunnelRows(t *testing.T) {
	for _, name := range []string{"dns", "external_reach", "hydraroute", "awg_manager", "tunnels"} {
		if _, ok := miniappTunnelFromEvent(db.EventRow{CheckName: name, Status: "ok"}); ok {
			t.Fatalf("%q is not a per-tunnel row", name)
		}
	}
}

// An agent older than Task 1 sends neither key. We must report "unknown",
// never fall back to default_route_intent -- on a two-default router that is
// a coin flip presented as fact.
func TestMiniappTunnelFromEventOldAgentEgressUnknown(t *testing.T) {
	row := db.EventRow{
		CheckName:   "tunnel_awg10",
		Status:      "ok",
		DetailsJSON: `{"tunnel_id":"awg10","tunnel_name":"nkt","default_route_intent":true}`,
	}
	got, ok := miniappTunnelFromEvent(row)
	if !ok {
		t.Fatal("must project")
	}
	if got.ActiveDefaultKnown {
		t.Fatal("old agent sends no active_default_known, want false")
	}
	if got.IsActiveDefault {
		t.Fatal("unknown egress must never be claimed as active")
	}
	if !got.DefaultRouteIntent {
		t.Fatal("intent is still worth showing -- it just isn't the answer")
	}
}

func TestMiniappTunnelFromEventSurvivesGarbageDetails(t *testing.T) {
	for _, blob := range []string{"", "null", "not json", "[]"} {
		got, ok := miniappTunnelFromEvent(db.EventRow{CheckName: "tunnel_awg1", Status: "fail", DetailsJSON: blob})
		if !ok {
			t.Fatalf("details %q: row must still project from its check name", blob)
		}
		if got.TunnelID != "awg1" {
			t.Fatalf("details %q: tunnel id must fall back to the check-name suffix, got %q", blob, got.TunnelID)
		}
	}
}
