package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRouteAddJSON_CanTargetNDMSRoutingTunnel(t *testing.T) {
	var created awgmgr.DNSRoute
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"wan-eth3","name":"ISP","iface":"eth3","type":"wan","status":"up","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "dns", Name: "ru", TunnelID: "eth3", Targets: []string{"gosuslugi.ru"}}
	plan, err := buildRouteAddPlan(context.Background(), c, req)
	if err != nil {
		t.Fatalf("buildRouteAddPlan: %v", err)
	}
	if plan.Route.Bind != "eth3" {
		t.Fatalf("plan bind = %q, want eth3", plan.Route.Bind)
	}

	req.DraftHash = plan.Hash
	if _, err := RouteAddJSON(context.Background(), c, req); err != nil {
		t.Fatalf("RouteAddJSON: %v", err)
	}
	if created.Backend != "ndms" || len(created.Routes) != 1 || created.Routes[0].Interface != "eth3" || created.Routes[0].TunnelID != "eth3" {
		t.Fatalf("created route = %+v, want NDMS route bound to eth3", created)
	}
}

func TestRouteAddJSON_ReturnsWarningWhenPostCreateRefreshFails(t *testing.T) {
	var created bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"wan-eth3","name":"ISP","iface":"eth3","type":"wan","status":"up","available":true}]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		created = true
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	refreshCalls := 0
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		if refreshCalls == 1 {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"message":"refresh failed"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteAddJSON(context.Background(), awgmgr.New(srv.URL), wire.RouteAddRequest{Kind: "dns", Name: "ru", TunnelID: "eth3", Targets: []string{"gosuslugi.ru"}})
	if err != nil {
		t.Fatalf("RouteAddJSON should return partial result after mutation, got err=%v", err)
	}
	if !created {
		t.Fatal("route was not created")
	}
	var res wire.RouteApplyResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Action != "add" || res.Warning == "" {
		t.Fatalf("missing partial add warning: %+v", res)
	}
}

func TestRouteAddJSON_ReconcilesAppliedCreateError(t *testing.T) {
	var dnsRules []awgmgr.DNSRoute
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"wan-eth3","name":"ISP","iface":"eth3","type":"wan","status":"up","available":true}]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": dnsRules})
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		var created awgmgr.DNSRoute
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		created.ID = "dns:ru"
		dnsRules = append(dnsRules, created)
		http.Error(w, "response lost after create", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteAddJSON(context.Background(), awgmgr.New(srv.URL), wire.RouteAddRequest{Kind: "dns", Name: "ru", TunnelID: "eth3", Targets: []string{"gosuslugi.ru"}})
	if err != nil {
		t.Fatalf("RouteAddJSON should reconcile applied create, got err=%v", err)
	}
	var res wire.RouteApplyResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Warning == "" || !strings.Contains(res.Warning, "create response failed") {
		t.Fatalf("expected reconciliation warning, got %+v", res)
	}
	if res.RouteID != "dns:ru" {
		t.Fatalf("route id = %q, want reconciled AWG route id", res.RouteID)
	}
}

func TestRouteDeleteJSON_ReturnsWarningWhenPostDeleteRefreshFails(t *testing.T) {
	var deleted bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"dns1","name":"YouTube","domains":["youtube.com"],"manualDomains":["youtube.com"],"enabled":true,"backend":"ndms","routes":[{"interface":"nwg5","tunnelId":"Wireguard3"}]}]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/delete", func(w http.ResponseWriter, r *http.Request) {
		deleted = true
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"refresh failed"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteDeleteJSON(context.Background(), awgmgr.New(srv.URL), wire.RouteDeleteRequest{Kind: "dns", RouteID: "dns1"})
	if err != nil {
		t.Fatalf("RouteDeleteJSON should return partial result after mutation, got err=%v", err)
	}
	if !deleted {
		t.Fatal("route was not deleted")
	}
	var res wire.RouteApplyResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Action != "delete" || res.Warning == "" {
		t.Fatalf("missing partial delete warning: %+v", res)
	}
}

