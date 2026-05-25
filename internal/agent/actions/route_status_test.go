package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
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
		// 4 rules: 2 explicit on nwg1 (one hr, one ndms), 1 on nwg0, 1 on WAN (eth3)
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:Vk","backend":"hydraroute","routes":[{"interface":"nwg1","tunnelId":"nwg1"}]},
			{"id":"ndms:Yandex","backend":"ndms","routes":[{"interface":"nwg1","tunnelId":"nwg1"}]},
			{"id":"hr:Cn","backend":"hydraroute","routes":[{"interface":"nwg0","tunnelId":"nwg0"}]},
			{"id":"hr:Sber","backend":"hydraroute","routes":[{"interface":"eth3","tunnelId":"eth3"}]}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"s1","tunnelID":"nwg1"},
			{"id":"s2","tunnelID":"eth3"}
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
			{"id":"hr:C","backend":"hydraroute","routes":null,"hrPolicyName":"HydraRoute"}
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
