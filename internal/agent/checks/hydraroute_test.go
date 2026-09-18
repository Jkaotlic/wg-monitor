package checks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestHydraRouteCheckStoppedWithNDMSRoutesDoesNotFail(t *testing.T) {
	srv := newHydraRouteCheckServer(t, hydraRouteFixtures{
		statusJSON: `{"success":true,"data":{"installed":true,"running":false}}`,
		dnsJSON: `{"success":true,"data":[
			{"id":"ndms:ru","name":"RU","enabled":true,"backend":"ndms","domains":["gosuslugi.ru"],"routes":[{"interface":"Wireguard0","tunnelId":"Wireguard0"}]}
		]}`,
		staticJSON: `{"success":true,"data":[]}`,
		systemJSON: `{"success":true,"data":{"activeBackend":"ndms","singbox":{"installed":false}}}`,
	})
	defer srv.Close()

	got := (HydraRouteCheck{Client: awgmgr.New(srv.URL)}).Run(context.Background(), Deps{})

	if got.Status != "ok" {
		t.Fatalf("stopped HydraRoute should be ignored when NDMS routing is active; got %+v", got)
	}
	if got.Details["routes_ndms"] != 1 {
		t.Fatalf("routes_ndms detail = %v, want 1; details=%+v", got.Details["routes_ndms"], got.Details)
	}
	if got.Details["hrneo_required"] != false {
		t.Fatalf("hrneo_required detail = %v, want false; details=%+v", got.Details["hrneo_required"], got.Details)
	}
}

func TestHydraRouteCheckStoppedWithHRNeoRoutesStillFails(t *testing.T) {
	srv := newHydraRouteCheckServer(t, hydraRouteFixtures{
		statusJSON: `{"success":true,"data":{"installed":true,"running":false}}`,
		dnsJSON: `{"success":true,"data":[
			{"id":"hr:telegram","name":"Telegram","enabled":true,"backend":"hydraroute","domains":["telegram.org"],"routes":[{"interface":"nwg0","tunnelId":"nwg0"}]}
		]}`,
		staticJSON: `{"success":true,"data":[]}`,
		systemJSON: `{"success":true,"data":{"activeBackend":"hydraroute","singbox":{"installed":false}}}`,
	})
	defer srv.Close()

	got := (HydraRouteCheck{Client: awgmgr.New(srv.URL)}).Run(context.Background(), Deps{})

	if got.Status != "fail" {
		t.Fatalf("stopped HydraRoute must fail when HR-Neo routes are active; got %+v", got)
	}
	if got.Details["routes_hrneo"] != 1 {
		t.Fatalf("routes_hrneo detail = %v, want 1; details=%+v", got.Details["routes_hrneo"], got.Details)
	}
	if got.Details["hrneo_required"] != true {
		t.Fatalf("hrneo_required detail = %v, want true; details=%+v", got.Details["hrneo_required"], got.Details)
	}
}

func TestHydraRouteCheckStoppedWithDisabledHRNeoRulesDoesNotFail(t *testing.T) {
	srv := newHydraRouteCheckServer(t, hydraRouteFixtures{
		statusJSON: `{"success":true,"data":{"installed":true,"running":false}}`,
		dnsJSON: `{"success":true,"data":[
			{"id":"hr:disabled","name":"Disabled","enabled":false,"backend":"hydraroute","domains":["example.org"],"routes":[{"interface":"nwg0","tunnelId":"nwg0"}]}
		]}`,
		staticJSON: `{"success":true,"data":[]}`,
		systemJSON: `{"success":true,"data":{"activeBackend":"ndms","singbox":{"installed":false}}}`,
	})
	defer srv.Close()

	got := (HydraRouteCheck{Client: awgmgr.New(srv.URL)}).Run(context.Background(), Deps{})

	if got.Status != "ok" {
		t.Fatalf("disabled HR-Neo routes must not require HydraRoute; got %+v", got)
	}
	if got.Details["routes_hrneo"] != 0 {
		t.Fatalf("routes_hrneo detail = %v, want 0; details=%+v", got.Details["routes_hrneo"], got.Details)
	}
}

