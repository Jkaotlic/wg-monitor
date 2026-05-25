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
