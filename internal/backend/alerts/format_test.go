package alerts

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestFormatHardTunnelLinkedRoutes(t *testing.T) {
	t.Run("default tunnel with HR-Neo fall-through", func(t *testing.T) {
		got := FormatHard(HardArgs{
			Nickname: "vasya", CheckName: "tunnel_awg11",
			HardSince: time.Now(),
			Check: wire.Check{
				Name: "tunnel_awg11", Status: "fail",
				Details: map[string]any{
					"tunnel_name":   "primary",
					"interface":     "nwg1",
					"routes_dns":    48,
					"routes_dns_hr": 48,
					"routes_static": 3,
				},
			},
		})
		if !strings.Contains(got, "Через этот VPN-туннель идут правила: 48 по именам сайтов, 3 по адресам") {
			t.Fatalf("missing linked-routes line:\n%s", got)
		}
	})
	t.Run("mixed HR-Neo and ndms DNS", func(t *testing.T) {
		got := FormatHard(HardArgs{
			Nickname: "vasya", CheckName: "tunnel_awg11",
			HardSince: time.Now(),
			Check: wire.Check{
				Details: map[string]any{"routes_dns": 10, "routes_dns_hr": 7, "routes_static": 0},
			},
		})
		if !strings.Contains(got, "Через этот VPN-туннель идут правила: 10 по именам сайтов") {
			t.Fatalf("missing mixed-HR line:\n%s", got)
		}
		if strings.Contains(got, "Static") {
			t.Fatalf("Static must be omitted when zero:\n%s", got)
		}
	})
	t.Run("zero routes omits the line entirely", func(t *testing.T) {
		got := FormatHard(HardArgs{
			Nickname: "vasya", CheckName: "tunnel_awg11",
			HardSince: time.Now(),
			Check:     wire.Check{Details: map[string]any{"tunnel_name": "primary"}},
		})
		if strings.Contains(got, "Связано правил:") {
			t.Fatalf("line must be absent when fields missing:\n%s", got)
		}
	})
	t.Run("only static routes", func(t *testing.T) {
		got := FormatHard(HardArgs{
			Nickname: "vasya", CheckName: "tunnel_awg11",
			HardSince: time.Now(),
			Check:     wire.Check{Details: map[string]any{"routes_static": 4}},
		})
		if !strings.Contains(got, "Через этот VPN-туннель идут правила: 4 по адресам") {
			t.Fatalf("missing static-only line:\n%s", got)
		}
	})
}

func TestFormatHardGenericFallback(t *testing.T) {
	hardSince := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatHard(HardArgs{
		Nickname:    "vasya",
		CheckName:   "awg_handshake", // generic
		ConsecFails: 3,
		HardSince:   hardSince,
		Check:       wire.Check{Name: "awg_handshake", Status: "fail", Details: map[string]any{"error": "handshake age 312s > 180s"}},
	})
	for _, want := range []string{"🔴", "vasya", "Что не работает:", "handshake age 312s", "проверок подряд без ответа: 3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "📱") {
		t.Fatalf("static user must not have mobile badge:\n%s", got)
	}
}

func TestFormatHardMobileShowsBadge(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname: "client-h", CheckName: "awg_handshake",
		HardSince: time.Now(), IsMobile: true,
		Check: wire.Check{Status: "fail", Details: map[string]any{"error": "x"}},
	})
	if !strings.Contains(got, "📱") {
		t.Fatalf("mobile badge missing:\n%s", got)
	}
}

func TestFormatHardTunnelRichBody(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:    "vasya",
		CheckName:   "tunnel_awg11",
		ConsecFails: 3,
		HardSince:   time.Date(2026, 4, 29, 14, 30, 0, 0, time.UTC),
		Check: wire.Check{
			Name:   "tunnel_awg11",
			Status: "fail",
			Details: map[string]any{
				"tunnel_name":               "amnezia_for_awg2",
				"interface":                 "nwg0",
				"endpoint":                  "198.51.100.21:37634",
				"isp_interface":             "eth3",
				"handshake_age_sec":         277, // 4m37s
				"ping_check_status":         "dead",
				"ping_check_fail_count":     3,
				"ping_check_fail_threshold": 3,
				"ping_check_restart_count":  2,
				"backend":                   "nativewg",
				"awg_version":               "AWG2.0",
				"mtu":                       1280,
			},
		},
	})
	wants := []string{
		"🟡",
		"VPN-туннель «amnezia_for_awg2» не отвечает",
		"На что обратить внимание:",
		"Сервер VPN-туннеля: 198.51.100.21:37634", "выход провайдера: eth3",
		"Последний обмен ключами:", "4 мин 37 с",
		"Проверка связи: падает", "неудачных попыток 3 из 3",
		"автоперезапусков: 2",
		"Параметры:", "nativewg", "AWG AWG2.0", "MTU 1280",
		"Что может пострадать:",
		"Что я думаю:",
		"Что делать:",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestFormatHardDNSBody(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "dns",
		HardSince: time.Now(),
		Check: wire.Check{Name: "dns", Status: "fail", Details: map[string]any{
			"endpoints": 4, "failed_count": 0,
			"rkn_probed": 4, "rkn_suspect": 4,
			"error": "RKN block suspected",
		}},
	})
	wants := []string{
		"Роутер не находит сайты по имени",
		"Что не работает:",
		"трафик подменяется",
		"RKN-блокировка похоже на ВСЕХ",
		"Что я думаю:",
		"шифрованный поиск имён",
		"Что это ломает:",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "fail") {
		t.Errorf("ordinary-user alert should not expose raw fail token:\n%s", got)
	}
}

