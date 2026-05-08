package wire

import (
	"encoding/json"
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