func TestHydraRouteCheckStoppedWithIPAndSingboxRoutesDoesNotFail(t *testing.T) {
	srv := newHydraRouteCheckServer(t, hydraRouteFixtures{
		statusJSON: `{"success":true,"data":{"installed":true,"running":false}}`,
		dnsJSON:    `{"success":true,"data":[]}`,
		staticJSON: `{"success":true,"data":[{"id":"s1","name":"corp","enabled":true,"tunnelID":"nwg1","subnets":["10.0.0.0/8"]}]}`,
		systemJSON: `{"success":true,"data":{"activeBackend":"singbox","singbox":{"installed":true,"version":"1.11.0"}}}`,
	})
	defer srv.Close()

	got := (HydraRouteCheck{Client: awgmgr.New(srv.URL)}).Run(context.Background(), Deps{})

	if got.Status != "ok" {
		t.Fatalf("stopped HydraRoute should be ignored when IP/static or sing-box routing is active; got %+v", got)
	}
	if got.Details["routes_static"] != 1 {
		t.Fatalf("routes_static detail = %v, want 1; details=%+v", got.Details["routes_static"], got.Details)
	}
	if got.Details["singbox_installed"] != true {
		t.Fatalf("singbox_installed detail = %v, want true; details=%+v", got.Details["singbox_installed"], got.Details)
	}
	if got.Details["active_backend"] != "singbox" {
		t.Fatalf("active_backend detail = %v, want singbox; details=%+v", got.Details["active_backend"], got.Details)
	}
}

func TestHydraRouteCheckStoppedOnSingboxRouterDoesNotFail(t *testing.T) {
	// client-c's shape: sing-box router active (deviceMode all), but stale
	// enabled hydraroute DNS rules still linger and HydraRoute is stopped. The
	// pre-fix logic flagged this (HR-Neo routes "active"); sing-box awareness
	// must treat it as OK because sing-box is the real router.
	srv := newHydraRouteCheckServer(t, hydraRouteFixtures{
		statusJSON: `{"success":true,"data":{"installed":true,"running":false}}`,
		dnsJSON: `{"success":true,"data":[
			{"id":"hr:telegram","name":"Telegram","enabled":true,"backend":"hydraroute","domains":["telegram.org"],"routes":[{"interface":"nwg0","tunnelId":"nwg0"}]}
		]}`,
		staticJSON:   `{"success":true,"data":[]}`,
		systemJSON:   `{"success":true,"data":{"activeBackend":"kernel","singbox":{"installed":true,"version":"1.11.0"}}}`,
		settingsJSON: `{"success":true,"data":{"download":{"routeTag":"direct","routeKind":"direct"},"singboxRouter":{"enabled":true,"deviceMode":"all"}}}`,
	})
	defer srv.Close()

	got := (HydraRouteCheck{Client: awgmgr.New(srv.URL)}).Run(context.Background(), Deps{})

	if got.Status != "ok" {
		t.Fatalf("stopped HydraRoute must be ignored on an active sing-box router; got %+v", got)
	}
	if got.Details["singbox_router_active"] != true {
		t.Fatalf("singbox_router_active detail = %v, want true; details=%+v", got.Details["singbox_router_active"], got.Details)
	}
	if got.Details["ignored_singbox_router"] != true {
		t.Fatalf("ignored_singbox_router detail = %v, want true; details=%+v", got.Details["ignored_singbox_router"], got.Details)
	}
}

type hydraRouteFixtures struct {
	statusJSON   string
	dnsJSON      string
	staticJSON   string
	systemJSON   string
	settingsJSON string
}

func newHydraRouteCheckServer(t *testing.T, fx hydraRouteFixtures) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fx.statusJSON))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fx.dnsJSON))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fx.staticJSON))
	})
	mux.HandleFunc("/api/system/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fx.systemJSON))
	})
	// Register /api/settings/get only when the fixture provides it; otherwise the
	// endpoint 404s, Settings() errors, and SingboxRouterActive stays false —
	// matching older awg-manager builds and the pre-sing-box fixtures.
	if fx.settingsJSON != "" {
		mux.HandleFunc("/api/settings/get", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(fx.settingsJSON))
		})
	}
	return httptest.NewServer(mux)
}