func TestFormatHardDNSPartial(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "dns",
		HardSince: time.Now(),
		Check: wire.Check{Name: "dns", Status: "fail", Details: map[string]any{
			"endpoints":    7,
			"failed_count": 2,
			"endpoints_detail": []any{
				map[string]any{"reachable": false, "type": "plain", "target": "100.64.0.1:53", "ndms_name": "Wireguard3", "err": "read: read udp 1.1.1.1:1->100.64.0.1:53: i/o timeout"},
				map[string]any{"reachable": false, "type": "plain", "target": "8.8.4.4:53", "ndms_name": "Wireguard3", "err": "i/o timeout"},
				map[string]any{"reachable": true, "type": "doh", "target": "https://dns.google/dns-query"},
			},
			"rkn_probed": 5, "rkn_suspect": 0,
		}},
		Neighbors: []NeighborSummary{
			{CheckName: "tunnel_awg11", TunnelName: "main", Interface: "nwg1", Status: "alive", HandshakeAge: 12},
			{CheckName: "tunnel_awg12", TunnelName: "backup", Interface: "nwg0", Status: "alive", HandshakeAge: 8},
		},
	})
	wants := []string{
		"🟡",
		"Часть сайтов может не открываться по имени",
		"На что обратить внимание:",
		"Что может пострадать:",
		"Не отвечают 2 из",
		"plain 100.64.0.1:53 через Wireguard3 — таймаут",
		"plain 8.8.4.4:53 через Wireguard3 — таймаут",
		"RKN-блокировок не видно",
		"Wireguard3",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	// raw Go socket pair must NOT leak through
	if strings.Contains(got, "->100.64.0.1:53") {
		t.Errorf("raw socket pair leaked:\n%s", got)
	}
}

func TestFormatHardDNSPartialUsesHumanTunnelContext(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "del",
		CheckName: "dns",
		HardSince: time.Now(),
		Check: wire.Check{Name: "dns", Status: "fail", Details: map[string]any{
			"endpoints":    4,
			"failed_count": 2,
			"endpoints_detail": []any{
				map[string]any{"reachable": false, "type": "plain", "target": "100.64.0.1:53", "ndms_name": "Wireguard3", "err": "network is unreachable"},
				map[string]any{"reachable": false, "type": "plain", "target": "8.8.4.4:53", "ndms_name": "Wireguard3", "err": "network is unreachable"},
			},
			"rkn_probed": 2, "rkn_suspect": 0,
		}},
		Neighbors: []NeighborSummary{
			{CheckName: "tunnel_awg13", TunnelName: "Germany backup", NDMSName: "Wireguard3", Interface: "nwg3", Status: "alive", HandshakeAge: 12},
			{CheckName: "tunnel_awg14", TunnelName: "US main", NDMSName: "Wireguard4", Interface: "nwg4", Status: "alive", HandshakeAge: 8},
		},
	})
	for _, want := range []string{
		"plain 100.64.0.1:53 через Germany backup (Wireguard3 / nwg3) — сеть недоступна",
		"Оба упавших DNS-сервера идут через Germany backup",
		"Остальные VPN-туннели выглядят живыми",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "начни с Wireguard3") {
		t.Fatalf("advice should use human tunnel name, got:\n%s", got)
	}
}

func TestFormatHardDNSAllFailedWithAliveNeighborsIsAdvisory(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "testkeen",
		CheckName: "dns",
		HardSince: time.Now(),
		Check: wire.Check{Name: "dns", Status: "fail", Details: map[string]any{
			"endpoints":    2,
			"failed_count": 2,
			"endpoints_detail": []any{
				map[string]any{"reachable": false, "type": "plain", "target": "100.64.0.1:53", "ndms_name": "Wireguard0", "err": "i/o timeout"},
				map[string]any{"reachable": false, "type": "plain", "target": "8.8.8.8:53", "ndms_name": "Wireguard0", "err": "i/o timeout"},
			},
		}},
		Neighbors: []NeighborSummary{
			{CheckName: "tunnel_awg11", TunnelName: "working backup", NDMSName: "Wireguard5", Interface: "nwg5", Status: "alive", HandshakeAge: 30},
			{CheckName: "tunnel_awg12", TunnelName: "working nl", NDMSName: "Wireguard3", Interface: "nwg3", Status: "alive", HandshakeAge: 90},
		},
	})
	for _, want := range []string{
		"🟡",
		"Роутер стал хуже находить сайты по имени",
		"На что обратить внимание:",
		"Остальные VPN-туннели выглядят живыми",
		"интернет на месте",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "🔴") || strings.Contains(got, "нет связи наружу") {
		t.Fatalf("alive neighbor DNS alert must not look like total WAN outage:\n%s", got)
	}
}

