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
	if created.Name != "YouTube" || created.Backend != "hydraroute" || len(created.ManualDomains) != 2 || created.Routes[0].Interface != "nwg5" {
		t.Fatalf("created route did not use template: %+v", created)
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
	if len(created.Routes) != 1 || created.Routes[0].Interface != "nwg-right" || created.Routes[0].TunnelID != "nwg-right" {
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
	if created.Backend != "hydraroute" || created.HRPolicyName != "HydraRoute" || created.HRRouteMode != "proxy" || len(created.ManualDomains) != 2 {
		t.Fatalf("created route should be HR-Neo template, got %+v", created)
	}
	if len(created.Routes) != 1 || created.Routes[0].Interface != "nwg-right" || created.Routes[0].TunnelID != "nwg-right" {
		t.Fatalf("created HR-Neo template route bound to wrong iface: %+v", created)
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
	if len(created.Routes) != 1 || created.Routes[0].Interface != "nwg5" || created.Routes[0].TunnelID != "nwg5" {
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
	if len(created.Routes) != 1 || created.Routes[0].Interface != "nwg5" || created.Routes[0].TunnelID != "nwg5" {
		t.Fatalf("created route used stale tunnel iface instead of routing iface: %+v", created)
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
