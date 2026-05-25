package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// rebindMock is a stateful mock of awg-manager. Rules are mutated on update —
// we can verify post-state at the end of the test. Designed to catch the
// canary case: a rule whose Backend points at WAN (eth3) MUST NOT be
// modified by rebind.
type rebindMock struct {
	t              *testing.T
	mu             sync.Mutex
	dnsRules       []awgmgr.DNSRoute
	staticRules    []awgmgr.StaticRoute
	hrInstalled    bool
	hrControlCalls atomic.Int32
	refreshCalls   atomic.Int32
}

func newRebindMock(t *testing.T) *rebindMock {
	return &rebindMock{
		t:           t,
		hrInstalled: true,
		dnsRules: []awgmgr.DNSRoute{
			{ID: "hr:Vk", Backend: "hydraroute", HRPolicyName: "HydraRoute",
				Routes: []awgmgr.DNSRouteEntry{{Interface: "nwg1", TunnelID: "nwg1"}}},
			{ID: "ndms:Yandex", Backend: "ndms",
				Routes: []awgmgr.DNSRouteEntry{{Interface: "nwg1", TunnelID: "nwg1"}}},
			{ID: "hr:Sber", Backend: "hydraroute", HRPolicyName: "HydraRoute",
				Routes: []awgmgr.DNSRouteEntry{{Interface: "eth3", TunnelID: "eth3"}}}, // CANARY: WAN
			{ID: "hr:Fallthru", Backend: "hydraroute", HRPolicyName: "HydraRoute",
				Routes: nil}, // fall-through
		},
		staticRules: []awgmgr.StaticRoute{
			{ID: "s1", Name: "work", TunnelID: "nwg1", Subnets: []string{"10.0.0.0/8"}, Enabled: true},
			{ID: "s2", Name: "wan-rule", TunnelID: "eth3", Subnets: []string{"203.0.113.0/24"}, Enabled: true}, // CANARY
		},
	}
}