func TestFormatHardExternalReachSeverity(t *testing.T) {
	partial := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "external_reach",
		HardSince: time.Now(),
		Check: wire.Check{Name: "external_reach", Status: "fail", Details: map[string]any{
			"targets_total": 3,
			"targets_failed": []any{
				map[string]any{"name": "youtube", "err": "i/o timeout"},
			},
			"targets_ok": []any{"telegram", "github"},
		}},
	})
	for _, want := range []string{"🟡", "На что обратить внимание:", "Что может пострадать:"} {
		if !strings.Contains(partial, want) {
			t.Fatalf("partial external reach should be advisory, missing %q in:\n%s", want, partial)
		}
	}

	total := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "external_reach",
		HardSince: time.Now(),
		Check: wire.Check{Name: "external_reach", Status: "fail", Details: map[string]any{
			"targets_total": 2,
			"targets_failed": []any{
				map[string]any{"name": "youtube", "err": "i/o timeout"},
				map[string]any{"name": "telegram", "err": "no route to host"},
			},
			"via_interface": "nwg0",
		}},
	})
	if !strings.Contains(total, "🔴") || !strings.Contains(total, "Что не работает:") {
		t.Fatalf("full external reach outage should stay critical:\n%s", total)
	}
}

func TestFormatHardExternalReachDegradedShowsStatus(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "external_reach",
		HardSince: time.Now(),
		Check: wire.Check{Name: "external_reach", Status: "fail", Details: map[string]any{
			"targets_total": 3,
			"targets_failed": []any{
				map[string]any{"name": "youtube", "err": "i/o timeout"},
			},
			"targets_ok":       []any{"telegram"},
			"targets_degraded": []any{map[string]any{"name": "instagram", "status": 403}},
		}},
	})
	for _, want := range []string{"Доступны, но вернули отказ", "instagram (403)", "сервис отверг бота"} {
		if !strings.Contains(got, want) {
			t.Fatalf("degraded target should be surfaced with status, missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatHardTunnelPingCheckDisabledNudge(t *testing.T) {
	// Stale handshake on a tunnel whose pingCheck is disabled → advice should
	// nudge enabling pingCheck.
	withNudge := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "tunnel_awg12",
		HardSince: time.Now(),
		Check: wire.Check{Name: "tunnel_awg12", Status: "fail", Details: map[string]any{
			"tunnel_name":       "NL",
			"handshake_age_sec": 900,
			"ping_check_status": "disabled",
		}},
	})
	// Подсказка осталась, но словами владельца: «проверка связи», а не
	// pingCheck -- этого слова он нигде не видел.
	if !strings.Contains(withNudge, "проверку связи") || !strings.Contains(withNudge, "выключена") {
		t.Fatalf("на молчащей линии с выключенной проверкой связи нужна подсказка её включить:\n%s", withNudge)
	}

	// With pingCheck alive, no nudge (would be noise).
	noNudge := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "tunnel_awg12",
		HardSince: time.Now(),
		Check: wire.Check{Name: "tunnel_awg12", Status: "fail", Details: map[string]any{
			"tunnel_name":       "NL",
			"handshake_age_sec": 900,
			"ping_check_status": "alive",
		}},
	})
	if strings.Contains(noNudge, "включите проверку связи") {
		t.Fatalf("healthy pingCheck should not trigger the nudge:\n%s", noNudge)
	}
}

func TestFormatHardHydraRouteBody(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "hydraroute",
		HardSince: time.Now(),
		Check: wire.Check{Name: "hydraroute", Status: "fail", Details: map[string]any{
			"installed": true, "running": false,
			"error": "installed but not running",
		}},
	})
	wants := []string{
		"🟡",
		"HydraRoute (движок умной раздельной маршрутизации) остановлен",
		"На что обратить внимание:",
		"HydraRoute установлен, но сервис остановлен",
		"Что может пострадать:",
		"установлен, но не запущен",
		"перезагрузка роутера",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "installed=true") || strings.Contains(got, "running=false") {
		t.Errorf("technical booleans should not leak to TG alert:\n%s", got)
	}
}

func TestFormatHardAwgManagerControlPlane(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "awg_manager",
		HardSince: time.Now(),
		Check: wire.Check{Name: "awg_manager", Status: "fail", Details: map[string]any{
			"base_url": "http://127.0.0.1:2222",
			"error":    "connection refused",
		}},
	})
	for _, want := range []string{
		"🔴",
		"Панель управления роутера не отвечает",
		"кнопки в приложении",
		"перезагрузите роутер",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "VPN умер") || strings.Contains(got, "всё не работает") {
		t.Fatalf("awg-manager alert must not sound like total VPN outage:\n%s", got)
	}
}

