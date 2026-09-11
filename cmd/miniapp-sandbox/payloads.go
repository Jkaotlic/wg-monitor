package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Ответы «роутера, которого нет». Собираются из настоящих типов wire, а не из
// строк с JSON: строка разъедется с форматом на первой же правке поля, и
// песочница начнёт показывать экраны, которых в жизни не бывает, -- то есть
// ровно то враньё, против которого написано это приложение.
//
// Экран, который не получил разбираемый ответ, честно говорит «снимок не
// разобрать». Это правильно, но проверять на таком ответе можно только одну
// ветку из десяти -- поэтому здесь лежат данные, а не заглушки.
func sandboxOutput(action string, args map[string]any) string {
	switch action {
	case "route_status":
		return mustJSON(routeSnapshot(routerState()))
	case "tunnel_import":
		// Мастер вытаскивает идентификатор нового туннеля из этой строки:
		// формат ровно тот, что печатает агент. Соврать здесь форматом --
		// значит проверять экран на ответе, которого не бывает.
		id := importTunnel(argString(args, "name", "amnezia_nl"))
		return fmt.Sprintf("✅ Туннель %q создан (id=%s)", argString(args, "name", "amnezia_nl"), id)
	case "route_policy_promote":
		promoteTunnel(argString(args, "tunnel_id", ""))
		return "политика переведена на " + argString(args, "tunnel_id", "")
	case "tunnel_power":
		on, _ := args["on"].(bool)
		setTunnelPower(argString(args, "tunnel_id", ""), on)
		return "интерфейс переключён"
	case "tunnel_traffic":
		return mustJSON(tunnelTraffic(argString(args, "tunnel_id", "awg12"), argString(args, "period", "24h")))
	case "diag_report":
		return mustJSON(diagReport())
	case "diag_now":
		return mustJSON(diagNowReport(time.Now()))
	case "route_lookup":
		return mustJSON(routeLookup(argString(args, "domain", "")))
	// Мастер сверяет адреса выхода этими двумя командами (а не exit_ip_*):
	// адрес через туннель обязан отличаться от прямого, иначе трафик мимо
	// VPN, и шаг проверки честно проваливается.
	case "check_direct", "exit_ip_direct":
		return "Проверка напрямую\nExit IP: 203.0.113.1"
	case "check_via_tunnel", "exit_ip_tunnel", "exit_ip":
		return "Проверка через туннель\nExit IP: 203.0.113.9"
	case "recheck":
		return "проверки перезапущены"
	default:
		return "песочница: " + action + " выполнен"
	}
}

