package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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

	changed, err := addTunnelToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), awgmgr.Tunnel{InterfaceName: "nwg7"})
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

// Заглушка awg-manager на модели политик: отдаёт цепочки, принимает permit и
// показывает результат в следующем чтении.
type policyStub struct {
	policies map[string][]awgmgr.AccessPolicyInterface
	ifaces   []awgmgr.PolicyInterface
	permits  []string
	drop     bool // роутер отвечает 200, но ничего не меняет
}

func (s *policyStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/access-policies", func(w http.ResponseWriter, r *http.Request) {
		out := []awgmgr.AccessPolicy{}
		for name, chain := range s.policies {
			out = append(out, awgmgr.AccessPolicy{Name: name, Interfaces: chain})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": out})
	})
	mux.HandleFunc("/api/routing/policy-interfaces", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": s.ifaces})
	})
	mux.HandleFunc("/api/access-policies/permit", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name      string `json:"name"`
			Interface string `json:"interface"`
			Order     int    `json:"order"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.permits = append(s.permits, fmt.Sprintf("%s/%s@%d", body.Name, body.Interface, body.Order))
		if !s.drop {
			s.policies[body.Name] = append(s.policies[body.Name], awgmgr.AccessPolicyInterface{Name: body.Interface, Order: body.Order})
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestAddTunnelToHydraRoutePolicies_AppendsAsLastFallback(t *testing.T) {
	s := &policyStub{
		policies: map[string][]awgmgr.AccessPolicyInterface{
			"HydraRoute": {{Name: "OpkgTun11", Order: 0}, {Name: "OpkgTun10", Order: 1}},
		},
		ifaces: []awgmgr.PolicyInterface{
			{Name: "OpkgTun11", Up: true}, {Name: "OpkgTun10", Up: false},
			{Name: "Wireguard0", Label: "NetherlandsKerkradeS24", Up: false},
		},
	}
	srv := s.server(t)

	tunnel := awgmgr.Tunnel{ID: "awg20", Name: "NetherlandsKerkradeS24", InterfaceName: "nwg0", NDMSName: "Wireguard0"}
	changed, err := addTunnelToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), tunnel)
	if err != nil {
		t.Fatalf("addTunnelToHydraRoutePolicies: %v", err)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}
	// Новый туннель приезжает последним резервом: заняв нулевой приоритет,
	// он перехватил бы весь трафик политики сразу после импорта.
	if len(s.permits) != 1 || s.permits[0] != "HydraRoute/Wireguard0@2" {
		t.Errorf("permits = %+v", s.permits)
	}
}

func TestAddTunnelToHydraRoutePolicies_SkipsEmptyChain(t *testing.T) {
	s := &policyStub{
		policies: map[string][]awgmgr.AccessPolicyInterface{"HydraRoute": nil},
		ifaces:   []awgmgr.PolicyInterface{{Name: "Wireguard0", Up: false}},
	}
	srv := s.server(t)
	tunnel := awgmgr.Tunnel{ID: "awg20", InterfaceName: "nwg0", NDMSName: "Wireguard0"}
	changed, err := addTunnelToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), tunnel)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// Пустая цепочка означает "следуй глобальному маршруту". Добавив туда
	// единственный интерфейс, мы сделали бы его активным для всей политики.
	if changed != 0 || len(s.permits) != 0 {
		t.Errorf("changed=%d permits=%+v", changed, s.permits)
	}
}

func TestAddTunnelToHydraRoutePolicies_SkipsAlreadyPresent(t *testing.T) {
	s := &policyStub{
		policies: map[string][]awgmgr.AccessPolicyInterface{
			"HydraRoute": {{Name: "Wireguard0", Order: 0}},
		},
		ifaces: []awgmgr.PolicyInterface{{Name: "Wireguard0", Up: true}},
	}
	srv := s.server(t)
	tunnel := awgmgr.Tunnel{ID: "awg20", InterfaceName: "nwg0", NDMSName: "Wireguard0"}
	changed, err := addTunnelToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), tunnel)
	if err != nil || changed != 0 || len(s.permits) != 0 {
		t.Errorf("changed=%d err=%v permits=%+v", changed, err, s.permits)
	}
}

func TestAddTunnelToHydraRoutePolicies_FailsWhenRouterDoesNotOfferInterface(t *testing.T) {
	s := &policyStub{
		policies: map[string][]awgmgr.AccessPolicyInterface{
			"HydraRoute": {{Name: "OpkgTun11", Order: 0}},
		},
		ifaces: []awgmgr.PolicyInterface{{Name: "OpkgTun11", Up: true}},
	}
	srv := s.server(t)
	tunnel := awgmgr.Tunnel{ID: "awg99", Name: "unknown", InterfaceName: "nwg9"}
	_, err := addTunnelToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), tunnel)
	if err == nil {
		t.Fatal("want error: the router offers no such interface to policies")
	}
}

func TestAddTunnelToHydraRoutePolicies_FailsWhenPostconditionNotMet(t *testing.T) {
	s := &policyStub{
		policies: map[string][]awgmgr.AccessPolicyInterface{
			"HydraRoute": {{Name: "OpkgTun11", Order: 0}},
		},
		ifaces: []awgmgr.PolicyInterface{{Name: "OpkgTun11", Up: true}, {Name: "Wireguard0", Up: false}},
		drop:   true,
	}
	srv := s.server(t)
	tunnel := awgmgr.Tunnel{ID: "awg20", InterfaceName: "nwg0", NDMSName: "Wireguard0"}
	_, err := addTunnelToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), tunnel)
	// Исходный дефект и был в том, что функция рапортовала об успехе, не
	// проверив результат. Повторять его в новом коде нельзя.
	if err == nil {
		t.Fatal("want error: interface did not appear in the chain after permit")
	}
}

// TestAddTunnelToHydraRoutePolicies_OrderPerPolicyDoesNotLeak covers a single
// call that permits into two policies whose chains have different lengths.
// A single-policy test cannot show whether the order argument is computed
// fresh per policy or leaks a counter/length across loop iterations.
func TestAddTunnelToHydraRoutePolicies_OrderPerPolicyDoesNotLeak(t *testing.T) {
	s := &policyStub{
		policies: map[string][]awgmgr.AccessPolicyInterface{
			"Short": {{Name: "OpkgTun11", Order: 0}},
			"Long":  {{Name: "OpkgTun11", Order: 0}, {Name: "OpkgTun10", Order: 1}, {Name: "OpkgTun9", Order: 2}},
		},
		ifaces: []awgmgr.PolicyInterface{
			{Name: "OpkgTun11", Up: true}, {Name: "OpkgTun10", Up: false}, {Name: "OpkgTun9", Up: false},
			{Name: "Wireguard0", Up: false},
		},
	}
	srv := s.server(t)

	tunnel := awgmgr.Tunnel{ID: "awg20", InterfaceName: "nwg0", NDMSName: "Wireguard0"}
	changed, err := addTunnelToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), tunnel)
	if err != nil {
		t.Fatalf("addTunnelToHydraRoutePolicies: %v", err)
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2", changed)
	}
	want := map[string]bool{"Short/Wireguard0@1": true, "Long/Wireguard0@3": true}
	if len(s.permits) != 2 {
		t.Fatalf("permits = %+v", s.permits)
	}
	for _, p := range s.permits {
		if !want[p] {
			t.Errorf("unexpected permit %q (order must be computed per-policy, not leaked across iterations)", p)
		}
	}
}

// TestAddTunnelToHydraRoutePolicies_FailsOnPolicyReadError covers the door the
// fallback-on-any-error version of this function left open: on a genuine
// 2.16+ router, a transient failure reading /api/routing/access-policies (not
// a 404 — the endpoint exists) must NOT be treated as "old router, use the
// legacy path". The binding no longer lives on the DNS rule on this build, so
// silently falling back there would edit nothing that matters and report
// success for a tunnel that never entered routing — the exact defect this
// task exists to eliminate, reached through a different door.
func TestAddTunnelToHydraRoutePolicies_FailsOnPolicyReadError(t *testing.T) {
	var paths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/access-policies", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tunnel := awgmgr.Tunnel{ID: "awg20", InterfaceName: "nwg0", NDMSName: "Wireguard0"}
	_, err := addTunnelToHydraRoutePolicies(context.Background(), awgmgr.New(srv.URL), tunnel)
	if err == nil {
		t.Fatal("want error: access-policies read failed with a non-404 status")
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "/api/dns-routes/") {
			t.Errorf("legacy endpoint %q was called after a non-404 access-policies read error; that endpoint no longer holds the binding on this router", p)
		}
	}
}