func TestFormatHardWithNeighborsInfluencesDiagnosis(t *testing.T) {
	// Handshake — относительно свежий (60s, не критично), pingCheck не задан,
	// конфликта нет: per-tunnel диагностика не выдаёт строк, и в дело вступает
	// корреляция с соседями. Живые соседи → "проблема локальная".
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "tunnel_awg11",
		HardSince: time.Now(),
		Check: wire.Check{Name: "tunnel_awg11", Status: "fail", Details: map[string]any{
			"tunnel_name":       "main",
			"interface":         "nwg0",
			"handshake_age_sec": 60,
		}},
		Neighbors: []NeighborSummary{
			{CheckName: "tunnel_awg12", TunnelName: "backup", Interface: "nwg1", Status: "alive", HandshakeAge: 12},
		},
	})
	if !strings.Contains(got, "Соседние VPN-туннели живы") {
		t.Errorf("missing neighbors-alive hypothesis:\n%s", got)
	}
	if !strings.Contains(got, "🟡") {
		t.Errorf("single-tunnel degradation with alive neighbors should be advisory:\n%s", got)
	}

	// Мёртвые соседи → диагноз указывает на WAN/провайдера.
	got2 := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "tunnel_awg11",
		HardSince: time.Now(),
		Check: wire.Check{Name: "tunnel_awg11", Status: "fail", Details: map[string]any{
			"tunnel_name":       "main",
			"interface":         "nwg0",
			"handshake_age_sec": 60,
		}},
		Neighbors: []NeighborSummary{
			{CheckName: "tunnel_awg12", TunnelName: "backup", Interface: "nwg1", Status: "dead", HandshakeAge: 800},
		},
	})
	if !strings.Contains(got2, "WAN") && !strings.Contains(got2, "провайдер") {
		t.Errorf("missing WAN/provider hypothesis:\n%s", got2)
	}
	if !strings.Contains(got2, "🔴") {
		t.Errorf("multi-tunnel outage should stay red:\n%s", got2)
	}
}

func TestFormatRecovery(t *testing.T) {
	since := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatRecovery(RecoveryArgs{
		Nickname:    "vasya",
		CheckName:   "awg_handshake",
		HardSince:   since,
		RecoveredAt: since.Add(7 * time.Minute),
	})
	for _, want := range []string{"🟢", "vasya", "снова в норме", "Простой:", "7 мин"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFormatRecovery_TunnelEchoesLinkedRoutesAndName(t *testing.T) {
	since := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatRecovery(RecoveryArgs{
		Nickname:    "vasya",
		CheckName:   "tunnel_awg11",
		HardSince:   since,
		RecoveredAt: since.Add(12 * time.Minute),
		Check: wire.Check{
			Status: "ok",
			Details: map[string]any{
				"tunnel_name":   "primary",
				"interface":     "nwg1",
				"routes_dns":    48,
				"routes_static": 3,
			},
		},
	})
	if !strings.Contains(got, "primary") {
		t.Errorf("recovery should mention tunnel name: %s", got)
	}
	if !strings.Contains(got, "Снова работают правила: 48 по именам сайтов, 3 по адресам") {
		t.Errorf("recovery footer with linked routes missing: %s", got)
	}
}

func TestFormatRecovery_TunnelWithoutDetails_StillRendersBare(t *testing.T) {
	got := FormatRecovery(RecoveryArgs{
		Nickname:    "vasya",
		CheckName:   "tunnel_awg11",
		HardSince:   time.Now(),
		RecoveredAt: time.Now(),
	})
	if !strings.Contains(got, "снова работает") {
		t.Errorf("bare recovery line missing: %s", got)
	}
	if strings.Contains(got, "Вернулись правила") {
		t.Errorf("footer must be absent when no details: %s", got)
	}
}

func TestFormatRouterOffline(t *testing.T) {
	got := FormatRouterOffline("vasya", 11*time.Minute)
	for _, want := range []string{"vasya", "Роутер не на связи", "Что не работает:", "Что я думаю:", "Что делать:", "11 мин"} {
		if !strings.Contains(got, want) {
			t.Fatalf("got: %s", got)
		}
	}
}

func TestFormatRealert(t *testing.T) {
	hardSince := time.Date(2026, 4, 28, 9, 3, 0, 0, time.UTC)
	msg := FormatRealert(RealertArgs{
		Nickname:     "vasya",
		CheckName:    "awg_handshake",
		HardSince:    hardSince,
		RealertCount: 2,
	})
	for _, want := range []string{"🔁", "vasya", "Всё ещё:", "напомню снова через", "#2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in: %q", want, msg)
		}
	}
}

func TestFormatRealertKeepsAdvisoryToneAndAction(t *testing.T) {
	hardSince := time.Date(2026, 4, 28, 9, 3, 0, 0, time.UTC)
	msg := FormatRealert(RealertArgs{
		Nickname:     "vasya",
		CheckName:    "hydraroute",
		HardSince:    hardSince,
		RealertCount: 1,
		Check: wire.Check{Name: "hydraroute", Status: "fail", Details: map[string]any{
			"installed": true, "running": false,
		}},
	})
	for _, want := range []string{"🟡", "Всё ещё требует внимания:", "На что обратить внимание:", "Что делать:", "перезагрузка роутера"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing advisory realert part %q in:\n%s", want, msg)
		}
	}
}

