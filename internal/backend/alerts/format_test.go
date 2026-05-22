package alerts

import (
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
		if !strings.Contains(got, "Связано правил: 48 DNS (HR-Neo), 3 Static") {
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
		if !strings.Contains(got, "Связано правил: 10 DNS (HR-Neo: 7)") {
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
		if !strings.Contains(got, "Связано правил: 4 Static") {
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
	for _, want := range []string{"🔴", "vasya", "Что не работает:", "handshake age 312s", "3 fails"} {
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
		"Туннель amnezia_for_awg2 (nwg0) не на связи",
		"Что не работает:",
		"Сервер туннеля: 198.51.100.21:37634", "провайдерский выход: eth3",
		"Последний обмен ключами:", "4 мин 37 с",
		"Проверка связи: падает", "неудачных попыток 3 из 3",
		"авто-рестартов: 2",
		"Параметры:", "nativewg", "AWG AWG2.0", "MTU 1280",
		"Что это ломает:",
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
		"DNS-резолвинг не работает",
		"Что не работает:",
		"трафик подменяется",
		"RKN-блокировка похоже на ВСЕХ",
		"Что я думаю:",
		"DoH",
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
		"DNS-резолвинг частично не работает",
		"Не отвечают 2 из",
		"plain 100.64.0.1:53 (Wireguard3) — таймаут",
		"plain 8.8.4.4:53 (Wireguard3) — таймаут",
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
		"HydraRoute остановлен",
		"HydraRoute установлен, но сервис остановлен",
		"Что это ломает:",
		"демон не запущен",
		"Перезапустить hrneo",
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
	if !strings.Contains(got, "Соседние тоннели живы") {
		t.Errorf("missing neighbors-alive hypothesis:\n%s", got)
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
}

func TestFormatRecovery(t *testing.T) {
	since := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatRecovery(RecoveryArgs{
		Nickname:    "vasya",
		CheckName:   "awg_handshake",
		HardSince:   since,
		RecoveredAt: since.Add(7 * time.Minute),
	})
	for _, want := range []string{"🟢", "vasya", "снова в норме", "Простой:", "7m"} {
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
	if !strings.Contains(got, "Вернулись правила: 48 DNS, 3 Static") {
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
	if !strings.Contains(got, "снова на связи") {
		t.Errorf("bare recovery line missing: %s", got)
	}
	if strings.Contains(got, "Вернулись правила") {
		t.Errorf("footer must be absent when no details: %s", got)
	}
}

func TestFormatRouterOffline(t *testing.T) {
	got := FormatRouterOffline("vasya", 11*time.Minute)
	for _, want := range []string{"vasya", "Роутер не на связи", "Что не работает:", "Что я думаю:", "Что делать:", "11m"} {
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
