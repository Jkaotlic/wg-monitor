package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// migrateRouter -- роутер с памятью: создание правила кладёт его в список DNS,
// удаление статики убирает её. Флаги ломают отдельные шаги.
type migrateRouter struct {
	mu          sync.Mutex
	hrRunning   bool
	statics     []awgmgr.StaticRoute
	dns         []awgmgr.DNSRoute
	created     []awgmgr.DNSRoute
	deleted     []string
	calls       []string
	failDelete  bool
	hideCreated bool
	// ifaceAfterRefresh -- интерфейс туннеля после /api/routing/refresh: у
	// opkg-туннелей он плавает.
	ifaceAfterRefresh string
	refreshed         bool
}

func (m *migrateRouter) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		b, _ := json.Marshal(map[string]any{"success": true, "data": v})
		_, _ = w.Write(b)
	}
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		iface := "opkgtun12"
		if m.refreshed && m.ifaceAfterRefresh != "" {
			iface = m.ifaceAfterRefresh
		}
		m.mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"awg12","name":"awg12","iface":"` + iface + `","type":"managed","status":"running","available":true}]}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]bool{"installed": true, "running": m.hrRunning})
	})
	mux.HandleFunc("/api/system/hydraroute-control", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.refreshed = true
		m.mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		write(w, m.statics)
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		write(w, m.dns)
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		var rule awgmgr.DNSRoute
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			t.Fatalf("decode create: %v", err)
		}
		m.mu.Lock()
		m.calls = append(m.calls, "create")
		m.created = append(m.created, rule)
		if !m.hideCreated {
			rule.ID = "hr:new"
			m.dns = append(m.dns, rule)
		}
		m.mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/static-routes/delete", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		m.mu.Lock()
		defer m.mu.Unlock()
		m.calls = append(m.calls, "delete:"+id)
		if m.failDelete {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		m.deleted = append(m.deleted, id)
		kept := m.statics[:0]
		for _, s := range m.statics {
			if s.ID != id {
				kept = append(kept, s)
			}
		}
		m.statics = kept
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	return httptest.NewServer(mux)
}

func officeStatic() []awgmgr.StaticRoute {
	return []awgmgr.StaticRoute{{ID: "st1", Name: "Офис", TunnelID: "OpkgTun12", Subnets: []string{"203.0.113.0/24", "198.51.100.7"}, Enabled: true}}
}

