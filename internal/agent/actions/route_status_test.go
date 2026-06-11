package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// fakeAwgmgrStatus serves canned JSON for the four endpoints route_status
// hits. Designed for both happy-path (with HR-Neo installed) and HR-absent.
func fakeAwgmgrStatus(t *testing.T, hrInstalled bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		body := `{"success":true,"data":{"installed":false,"running":false}}`
		if hrInstalled {
			body = `{"success":true,"data":{"installed":true,"running":true}}`
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","name":"amnezia","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true},
			{"id":"t2","name":"newtun","interfaceName":"nwg0","ndmsName":"Wireguard0","enabled":true,"defaultRoute":false}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"t1","name":"amnezia","iface":"nwg1","type":"managed","status":"running","available":true},
			{"id":"t2","name":"newtun","iface":"nwg0","type":"managed","status":"running","available":true},
			{"id":"wan-eth3","name":"ISP","iface":"eth3","type":"wan","status":"up","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		// 4 enabled rules plus disabled noise that must stay out of counts.
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:Vk","backend":"hydraroute","routes":[{"interface":"nwg1","tunnelId":"nwg1"}]},
			{"id":"ndms:Yandex","backend":"ndms","routes":[{"interface":"nwg1","tunnelId":"nwg1"}]},
			{"id":"hr:Cn","backend":"hydraroute","routes":[{"interface":"nwg0","tunnelId":"nwg0"}]},
			{"id":"hr:Sber","backend":"hydraroute","routes":[{"interface":"eth3","tunnelId":"eth3"}]},
			{"id":"hr:Disabled","backend":"hydraroute","enabled":false,"routes":[{"interface":"nwg1","tunnelId":"nwg1"}]}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"s1","tunnelID":"nwg1"},
			{"id":"s2","tunnelID":"eth3"},
			{"id":"s-disabled","tunnelID":"nwg1","enabled":false}
		]}`))
	})
	return httptest.NewServer(mux)
}

func TestRouteStatus_HappyPath(t *testing.T) {
	srv := fakeAwgmgrStatus(t, true)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := RouteStatus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if !snap.HRNeo.Installed || !snap.HRNeo.Running {
		t.Errorf("HR-Neo: %+v", snap.HRNeo)
	}
	if len(snap.Tunnels) != 3 {
		t.Errorf("tunnels: %d", len(snap.Tunnels))
	}
	if snap.Counts["t1"].DNS != 2 || snap.Counts["t1"].Static != 1 || snap.Counts["t1"].HRNeo != 1 {
		t.Errorf("t1 counts: %+v", snap.Counts["t1"])
	}
	if snap.Counts["t2"].DNS != 1 || snap.Counts["t2"].Static != 0 || snap.Counts["t2"].HRNeo != 1 {
		t.Errorf("t2 counts: %+v", snap.Counts["t2"])
	}
	if snap.Counts["eth3"].DNS != 1 || snap.Counts["eth3"].Static != 1 || snap.Counts["eth3"].HRNeo != 1 {
		t.Errorf("eth3 counts: %+v", snap.Counts["eth3"])
	}
	if snap.Other.DNS != 0 || snap.Other.Static != 0 {
		t.Errorf("other counts: %+v", snap.Other)
	}
	var sawDisabled bool
	for _, r := range snap.Rules {
		if r.ID == "hr:Disabled" && !r.Enabled {
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Fatalf("disabled route should remain visible in rules: %+v", snap.Rules)
	}
	t1, t2 := snap.Tunnels[0], snap.Tunnels[1]
	if !t1.DefaultRoute || t2.DefaultRoute {
		t.Errorf("default_route flags: t1=%v t2=%v (want true,false)", t1.DefaultRoute, t2.DefaultRoute)
	}
}

func TestRouteStatus_HRNeoAbsent(t *testing.T) {
	srv := fakeAwgmgrStatus(t, false)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := RouteStatus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	_ = json.Unmarshal([]byte(out), &snap)
	if snap.HRNeo.Installed {
		t.Errorf("HR-Neo should not be reported installed")
	}
}

func TestRouteStatus_RoutingTunnelsErrorAddsWarning(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","name":"amnezia","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteStatus(context.Background(), awgmgr.New(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Warnings) == 0 || !strings.Contains(snap.Warnings[0], "/api/routing/tunnels") {
		t.Fatalf("expected routing warning, got %+v", snap.Warnings)
	}
}

func TestRouteStatus_FallthroughRulesAreCounted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:A","backend":"hydraroute","routes":null,"hrPolicyName":"HydraRoute"},
			{"id":"hr:B","backend":"hydraroute","routes":null,"hrPolicyName":"HydraRoute"},
			{"id":"hr:C","backend":"hydraroute","routes":null,"hrPolicyName":"HydraRoute"},
			{"id":"hr:D-disabled","backend":"hydraroute","enabled":false,"routes":null,"hrPolicyName":"HydraRoute"}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, _ := RouteStatus(context.Background(), c)
	var snap wire.RouteSnapshot
	_ = json.Unmarshal([]byte(out), &snap)
	if snap.Counts["t1"].DNS != 3 || snap.Counts["t1"].HRNeo != 3 {
		t.Errorf("expected 3 fall-through DNS+HR on t1, got %+v", snap.Counts["t1"])
	}
}

func TestRouteStatus_DoesNotCreditFallthroughToDisabledDefaultTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg10","interfaceName":"nwg0","ndmsName":"Wireguard0","enabled":false,"defaultRoute":true}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:ProxyDefault","backend":"hydraroute","enabled":true,"routes":null,"hrPolicyName":"HydraRoute"}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := awgmgr.New(srv.URL)

	out, err := RouteStatus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if got := snap.Counts["awg10"]; got.DNS != 0 || got.HRNeo != 0 {
		t.Fatalf("disabled default tunnel must not receive fallthrough counts: %+v", got)
	}
	if snap.Other.DNS != 1 || snap.Other.HRNeo != 1 {
		t.Fatalf("enabled fallthrough rule should stay in Other when default tunnel is disabled: %+v", snap.Other)
	}
}

func TestRouteStatus_CreditsNDMSNameBoundRulesToManagedTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","name":"amnezia","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true},
			{"id":"t2","name":"newtun","interfaceName":"nwg0","ndmsName":"Wireguard0","enabled":true}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"Wireguard1","name":"amnezia","iface":"Wireguard1","type":"managed","status":"running","available":true},
			{"id":"Wireguard0","name":"newtun","iface":"Wireguard0","type":"managed","status":"running","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"ndms:Legacy","backend":"ndms","routes":[{"interface":"Wireguard1","tunnelId":"Wireguard1"}]},
			{"id":"hr:Legacy","backend":"hydraroute","routes":[{"interface":"Wireguard1","tunnelId":"Wireguard1"}]},
			{"id":"ndms:Other","backend":"ndms","routes":[{"interface":"Wireguard0","tunnelId":"Wireguard0"}]}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"s1","tunnelID":"Wireguard1"}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := awgmgr.New(srv.URL)

	out, err := RouteStatus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if snap.Counts["t1"].DNS != 2 || snap.Counts["t1"].Static != 1 || snap.Counts["t1"].HRNeo != 1 {
		t.Fatalf("t1 counts: %+v, want Wireguard1-bound DNS/static credited to managed tunnel", snap.Counts["t1"])
	}
	if snap.Counts["t2"].DNS != 1 {
		t.Fatalf("t2 counts: %+v, want Wireguard0-bound DNS credited to t2", snap.Counts["t2"])
	}
	if snap.Other.DNS != 0 || snap.Other.Static != 0 {
		t.Fatalf("NDMS-name managed binds must not fall into Other: %+v", snap.Other)
	}
}

func TestRouteStatus_CreditsFreshRoutingIfaceToManagedTunnelWhenRoutingIDDiffers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg12","name":"Primary","interfaceName":"stale-nwg3","ndmsName":"Wireguard3","enabled":true,"defaultRoute":false}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"Wireguard3","name":"Primary","iface":"nwg5","type":"managed","status":"running","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:Fresh","backend":"hydraroute","routes":[{"interface":"nwg5","tunnelId":"nwg5"}]}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"s-fresh","tunnelID":"nwg5"}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteStatus(context.Background(), awgmgr.New(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if snap.Counts["awg12"].DNS != 1 || snap.Counts["awg12"].Static != 1 || snap.Counts["awg12"].HRNeo != 1 {
		t.Fatalf("fresh routing iface should be credited to managed tunnel: counts=%+v other=%+v tunnels=%+v", snap.Counts["awg12"], snap.Other, snap.Tunnels)
	}
	if snap.Other.DNS != 0 || snap.Other.Static != 0 {
		t.Fatalf("fresh managed iface must not fall into Other: %+v", snap.Other)
	}
}

func TestRouteStatus_DoesNotCreditHRNeoDirectProviderPolicyToDefaultTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:ProviderDirect","backend":"hydraroute","hrRouteMode":"direct","hrPolicyName":"Provider","routes":null},
			{"id":"hr:ProxyDefault","backend":"hydraroute","hrRouteMode":"proxy","hrPolicyName":"HydraRoute","routes":null}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := awgmgr.New(srv.URL)

	out, err := RouteStatus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if snap.Counts["t1"].DNS != 1 || snap.Counts["t1"].HRNeo != 1 {
		t.Fatalf("default tunnel counts: %+v, want only proxy/default HR-Neo credited", snap.Counts["t1"])
	}
	if snap.Other.DNS != 1 || snap.Other.HRNeo != 1 {
		t.Fatalf("direct provider policy should stay in Other/policy bucket, got %+v", snap.Other)
	}
}

func TestRouteStatus_ReportsHRNeoPolicyInterfacesWithoutCreditingSingleTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","name":"first-default","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true},
			{"id":"t2","name":"actual-policy","interfaceName":"nwg5","ndmsName":"Wireguard5","enabled":true,"defaultRoute":true}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:YOUTUBE","backend":"hydraroute","enabled":true,"routes":null,"hrRouteMode":"policy","hrPolicyName":"HydraRoute","hrPolicyInterfaces":["Wireguard5","Wireguard1"]}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteStatus(context.Background(), awgmgr.New(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if snap.Counts["t1"].DNS != 0 || snap.Counts["t1"].HRNeo != 0 {
		t.Fatalf("default tunnel must not receive policy-interface rule: %+v", snap.Counts["t1"])
	}
	if snap.Counts["t2"].DNS != 0 || snap.Counts["t2"].HRNeo != 0 {
		t.Fatalf("policy-interface rule must not look bound to one tunnel: %+v", snap.Counts["t2"])
	}
	if len(snap.Policies) != 1 {
		t.Fatalf("policies: %+v", snap.Policies)
	}
	p := snap.Policies[0]
	if p.Name != "HydraRoute" || p.DNS != 1 || p.HRNeo != 1 {
		t.Fatalf("bad policy summary: %+v", p)
	}
	if len(p.Interfaces) != 2 || p.Interfaces[0].Name != "actual-policy" || p.Interfaces[0].Bind != "nwg5" || p.Interfaces[0].Role != "active" || p.Interfaces[1].Name != "first-default" || p.Interfaces[1].Bind != "nwg1" || p.Interfaces[1].Role != "fallback" {
		t.Fatalf("bad policy interfaces: %+v", p.Interfaces)
	}
}
