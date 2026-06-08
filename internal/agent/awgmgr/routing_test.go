package awgmgr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListDNSRoutes_HappyPath(t *testing.T) {
	const payload = `{"success":true,"data":[
		{"id":"hr:Vk","name":"Vk","domains":["vk.com"],"manualDomains":["vk.com"],
		 "routes":[{"interface":"nwg1","tunnelId":"nwg1","fallback":"auto"}],
		 "enabled":true,"backend":"hydraroute","hrPolicyName":"HydraRoute"},
		{"id":"hr:Sber","name":"Sber","domains":["sberbank.ru"],"manualDomains":["sberbank.ru"],
		 "routes":null,"enabled":true,"backend":"hydraroute","hrPolicyName":"HydraRoute"}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns-routes/list" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.ListDNSRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len: %d", len(got))
	}
	if got[0].ID != "hr:Vk" || len(got[0].Routes) != 1 || got[0].Routes[0].Interface != "nwg1" {
		t.Errorf("got[0]: %+v", got[0])
	}
	if got[1].Routes != nil {
		t.Errorf("got[1] should have nil routes (fall-through): %+v", got[1])
	}
}

func TestUpdateDNSRoute_SendsFullBody(t *testing.T) {
	var got DNSRoute
	var gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns-routes/update" || r.Method != http.MethodPost {
			t.Errorf("method/path: %s %q", r.Method, r.URL.Path)
		}
		gotID = r.URL.Query().Get("id")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	rule := DNSRoute{
		ID: "hr:Vk", Name: "Vk", Backend: "hydraroute", HRPolicyName: "HydraRoute",
		Routes: []DNSRouteEntry{{Interface: "nwg0", TunnelID: "nwg0", Fallback: "auto"}},
	}
	if err := c.UpdateDNSRoute(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	if gotID != "hr:Vk" {
		t.Errorf("id query: %q", gotID)
	}
	if got.Routes == nil || got.Routes[0].Interface != "nwg0" {
		t.Errorf("body: %+v", got)
	}
	if !strings.Contains(got.Backend, "hydraroute") {
		t.Errorf("backend not preserved: %+v", got)
	}
}

// Regression for a 2026-05-13 prod incident: HR-Neo IDs routinely contain
// spaces+colons (e.g. "hr:CIDR: iplist: Telegram.org"). Without URL-encoding
// the request gets rejected with HTTP 400 by the HTTP parser before reaching
// awg-manager — the rebind result was 6 ok / 6 FAIL where every failure was
// an ID with a space.
func TestUpdateDNSRoute_EscapesSpacesAndColonsInID(t *testing.T) {
	var gotRawQuery, gotDecodedID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		gotDecodedID = r.URL.Query().Get("id")
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	rule := DNSRoute{
		ID: "hr:CIDR: iplist: Telegram.org", Name: "Telegram",
		Backend: "hydraroute", HRPolicyName: "HydraRoute",
		Routes: []DNSRouteEntry{{Interface: "nwg0", TunnelID: "nwg0"}},
	}
	if err := c.UpdateDNSRoute(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotRawQuery, " ") {
		t.Errorf("raw query must not contain unencoded space: %q", gotRawQuery)
	}
	if gotDecodedID != "hr:CIDR: iplist: Telegram.org" {
		t.Errorf("decoded id mismatch: %q", gotDecodedID)
	}
}

func TestCreateDNSRoute_SendsCreateBody(t *testing.T) {
	var got DNSRoute
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns-routes/create" || r.Method != http.MethodPost || r.URL.RawQuery != "" {
			t.Errorf("expected POST /api/dns-routes/create with no query, got %s %q?%q", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("Content-Type: %q", ct)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	rule := DNSRoute{
		Name: "Telegram", Domains: []string{"telegram.org"}, ManualDomains: []string{"telegram.org"},
		Routes: []DNSRouteEntry{{Interface: "nwg0", TunnelID: "nwg0", Fallback: "auto"}}, Enabled: true,
	}
	if err := c.CreateDNSRoute(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Telegram" || got.Domains[0] != "telegram.org" || got.Routes[0].TunnelID != "nwg0" {
		t.Errorf("body: %+v", got)
	}
}

func TestDeleteDNSRoute_EscapesIDInQuery(t *testing.T) {
	var gotRawQuery, gotDecodedID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns-routes/delete" || r.Method != http.MethodPost {
			t.Errorf("expected POST /api/dns-routes/delete, got %s %q", r.Method, r.URL.Path)
		}
		gotRawQuery = r.URL.RawQuery
		gotDecodedID = r.URL.Query().Get("id")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.DeleteDNSRoute(context.Background(), "hr:CIDR: iplist: Telegram.org"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotRawQuery, " ") {
		t.Errorf("raw query must not contain unencoded space: %q", gotRawQuery)
	}
	if gotDecodedID != "hr:CIDR: iplist: Telegram.org" {
		t.Errorf("decoded id mismatch: %q", gotDecodedID)
	}
}

func TestListStaticRoutes_HappyPath(t *testing.T) {
	const payload = `{"success":true,"data":[
		{"id":"s1","name":"work","tunnelID":"nwg1","subnets":["10.0.0.0/8"],"fallback":"auto","enabled":true}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/static-routes/list" {
			t.Errorf("path: %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.ListStaticRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TunnelID != "nwg1" {
		t.Errorf("got: %+v", got)
	}
}

func TestUpdateStaticRoute_NoIDInURL_FullBody(t *testing.T) {
	var got StaticRoute
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/static-routes/update" || r.URL.RawQuery != "" {
			t.Errorf("expected path /api/static-routes/update with NO query, got %q?%q", r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	rule := StaticRoute{ID: "s1", Name: "work", TunnelID: "nwg0", Subnets: []string{"10.0.0.0/8"}, Fallback: "auto", Enabled: true}
	if err := c.UpdateStaticRoute(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	if got.ID != "s1" || got.TunnelID != "nwg0" {
		t.Errorf("body: %+v", got)
	}
}

func TestCreateStaticRoute_SendsCreateBody(t *testing.T) {
	var got StaticRoute
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/static-routes/create" || r.Method != http.MethodPost || r.URL.RawQuery != "" {
			t.Errorf("expected POST /api/static-routes/create with no query, got %s %q?%q", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("Content-Type: %q", ct)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	rule := StaticRoute{Name: "corp", TunnelID: "nwg0", Subnets: []string{"10.0.0.0/8"}, Fallback: "auto", Enabled: true}
	if err := c.CreateStaticRoute(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	if got.Name != "corp" || got.TunnelID != "nwg0" || got.Subnets[0] != "10.0.0.0/8" {
		t.Errorf("body: %+v", got)
	}
}

func TestDeleteStaticRoute_SendsIDInQuery(t *testing.T) {
	var gotBody []byte
	var gotRawQuery, gotDecodedID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/static-routes/delete" || r.Method != http.MethodPost {
			t.Errorf("expected POST /api/static-routes/delete, got %s %q", r.Method, r.URL.Path)
		}
		gotRawQuery = r.URL.RawQuery
		gotDecodedID = r.URL.Query().Get("id")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.DeleteStaticRoute(context.Background(), "static 1"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotRawQuery, " ") {
		t.Errorf("raw query must not contain unencoded space: %q", gotRawQuery)
	}
	if gotDecodedID != "static 1" {
		t.Errorf("decoded id mismatch: %q", gotDecodedID)
	}
	if len(strings.TrimSpace(string(gotBody))) != 0 {
		t.Errorf("expected empty body for v2.10 OpenAPI delete shape, got %q", string(gotBody))
	}
}

func TestDeleteStaticRoute_FallsBackToLegacyBody(t *testing.T) {
	var calls int
	var got StaticRoute
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			if r.URL.Path != "/api/static-routes/delete" || r.URL.Query().Get("id") != "static 1" {
				t.Errorf("first call should use query id, got %q?%q", r.URL.Path, r.URL.RawQuery)
			}
			http.Error(w, `{"error":true,"message":"missing body id"}`, http.StatusBadRequest)
		case 2:
			if r.URL.Path != "/api/static-routes/delete" || r.URL.RawQuery != "" {
				t.Errorf("fallback should use no query, got %q?%q", r.URL.Path, r.URL.RawQuery)
			}
			if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type: %q", ct)
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Fatalf("unexpected extra call %d", calls)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.DeleteStaticRoute(context.Background(), "static 1"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
	if got.ID != "static 1" {
		t.Errorf("fallback body: %+v", got)
	}
}

func TestRoutingTunnels_HappyPath(t *testing.T) {
	const payload = `{"success":true,"data":[
		{"id":"awg11","name":"amnezia_for_awg","iface":"nwg1","type":"managed","status":"running","available":true},
		{"id":"wan:eth3","name":"WAN","iface":"eth3","type":"wan","status":"up","available":true}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/routing/tunnels" {
			t.Errorf("path: %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.RoutingTunnels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Iface != "nwg1" || got[1].Type != "wan" {
		t.Errorf("got: %+v", got)
	}
}

func TestRoutingRefresh_HappyPath(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/routing/refresh" && r.Method == http.MethodPost {
			called = true
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.RoutingRefresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Errorf("/api/routing/refresh not called")
	}
}

func TestPresets_AcceptsAWGManagerEnvelope(t *testing.T) {
	const payload = `{"success":true,"data":{"presets":[
		{"id":"openai","name":"OpenAI","category":"ai","engines":{"dns":{"domains":["chatgpt.com"]}}}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/presets" {
			t.Errorf("path: %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	got, err := New(srv.URL).Presets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "openai" || got[0].Engines.DNS.Domains[0] != "chatgpt.com" {
		t.Fatalf("presets = %+v", got)
	}
}

func TestHydraRouteControl_BodyShape(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/hydraroute-control" {
			t.Errorf("path: %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.HydraRouteControl(context.Background(), "restart"); err != nil {
		t.Fatal(err)
	}
	if body["action"] != "restart" {
		t.Errorf("body: %+v", body)
	}
}
