package checks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

func TestTallyRouteCounts_ExplicitAndFallThrough(t *testing.T) {
	tunnels := []awgmgr.Tunnel{
		{ID: "awg11", InterfaceName: "nwg1", DefaultRoute: true, Enabled: true},
		{ID: "awg12", InterfaceName: "nwg0", DefaultRoute: false, Enabled: true},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/dns-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[
				{"id":"hr:Vk","routes":[{"interface":"nwg0","tunnelId":"nwg0"}],"backend":"hydraroute","hrPolicyName":"HydraRoute"},
				{"id":"hr:AI","routes":null,"backend":"hydraroute","hrPolicyName":"HydraRoute"},
				{"id":"hr:Sber","routes":null,"backend":"hydraroute","hrPolicyName":"HydraRoute"},
				{"id":"ndms:WAN","routes":[{"interface":"eth3","tunnelId":"eth3"}],"backend":"ndms"},
				{"id":"hr:Explicit-default","routes":[{"interface":"nwg1","tunnelId":"nwg1"}],"backend":"hydraroute","hrPolicyName":"HydraRoute"},
				{"id":"hr:Disabled","enabled":false,"routes":[{"interface":"nwg1","tunnelId":"nwg1"}],"backend":"hydraroute","hrPolicyName":"HydraRoute"}
			]}`))
		case "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[
				{"id":"s1","tunnelID":"nwg0","subnets":["10.0.0.0/24"]},
				{"id":"s2","tunnelID":"nwg0","subnets":["10.0.1.0/24"]},
				{"id":"s3","tunnelID":"nwg1","subnets":["10.0.2.0/24"]},
				{"id":"s-disabled","tunnelID":"nwg1","enabled":false,"subnets":["10.0.3.0/24"]}
			]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	got := tallyRouteCounts(context.Background(), c, tunnels)
	// nwg1 (default): 1 explicit HR + 2 fall-through HR credited = 3 DNS, all HR.
	if c := got["nwg1"]; c.DNS != 3 || c.DNSHR != 3 || c.Static != 1 {
		t.Errorf("nwg1: %+v (want DNS=3 HR=3 Static=1)", c)
	}
	// nwg0: 1 explicit HR DNS, 2 static.
	if c := got["nwg0"]; c.DNS != 1 || c.DNSHR != 1 || c.Static != 2 {
		t.Errorf("nwg0: %+v (want DNS=1 HR=1 Static=2)", c)
	}
	// eth3: 1 ndms DNS, no HR.
	if c := got["eth3"]; c.DNS != 1 || c.DNSHR != 0 {
		t.Errorf("eth3: %+v (want DNS=1 HR=0)", c)
	}
}

func TestTallyRouteCounts_NoDefaultRoute_FallThroughDropped(t *testing.T) {
	tunnels := []awgmgr.Tunnel{
		{ID: "awg11", InterfaceName: "nwg1", DefaultRoute: true, Enabled: false},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/dns-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[
				{"id":"hr:AI","routes":null,"backend":"hydraroute","hrPolicyName":"HydraRoute"}
			]}`))
		case "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		}
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	got := tallyRouteCounts(context.Background(), c, tunnels)
	if len(got) != 0 {
		t.Errorf("want empty (disabled default iface, fall-through dropped); got %+v", got)
	}
}

