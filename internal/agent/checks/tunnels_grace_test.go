package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
	now := time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	tick := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	chk := TunnelsCheck{Client: awgmgr.New(srv.URL), Grace: &RunGrace{Window: 2 * time.Minute, Now: clock}}
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
	// Плановые отчёты раз в минуту: остановлен минуту и две назад -- в окне.
	for i := 1; i <= 2; i++ {
		tick(time.Minute)
		c := run()
		if c.Status != "fail" {
			t.Fatalf("минута %d после остановки: падение заглушено как у неиспользуемого: %+v", i, c)
		}
		if c.Details["routes_dns"] != 1 || c.Details["run_grace"] != true {
			t.Fatalf("минута %d после остановки: правила потеряны: %+v", i, c.Details)
		}
	}
	// Окно кончилось: правила снова не засчитываются.
	tick(time.Minute)
	if c := run(); c.Details["routes_dns"] != nil || c.Details["run_grace"] != nil {
		t.Fatalf("окно терпимости не кончилось через две минуты: %+v", c.Details)
	}
	// Поднялся -- снова свой, и следующая остановка снова с окном.
	set("running")
	tick(time.Minute)
	run()
	set("starting")
	tick(time.Minute)
	if c := run(); c.Status != "fail" || c.Details["routes_dns"] != 1 {
		t.Fatalf("повторный перезапуск без окна: %+v", c)
	}
}

// Окно меряется временем, а не числом прогонов: при флапе интерфейса хук
// будит отчёт каждые 10-20 с, и окно «на два прогона» кончалось бы за
// полминуты, посреди того же перезапуска.
func TestRunGraceWindowIsTimeNotRuns(t *testing.T) {
	now := time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)
	g := &RunGrace{Window: 2 * time.Minute, Now: func() time.Time { return now }}
	running := []awgmgr.Tunnel{{ID: "awg10", Enabled: true, InterfaceName: "nwg1", Status: "running"}}
	stopped := []awgmgr.Tunnel{{ID: "awg10", Enabled: true, InterfaceName: "nwg1", Status: "stopped"}}
	g.Observe(running)
	for i := 0; i < 6; i++ {
		now = now.Add(15 * time.Second)
		if got := g.Observe(stopped); !got["awg10"] {
			t.Fatalf("прогон %d через %v после остановки: окно кончилось по счёту прогонов", i+1, time.Duration(i+1)*15*time.Second)
		}
	}
	now = now.Add(31 * time.Second)
	if got := g.Observe(stopped); got["awg10"] {
		t.Fatal("окно не кончилось через две минуты")
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