func TestRouteDeleteJSON_ReconcilesAppliedDeleteError(t *testing.T) {
	dnsRules := []awgmgr.DNSRoute{{
		ID: "dns1", Name: "YouTube", Domains: []string{"youtube.com"}, ManualDomains: []string{"youtube.com"},
		Enabled: true, Backend: "ndms", Routes: []awgmgr.DNSRouteEntry{{Interface: "nwg5", TunnelID: "Wireguard3"}},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": dnsRules})
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/delete", func(w http.ResponseWriter, r *http.Request) {
		dnsRules = nil
		http.Error(w, "response lost after delete", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteDeleteJSON(context.Background(), awgmgr.New(srv.URL), wire.RouteDeleteRequest{Kind: "dns", RouteID: "dns1"})
	if err != nil {
		t.Fatalf("RouteDeleteJSON should reconcile applied delete, got err=%v", err)
	}
	var res wire.RouteApplyResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Warning == "" || !strings.Contains(res.Warning, "delete response failed") {
		t.Fatalf("expected reconciliation warning, got %+v", res)
	}
}

func TestRouteAddJSON_CanApplyAWGManagerPreset(t *testing.T) {
	var created awgmgr.DNSRoute
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg11","name":"exit","interfaceName":"nwg5","enabled":true}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"presets":[{"id":"youtube","name":"YouTube","engines":{"dns":{"domains":["youtube.com"]},"hydraroute":{"geoTags":["geosite:YOUTUBE"]}}}]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-control", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "dns", TunnelID: "awg11", TemplateID: "youtube", UseHRNeo: true}
	plan, err := buildRouteAddPlan(context.Background(), c, req)
	if err != nil {
		t.Fatalf("buildRouteAddPlan: %v", err)
	}
	if plan.Route.Name != "YouTube" || plan.Route.Backend != "hydraroute" || len(plan.Route.Targets) != 2 {
		t.Fatalf("bad template plan: %+v", plan)
	}
	req.DraftHash = plan.Hash
	if _, err := RouteAddJSON(context.Background(), c, req); err != nil {
		t.Fatalf("RouteAddJSON: %v", err)
	}
	if created.Name != "YouTube" || created.Backend != "hydraroute" || created.HRRouteMode != "policy" || len(created.ManualDomains) != 2 {
		t.Fatalf("created route did not use template: %+v", created)
	}
	if len(created.Routes) != 0 || len(created.HRPolicyInterfaces) != 1 || created.HRPolicyInterfaces[0] != "nwg5" {
		t.Fatalf("created route did not use HR-Neo policy iface: %+v", created)
	}
}

func TestRouteAddJSON_AWGTemplateNDMSBindsSelectedRoutingInterface(t *testing.T) {
	var created awgmgr.DNSRoute
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"wrong","name":"wrong","iface":"nwg-wrong","type":"managed","status":"running","available":true},
			{"id":"right","name":"right","iface":"nwg-right","type":"managed","status":"running","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"presets":[{"id":"youtube","name":"YouTube","engines":{"dns":{"domains":["youtube.com"]},"hydraroute":{"geoTags":["geosite:YOUTUBE"]}}}]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "dns", TunnelID: "right", TemplateID: "youtube"}
	plan, err := buildRouteAddPlan(context.Background(), c, req)
	if err != nil {
		t.Fatalf("buildRouteAddPlan: %v", err)
	}
	if plan.Route.Backend != "ndms" || plan.Route.Bind != "nwg-right" || len(plan.Route.Targets) != 1 {
		t.Fatalf("bad NDMS template plan: %+v", plan)
	}
	req.DraftHash = plan.Hash
	if _, err := RouteAddJSON(context.Background(), c, req); err != nil {
		t.Fatalf("RouteAddJSON: %v", err)
	}
	if created.Backend != "ndms" || created.HRPolicyName != "" || len(created.ManualDomains) != 1 {
		t.Fatalf("created route should be NDMS-only DNS template, got %+v", created)
	}
	if len(created.Routes) != 1 || created.Routes[0].Interface != "nwg-right" || created.Routes[0].TunnelID != "right" {
		t.Fatalf("created NDMS template route bound to wrong iface: %+v", created)
	}
}

func TestRouteAddJSON_AWGTemplateHRNeoBindsSelectedRoutingInterface(t *testing.T) {
	var created awgmgr.DNSRoute
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"wrong","name":"wrong","iface":"nwg-wrong","type":"managed","status":"running","available":true},
			{"id":"right","name":"right","iface":"nwg-right","type":"managed","status":"running","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"presets":[{"id":"youtube","name":"YouTube","engines":{"dns":{"domains":["youtube.com"]},"hydraroute":{"geoTags":["geosite:YOUTUBE"]}}}]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-control", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "dns", TunnelID: "right", TemplateID: "youtube", UseHRNeo: true}
	plan, err := buildRouteAddPlan(context.Background(), c, req)
	if err != nil {
		t.Fatalf("buildRouteAddPlan: %v", err)
	}
	if plan.Route.Backend != "hydraroute" || plan.Route.Bind != "nwg-right" || len(plan.Route.Targets) != 2 {
		t.Fatalf("bad HR-Neo template plan: %+v", plan)
	}
	req.DraftHash = plan.Hash
	if _, err := RouteAddJSON(context.Background(), c, req); err != nil {
		t.Fatalf("RouteAddJSON: %v", err)
	}
	if created.Backend != "hydraroute" || created.HRPolicyName != "HydraRoute" || created.HRRouteMode != "policy" || len(created.ManualDomains) != 2 {
		t.Fatalf("created route should be HR-Neo template, got %+v", created)
	}
	if len(created.Routes) != 0 {
		t.Fatalf("HR-Neo policy route must not create explicit routes: %+v", created)
	}
	if len(created.HRPolicyInterfaces) != 1 || created.HRPolicyInterfaces[0] != "nwg-right" {
		t.Fatalf("created HR-Neo policy route bound to wrong policy iface: %+v", created)
	}
}

func TestRouteAddJSON_RefreshesAndUsesRoutingIfaceForCreate(t *testing.T) {
	var created awgmgr.DNSRoute
	refreshed := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg12","name":"stale","interfaceName":"nwg3","enabled":true}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"awg12","name":"fresh","iface":"nwg5","type":"managed","status":"running","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		if !refreshed {
			t.Fatalf("route_add must refresh AWG Manager routing before create")
		}
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		refreshed = true
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "dns", Name: "OpenAI", TunnelID: "awg12", Targets: []string{"chatgpt.com"}}
	if _, err := RouteAddJSON(context.Background(), c, req); err != nil {
		t.Fatalf("RouteAddJSON: %v", err)
	}
	if len(created.Routes) != 1 || created.Routes[0].Interface != "nwg5" || created.Routes[0].TunnelID != "awg12" {
		t.Fatalf("created route used stale iface, got %+v", created)
	}
}