func TestTallyRouteCounts_DoesNotCreditFallThroughToStartingDefaultTunnel(t *testing.T) {
	tunnels := []awgmgr.Tunnel{
		{ID: "awg10", InterfaceName: "nwg1", DefaultRoute: true, Enabled: true, Status: "starting"},
		{ID: "awg11", InterfaceName: "nwg5", DefaultRoute: true, Enabled: true, Status: "running"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/dns-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[
				{"id":"hr:AI","routes":null,"backend":"hydraroute","hrPolicyName":"HydraRoute"}
			]}`))
		case "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		}
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)

	got := tallyRouteCounts(context.Background(), c, tunnels)
	if c := got["nwg1"]; c.DNS != 0 || c.DNSHR != 0 {
		t.Fatalf("starting default tunnel must not receive HR fall-through counts: %+v", c)
	}
	if c := got["nwg5"]; c.DNS != 1 || c.DNSHR != 1 {
		t.Fatalf("running default tunnel should receive HR fall-through counts: %+v", c)
	}
}

func TestTallyRouteCounts_ListError_ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	got := tallyRouteCounts(context.Background(), c, nil)
	if got != nil {
		t.Errorf("want nil on list error; got %+v", got)
	}
}

func TestTunnelsCheck_UnusedTunnelWithPingCheckDisabledDoesNotFailOnNoHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg10","name":"unused","type":"awg","status":"starting","enabled":true,"defaultRoute":true,"interfaceName":"nwg1"}
			]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"tunnelId":"awg10","status":"disabled","method":"icmp","failCount":0,"failThreshold":3}
			]}}`))
		case "/api/dns-routes/list", "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	chk := TunnelsCheck{Client: awgmgr.New(srv.URL)}
	out := chk.Run(context.Background(), Deps{})
	for _, c := range out {
		if c.Name != "tunnel_awg10" {
			continue
		}
		if c.Status != "ok" {
			t.Fatalf("unused tunnel with pingCheck disabled must not fail: %+v", c)
		}
		if c.Details["note"] == "" {
			t.Fatalf("suppressed tunnel should explain why: %+v", c.Details)
		}
		return
	}
	t.Fatalf("tunnel_awg10 check not emitted; got: %+v", out)
}