func (m *rebindMock) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("id") {
		case "t1":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"t1","name":"awg11","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true}}`))
		case "t2":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"t2","name":"awg13","interfaceName":"nwg0","ndmsName":"Wireguard0","enabled":true,"defaultRoute":false}}`))
		default:
			http.Error(w, "not found", 404)
		}
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"t1","name":"awg11","iface":"nwg1","type":"managed","status":"running","available":true},
			{"id":"t2","name":"awg13","iface":"nwg0","type":"managed","status":"running","available":true},
			{"id":"wan-eth3","name":"ISP","iface":"eth3","type":"wan","status":"up","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": m.dnsRules})
	})
	mux.HandleFunc("/api/dns-routes/update", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		var rule awgmgr.DNSRoute
		_ = json.NewDecoder(r.Body).Decode(&rule)
		m.mu.Lock()
		defer m.mu.Unlock()
		for i := range m.dnsRules {
			if m.dnsRules[i].ID == id {
				m.dnsRules[i] = rule
				break
			}
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": m.staticRules})
	})
	mux.HandleFunc("/api/static-routes/update", func(w http.ResponseWriter, r *http.Request) {
		var rule awgmgr.StaticRoute
		_ = json.NewDecoder(r.Body).Decode(&rule)
		m.mu.Lock()
		defer m.mu.Unlock()
		for i := range m.staticRules {
			if m.staticRules[i].ID == rule.ID {
				m.staticRules[i] = rule
				break
			}
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		body := `{"success":true,"data":{"installed":false}}`
		if m.hrInstalled {
			body = `{"success":true,"data":{"installed":true,"running":true}}`
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/api/system/hydraroute-control", func(w http.ResponseWriter, r *http.Request) {
		m.hrControlCalls.Add(1)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		m.refreshCalls.Add(1)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	return mux
}

func TestRouteRebind_HappyPath_WANCanaryUntouched(t *testing.T) {
	mock := newRebindMock(t)
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := awgmgr.New(srv.URL)

	out, err := RouteRebind(context.Background(), c, "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	// 3 DNS rules touched (Vk, Yandex, Fallthru). Sber on eth3 (WAN) untouched.
	if res.DNS.OK != 3 || res.DNS.Failed != 0 {
		t.Errorf("DNS counts: %+v (want 3 ok / 0 fail)", res.DNS)
	}
	// 1 static touched (s1 on nwg1). s2 on eth3 (WAN) untouched.
	if res.Static.OK != 1 || res.Static.Failed != 0 {
		t.Errorf("Static counts: %+v", res.Static)
	}
	// HR-Neo subcount: Vk + Fallthru (Yandex is ndms engine, not hr).
	if res.HRNeo.OK != 2 {
		t.Errorf("HRNeo subcount: %+v (want 2 — Vk + Fallthru)", res.HRNeo)
	}

	// CANARY: WAN-targeted DNS rule and static rule must be unchanged.
	for _, r := range mock.dnsRules {
		if r.ID == "hr:Sber" {
			if len(r.Routes) != 1 || r.Routes[0].Interface != "eth3" {
				t.Errorf("WAN canary DNS rule mutated: %+v", r)
			}
		}
	}
	for _, r := range mock.staticRules {
		if r.ID == "s2" && r.TunnelID != "eth3" {
			t.Errorf("WAN canary static rule mutated: %+v", r)
		}
	}

	if mock.refreshCalls.Load() != 1 {
		t.Errorf("/api/routing/refresh calls: %d (want 1)", mock.refreshCalls.Load())
	}
	if mock.hrControlCalls.Load() != 1 {
		t.Errorf("/api/system/hydraroute-control calls: %d (want 1, since HR-Neo rules touched)", mock.hrControlCalls.Load())
	}
}

func TestRouteRebind_SrcEqDst(t *testing.T) {
	c := awgmgr.New("http://unused.invalid")
	out, err := RouteRebind(context.Background(), c, "t1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.DNS.OK+res.Static.OK+res.HRNeo.OK != 0 {
		t.Errorf("src==dst should be no-op, got %+v", res)
	}
}

func TestRouteRebind_CanMoveWANToManagedTunnel(t *testing.T) {
	mock := newRebindMock(t)
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := awgmgr.New(srv.URL)

	out, err := RouteRebind(context.Background(), c, "eth3", "t2")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if res.DNS.OK != 1 || res.Static.OK != 1 || res.HRNeo.OK != 1 {
		t.Fatalf("counts = dns=%+v static=%+v hr=%+v, want one WAN DNS+static moved", res.DNS, res.Static, res.HRNeo)
	}

	for _, r := range mock.dnsRules {
		switch r.ID {
		case "hr:Sber":
			if len(r.Routes) != 1 || r.Routes[0].Interface != "nwg0" || r.Routes[0].TunnelID != "nwg0" {
				t.Fatalf("WAN DNS rule was not moved to nwg0: %+v", r)
			}
		case "hr:Vk", "ndms:Yandex":
			if len(r.Routes) != 1 || r.Routes[0].Interface != "nwg1" {
				t.Fatalf("non-WAN DNS rule mutated: %+v", r)
			}
		}
	}
	for _, r := range mock.staticRules {
		switch r.ID {
		case "s2":
			if r.TunnelID != "nwg0" {
				t.Fatalf("WAN static rule was not moved to nwg0: %+v", r)
			}
		case "s1":
			if r.TunnelID != "nwg1" {
				t.Fatalf("non-WAN static rule mutated: %+v", r)
			}
		}
	}
}

func TestRouteRebind_DNSPartialFail(t *testing.T) {
	mock := newRebindMock(t)
	mock.hrInstalled = false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/dns-routes/update" && r.URL.Query().Get("id") == "ndms:Yandex" {
			http.Error(w, "boom", 500)
			return
		}
		mock.handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := RouteRebind(context.Background(), c, "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.DNS.Failed < 1 {
		t.Errorf("expected at least 1 failure on DNS, got %+v", res.DNS)
	}
	if len(res.DNS.Errors) == 0 {
		t.Errorf("errors should be reported")
	}
}
