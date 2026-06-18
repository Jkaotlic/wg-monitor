package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
)

func TestBuildExternalReachCheckFailsClosedWhenNoDefaultRouteTunnel(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	awgm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/settings/get":
			_, _ = w.Write([]byte(`{"success":true,"data":{"download":{"routeTag":""}}}`))
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"Wireguard0","name":"nl","interfaceName":"nwg0","enabled":true,"defaultRoute":false}],"external":[],"system":[]}}`))
		default:
			t.Fatalf("unexpected awgm path %s", r.URL.Path)
		}
	}))
	defer awgm.Close()

	check := buildExternalReachCheck(&agent.Config{
		ExternalReach: agent.ExternalReachConfig{
			BindToDefault: true,
			Targets:       []agent.ExternalReachTarget{{Name: "local", URL: target.URL}},
		},
	}, awgmgr.New(awgm.URL), nil)
	if check == nil {
		t.Fatal("external reach check must be built")
	}
	got := check.Run(context.Background(), checks.Deps{})
	if got.Status != "fail" {
		t.Fatalf("status=%q, want fail; details=%v", got.Status, got.Details)
	}
	if got.Details["reason"] != "no_default_route_tunnel" {
		t.Fatalf("reason=%v, want no_default_route_tunnel; details=%v", got.Details["reason"], got.Details)
	}
	if !strings.Contains(got.Details["error"].(string), "defaultRoute") {
		t.Fatalf("error should mention defaultRoute: %v", got.Details["error"])
	}
}

func TestBuildExternalReachCheckRecoversWhenDefaultRouteAppears(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	var calls atomic.Int32
	awgm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/settings/get" {
			// No authoritative routeTag in this fixture → callers keep the
			// first-defaultRoute heuristic. Handled separately so the call
			// counter below only advances on /api/tunnels/all.
			_, _ = w.Write([]byte(`{"success":true,"data":{"download":{"routeTag":""}}}`))
			return
		}
		if r.URL.Path != "/api/tunnels/all" {
			t.Fatalf("unexpected awgm path %s", r.URL.Path)
		}
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"Wireguard0","name":"nl","interfaceName":"nwg0","enabled":true,"defaultRoute":false}],"external":[],"system":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"Wireguard1","name":"nl","interfaceName":"nwg1","enabled":true,"defaultRoute":true}],"external":[],"system":[]}}`))
	}))
	defer awgm.Close()

	oldClientForIface := externalReachHTTPClientForIface
	externalReachHTTPClientForIface = func(string) *http.Client {
		return &http.Client{Timeout: time.Second}
	}
	defer func() { externalReachHTTPClientForIface = oldClientForIface }()

	check := buildExternalReachCheck(&agent.Config{
		ExternalReach: agent.ExternalReachConfig{
			BindToDefault: true,
			Targets:       []agent.ExternalReachTarget{{Name: "local", URL: target.URL}},
		},
	}, awgmgr.New(awgm.URL), nil)
	if check == nil {
		t.Fatal("external reach check must be built")
	}
	first := check.Run(context.Background(), checks.Deps{})
	if first.Status != "fail" || first.Details["reason"] != "no_default_route_tunnel" {
		t.Fatalf("first run should fail closed on missing default route: status=%s details=%v", first.Status, first.Details)
	}
	second := check.Run(context.Background(), checks.Deps{})
	if second.Status != "ok" {
		t.Fatalf("second run should recover after default route appears: status=%s details=%v", second.Status, second.Details)
	}
	if second.Details["via_interface"] != "nwg1" {
		t.Fatalf("via_interface=%v, want nwg1; details=%v", second.Details["via_interface"], second.Details)
	}
}

func TestFetchIfaceMapSkipsStartingTunnels(t *testing.T) {
	awgm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tunnels/all" {
			t.Fatalf("unexpected awgm path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg10","ndmsName":"Wireguard1","interfaceName":"nwg1","enabled":true,"status":"starting"},
			{"id":"awg11","ndmsName":"Wireguard5","interfaceName":"nwg5","enabled":true,"status":"running"}
		],"external":[],"system":[]}}`))
	}))
	defer awgm.Close()

	got, err := fetchIfaceMap(context.Background(), awgmgr.New(awgm.URL))
	if err != nil {
		t.Fatal(err)
	}
	if got["Wireguard1"] != "" {
		t.Fatalf("starting tunnel must not be mapped: %+v", got)
	}
	if got["Wireguard5"] != "nwg5" {
		t.Fatalf("running tunnel missing from map: %+v", got)
	}
}

// TestPickDefaultRouteIfacePrefersRouteTagOverFirstListed pins the fix: when
// several tunnels carry defaultRoute=true, external_reach must bind to the one
// awg-manager actually routes through (settings.download.routeTag → awg12/nwg3),
// not the first-listed default (awg10/nwg0).
func TestPickDefaultRouteIfacePrefersRouteTagOverFirstListed(t *testing.T) {
	awgm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg10","name":"local","interfaceName":"nwg0","enabled":true,"defaultRoute":true},
				{"id":"awg12","name":"nl","interfaceName":"nwg3","enabled":true,"defaultRoute":true}
			],"external":[],"system":[]}}`))
		case "/api/settings/get":
			_, _ = w.Write([]byte(`{"success":true,"data":{"download":{"routeTag":"awg-awg12","routeKind":"awg"}}}`))
		default:
			t.Fatalf("unexpected awgm path %s", r.URL.Path)
		}
	}))
	defer awgm.Close()

	got := pickDefaultRouteIface(context.Background(), awgmgr.New(awgm.URL), nil)
	if got != "nwg3" {
		t.Fatalf("pickDefaultRouteIface=%q, want nwg3 (authoritative routeTag awg12), not nwg0 (first-listed)", got)
	}
}
