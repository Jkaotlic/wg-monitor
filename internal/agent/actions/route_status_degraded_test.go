package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// degradedRouterOpts описывает роутер, у которого часть эндпоинтов отвечает
// ошибкой. Ровно этот случай и разбирается ниже: снимок обязан сказать, чего
// он не знает, а не выдать догадку за факт.
type degradedRouterOpts struct {
	policiesStatus  int // 0 -- отдать политики нормально
	polIfacesStatus int // 0 -- отдать интерфейсы политик нормально
}

// fakeDegradedRouter отдаёт один туннель с defaultRoute=true и одно
// HR-Neo-правило, привязанное политикой (hrRouteMode="policy", пустой
// hrPolicyInterfaces -- ровно форма живого 2.16+).
func fakeDegradedRouter(t *testing.T, opts degradedRouterOpts) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg11","name":"work","interfaceName":"opkgtun11","enabled":true,"defaultRoute":true,"status":"running"}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"awg11","name":"work","iface":"opkgtun11","type":"managed","status":"running","available":true}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:Streaming","name":"Streaming","backend":"hydraroute","hrRouteMode":"policy","hrPolicyName":"HydraRoute"}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/routing/access-policies", func(w http.ResponseWriter, r *http.Request) {
		if opts.policiesStatus != 0 {
			http.Error(w, "boom", opts.policiesStatus)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"name":"HydraRoute","interfaces":[{"name":"OpkgTun11","label":"work","order":0}]}
		]}`))
	})
	mux.HandleFunc("/api/routing/policy-interfaces", func(w http.ResponseWriter, r *http.Request) {
		if opts.polIfacesStatus != 0 {
			http.Error(w, "boom", opts.polIfacesStatus)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"OpkgTun11","label":"work","up":true}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func routeStatusSnapshot(t *testing.T, srv *httptest.Server) wire.RouteSnapshot {
	t.Helper()
	out, err := RouteStatus(context.Background(), awgmgr.New(srv.URL))
	if err != nil {
		t.Fatalf("RouteStatus: %v", err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	return snap
}

func hasWarningAbout(snap wire.RouteSnapshot, needle string) bool {
	for _, w := range snap.Warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}

// Провал /api/routing/policy-interfaces сегодня глотается. Без up/down резолвер
// считает КАЖДЫЙ интерфейс лежачим: активного звена нет ни у одной политики,
// active_tunnel_id пуст, бот печатает "нет доступного интерфейса" на всём
// подряд -- и всё это под видом полного снимка, потому что warnings пуст.
func TestRouteStatus_PolicyInterfacesErrorAddsWarning(t *testing.T) {
	srv := fakeDegradedRouter(t, degradedRouterOpts{polIfacesStatus: http.StatusBadGateway})
	snap := routeStatusSnapshot(t, srv)

	if !hasWarningAbout(snap, "/api/routing/policy-interfaces") {
		t.Fatalf("provider of up/down failed silently; warnings = %+v", snap.Warnings)
	}
	// Ровно одна строка: 502 -- это настоящий отказ, и он один.
	if len(snap.Warnings) != 1 {
		t.Errorf("warnings = %+v, want exactly one", snap.Warnings)
	}
	// Роли при этом честно деградируют -- это не ошибка, а следствие; важно,
	// что экран узнаёт о неполноте снимка.
	if len(snap.Policies) != 1 || snap.Policies[0].ActiveTunnelID != "" {
		t.Errorf("policies = %+v", snap.Policies)
	}
}

// 404 -- это "эндпоинта нет", то есть сборка действительно старая. Старая
// модель для неё правильна, и снимок обязан остаться прежним.
func TestRouteStatus_MissingPoliciesEndpointKeepsLegacyModel(t *testing.T) {
	srv := fakeDegradedRouter(t, degradedRouterOpts{
		policiesStatus:  http.StatusNotFound,
		polIfacesStatus: http.StatusNotFound,
	})
	snap := routeStatusSnapshot(t, srv)

	if snap.PolicyModel {
		t.Error("404 on access-policies must not claim the policy model")
	}
	// Старая ветка кредитует fall-through правило активному default-туннелю --
	// ровно как до фазы B.
	if snap.Counts["awg11"].DNS != 1 || snap.Counts["awg11"].HRNeo != 1 {
		t.Errorf("legacy fall-through must stay credited to the default tunnel: %+v", snap.Counts)
	}
	if snap.Other.DNS != 0 {
		t.Errorf("Other = %+v, want zero on the legacy path", snap.Other)
	}
	// И главное: снимок НЕ неполный. 404 -- это ответ роутера "я старая
	// сборка", а не пропавшие данные. Иначе вся легаси-половина парка
	// навсегда получает "⚠ снапшот неполный" на каждом опросе -- ложь о
	// состоянии роутера, у которого всё в порядке.
	if len(snap.Warnings) != 0 {
		t.Errorf("an absent endpoint must not brand the snapshot incomplete: %+v", snap.Warnings)
	}
}

// Старая сборка: нет ни политик, ни их интерфейсов. Второй эндпоинт
// отсутствует по той же причине, что и первый, и молчание про него -- тот же
// сигнал модели, а не потеря данных.
func TestRouteStatus_LegacyRouterWithNeitherPolicyEndpointDoesNotWarn(t *testing.T) {
	srv := fakeDegradedRouter(t, degradedRouterOpts{
		policiesStatus:  http.StatusNotFound,
		polIfacesStatus: http.StatusNotFound,
	})
	snap := routeStatusSnapshot(t, srv)

	if len(snap.Warnings) != 0 {
		t.Errorf("404 on both policy endpoints must not warn: %+v", snap.Warnings)
	}
}

// А вот смешанный случай -- настоящая потеря данных, и молчать про неё
// нельзя.
//
// Политики прочитались, значит модель политик включена и снимок ими живёт.
// Без up/down резолвер считает лежачим каждый интерфейс: активного звена нет
// ни у одной политики, ActiveTunnelID пустеет, бот печатает "нет доступного
// интерфейса" на всё подряд. 404 здесь означает не "старая сборка" (сборка
// заведомо новая -- она отдала политики), а именно недостающие данные.
func TestRouteStatus_MissingPolicyInterfacesOnPolicyModelWarns(t *testing.T) {
	srv := fakeDegradedRouter(t, degradedRouterOpts{polIfacesStatus: http.StatusNotFound})
	snap := routeStatusSnapshot(t, srv)

	if !snap.PolicyModel {
		t.Fatalf("precondition: policies were served, so the policy model must be on")
	}
	if !hasWarningAbout(snap, "/api/routing/policy-interfaces") {
		t.Fatalf("policy model with no up/down is a partial snapshot; warnings = %+v", snap.Warnings)
	}
	if len(snap.Warnings) != 1 {
		t.Errorf("warnings = %+v, want exactly one", snap.Warnings)
	}
}

// 500/таймаут -- это НЕ "старая сборка". Уйти в старую модель здесь значит
// приписать правило default-туннелю по выдумке, которую фаза и удаляет.
func TestRouteStatus_PolicyReadErrorDoesNotInventDefaultBinding(t *testing.T) {
	srv := fakeDegradedRouter(t, degradedRouterOpts{policiesStatus: http.StatusInternalServerError})
	snap := routeStatusSnapshot(t, srv)

	if !hasWarningAbout(snap, "/api/routing/access-policies") {
		t.Fatalf("warnings = %+v", snap.Warnings)
	}
	// Здесь предупреждение уместно и оно ровно одно: интерфейсы политик
	// прочитались нормально.
	if len(snap.Warnings) != 1 {
		t.Errorf("warnings = %+v, want exactly one", snap.Warnings)
	}
	if snap.PolicyModel {
		t.Error("a failed policy read must not claim the policy model either")
	}
	if got := snap.Counts["awg11"].DNS; got != 0 {
		t.Errorf("Counts[awg11].DNS = %d, want 0: the binding is unknown, not the default route", got)
	}
	if snap.Other.DNS != 1 || snap.Other.HRNeo != 1 {
		t.Errorf("Other = %+v, want DNS=1 HRNeo=1 (binding unknown)", snap.Other)
	}
	// И никакой выдуманной политики с выдуманной цепочкой.
	for _, p := range snap.Policies {
		if len(p.Interfaces) > 0 {
			t.Errorf("policy %q got an invented chain: %+v", p.Name, p.Interfaces)
		}
	}
}