func argString(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Парк из двух линий: одна несёт трафик, вторая готова подхватить. Плюс
// политика с цепочкой и правила -- без них экраны «Туннели» и «Маршруты»
// показывают форму, но не смысл.
// Состояние «роутера» между командами. Мастер замены -- цепочка из шести
// шагов, и каждый следующий смотрит на последствия предыдущего: без памяти
// песочница проверяла бы только первый.
var sandboxRouter = struct {
	mu       sync.Mutex
	imported []wire.TunnelMeta
	active   string
	disabled map[string]bool
	nextID   int
}{active: "awg12", disabled: map[string]bool{}, nextID: 21}

type routerSnapshotState struct {
	imported []wire.TunnelMeta
	active   string
	disabled map[string]bool
}

func routerState() routerSnapshotState {
	sandboxRouter.mu.Lock()
	defer sandboxRouter.mu.Unlock()
	st := routerSnapshotState{active: sandboxRouter.active, disabled: map[string]bool{}}
	st.imported = append(st.imported, sandboxRouter.imported...)
	for k, v := range sandboxRouter.disabled {
		st.disabled[k] = v
	}
	return st
}

func importTunnel(name string) string {
	sandboxRouter.mu.Lock()
	defer sandboxRouter.mu.Unlock()
	id := fmt.Sprintf("awg%d", sandboxRouter.nextID)
	sandboxRouter.nextID++
	sandboxRouter.imported = append(sandboxRouter.imported, wire.TunnelMeta{
		ID: id, Name: name, Iface: "nwg" + id, Type: "managed",
		Enabled: true, Available: true, Status: "up",
		HasHandshake: true, HandshakeAge: 5, PingStatus: "ok", RestartMethod: "control",
	})
	return id
}

func promoteTunnel(id string) {
	if id == "" {
		return
	}
	sandboxRouter.mu.Lock()
	defer sandboxRouter.mu.Unlock()
	sandboxRouter.active = id
}

func setTunnelPower(id string, on bool) {
	if id == "" {
		return
	}
	sandboxRouter.mu.Lock()
	defer sandboxRouter.mu.Unlock()
	sandboxRouter.disabled[id] = !on
}

func routeSnapshot(st routerSnapshotState) wire.RouteSnapshot {
	snap := wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{
			{
				ID: "awg12", Name: "Амстердам", Iface: "opkgtun12", Type: "amneziawg",
				Enabled: true, Available: true, Status: "up",
				HasHandshake: true, HandshakeAge: 21, PingStatus: "ok",
				DefaultRoute: true, RestartMethod: "control",
			},
			{
				ID: "awg10", Name: "Франкфурт", Iface: "opkgtun10", Type: "amneziawg",
				Enabled: true, Available: true, Status: "down",
				HasHandshake: true, HandshakeAge: 3600, PingStatus: "fail", PingFails: 4, PingFailMax: 5,
				RestartMethod: "control",
			},
		},
		// Правила привязаны ЛИБО к интерфейсу, либо к политике -- эти
		// множества не пересекаются (сверено с живым роутером: у него
		// counts пуст, а девятнадцать правил висят на политике). Держать
		// одни и те же правила в обоих местах значило бы показывать в
		// песочнице числа, которых на роутере не бывает.
		Counts: map[string]wire.TunnelCounts{
			"awg12": {Static: 4},
			"awg10": {DNS: 2},
		},
		Other: wire.TunnelCounts{DNS: 1},
		Policies: []wire.RoutePolicySummary{
			{
				Name: "HydraRoute", Description: "обход блокировок",
				Interfaces: []wire.RoutePolicyInterface{
					{Bind: "OpkgTun12", Name: "Амстердам", Role: "active", Available: true, Order: 1, TunnelID: "awg12", ViaVPN: true},
					// Резерв ДОСТУПЕН: иначе движку починки некуда уводить
					// трафик, и весь сценарий с failover не отрепетировать.
					{Bind: "OpkgTun10", Name: "Франкфурт", Role: "fallback", Available: true, Order: 2, TunnelID: "awg10", ViaVPN: true},
				},
				DNS: 32, HRNeo: 28, ActiveTunnelID: "awg12", ViaVPN: true,
			},
		},
		Rules: []wire.RouteRuleSummary{
			{Name: "Figma", Bind: "OpkgTun12", Backend: "hydraroute", Kind: "dns", Enabled: true},
			{Name: "GitHub", Bind: "OpkgTun12", Backend: "hydraroute", Kind: "dns", Enabled: true},
			{Name: "офисная сеть", Bind: "OpkgTun10", Backend: "ndms", Kind: "static", Enabled: true},
		},
		DefaultEgress: wire.DefaultEgressDirect,
		PolicyModel:   true,
	}
	snap.Tunnels = append(snap.Tunnels, st.imported...)
	for i := range snap.Tunnels {
		if st.disabled[snap.Tunnels[i].ID] {
			snap.Tunnels[i].Enabled = false
			snap.Tunnels[i].Status = "down"
		}
	}
	// Политика ведёт туда, куда её перевели: цепочку строим от активного
	// звена, иначе шаг проверки смотрел бы на вчерашнюю картину.
	if st.active != "" {
		snap.Policies[0].ActiveTunnelID = st.active
		if st.active != "awg12" {
			for _, t := range st.imported {
				if t.ID == st.active {
					snap.Policies[0].Interfaces = append([]wire.RoutePolicyInterface{{
						Bind: t.Iface, Name: t.Name, Role: "active", Available: true, Order: 0,
						TunnelID: t.ID, ViaVPN: true,
					}}, snap.Policies[0].Interfaces...)
				}
			}
		}
	}
	return snap
}

// Ряд -- это СКОРОСТИ в байтах в секунду, а объём лежит отдельными суммами:
// складывать точки нельзя, и песочница повторяет эту ловушку намеренно.
func tunnelTraffic(tunnelID, period string) wire.TunnelTraffic {
	base := time.Now().Add(-24 * time.Hour).Unix()
	points := make([]wire.TrafficPoint, 0, 48)
	for i := 0; i < 48; i++ {
		points = append(points, wire.TrafficPoint{
			T:  base + int64(i)*1800,
			RX: float64(40_000 + (i%7)*15_000),
			TX: float64(12_000 + (i%5)*4_000),
		})
	}
	return wire.TunnelTraffic{
		TunnelID: tunnelID, Period: period,
		RXTotal: 5_741_000_000, TXTotal: 3_120_000_000,
		CurrentRx: 61_000, CurrentTx: 17_500,
		Points: points,
	}
}

// Отчёт диагностики -- форма ответа awg-manager /api/diagnostics, которую
// разбирает parseDiag.
func diagReport() map[string]any {
	return map[string]any{
		"generatedAt": time.Now().Format(time.RFC3339),
		"durationMs":  1840,
		"system": map[string]any{
			"appVersion":    "2.17.2",
			"keeneticOS":    "4.3.9",
			"uptime":        "12 дней",
			"totalMemoryMB": 256,
			"kernelModule":  map[string]any{"loaded": true},
		},
		"network": map[string]any{
			"internet": true,
			"dns":      map[string]any{"ok": true, "servers": []string{"1.1.1.1", "8.8.8.8"}},
		},
		"tunnels": []map[string]any{
			{"id": "awg12", "name": "Амстердам", "status": "up", "handshakeAgeSec": 21},
			{"id": "awg10", "name": "Франкфурт", "status": "down"},
		},
	}
}