func TestPluralServers(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, "1 сервер"},
		{2, "2 сервера"},
		{4, "4 сервера"},
		{5, "5 серверов"},
		{11, "11 серверов"},
		{14, "14 серверов"},
		{21, "21 сервер"},
		{22, "22 сервера"},
		{25, "25 серверов"},
		{100, "100 серверов"},
		{101, "101 сервер"},
	}
	for _, c := range cases {
		if got := pluralServers(c.n); got != c.want {
			t.Errorf("pluralServers(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestHumaniseNetErr_CoversCommonErrors(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"read tcp 1.2.3.4:80->5.6.7.8:443: connection reset by peer", "соединение сброшено"},
		{"write: broken pipe", "канал порван"},
		{"write: EPIPE", "канал порван"},
		{"lookup foo.bar: NXDOMAIN", "имя не резолвится"},
		{"context canceled", "отменено"},
	}
	for _, c := range cases {
		if got := humaniseNetErr(c.in); got != c.want {
			t.Errorf("humaniseNetErr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanAgeSec(t *testing.T) {
	cases := map[int]string{
		5:    "5с",
		90:   "1 мин 30 с",
		3600: "1ч 0м",
		7320: "2ч 2м",
	}
	for in, want := range cases {
		if got := humanAgeSec(in); got != want {
			t.Errorf("humanAgeSec(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHumaniseNetErr(t *testing.T) {
	cases := map[string]string{
		"read: read udp 1.1.1.1:1->2.2.2.2:53: i/o timeout": "таймаут",
		"connect: connection refused":                       "отказ соединения",
		"dial: no route to host":                            "нет маршрута",
		"":                                                  "ошибка",
	}
	for in, want := range cases {
		if got := humaniseNetErr(in); got != want {
			t.Errorf("humaniseNetErr(%q) = %q, want %q", in, got, want)
		}
	}
}

// Линия упала, но соседняя жива -- значит обход работает, и трафик идёт через
// неё. Гнать человека чинить то, что у него работает, тревога не имеет права:
// приложение в этот же момент честно пишет «работает на запасной линии»
// (miniapp/src/routerHeadline.js), и два голоса одной системы обязаны
// говорить одно и то же. Чинить всё равно надо -- запасная осталась одна, --
// но это предупреждение, а не «всё сломалось».
func TestFormatHardTunnelWithLiveSpareIsHonest(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname: "router-a", CheckName: "tunnel_awg11",
		HardSince: time.Now(),
		Check: wire.Check{
			Name: "tunnel_awg11", Status: "fail",
			Details: map[string]any{"tunnel_name": "Франкфурт", "interface": "nwg1"},
		},
		Neighbors: []NeighborSummary{
			{CheckName: "tunnel_awg12", TunnelName: "Амстердам", Status: "alive"},
		},
	})

	if !strings.Contains(got, "запасн") {
		t.Fatalf("при живом резерве тревога обязана сказать, что обход работает:\n%s", got)
	}
	if !strings.Contains(got, "Амстердам") {
		t.Fatalf("надо назвать линию, через которую сейчас идёт обход:\n%s", got)
	}
	if strings.Contains(got, "🔴") {
		t.Fatalf("работающий обход -- не красная тревога:\n%s", got)
	}
}

// А вот когда живых соседей нет, обход действительно потерян -- и тут тревога
// обязана быть тревогой.
func TestFormatHardTunnelWithoutSpareIsAlarming(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname: "router-a", CheckName: "tunnel_awg11",
		HardSince: time.Now(),
		Check: wire.Check{
			Name: "tunnel_awg11", Status: "fail",
			Details: map[string]any{"tunnel_name": "Франкфурт", "interface": "nwg1"},
		},
		Neighbors: []NeighborSummary{
			{CheckName: "tunnel_awg12", TunnelName: "Амстердам", Status: "dead"},
		},
	})
	if strings.Contains(got, "запасн") {
		t.Fatalf("живого резерва нет -- обещать обход нельзя:\n%s", got)
	}
}

// После переезда уведомлений в личку советы читает ВЛАДЕЛЕЦ роутера, а не
// оператор со старой панелью бота в теме группы. У него нет ни этих кнопок,
// ни SSH на роутер, ни opkg. Совет, который нельзя выполнить, хуже
// отсутствующего: человек решает, что сломано непоправимо.
func TestAdviceNeverSendsOwnerWhereHeCannotGo(t *testing.T) {
	// Запрещённые обороты: доступ, которого у владельца нет, и кнопки
	// старой панели, которых в личке не существует.
	forbidden := []string{"ssh ", "SSH", "opkg", "logread", "/opt/etc/init.d"}
	oldPanel := []string{"🛠 Обслуживание", "🎛 Туннели", "📊 Что происходит?", "🛡 PingCheck", "🌍 Через туннель?", "🇷🇺 Напрямую?"}

	cases := []struct {
		name  string
		check string
		d     map[string]any
	}{
		{"линия без рукопожатия", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт"}},
		{"линия давно молчит", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900}},
		{"линия с выключенной проверкой связи", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900, "ping_check_status": "disabled"}},
		{"конфликт адресов", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "address_conflict": true}},
		{"имена сайтов не находятся", "dns", map[string]any{"endpoints": 2, "failed_count": 2}},
		{"панель роутера молчит", "awg_manager", map[string]any{}},
		{"реестр линий недоступен", "tunnels", map[string]any{}},
		{"HydraRoute не установлен", "hydraroute", map[string]any{"installed": false}},
		{"HydraRoute остановлен", "hydraroute", map[string]any{"installed": true, "running": false}},
		{"сервисы не открываются через VPN-туннель", "external_reach", map[string]any{
			"targets_total": 2, "via_interface": "nwg0",
			"targets_failed": []any{
				map[string]any{"name": "youtube", "err": "i/o timeout"},
				map[string]any{"name": "telegram", "err": "i/o timeout"},
			},
		}},
		{"часть сервисов не открывается", "external_reach", map[string]any{
			"targets_total":  3,
			"targets_failed": []any{map[string]any{"name": "youtube", "err": "i/o timeout"}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatHard(HardArgs{
				Nickname: "router-a", CheckName: tc.check, HardSince: time.Now(),
				Check: wire.Check{Name: tc.check, Status: "fail", Details: tc.d},
			})
			for _, bad := range forbidden {
				if strings.Contains(got, bad) {
					t.Errorf("владельцу советуют недоступное (%q):\n%s", bad, got)
				}
			}
			for _, bad := range oldPanel {
				if strings.Contains(got, bad) {
					t.Errorf("совет ведёт в старую панель бота (%q):\n%s", bad, got)
				}
			}
		})
	}
}

// Уведомление «роутер не на связи» -- то же самое: его читает владелец.
func TestOfflineAdviceIsForOwner(t *testing.T) {
	got := FormatRouterOffline("router-a", 12*time.Minute)
	for _, bad := range []string{"SSH", "ssh ", "/opt/etc/init.d", "heartbeat"} {
		if strings.Contains(got, bad) {
			t.Errorf("владельцу советуют недоступное или непонятное (%q):\n%s", bad, got)
		}
	}
}

// Тревогу читает владелец роутера, а не инженер. Слова, которых он нигде не
// видел, не объясняют поломку -- они её прячут.
func TestAlertSpeaksHumanRussian(t *testing.T) {
	// «hrneo», «selective», «демон» -- из разбора HydraRoute; «0 из 0» --
	// счётчик попыток выключенной проверки связи, которая ничего не пробовала.
	jargon := []string{"fails", "резолвинг", "heartbeat", "handshake", "апстрим",
		"static-маршрут", "DoH", "/24", "WAN", "UDP", "hrneo", "selective", "демон", "0 из 0",
		"ping", "рестарт"}

	cases := []struct {
		name  string
		check string
		d     map[string]any
	}{
		{"VPN-туннель", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900, "routes_dns": 48, "routes_static": 3}},
		{"проверка связи выключена", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900, "ping_check_status": "disabled"}},
		{"проверка связи включена", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900,
			"ping_check_status": "fail", "ping_check_fail_count": 2, "ping_check_fail_threshold": 3,
			"ping_check_restart_count": 2, "ping_check_last_latency_ms": 120}},
		{"имена сайтов", "dns", map[string]any{"endpoints": 2, "failed_count": 2, "rkn_probed": 2, "rkn_suspect": 2}},
		{"панель роутера", "awg_manager", map[string]any{}},
		{"реестр VPN-туннелей", "tunnels", map[string]any{}},
		{"HydraRoute не установлен", "hydraroute", map[string]any{"installed": false}},
		{"HydraRoute остановлен", "hydraroute", map[string]any{"installed": true, "running": false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatHard(HardArgs{
				Nickname: "router-a", CheckName: tc.check, HardSince: time.Now(), ConsecFails: 3,
				Check: wire.Check{Name: tc.check, Status: "fail", Details: tc.d},
			})
			for _, w := range jargon {
				if strings.Contains(got, w) {
					t.Errorf("жаргон %q в тексте для владельца:\n%s", w, got)
				}
			}
		})
	}
}

