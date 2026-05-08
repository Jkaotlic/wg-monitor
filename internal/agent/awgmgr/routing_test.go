package awgmgr

import (
	"context"
	"encoding/json"
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