// Оператор 28.10.2026: уже заведённые статические маршруты переносятся в
// HydraRoute Neo. Порядок -- сначала новое правило, потом удаление старого:
// трафик не остаётся без маршрута ни на миг.
func TestRouteMigrate_CreatesHRNeoRuleThenDeletesStatic(t *testing.T) {
	m := &migrateRouter{hrRunning: true, statics: officeStatic()}
	srv := m.server(t)
	defer srv.Close()
	c := awgmgr.New(srv.URL)

	plan, err := buildRouteMigratePlan(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || plan.Route.Backend != "hydraroute" || plan.From.ID != "st1" {
		t.Fatalf("plan = %+v", plan)
	}
	// Регистр привязки статики («OpkgTun12») и интерфейса туннеля («opkgtun12»)
	// разный -- это одно имя, и перенос обязан его узнать.
	if !strings.EqualFold(plan.Route.Bind, "opkgtun12") {
		t.Fatalf("bind = %q", plan.Route.Bind)
	}
	out, err := RouteMigrateJSON(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1", PreviewHash: plan.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.calls, ",") != "create,delete:st1" {
		t.Fatalf("порядок вызовов: %v", m.calls)
	}
	rule := m.created[0]
	if rule.Backend != "hydraroute" || rule.Name != "Офис" || strings.Join(rule.Subnets, ",") != "203.0.113.0/24,198.51.100.7" || len(rule.Domains) != 0 {
		t.Fatalf("created = %+v", rule)
	}
	if len(rule.Routes) != 1 || rule.Routes[0].Interface != "opkgtun12" || rule.Routes[0].TunnelID != "awg12" {
		t.Fatalf("привязка = %+v", rule.Routes)
	}
	var res wire.RouteApplyResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Action != "migrate" || res.RouteID != "hr:new" || res.Warning != "" {
		t.Fatalf("result = %+v", res)
	}
}

// Выключенная статика переезжает выключенной: перенос не должен молча
// включить маршрут, который человек погасил.
func TestRouteMigrate_KeepsDisabledState(t *testing.T) {
	statics := officeStatic()
	statics[0].Enabled = false
	m := &migrateRouter{hrRunning: true, statics: statics}
	srv := m.server(t)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	plan, err := buildRouteMigratePlan(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RouteMigrateJSON(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1", PreviewHash: plan.Hash}); err != nil {
		t.Fatal(err)
	}
	if m.created[0].Enabled {
		t.Fatal("выключенный маршрут переехал включённым")
	}
}

func TestRouteMigrate_RefusedWithoutHRNeo(t *testing.T) {
	m := &migrateRouter{hrRunning: false, statics: officeStatic()}
	srv := m.server(t)
	defer srv.Close()
	_, err := buildRouteMigratePlan(context.Background(), awgmgr.New(srv.URL), wire.RouteMigrateRequest{RouteID: "st1"})
	if err == nil || !strings.Contains(err.Error(), "HydraRoute") {
		t.Fatalf("без Neo переносить некуда: %v", err)
	}
}

func TestRouteMigrate_UnknownRoute(t *testing.T) {
	m := &migrateRouter{hrRunning: true, statics: officeStatic()}
	srv := m.server(t)
	defer srv.Close()
	if _, err := buildRouteMigratePlan(context.Background(), awgmgr.New(srv.URL), wire.RouteMigrateRequest{RouteID: "nope"}); err == nil {
		t.Fatal("несуществующий маршрут -- ошибка")
	}
}

// Новое правило не нашлось в живом списке -- статику НЕ трогать: иначе сеть
// осталась бы вовсе без маршрута.
func TestRouteMigrate_KeepsStaticWhenNewRuleNotConfirmed(t *testing.T) {
	m := &migrateRouter{hrRunning: true, statics: officeStatic(), hideCreated: true}
	srv := m.server(t)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	plan, err := buildRouteMigratePlan(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := RouteMigrateJSON(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1", PreviewHash: plan.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.deleted) != 0 {
		t.Fatalf("статика удалена без подтверждённой замены: %v", m.deleted)
	}
	var res wire.RouteApplyResult
	_ = json.Unmarshal([]byte(out), &res)
	if !res.NotApplied || res.Warning == "" {
		t.Fatalf("неподтверждённая замена -- not_applied и оговорка: %s", out)
	}
}

// Удаление старого не удалось -- правило HR Neo уже создано и ведёт туда же;
// это не провал, а оговорка, которую человек обязан увидеть.
func TestRouteMigrate_WarnsWhenStaticDeleteFails(t *testing.T) {
	m := &migrateRouter{hrRunning: true, statics: officeStatic(), failDelete: true}
	srv := m.server(t)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	plan, err := buildRouteMigratePlan(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := RouteMigrateJSON(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1", PreviewHash: plan.Hash})
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteApplyResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.RouteID != "hr:new" || !strings.Contains(res.Warning, "static") {
		t.Fatalf("result = %+v", res)
	}
}

func TestRouteMigrate_StalePreviewRefused(t *testing.T) {
	m := &migrateRouter{hrRunning: true, statics: officeStatic()}
	srv := m.server(t)
	defer srv.Close()
	_, err := RouteMigrateJSON(context.Background(), awgmgr.New(srv.URL), wire.RouteMigrateRequest{RouteID: "st1", PreviewHash: "stale"})
	if err == nil || len(m.calls) != 0 {
		t.Fatalf("устаревшее превью -- отказ без единой записи: err=%v calls=%v", err, m.calls)
	}
}

// Интерфейс opkg-туннеля сменился между превью и применением: правило встало
// на новый, и сверка обязана искать его там же, а не объявлять «не найдено»
// и оставлять дубль.
func TestRouteMigrate_ReconcilesOnIfaceAfterRefresh(t *testing.T) {
	m := &migrateRouter{hrRunning: true, statics: officeStatic(), ifaceAfterRefresh: "opkgtun13"}
	srv := m.server(t)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	plan, err := buildRouteMigratePlan(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := RouteMigrateJSON(context.Background(), c, wire.RouteMigrateRequest{RouteID: "st1", PreviewHash: plan.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if m.created[0].Routes[0].Interface != "opkgtun13" {
		t.Fatalf("правило обязано встать на нынешний интерфейс: %+v", m.created[0].Routes)
	}
	var res wire.RouteApplyResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.NotApplied || res.Warning != "" || len(m.deleted) != 1 {
		t.Fatalf("перенос на сменившийся интерфейс -- обычный успех: %s deleted=%v", out, m.deleted)
	}
}