// Число правил согласуется со словом. «3 правил по адресам» уходило в личку
// из ветки без резерва, где правила перечислены: слово было вбито в шаблон.
func TestAlertRulesCountAgrees(t *testing.T) {
	d := map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900, "routes_dns": 1, "routes_static": 3}
	got := FormatHard(HardArgs{
		Nickname: "router-a", CheckName: "tunnel_awg11", HardSince: time.Now(), ConsecFails: 3,
		Check: wire.Check{Name: "tunnel_awg11", Status: "fail", Details: d},
	})
	for _, wrong := range []string{"1 правил ", "3 правил ", "1 правила", "3 правило"} {
		if strings.Contains(got, wrong) {
			t.Errorf("число не согласовано со словом: %q\n%s", wrong, got)
		}
	}
	if !strings.Contains(got, "3 по адресам") && !strings.Contains(got, "3 правила по адресам") {
		t.Errorf("тревога не называет правила по адресам:\n%s", got)
	}
}

// Напоминание обещает, когда напомнит снова, -- и обещает по-русски. «Через
// 6h» -- запись для логов, а не для владельца.
func TestRealertSaysWhenInRussian(t *testing.T) {
	got := FormatRealert(RealertArgs{
		Nickname: "router-a", CheckName: "tunnel_awg11", HardSince: time.Now().Add(-3 * time.Hour),
		RealertCount: 1, RealertEvery: 6 * time.Hour,
		Check: wire.Check{Name: "tunnel_awg11", Status: "fail", Details: map[string]any{"tunnel_name": "Франкфурт"}},
	})
	if !strings.Contains(got, "напомню снова через 6 ч") {
		t.Errorf("срок напоминания не по-русски:\n%s", got)
	}
}