func TestTunnelsCheck_EmitsRouteCountsInDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			body := map[string]any{
				"success": true,
				"data": map[string]any{
					"tunnels": []map[string]any{
						{"id": "awg11", "name": "primary", "type": "awg", "status": "running", "enabled": true, "defaultRoute": true, "interfaceName": "nwg1", "lastHandshake": "2026-05-14T09:00:00Z"},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
		case "/api/dns-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[
				{"id":"hr:1","routes":null,"backend":"hydraroute","hrPolicyName":"HydraRoute"},
				{"id":"hr:2","routes":[{"interface":"nwg1","tunnelId":"nwg1"}],"backend":"hydraroute","hrPolicyName":"HydraRoute"},
				{"id":"hr:disabled","enabled":false,"routes":[{"interface":"nwg1","tunnelId":"nwg1"}],"backend":"hydraroute","hrPolicyName":"HydraRoute"}
			]}`))
		case "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[
				{"id":"s1","tunnelID":"nwg1"},
				{"id":"s-disabled","tunnelID":"nwg1","enabled":false}
			]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	chk := TunnelsCheck{Client: awgmgr.New(srv.URL)}
	out := chk.Run(context.Background(), Deps{})
	var tunnelCheck map[string]any
	for _, c := range out {
		if c.Name == "tunnel_awg11" {
			tunnelCheck = c.Details
			break
		}
	}
	if tunnelCheck == nil {
		t.Fatalf("tunnel_awg11 check not emitted; got: %+v", out)
	}
	if got, _ := tunnelCheck["routes_dns"].(int); got != 2 {
		t.Errorf("routes_dns: %v (want 2)", tunnelCheck["routes_dns"])
	}
	if got, _ := tunnelCheck["routes_dns_hr"].(int); got != 2 {
		t.Errorf("routes_dns_hr: %v (want 2)", tunnelCheck["routes_dns_hr"])
	}
	if got, _ := tunnelCheck["routes_static"].(int); got != 1 {
		t.Errorf("routes_static: %v (want 1)", tunnelCheck["routes_static"])
	}
}

func TestTunnelsCheck_SyntheticReportsEnabledCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg11","name":"a","type":"awg","status":"running","enabled":true,"interfaceName":"nwg1","lastHandshake":"2026-05-14T09:00:00Z"},
				{"id":"awg12","name":"b","type":"awg","status":"running","enabled":false,"interfaceName":"nwg0","lastHandshake":"2026-05-14T09:00:00Z"},
				{"id":"awg13","name":"c","type":"awg","status":"running","enabled":true,"interfaceName":"nwg2","lastHandshake":"2026-05-14T09:00:00Z"}
			]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
		case "/api/dns-routes/list", "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		}
	}))
	defer srv.Close()
	chk := TunnelsCheck{Client: awgmgr.New(srv.URL)}
	out := chk.Run(context.Background(), Deps{})
	for _, c := range out {
		if c.Name != "tunnels" {
			continue
		}
		if got, _ := c.Details["tunnel_count"].(int); got != 3 {
			t.Errorf("tunnel_count: %v (want 3)", c.Details["tunnel_count"])
		}
		if got, _ := c.Details["tunnel_count_enabled"].(int); got != 2 {
			t.Errorf("tunnel_count_enabled: %v (want 2)", c.Details["tunnel_count_enabled"])
		}
		return
	}
	t.Fatal("synthetic 'tunnels' check not emitted")
}

func TestTunnelsCheck_NoRoutes_OmitsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg11","name":"primary","type":"awg","status":"running","enabled":true,"interfaceName":"nwg1","lastHandshake":"2026-05-14T09:00:00Z"}
			]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
		case "/api/dns-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		case "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		}
	}))
	defer srv.Close()
	chk := TunnelsCheck{Client: awgmgr.New(srv.URL)}
	out := chk.Run(context.Background(), Deps{})
	for _, c := range out {
		if c.Name != "tunnel_awg11" {
			continue
		}
		if _, ok := c.Details["routes_dns"]; ok {
			t.Errorf("routes_dns should be omitted when zero; got: %v", c.Details["routes_dns"])
		}
		if _, ok := c.Details["routes_static"]; ok {
			t.Errorf("routes_static should be omitted when zero; got: %v", c.Details["routes_static"])
		}
		return
	}
	t.Fatalf("tunnel_awg11 check not emitted; got: %+v", out)
}

func TestTunnelsCheck_PingCheckDisabledDoesNotFailLiveTunnel(t *testing.T) {
	handshake := time.Now().UTC().Add(-45 * time.Second).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg11","name":"primary","type":"awg","status":"running","enabled":true,"interfaceName":"nwg1","lastHandshake":"` + handshake + `","pingCheck":{"status":"disabled","failCount":0,"failThreshold":3}}
			]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":false,"tunnels":[
				{"tunnelId":"awg11","tunnelName":"primary","enabled":false,"status":"disabled","method":"icmp","failCount":0,"failThreshold":3,"tunnelRunning":true}
			]}}`))
		case "/api/dns-routes/list", "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	chk := TunnelsCheck{Client: awgmgr.New(srv.URL)}
	out := chk.Run(context.Background(), Deps{})
	for _, c := range out {
		if c.Name != "tunnel_awg11" {
			continue
		}
		if c.Status != "ok" {
			t.Fatalf("pingcheck disabled must not fail a live tunnel: %+v", c)
		}
		return
	}
	t.Fatalf("tunnel_awg11 check not emitted; got: %+v", out)
}

func TestTunnelsCheck_FutureHandshakeDoesNotReportOK(t *testing.T) {
	handshake := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg11","name":"primary","type":"awg","status":"running","enabled":true,"interfaceName":"nwg1","lastHandshake":"` + handshake + `"}
			]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
		case "/api/dns-routes/list", "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	chk := TunnelsCheck{Client: awgmgr.New(srv.URL)}
	out := chk.Run(context.Background(), Deps{})
	for _, c := range out {
		if c.Name != "tunnel_awg11" {
			continue
		}
		if c.Status != "fail" {
			t.Fatalf("future handshake must not be reported as OK: %+v", c)
		}
		if errText, _ := c.Details["error"].(string); !strings.Contains(errText, "future") {
			t.Fatalf("future handshake failure should explain skew, details=%+v", c.Details)
		}
		return
	}
	t.Fatalf("tunnel_awg11 check not emitted; got: %+v", out)
}
