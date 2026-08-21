package main

import (
	"encoding/json"
	"fmt"
	"strings"
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
		return mustJSON(routeSnapshot())
	case "tunnel_traffic":
		return mustJSON(tunnelTraffic(argString(args, "tunnel_id", "awg12"), argString(args, "period", "24h")))
	case "diag_report":
		return mustJSON(diagReport())
	case "exit_ip_direct":
		return "Exit IP: 203.0.113.1"
	case "exit_ip_tunnel", "exit_ip":
		return "Exit IP: 203.0.113.9"
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
func routeSnapshot() wire.RouteSnapshot {
	return wire.RouteSnapshot{
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
					{Bind: "OpkgTun10", Name: "Франкфурт", Role: "fallback", Available: false, Order: 2, TunnelID: "awg10", ViaVPN: true},
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