// «Линия» отменена владельцем проекта: в приложении это VPN-туннель, полной
// формой в каждом упоминании. Голое «туннель» тоже не годится -- такого слова
// в приложении нет. Сторож проходит тревогу, напоминание и восстановление по
// всем веткам, которые говорят о VPN-туннелях, включая соседей и резерв.
var bareTunnelRe = regexp.MustCompile(`(^|[^-])[Тт]уннел`)

func assertSaysVPNTunnel(t *testing.T, what, got string) {
	t.Helper()
	if strings.Contains(strings.ToLower(got), "лини") {
		t.Errorf("%s: «линия» в тексте для владельца:\n%s", what, got)
	}
	if bareTunnelRe.MatchString(got) {
		t.Errorf("%s: голое «туннель» без «VPN-»:\n%s", what, got)
	}
}

func TestAlertSaysVPNTunnelNotLine(t *testing.T) {
	alive := []NeighborSummary{{CheckName: "tunnel_awg12", TunnelName: "Амстердам", NDMSName: "Wireguard3", Interface: "nwg3", Status: "alive"}}
	dead := []NeighborSummary{{CheckName: "tunnel_awg12", TunnelName: "Амстердам", NDMSName: "Wireguard3", Interface: "nwg3", Status: "dead"}}
	dnsViaOne := map[string]any{"endpoints": 2, "failed_count": 2, "endpoints_detail": []any{
		map[string]any{"reachable": false, "type": "plain", "target": "198.51.100.53:53", "ndms_name": "Wireguard3", "err": "i/o timeout"},
		map[string]any{"reachable": false, "type": "plain", "target": "203.0.113.53:53", "ndms_name": "Wireguard3", "err": "i/o timeout"},
	}}
	cases := []struct {
		name  string
		check string
		d     map[string]any
		ns    []NeighborSummary
	}{
		{"без обмена ключами, резерв жив", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт"}, alive},
		{"без обмена ключами, резерва нет", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт"}, dead},
		{"давно молчит, резерв жив", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900,
			"routes_dns": 48, "routes_static": 3, "endpoint": "198.51.100.21:37634", "ping_check_status": "disabled"}, alive},
		{"давно молчит, резерва нет", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900, "routes_dns": 48}, dead},
		{"давно молчит, правил нет", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 900}, nil},
		{"обмен ключами устарел", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 300}, nil},
		{"свежий обмен ключами, соседи живы", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 60}, alive},
		{"свежий обмен ключами, соседи молчат", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "handshake_age_sec": 60}, dead},
		{"конфликт адресов", "tunnel_awg11", map[string]any{"tunnel_name": "Франкфурт", "address_conflict": true, "handshake_age_sec": 60}, nil},
		{"имена сайтов через один VPN-туннель, соседи живы", "dns", dnsViaOne, alive},
		{"имена сайтов через один VPN-туннель, соседи молчат", "dns", dnsViaOne, dead},
		{"имена сайтов через один VPN-туннель, соседей нет", "dns", dnsViaOne, nil},
		{"все серверы имён молчат, соседи живы", "dns", map[string]any{"endpoints": 2, "failed_count": 2}, alive},
		{"подмена ответов", "dns", map[string]any{"endpoints": 2, "failed_count": 0, "rkn_probed": 2, "rkn_suspect": 2}, nil},
		{"панель роутера", "awg_manager", map[string]any{}, nil},
		{"список VPN-туннелей", "tunnels", map[string]any{"error": "timeout", "tunnel_count": 3}, nil},
		{"HydraRoute сбоит", "hydraroute", map[string]any{"installed": true, "running": true}, nil},
		{"сервисы не открываются", "external_reach", map[string]any{"targets_total": 2, "via_interface": "nwg0",
			"targets_failed": []any{map[string]any{"name": "youtube", "err": "i/o timeout"}, map[string]any{"name": "telegram", "err": "i/o timeout"}}}, nil},
		{"сервисы не открываются, соседи молчат", "external_reach", map[string]any{"targets_total": 3}, dead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chk := wire.Check{Name: tc.check, Status: "fail", Details: tc.d}
			hard := FormatHard(HardArgs{Nickname: "router-a", CheckName: tc.check, HardSince: time.Now(), Check: chk, Neighbors: tc.ns})
			assertSaysVPNTunnel(t, "тревога", hard)
			assertSaysVPNTunnel(t, "напоминание", FormatRealert(RealertArgs{
				Nickname: "router-a", CheckName: tc.check, HardSince: time.Now(), RealertCount: 1, Check: chk, Neighbors: tc.ns,
			}))
			assertSaysVPNTunnel(t, "восстановление", FormatRecovery(RecoveryArgs{
				Nickname: "router-a", CheckName: tc.check, HardSince: time.Now().Add(-time.Hour), RecoveredAt: time.Now(),
				Check: wire.Check{Status: "ok", Details: tc.d},
			}))
			if strings.HasPrefix(tc.check, "tunnel_") && !strings.Contains(hard, "VPN-туннель «Франкфурт»") {
				t.Errorf("VPN-туннель называется по имени в ёлочках:\n%s", hard)
			}
			if strings.HasPrefix(tc.check, "tunnel_") && tc.ns != nil && neighborsAlive(tc.ns) &&
				!strings.Contains(hard, "запасной VPN-туннель «Амстердам»") {
				t.Errorf("резерв -- запасной VPN-туннель, мужской род:\n%s", hard)
			}
		})
	}
	bare := FormatRecovery(RecoveryArgs{Nickname: "router-a", CheckName: "tunnel_awg11", HardSince: time.Now(), RecoveredAt: time.Now()})
	assertSaysVPNTunnel(t, "восстановление без подробностей", bare)
	if !strings.Contains(bare, "VPN-туннель снова работает") {
		t.Errorf("голое восстановление тоже про VPN-туннель:\n%s", bare)
	}
}

