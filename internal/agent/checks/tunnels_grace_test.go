package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// A2.10: разовый перезапуск VPN-туннеля (fleet-audit 15.09, alyaba). Правила
// HydraRoute без явного маршрута засчитывались только работающему главному
// туннелю: на обходе, где он перезапускался, правил у него «не было», и
// падение глушилось как у неиспользуемого. Окно терпимости: туннель,
// работавший не больше двух обходов назад, правил не теряет.
func TestTunnelsCheck_RestartedTunnelKeepsRulesForTwoRuns(t *testing.T) {
	var mu sync.Mutex
	status := "running"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			mu.Lock()
			st := status
			mu.Unlock()
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"id":"awg10","name":"main","type":"awg","status":"` + st + `","enabled":true,"defaultRoute":true,"interfaceName":"nwg1"}
			]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
				{"tunnelId":"awg10","status":"disabled","method":"icmp","failCount":0,"failThreshold":3}
			]}}`))
		case "/api/dns-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[
				{"id":"hr:AI","routes":null,"backend":"hydraroute","hrPolicyName":"HydraRoute"}
			]}`))
		case "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		case "/api/routing/access-policies":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		case "/api/settings/get":
			_, _ = w.Write([]byte(`{"success":true,"data":{"download":{"routeTag":""}}}`))
		case "/api/monitoring/matrix":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	chk := TunnelsCheck{Client: awgmgr.New(srv.URL), Grace: &RunGrace{}}
	run := func() wire.Check {
		t.Helper()
		for _, c := range chk.Run(context.Background(), Deps{}) {
			if c.Name == "tunnel_awg10" {
				return c
			}
		}
		t.Fatal("tunnel_awg10 не выдан")
		return wire.Check{}
	}
	set := func(s string) { mu.Lock(); status = s; mu.Unlock() }

	if c := run(); c.Details["routes_dns"] != 1 {
		t.Fatalf("работающий главный: правила не засчитаны: %+v", c.Details)
	}
	set("stopped")
	for i := 1; i <= 2; i++ {
		c := run()
		if c.Status != "fail" {
			t.Fatalf("обход %d после остановки: падение заглушено как у неиспользуемого: %+v", i, c)
		}
		if c.Details["routes_dns"] != 1 || c.Details["run_grace"] != true {
			t.Fatalf("обход %d после остановки: правила потеряны: %+v", i, c.Details)
		}
	}
	// Третий обход подряд -- окно кончилось: правила снова не засчитываются.
	if c := run(); c.Details["routes_dns"] != nil || c.Details["run_grace"] != nil {
		t.Fatalf("окно терпимости не кончилось через два обхода: %+v", c.Details)
	}
	// Поднялся -- снова свой, и следующая остановка снова с окном.
	set("running")
	run()
	set("starting")
	if c := run(); c.Status != "fail" || c.Details["routes_dns"] != 1 {
		t.Fatalf("повторный перезапуск без окна: %+v", c)
	}
}

// Без памяти (Grace nil) -- прежнее поведение: остановленному правила не
// засчитываются.
func TestRunGraceNilKeepsOldBehaviour(t *testing.T) {
	var g *RunGrace
	if got := g.Observe([]awgmgr.Tunnel{{ID: "awg10", Enabled: true, InterfaceName: "nwg1", Status: "stopped"}}); len(got) != 0 {
		t.Fatalf("nil-память дала окно: %v", got)
	}
}

// Выключенный в настройках туннель окна не получает: это решение человека,
// а не перезапуск.
func TestRunGraceSkipsDisabledTunnel(t *testing.T) {
	g := &RunGrace{}
	g.Observe([]awgmgr.Tunnel{{ID: "awg10", Enabled: true, InterfaceName: "nwg1", Status: "running"}})
	if got := g.Observe([]awgmgr.Tunnel{{ID: "awg10", Enabled: false, InterfaceName: "nwg1", Status: "stopped"}}); got["awg10"] {
		t.Fatal("выключенному туннелю дано окно")
	}
}
