package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// AGENT-08: политики не прочитались (сбой, а не «старая сборка без ручки») --
// это не «политик нет». Прежде перенос в таком случае дописывал интерфейс
// правилам политики (менял модель маршрутизации) и отвечал «ок». Теперь
// такие правила не трогаются, а итог -- partial с причиной.
func TestRouteRebind_PolicyReadFailureIsNotNoPolicies(t *testing.T) {
	updated := map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"awg10","name":"main","iface":"opkgtun10","type":"managed","status":"up","available":true,"defaultRoute":true},
			{"id":"awg20","name":"spare","iface":"nwg0","ndmsName":"Wireguard0","type":"managed","status":"up","available":true}
		]}`))
	})
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") == "awg10" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg10","name":"main","interfaceName":"opkgtun10","enabled":true,"defaultRoute":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg20","name":"spare","interfaceName":"nwg0","ndmsName":"Wireguard0","enabled":true}}`))
	})
	mux.HandleFunc("/api/routing/access-policies", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ndms timeout", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:AIgeo","name":"AIgeo","backend":"hydraroute","hrPolicyName":"HydraRoute","enabled":true,
			 "domains":["geosite:OPENAI"],"manualDomains":["geosite:OPENAI"]},
			{"id":"ndms:Work","name":"Work","backend":"ndms","enabled":true,
			 "routes":[{"interface":"opkgtun10","tunnelId":"awg10"}]}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/update", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if id, _ := body["id"].(string); id != "" {
			updated[id] = true
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := RouteRebind(context.Background(), awgmgr.New(srv.URL), "awg10", "awg20")
	if err != nil {
		t.Fatalf("RouteRebind: %v", err)
	}
	if updated["hr:AIgeo"] {
		t.Fatal("правило политики переписано вслепую, хотя политики не прочитались")
	}
	if !updated["ndms:Work"] {
		t.Fatal("явная привязка к исходному туннелю от политик не зависит и должна переехать")
	}
	if st := routeRebindCommandStatus(out); st != "partial" {
		t.Fatalf("status = %q, want partial\n%s", st, out)
	}
	if !strings.Contains(out, "access-policies") {
		t.Fatalf("причина не названа:\n%s", out)
	}
}