// Свежий отчёт diag_now -- форма awg-manager 2.18.2 (плоский tests[], у
// проверок VPN-туннеля tunnelId/tunnelName), как в
// internal/backend/alerts/testdata/diag_report_awgm_2_18_2.json. Проверок
// restart_cycle нет намеренно: свежая диагностика туннели не перезапускает.
func diagNowReport(now time.Time) map[string]any {
	test := func(name, desc, status, detail string) map[string]any {
		return map[string]any{"name": name, "description": desc, "status": status, "detail": detail}
	}
	tests := []map[string]any{
		test("wan_connectivity", "WAN up с gateway", "pass", "default via 198.51.100.1 dev eth3"),
		test("ndms_health", "NDMS отвечает", "pass", "5.2 Alpha 8"),
		test("kernel_module", "Модули AmneziaWG", "skip", "Не требуется: NDMS обрабатывает обфускацию нативно"),
		test("clock_skew", "Расхождение времени с эталоном", "pass", "Расхождение 1s (норма)"),
		test("direct_connectivity", "Direct связность (без прокси/туннеля)", "pass", "Direct egress работает (HTTP 204)"),
		test("singbox_runtime", "Sing-box runtime", "skip", "Sing-box не установлен"),
	}
	for i, tun := range []struct{ id, name, endpoint string }{
		{"awg12", "vpn-nl", "203.0.113.21"},
		{"awg10", "vpn-reserve", "203.0.113.22"},
	} {
		per := []map[string]any{
			test("dns_resolve", "Резолв endpoint", "pass", "Endpoint уже IP-адрес"),
			test("endpoint_reachable", "Ping endpoint", "pass", fmt.Sprintf("Round-trip min/avg/max = %d.10/%d.40/%d.90 ms.", 60+i*120, 75+i*130, 88+i*140)),
			test("endpoint_route_check", "Host route до endpoint", "pass", tun.endpoint+" via 198.51.100.1 dev eth3"),
			test("awg_handshake", "Handshake свежий (<3 мин)", "pass", "1 minute, 5 seconds ago"),
			test("tunnel_connectivity", "Связность через туннель", "pass", "IP: "+tun.endpoint),
			test("config_parse", "Валидация конфига", "pass", "Конфиг валиден"),
			test("mtu_check", "MTU интерфейса", "pass", "MTU = 1280"),
			test("pingcheck_health", "PingCheck статус", "skip", "PingCheck не включён"),
			test("dns_leak_check", "DNS leak проверка", "skip", "DNS не настроен в конфигурации туннеля"),
		}
		for _, t := range per {
			t["tunnelId"], t["tunnelName"] = tun.id, tun.name
		}
		tests = append(tests, per...)
	}
	tests = append(tests, test("route_leak_check", "Осиротевшие маршруты", "pass", "Нет осиротевших маршрутов"))
	return map[string]any{
		"version":     "1.0",
		"generatedAt": now.Format(time.RFC3339),
		"durationMs":  16416,
		"tests":       tests,
		"system":      map[string]any{"appVersion": "2.18.2+r1", "backend": "kernel"},
	}
}

// Ответ route_lookup зависит только от имени сайта: снимки экрана обязаны
// повторяться. На каждую ветку ответа -- своё имя; остальное уходит главным
// выходом роутера, а он у песочницы -- провайдер (DefaultEgressDirect).
func routeLookup(domain string) wire.RouteLookupResult {
	viaNL := func(rule, pattern string) wire.RouteLookupMatch {
		return wire.RouteLookupMatch{RuleName: rule, Pattern: pattern, Via: wire.LookupViaTunnel, TunnelID: "awg12", TunnelName: "vpn-nl"}
	}
	res := wire.RouteLookupResult{Domain: domain, Matches: []wire.RouteLookupMatch{}}
	switch domain {
	case "claude.ai":
		res.Verdict, res.TunnelID, res.TunnelName = wire.LookupViaTunnel, "awg12", "vpn-nl"
		res.Matches = append(res.Matches, viaNL("Все AI сервисы", "geosite:ANTHROPIC"))
	case "example.org":
		// Правил нет, а главный выход -- VPN-туннель: ответ «напрямую» здесь
		// был бы враньём, и экран обязан это держать.
		res.Verdict, res.ByDefault, res.TunnelID, res.TunnelName = wire.LookupViaTunnel, true, "awg12", "vpn-nl"
	case "mixed.example.com":
		res.Verdict = wire.LookupMixed
		res.Matches = append(res.Matches,
			viaNL("Все AI сервисы", "mixed.example.com"),
			wire.RouteLookupMatch{RuleName: "Рабочие сайты", Pattern: "example.com", Via: wire.LookupViaDirect},
		)
	case "unknown.example.com":
		res.Verdict = wire.LookupViaUnknown
		res.Notes = []string{"hr_not_running"}
	default: // example.com и всё прочее
		res.Verdict, res.ByDefault = wire.LookupViaDirect, true
	}
	return res
}

// Строка для лога: аргументы команды в одну строчку, чтобы в консоли было
// видно, что именно нажал обход.
func argsLine(args map[string]any) string {
	if len(args) == 0 {
		return "без аргументов"
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}
