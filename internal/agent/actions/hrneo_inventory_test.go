package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestBuildHRNeoInventory_FiltersHydraRouteRules(t *testing.T) {
	inv := buildHRNeoInventory(&awgmgr.HydraRouteStatus{Installed: true, Running: true}, []awgmgr.DNSRoute{
		{
			ID:            "hr:AI",
			Name:          "AI",
			Enabled:       true,
			Backend:       "hydraroute",
			HRRouteMode:   "policy",
			HRPolicyName:  "HydraRoute",
			Domains:       []string{"openai.com", "10.42.0.0/16"},
			ManualDomains: []string{"chatgpt.com", "2001:db8::1"},
			Routes:        []awgmgr.DNSRouteEntry{{Interface: "nwg1", TunnelID: "nwg1"}},
		},
		{
			ID:      "ndms:Search",
			Name:    "Search",
			Enabled: true,
			Backend: "ndms",
			Domains: []string{"example.com"},
			Routes:  []awgmgr.DNSRouteEntry{{Interface: "nwg0", TunnelID: "nwg0"}},
		},
		{
			ID:           "hr:Fallthrough",
			Name:         "Fallthrough",
			Enabled:      false,
			Backend:      "hydraroute",
			HRPolicyName: "Default",
			Domains:      []string{"telegram.org"},
		},
	})

	if !inv.Status.Installed || !inv.Status.Running {
		t.Fatalf("status: %+v", inv.Status)
	}
	if len(inv.Rules) != 2 {
		t.Fatalf("rules: %d, want 2: %+v", len(inv.Rules), inv.Rules)
	}

	first := inv.Rules[0]
	if first.ID != "hr:AI" || first.Name != "AI" || !first.Enabled {
		t.Fatalf("first rule identity: %+v", first)
	}
	if first.Bind != "nwg1" || first.Mode != "policy" || first.PolicyName != "HydraRoute" {
		t.Fatalf("first rule metadata: %+v", first)
	}
	if got, want := first.Domains, []string{"openai.com"}; !stringSlicesEqual(got, want) {
		t.Fatalf("domains: %v, want %v", got, want)
	}
	if got, want := first.ManualDomains, []string{"chatgpt.com"}; !stringSlicesEqual(got, want) {
		t.Fatalf("manual domains: %v, want %v", got, want)
	}
	if got, want := first.Routes, []string{"10.42.0.0/16", "2001:db8::1"}; !stringSlicesEqual(got, want) {
		t.Fatalf("routes: %v, want %v", got, want)
	}

	second := inv.Rules[1]
	if second.ID != "hr:Fallthrough" || second.Bind != "" || second.Enabled {
		t.Fatalf("fallthrough rule: %+v", second)
	}
}

func TestHRNeoInventoryJSON_FetchesStatusAndDNSRoutes(t *testing.T) {
	var statusHits, dnsHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		statusHits++
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":false}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		dnsHits++
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:Vk","name":"Vk","enabled":true,"backend":"hydraroute","hrRouteMode":"policy","hrPolicyName":"HydraRoute","domains":["vk.com"],"manualDomains":["10.10.0.0/16"],"routes":[{"interface":"nwg0","tunnelId":"nwg0"}]},
			{"id":"ndms:Example","name":"Example","enabled":true,"backend":"ndms","domains":["example.com"],"routes":[{"interface":"nwg1","tunnelId":"nwg1"}]}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := HRNeoInventoryJSON(context.Background(), awgmgr.New(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if statusHits != 1 || dnsHits != 1 {
		t.Fatalf("endpoint hits: status=%d dns=%d", statusHits, dnsHits)
	}

	var inv wire.HRNeoInventory
	if err := json.Unmarshal([]byte(out), &inv); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if inv.Status.Installed != true || inv.Status.Running != false {
		t.Fatalf("status: %+v", inv.Status)
	}
	if len(inv.Rules) != 1 {
		t.Fatalf("rules: %+v", inv.Rules)
	}
	rule := inv.Rules[0]
	if rule.ID != "hr:Vk" || rule.Bind != "nwg0" || rule.PolicyName != "HydraRoute" {
		t.Fatalf("rule: %+v", rule)
	}
	if got, want := rule.Routes, []string{"10.10.0.0/16"}; !stringSlicesEqual(got, want) {
		t.Fatalf("routes: %v, want %v", got, want)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