// HydraRoute владельцу ничего не говорит: это имя он видел разве что в панели
// роутера. При первом упоминании в тексте -- пояснение, дальше просто имя:
// объяснять одно и то же в каждой строке -- шум.
func TestHydraRouteExplainedAtFirstMention(t *testing.T) {
	const explained = "HydraRoute (движок умной раздельной маршрутизации)"
	for _, st := range []struct {
		name string
		d    map[string]any
	}{
		{"не установлен", map[string]any{"installed": false}},
		{"остановлен", map[string]any{"installed": true, "running": false}},
		{"сбой", map[string]any{"installed": true, "running": true}},
	} {
		t.Run(st.name, func(t *testing.T) {
			chk := wire.Check{Name: "hydraroute", Status: "fail", Details: st.d}
			texts := map[string]string{
				"тревога": FormatHard(HardArgs{Nickname: "router-a", CheckName: "hydraroute", HardSince: time.Now(), Check: chk}),
				"напоминание": FormatRealert(RealertArgs{
					Nickname: "router-a", CheckName: "hydraroute", HardSince: time.Now(), RealertCount: 1, Check: chk,
				}),
				"восстановление": FormatRecovery(RecoveryArgs{
					Nickname: "router-a", CheckName: "hydraroute", HardSince: time.Now().Add(-time.Hour), RecoveredAt: time.Now(),
				}),
			}
			for what, got := range texts {
				first := strings.Index(got, "HydraRoute")
				if first < 0 || !strings.HasPrefix(got[first:], explained) {
					t.Errorf("%s: первое упоминание HydraRoute без пояснения:\n%s", what, got)
				}
				if n := strings.Count(got, "движок умной раздельной маршрутизации"); n != 1 {
					t.Errorf("%s: пояснение HydraRoute должно стоять ровно один раз, стоит %d:\n%s", what, n, got)
				}
			}
		})
	}
}

// Инженерные тревоги владельцу переписаны через последствие: он ничего не
// сделает с «реестром туннелей», но должен понять, что кнопки могут не
// сработать.
func TestControlPlaneAlertSpeaksAboutConsequence(t *testing.T) {
	// Проверка реестра линий называется «tunnels» -- категория awgmgr_api
	// нигде не является именем проверки.
	for _, check := range []string{"awg_manager", "tunnels"} {
		got := FormatHard(HardArgs{
			Nickname: "router-a", CheckName: check, HardSince: time.Now(),
			Check: wire.Check{Name: check, Status: "fail"},
		})
		if !strings.Contains(got, "интернет") && !strings.Contains(got, "Интернет") {
			t.Errorf("%s: владельцу надо сказать, мешает ли это интернету:\n%s", check, got)
		}
	}
}

// Под тревогой остались только «Открыть в приложении» и «Тише на час»:
// текст тревоги не имеет права ссылаться на командные кнопки, которых
// больше нет (restart_tunnel/diag_now/pingcheck_now/force_recheck/ack/mute).
func TestHardAndOfflineTextsDoNotReferenceRemovedButtons(t *testing.T) {
	forbidden := []string{
		"«Повторить проверку»", "«Диагностика»", "«Запустить диагностику»",
		"«Тест связи»", "«Запросить отчёт»", "«Перезапустить VPN-туннели»",
		"«Понял»", "«Тихо до утра»",
	}
	texts := map[string]string{
		"FormatHard(tunnel)": FormatHard(HardArgs{
			Nickname: "router-a", CheckName: "tunnel_awg11", HardSince: time.Now(),
			Check: wire.Check{Name: "tunnel_awg11", Status: "fail", Details: map[string]any{"tunnel_name": "vpn-nl"}},
		}),
		"FormatRouterOffline": FormatRouterOffline("router-a", 11*time.Minute),
	}
	for label, got := range texts {
		for _, bad := range forbidden {
			if strings.Contains(got, bad) {
				t.Errorf("%s: ссылается на исчезнувшую кнопку %s:\n%s", label, bad, got)
			}
		}
	}
}
