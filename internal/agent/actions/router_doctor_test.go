package actions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

// TestRouterDoctor_WarnsOnMultipleDefaultRoutes proves the 🩺 router-doctor
// snapshot flags the ambiguous "two tunnels both defaultRoute=true" config and
// names the authoritative one from settings.download.routeTag (awg12 / "NL").
func TestRouterDoctor_WarnsOnMultipleDefaultRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/system/info":
			_, _ = w.Write([]byte(`{"success":true,"data":{"version":"2.8.2"}}`))
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg10","name":"local","interfaceName":"nwg0","status":"running","enabled":true,"defaultRoute":true},
				{"id":"awg12","name":"NL","interfaceName":"nwg3","status":"running","enabled":true,"defaultRoute":true}
			]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":false,"tunnels":[]}}`))
		case "/api/settings/get":
			_, _ = w.Write([]byte(`{"success":true,"data":{"download":{"routeTag":"awg-awg12","routeKind":"awg"}}}`))
		}
	}))
	defer srv.Close()

	_, out := RouterDoctor(context.Background(), awgmgr.New(srv.URL), nil)
	if !strings.Contains(out, "default route conflict") {
		t.Fatalf("expected multiple-default warning, got:\n%s", out)
	}
	if !strings.Contains(out, "NL") {
		t.Fatalf("warning should name the authoritative default (awg12/NL), got:\n%s", out)
	}
}

// TestRouterDoctor_NoWarnOnSingleDefaultRoute proves the warning is silent on a
// normal single-default config.
func TestRouterDoctor_NoWarnOnSingleDefaultRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/system/info":
			_, _ = w.Write([]byte(`{"success":true,"data":{"version":"2.8.2"}}`))
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg12","name":"NL","interfaceName":"nwg3","status":"running","enabled":true,"defaultRoute":true}
			]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":false,"tunnels":[]}}`))
		}
	}))
	defer srv.Close()

	_, out := RouterDoctor(context.Background(), awgmgr.New(srv.URL), nil)
	if strings.Contains(out, "default route conflict") {
		t.Fatalf("single default must not trigger the conflict warning:\n%s", out)
	}
}