func hydraRoutePolicyFixtures() hydraRouteFixtures {
	return hydraRouteFixtures{
		statusJSON: `{"success":true,"data":{"installed":true,"running":true}}`,
		dnsJSON: `{"success":true,"data":[
			{"id":"hr:yt","name":"YT","enabled":true,"backend":"hydraroute","hrRouteMode":"policy","hrPolicyName":"HydraRoute","domains":["example.com"]}
		]}`,
		staticJSON: `{"success":true,"data":[]}`,
		systemJSON: `{"success":true,"data":{"activeBackend":"ndms","singbox":{"installed":false}}}`,
	}
}

// Сводка политик кладётся в details как есть; читатель получает уже
// прочитанный список правил, а не ходит за ним второй раз.
func TestHydraRouteCheckAddsPolicies(t *testing.T) {
	srv := newHydraRouteCheckServer(t, hydraRoutePolicyFixtures())
	defer srv.Close()
	var gotRules int
	want := []wire.PolicyBrief{{
		Name: "HydraRoute", ActiveTunnelID: "awg14", ViaVPN: true, DNS: 1, HRNeo: 1,
		Links: []wire.PolicyBriefLink{{TunnelID: "awg14", Role: "active"}, {TunnelID: "awg10", Role: "fallback"}},
	}}
	check := HydraRouteCheck{
		Client: awgmgr.New(srv.URL),
		Policies: func(_ context.Context, dns []awgmgr.DNSRoute) ([]wire.PolicyBrief, error) {
			gotRules = len(dns)
			return want, nil
		},
	}
	got := check.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("status = %q", got.Status)
	}
	if gotRules != 1 {
		t.Errorf("читателю политик передано %d правил, хотим 1", gotRules)
	}
	if !reflect.DeepEqual(got.Details["policies"], want) {
		t.Errorf("policies = %#v", got.Details["policies"])
	}
	if _, ok := got.Details["policies_error"]; ok {
		t.Errorf("policies_error при удачном чтении: %v", got.Details["policies_error"])
	}
}

// Ошибка чтения политик -- строка в details, проверка не падает: HydraRoute
// от этого не сломан.
func TestHydraRouteCheckPoliciesErrorKeepsCheckOK(t *testing.T) {
	srv := newHydraRouteCheckServer(t, hydraRoutePolicyFixtures())
	defer srv.Close()
	check := HydraRouteCheck{
		Client: awgmgr.New(srv.URL),
		Policies: func(context.Context, []awgmgr.DNSRoute) ([]wire.PolicyBrief, error) {
			return nil, errors.New("policy-interfaces: HTTP 500")
		},
	}
	got := check.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("status = %q, хотим ok", got.Status)
	}
	if got.Details["policies_error"] != "policy-interfaces: HTTP 500" {
		t.Errorf("policies_error = %v", got.Details["policies_error"])
	}
	if _, ok := got.Details["policies"]; ok {
		t.Error("policies при ошибке чтения")
	}
}

// Старая сборка без политик (читатель вернул nil, nil) -- поля нет вовсе.
func TestHydraRouteCheckNoPoliciesNoField(t *testing.T) {
	srv := newHydraRouteCheckServer(t, hydraRoutePolicyFixtures())
	defer srv.Close()
	check := HydraRouteCheck{
		Client:   awgmgr.New(srv.URL),
		Policies: func(context.Context, []awgmgr.DNSRoute) ([]wire.PolicyBrief, error) { return nil, nil },
	}
	got := check.Run(context.Background(), Deps{})
	for _, k := range []string{"policies", "policies_error"} {
		if _, ok := got.Details[k]; ok {
			t.Errorf("ключ %s у сборки без политик", k)
		}
	}
}
