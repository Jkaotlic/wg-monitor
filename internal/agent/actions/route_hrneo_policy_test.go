package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// TestAddIfaceToHydraRoutePolicies_SkipsGlobalDefaultPolicies pins the import
// behaviour decided 2026-06-18: a freshly imported tunnel must only be APPENDED
// to HR-Neo policies that already pin an explicit interface chain (landing as a
// fallback). Policies with an empty interface list are global-default — they
// follow the live default route, and turning them into [newIface] would make
// the new tunnel their sole/active egress and hijack all their traffic. Those
// must be left untouched.
func TestHRNeoPolicyRoute_PinsInterfaceThroughRoutes(t *testing.T) {
	got := hrNeoPolicyRoute("Streaming", []string{"netflix.com"}, "nwg1", "awg11")
	// hrRouteMode -- это enum interface|policy. В режиме policy привязку даёт
	// политика, а hrPolicyInterfaces игнорируется: прежняя комбинация
	// mode=policy + непустой hrPolicyInterfaces была самопротиворечивой,
	// и правило уходило не туда, куда просили.
	if got.HRRouteMode != "interface" {
		t.Errorf("HRRouteMode = %q, want interface", got.HRRouteMode)
	}
	// Привязка правила к интерфейсу живёт в routes[0]. Interface — это ядерный iface,
	// TunnelID — это стабильный bind id (два разных namespace'а).
	if len(got.Routes) != 1 || got.Routes[0].Interface != "nwg1" || got.Routes[0].TunnelID != "awg11" {
		t.Errorf("Routes = %+v", got.Routes)
	}
	if len(got.HRPolicyInterfaces) != 0 {
		t.Errorf("HRPolicyInterfaces = %+v, want empty: the binding is in routes", got.HRPolicyInterfaces)
	}
	if got.HRPolicyName != "" {
		t.Errorf("HRPolicyName = %q, want empty: the rule is pinned to an interface, not to a policy", got.HRPolicyName)
	}
	if got.Backend != "hydraroute" || !got.Enabled {
		t.Errorf("rule = %+v", got)
	}
}

func TestAddIfaceToHydraRoutePolicies_SkipsGlobalDefaultPolicies(t *testing.T) {
	var updatedIDs []string
	var updatedBodies []awgmgr.DNSRoute
	mux := http.NewServeMux()
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:Global","name":"Global","backend":"hydraroute","enabled":true,"routes":null,"hrRouteMode":"policy","hrPolicyName":"HydraRoute"},
			{"id":"hr:Pinned","name":"Pinned","backend":"hydraroute","enabled":true,"routes":null,"hrRouteMode":"policy","hrPolicyName":"HydraRoute","hrPolicyInterfaces":["nwg1"]}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/update", func(w http.ResponseWriter, r *http.Request) {
		updatedIDs = append(updatedIDs, r.URL.Query().Get("id"))
		var body awgmgr.DNSRoute
		_ = json.NewDecoder(r.Body).Decode(&body)
		updatedBodies = append(updatedBodies, body)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	changed, err := addIfaceToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), "nwg7")
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed=%d, want 1 (only the explicitly-pinned policy)", changed)
	}
	if len(updatedIDs) != 1 || updatedIDs[0] != "hr:Pinned" {
		t.Fatalf("updated IDs=%v, want only [hr:Pinned]; global-default policy must be skipped", updatedIDs)
	}
	if got := strings.Join(updatedBodies[0].HRPolicyInterfaces, ","); got != "nwg1,nwg7" {
		t.Fatalf("pinned policy interfaces=%q, want nwg1,nwg7 (new tunnel appended as fallback)", got)
	}
}
