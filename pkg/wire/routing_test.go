package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRouteSnapshot_RoundTrip(t *testing.T) {
	want := RouteSnapshot{
		HRNeo: HRStatus{Installed: true, Running: true},
		Tunnels: []TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true},
		},
		Counts: map[string]TunnelCounts{
			"t1": {DNS: 5, Static: 2, HRNeo: 1},
		},
		Other: TunnelCounts{DNS: 12, Static: 0, HRNeo: 0},
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got RouteSnapshot
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.HRNeo.Installed != true || got.Counts["t1"].DNS != 5 || got.Other.DNS != 12 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestRouteRebindResult_RoundTrip(t *testing.T) {
	want := RouteRebindResult{
		SrcTunnelID: "awg11", DstTunnelID: "awg13",
		DNS:    CategoryResult{OK: 3, Failed: 1, Errors: []string{"err1"}},
		Static: CategoryResult{OK: 0},
		HRNeo:  CategoryResult{OK: 5},
	}
	b, _ := json.Marshal(want)
	var got RouteRebindResult
	_ = json.Unmarshal(b, &got)
	if got.DNS.OK != 3 || got.DNS.Failed != 1 || len(got.DNS.Errors) != 1 {
		t.Errorf("got: %+v", got)
	}
}

func TestRoutePolicySummary_NewFieldsAreOmittedWhenEmpty(t *testing.T) {
	// Старый агент не заполняет новые поля; его снимок обязан сериализоваться
	// байт в байт так же, как до фазы B, иначе сравнения снимков поедят.
	b, err := json.Marshal(RoutePolicySummary{Name: "HydraRoute", DNS: 26, HRNeo: 26})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	want := `{"name":"HydraRoute","dns":26,"hr_neo":26}`
	if got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}

func TestRoutePolicySummary_CarriesActiveTunnel(t *testing.T) {
	b, err := json.Marshal(RoutePolicySummary{
		Name:           "HydraRoute",
		ActiveTunnelID: "awg11",
		ViaVPN:         true,
		Interfaces: []RoutePolicyInterface{
			{Bind: "OpkgTun11", Name: "awg3-work-via-ru1", Role: "active", Available: true, TunnelID: "awg11", ViaVPN: true},
			{Bind: "Wireguard0", Name: "NetherlandsKerkradeS24", Role: "unavailable", Order: 1, TunnelID: "awg20"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"active_tunnel_id":"awg11"`, `"via_vpn":true`, `"tunnel_id":"awg20"`, `"order":1`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json %s missing %s", b, want)
		}
	}
}

func TestTunnelMeta_RestartMethod(t *testing.T) {
	b, _ := json.Marshal(TunnelMeta{ID: "awg11", RestartMethod: "control"})
	if !strings.Contains(string(b), `"restart_method":"control"`) {
		t.Errorf("json = %s", b)
	}
	b, _ = json.Marshal(TunnelMeta{ID: "awg11"})
	if strings.Contains(string(b), "restart_method") {
		t.Errorf("empty restart_method must be omitted: %s", b)
	}
}