func TestRouteAddJSON_RefreshesRoutingIfaceWhenRoutingIDDiffersFromTunnelID(t *testing.T) {
	var created awgmgr.DNSRoute
	refreshed := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "awg12" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg12","name":"Primary","interfaceName":"stale-nwg3","ndmsName":"Wireguard3","enabled":true}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"Wireguard3","name":"Primary","iface":"nwg5","type":"managed","status":"running","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		if !refreshed {
			t.Fatalf("route_add must refresh AWG Manager routing before create")
		}
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		refreshed = true
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "dns", Name: "OpenAI", TunnelID: "awg12", Targets: []string{"chatgpt.com"}}
	if _, err := RouteAddJSON(context.Background(), c, req); err != nil {
		t.Fatalf("RouteAddJSON: %v", err)
	}
	if len(created.Routes) != 1 || created.Routes[0].Interface != "nwg5" || created.Routes[0].TunnelID != "Wireguard3" {
		t.Fatalf("created route used stale tunnel iface instead of routing iface: %+v", created)
	}
}

func TestRouteAddJSON_ManagedCreateUsesFreshIfaceAndStableTunnelID(t *testing.T) {
	var created awgmgr.DNSRoute
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "awg12" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg12","name":"Primary","interfaceName":"nwg3","ndmsName":"Wireguard3","enabled":true}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"Wireguard3","name":"Primary","iface":"nwg5","type":"managed","status":"running","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"presets":[{"id":"youtube","name":"YouTube","engines":{"dns":{"domains":["youtube.com"]}}}]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "dns", TunnelID: "awg12", TemplateID: "youtube"}
	if _, err := RouteAddJSON(context.Background(), c, req); err != nil {
		t.Fatalf("RouteAddJSON: %v", err)
	}
	if len(created.Routes) != 1 {
		t.Fatalf("expected one route entry, got %+v", created)
	}
	if created.Routes[0].Interface != "nwg5" {
		t.Fatalf("route interface = %q, want fresh iface nwg5", created.Routes[0].Interface)
	}
	if created.Routes[0].TunnelID == "nwg3" || created.Routes[0].TunnelID == "nwg5" {
		t.Fatalf("route tunnelId must be stable managed id, not iface: %+v", created.Routes[0])
	}
	if created.Routes[0].TunnelID != "Wireguard3" {
		t.Fatalf("route tunnelId = %q, want Wireguard3", created.Routes[0].TunnelID)
	}
}

func TestRouteAddPlanWarnsOnDefaultRouteLikeTarget(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"wan-eth3","name":"ISP","iface":"eth3","type":"wan","status":"up","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	plan, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL), wire.RouteAddRequest{
		Kind:     "static",
		Name:     "default",
		TunnelID: "eth3",
		Targets:  []string{"0.0.0.0/0"},
	})
	if err != nil {
		t.Fatalf("buildRouteAddPlan: %v", err)
	}
	if !plan.CanApply {
		t.Fatalf("default-route-like warning must not block apply: %+v", plan)
	}
	if len(plan.Overlaps) != 1 || plan.Overlaps[0].Severity != "warn" || plan.Overlaps[0].Reason != "default-route-like target" {
		t.Fatalf("missing default-route-like warning: %+v", plan.Overlaps)
	}
}

func TestRouteTemplatesJSONListsAWGManagerPresets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"presets":[
			{"id":"youtube","name":"YouTube","category":"video","engines":{"dns":{"domains":["youtube.com","youtu.be"]},"hydraroute":{"geoTags":["geosite:YOUTUBE"]}}},
			{"id":"empty","name":"Empty","engines":{}}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteTemplatesJSON(context.Background(), awgmgr.New(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var templates wire.RouteTemplates
	if err := json.Unmarshal([]byte(out), &templates); err != nil {
		t.Fatal(err)
	}
	if len(templates.Templates) != 1 {
		t.Fatalf("templates len = %d, want 1: %+v", len(templates.Templates), templates.Templates)
	}
	got := templates.Templates[0]
	if got.ID != "youtube" || got.Name != "YouTube" || got.Category != "video" || len(got.DNS) != 2 || len(got.HRNeo) != 1 {
		t.Fatalf("bad template: %+v", got)
	}
}
