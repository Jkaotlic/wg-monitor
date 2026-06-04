package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func TestSmartReplyStateValues(t *testing.T) {
	if StateOK == StateDegraded || StateDegraded == StateHard || StateHard == StateOffline {
		t.Errorf("states must be distinct")
	}
}

func TestSmartReplyArgsBuilderSmoke(t *testing.T) {
	a := SmartReplyArgs{
		Nickname:      "vasya",
		Tunnels:       []TunnelView{{Name: "amnezia", Interface: "nwg0", HandshakeAge: 47, PingStatus: "ok", Latency: 12}},
		LastReportAge: 0,
	}
	if a.Nickname != "vasya" || len(a.Tunnels) != 1 || a.Tunnels[0].HandshakeAge != 47 {
		t.Errorf("smoke: args build: %+v", a)
	}
}

func mkArgs(t *testing.T, build func(*SmartReplyArgs)) SmartReplyArgs {
	t.Helper()
	a := SmartReplyArgs{Nickname: "x"}
	build(&a)
	return a
}

func TestClassifyState_OfflineWhenReportStale(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) { a.LastReportAge = 6 * time.Minute })
	if got := ClassifyState(a); got != StateOffline {
		t.Errorf("got %v want offline", got)
	}
}

func TestClassifyState_HardWhenIncident(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 30 * time.Second
		a.ActiveIncidents = []IncidentView{{CheckName: "tunnel_awg11", FailCount: 5}}
	})
	if got := ClassifyState(a); got != StateHard {
		t.Errorf("got %v want hard", got)
	}
}

func TestClassifyState_DegradedHandshakeBoundary(t *testing.T) {
	cases := []struct {
		age   int
		state SmartReplyState
	}{
		{0, StateOK},
		{60, StateOK},  // was Degraded; threshold raised to align with "норма до 180" text
		{179, StateOK}, // was Degraded; same reason
		// 180+ matches the "FSM HARD threshold" — when handshake hits this
		// without an active incident yet (the race between agent tick and FSM
		// transition), surface as Degraded so the operator sees the early
		// warning without waiting for the next tick.
		{180, StateDegraded},
		{200, StateDegraded},
	}
	for _, c := range cases {
		t.Run(time.Duration(c.age).String(), func(t *testing.T) {
			a := mkArgs(t, func(a *SmartReplyArgs) {
				a.LastReportAge = 10 * time.Second
				a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: c.age, PingStatus: "ok"}}
			})
			if got := ClassifyState(a); got != c.state {
				t.Errorf("age=%d got %v want %v", c.age, got, c.state)
			}
		})
	}
}

func TestClassifyState_DegradedPingFails(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 30, PingStatus: "ok", FailCount: 2, FailThresh: 5}}
	})
	if got := ClassifyState(a); got != StateDegraded {
		t.Errorf("got %v want degraded", got)
	}
}

func TestClassifyState_OKBaseline(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 5 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 12, PingStatus: "ok"}}
	})
	if got := ClassifyState(a); got != StateOK {
		t.Errorf("got %v want ok", got)
	}
}

func TestClassifyState_DegradedWhenEnabledTunnelStartingWithoutHandshake(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 5 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia_nl", Enabled: true, HasEnabled: true, Status: "starting", PingStatus: "disabled"}}
	})
	if got := ClassifyState(a); got != StateDegraded {
		t.Errorf("got %v want degraded", got)
	}
}

func TestFormatSmartReply_DisabledTunnelDoesNotPretendHandshakeIsFresh(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "del", UserID: 7, LastReportAge: 23 * time.Second,
		Tunnels: []TunnelView{
			{Name: "nl", Enabled: false, HasEnabled: true, Status: "disabled"},
			{Name: "amnezia_nl", Enabled: true, HasEnabled: true, Status: "running", HasHandshake: true, HandshakeAge: 3, PingStatus: "disabled"},
		},
	}
	text, _ := FormatSmartReply(a)
	if !strings.Contains(text, "nl") || !strings.Contains(text, "выключ") {
		t.Fatalf("disabled tunnel should be rendered as disabled, got:\n%s", text)
	}
	if strings.Contains(text, "Туннель nl: последний обмен") {
		t.Fatalf("disabled tunnel must not claim fresh handshake:\n%s", text)
	}
}

func TestClassifyState_MobileLongerStaleWindow(t *testing.T) {
	// Mobile users have a 60-min OFFLINE grace window in heartbeat config,
	// but for smart-reply context we still want to surface "offline" as soon
	// as a report >5 min is stale — that's the user-facing definition.
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 6 * time.Minute
		a.IsMobile = true
	})
	if got := ClassifyState(a); got != StateOffline {
		t.Errorf("mobile: got %v want offline", got)
	}
}

func TestClassifyState_MultiTunnelOnlyOneDegraded(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{
			{Name: "amnezia", HandshakeAge: 12, PingStatus: "ok"},
			{Name: "secondary", HandshakeAge: 200, PingStatus: "ok"},
		}
	})
	if got := ClassifyState(a); got != StateDegraded {
		t.Errorf("got %v want degraded", got)
	}
}

func TestClassifyState_HardWinsOverDegradedHandshake(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 120, PingStatus: "ok"}}
		a.ActiveIncidents = []IncidentView{{CheckName: "dns"}}
	})
	if got := ClassifyState(a); got != StateHard {
		t.Errorf("got %v want hard", got)
	}
}

func TestClassifyState_IgnoresLegacyPingCheckDisabledHard(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{{
			Name: "de", CheckName: "tunnel_awg11", Interface: "nwg1",
			HandshakeAge: 51, PingStatus: "disabled", FailCount: 0, FailThresh: 3,
		}}
		a.ActiveIncidents = []IncidentView{{CheckName: "tunnel_awg11", HardSince: time.Now().Add(-7 * time.Minute), FailCount: 17}}
	})
	if got := ClassifyState(a); got != StateOK {
		t.Errorf("got %v want ok", got)
	}
}

func TestFormatSmartReply_OK(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 23 * time.Second,
		Tunnels: []TunnelView{{Name: "amnezia", HandshakeAge: 47, PingStatus: "ok", Latency: 12}},
	}
	text, kb := FormatSmartReply(a)
	for _, want := range []string{"✅", "vasya", "всё работает", "amnezia", "47с", "12 мс", "23с"} {
		if !strings.Contains(text, want) {
			t.Errorf("OK template missing %q in:\n%s", want, text)
		}
	}
	// OK template no longer carries an inline keyboard (details/last_report
	// removed 2026-05-06 — text-only).
	if len(kb.InlineKeyboard) != 0 {
		t.Errorf("OK keyboard expected empty, got: %+v", kb)
	}
}

func TestFormatSmartReply_Degraded(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 10 * time.Second,
		Tunnels: []TunnelView{{Name: "amnezia", CheckName: "tunnel_awg11", NDMSName: "Wireguard3", HandshakeAge: 142, PingStatus: "ok", FailCount: 3, FailThresh: 5}},
	}
	text, kb := FormatSmartReply(a)
	for _, want := range []string{"⚠️", "подозрения", "amnezia", "142", "3", "5"} {
		if !strings.Contains(text, want) {
			t.Errorf("Degraded missing %q in:\n%s", want, text)
		}
	}
	// inline kb: single row [Перезапуск][Тест связи] — details button removed.
	if len(kb.InlineKeyboard) < 1 {
		t.Fatalf("Degraded keyboard rows: %d, want ≥1", len(kb.InlineKeyboard))
	}
	want := map[string]bool{
		"tunnel_restart:7:tunnel_awg11:Wireguard3": true,
		"pingcheck_now:7:tunnel_awg11":             true,
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			delete(want, b.CallbackData)
		}
	}
	for k := range want {
		t.Errorf("Degraded missing button: %s", k)
	}
}

func TestFormatSmartReply_DoesNotFallbackTunnelRestartToAwgManagerRestart(t *testing.T) {
	cases := []struct {
		name string
		args SmartReplyArgs
	}{
		{
			name: "degraded",
			args: SmartReplyArgs{
				Nickname:      "vasya",
				UserID:        7,
				LastReportAge: 10 * time.Second,
				Tunnels: []TunnelView{{
					Name: "amnezia", CheckName: "tunnel_awg11", HandshakeAge: 200, PingStatus: "ok",
				}},
			},
		},
		{
			name: "hard",
			args: SmartReplyArgs{
				Nickname:      "vasya",
				UserID:        7,
				LastReportAge: 10 * time.Second,
				Tunnels:       []TunnelView{{Name: "amnezia", CheckName: "tunnel_awg11", HandshakeAge: 250, PingStatus: "dead"}},
				ActiveIncidents: []IncidentView{{
					CheckName: "tunnel_awg11",
					HardSince: time.Now().Add(-4 * time.Minute),
					FailCount: 5,
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, kb := FormatSmartReply(tc.args)
			if hasSmartReplyCallbackPrefix(kb, "restart_tunnel:") {
				t.Fatalf("missing NDMSName must not fallback to awg-manager restart, kb=%+v", kb)
			}
			if hasSmartReplyButtonText(kb, "Перезапустить туннель") {
				t.Fatalf("missing NDMSName must not render per-tunnel restart button, kb=%+v", kb)
			}
			if !hasSmartReplyCallbackPrefix(kb, "tunnels_refresh:") {
				t.Fatalf("missing NDMSName should offer live tunnels panel instead, kb=%+v", kb)
			}
		})
	}
}

func TestFormatSmartReply_TunnelProblemSeparatesFreshBackendFromTunnelFault(t *testing.T) {
	cases := []struct {
		name string
		args SmartReplyArgs
	}{
		{
			name: "degraded",
			args: SmartReplyArgs{
				Nickname:      "client-b",
				UserID:        7,
				LastReportAge: 20 * time.Second,
				Tunnels: []TunnelView{{
					Name: "awg11", CheckName: "tunnel_awg11", NDMSName: "Wireguard1",
					Enabled: true, HasEnabled: true, Status: "starting",
				}},
			},
		},
		{
			name: "hard",
			args: SmartReplyArgs{
				Nickname:      "client-b",
				UserID:        7,
				LastReportAge: 20 * time.Second,
				Tunnels: []TunnelView{{
					Name: "awg11", CheckName: "tunnel_awg11", NDMSName: "Wireguard1",
					HandshakeAge: 250, PingStatus: "dead",
				}},
				ActiveIncidents: []IncidentView{{CheckName: "tunnel_awg11", HardSince: time.Now().Add(-4 * time.Minute), FailCount: 5}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, _ := FormatSmartReply(tc.args)
			for _, want := range []string{"Агент жив", "backend получает отчёты", "проблема ниже"} {
				if !strings.Contains(text, want) {
					t.Fatalf("smart reply should separate backend health from tunnel fault, missing %q in:\n%s", want, text)
				}
			}
		})
	}
}

func TestFormatSmartReply_Hard(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 10 * time.Second,
		Tunnels:         []TunnelView{{Name: "amnezia", CheckName: "tunnel_awg11", NDMSName: "Wireguard3", HandshakeAge: 250, PingStatus: "dead"}},
		ActiveIncidents: []IncidentView{{CheckName: "tunnel_awg11", HardSince: time.Now().Add(-4 * time.Minute), FailCount: 5}},
	}
	text, kb := FormatSmartReply(a)
	for _, want := range []string{"🔴", "vasya", "проблема"} {
		if !strings.Contains(text, want) {
			t.Errorf("Hard missing %q in:\n%s", want, text)
		}
	}
	// inline kb must contain restart + diag + silence (details/last_report
	// buttons were removed in 2026-05-06 — no working handler).
	want := map[string]bool{
		"tunnel_restart:7:tunnel_awg11:Wireguard3": true,
		"diag_now:7:tunnel_awg11":                  true,
		"silence:7:tunnel_awg11:1h":                true,
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			delete(want, b.CallbackData)
		}
	}
	for k := range want {
		t.Errorf("Hard missing button: %s", k)
	}
}

func TestFormatSmartReply_HardDNSShowsHumanIncidentDetails(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "del", UserID: 7, LastReportAge: 20 * time.Second,
		Tunnels: []TunnelView{
			{Name: "Germany backup", CheckName: "tunnel_awg13", NDMSName: "Wireguard3", Interface: "nwg3", HandshakeAge: 12, PingStatus: "ok"},
		},
		ActiveIncidents: []IncidentView{{
			CheckName: "dns",
			HardSince: time.Now().Add(-20 * time.Hour),
			FailCount: 20,
			Details: map[string]any{
				"endpoints":    4,
				"failed_count": 2,
				"endpoints_detail": []any{
					map[string]any{"reachable": false, "type": "plain", "target": "100.64.0.1:53", "ndms_name": "Wireguard3", "err": "network is unreachable"},
					map[string]any{"reachable": false, "type": "plain", "target": "8.8.4.4:53", "ndms_name": "Wireguard3", "err": "network is unreachable"},
				},
				"rkn_probed": 2, "rkn_suspect": 0,
			},
		}},
	}

	text, kb := FormatSmartReply(a)
	for _, want := range []string{
		"DNS-резолвинг частично не работает",
		"2 из 4 DNS-серверов не отвечают",
		"Germany backup (Wireguard3 / nwg3)",
		"RKN-блокировок не видно",
		"Открой 🎛 Туннели",
		"затем 🛣 Маршруты",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	wantButton := "silence:7:dns:1h"
	found := false
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == wantButton {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("DNS smart-reply should include silence button %s, kb=%+v", wantButton, kb)
	}
}

func TestFormatSmartReply_HardHydraRouteIncludesSilence(t *testing.T) {
	a := SmartReplyArgs{
		Nickname:      "del",
		UserID:        7,
		LastReportAge: 20 * time.Second,
		ActiveIncidents: []IncidentView{{
			CheckName: "hydraroute",
			HardSince: time.Now().Add(-10 * time.Minute),
			FailCount: 5,
			Details: map[string]any{
				"installed": true,
				"running":   false,
			},
		}},
	}

	text, kb := FormatSmartReply(a)
	if !strings.Contains(text, "HydraRoute") {
		t.Fatalf("smart reply should explain HydraRoute incident, got:\n%s", text)
	}
	if !hasSmartReplyCallback(kb, "silence:7:hydraroute:1h") {
		t.Fatalf("HydraRoute smart reply should include silence button, kb=%+v", kb)
	}
}

func hasSmartReplyCallback(kb tg.InlineKeyboardMarkup, want string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == want {
				return true
			}
		}
	}
	return false
}

func hasSmartReplyCallbackPrefix(kb tg.InlineKeyboardMarkup, prefix string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, prefix) {
				return true
			}
		}
	}
	return false
}

func hasSmartReplyButtonText(kb tg.InlineKeyboardMarkup, needle string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.Text, needle) {
				return true
			}
		}
	}
	return false
}

func TestFormatSmartReply_HardUsesTunnelDisplayName(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 10 * time.Second,
		Tunnels:         []TunnelView{{Name: "amnezia", CheckName: "tunnel_awg11", HandshakeAge: 250, PingStatus: "dead"}},
		ActiveIncidents: []IncidentView{{CheckName: "tunnel_awg11", HardSince: time.Now().Add(-4 * time.Minute), FailCount: 5}},
	}
	text, _ := FormatSmartReply(a)
	if !strings.Contains(text, "amnezia") || strings.Contains(text, "tunnel_awg11") {
		t.Fatalf("Hard should render tunnel display name, got:\n%s", text)
	}
}

func TestFormatSmartReply_DropsOnlyLegacyPingCheckDisabledHard(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "client-a", UserID: 7, LastReportAge: 10 * time.Second,
		Tunnels: []TunnelView{{
			Name: "de", CheckName: "tunnel_awg11", Interface: "nwg1",
			HandshakeAge: 51, PingStatus: "disabled", FailCount: 0, FailThresh: 3,
		}},
		ActiveIncidents: []IncidentView{{CheckName: "tunnel_awg11", HardSince: time.Now().Add(-7 * time.Minute), FailCount: 17}},
	}
	text, kb := FormatSmartReply(a)
	if strings.Contains(text, "tunnel_awg11") {
		t.Fatalf("legacy false hard should not be shown:\n%s", text)
	}
	for _, want := range []string{"client-a", "de", "выключена"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if len(kb.InlineKeyboard) != 0 {
		t.Fatalf("false hard should not render incident buttons: %+v", kb)
	}
}

func TestFormatSmartReply_Offline(t *testing.T) {
	a := SmartReplyArgs{Nickname: "vasya", UserID: 7, LastReportAge: 14 * time.Minute}
	text, kb := FormatSmartReply(a)
	for _, want := range []string{"📵", "vasya", "не на связи", "14"} {
		if !strings.Contains(text, want) {
			t.Errorf("Offline missing %q in:\n%s", want, text)
		}
	}
	// Inline keyboard removed in 2026-05-06 — Offline is text-only now.
	if len(kb.InlineKeyboard) != 0 {
		t.Errorf("Offline kb expected empty, got: %+v", kb)
	}
}

func TestFormatSmartReply_WithUpdates(t *testing.T) {
	args := SmartReplyArgs{
		Nickname: "testkeen",
		Tunnels:  nil,
		Updates: []UpdateAvailable{
			{Name: "KeeneticOS", Installed: "5.00.C.11.0-0", Available: "5.00.C.12.0-0"},
			{Name: "awg-manager", Installed: "2.8.2", Available: "2.9.0", Hint: "NativeWG update may need router reboot"},
		},
	}
	text, _ := FormatSmartReply(args)
	for _, want := range []string{
		"🟡 Доступны обновления:",
		"KeeneticOS 5.00.C.11.0-0 → 5.00.C.12.0-0",
		"awg-manager 2.8.2 → 2.9.0",
		"NativeWG update may need router reboot",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestFormatSmartReply_EmptyUpdatesHidesSection(t *testing.T) {
	args := SmartReplyArgs{
		Nickname: "testkeen",
		Tunnels:  nil,
		Updates:  nil,
	}
	text, _ := FormatSmartReply(args)
	if strings.Contains(text, "Доступны обновления") {
		t.Errorf("expected NO updates section, got:\n%s", text)
	}
}

func TestFormatSmartReply_MultiTunnelHardSplit(t *testing.T) {
	a := SmartReplyArgs{
		Nickname: "vasya", UserID: 7, LastReportAge: 10 * time.Second,
		Tunnels: []TunnelView{
			{Name: "amnezia", CheckName: "tunnel_awg11", NDMSName: "Wireguard1", HandshakeAge: 250, PingStatus: "dead"},
			{Name: "secondary", CheckName: "tunnel_awg12", NDMSName: "Wireguard2", HandshakeAge: 200, PingStatus: "dead"},
		},
		ActiveIncidents: []IncidentView{{CheckName: "tunnel_awg11", HardSince: time.Now().Add(-2 * time.Minute), FailCount: 5}},
	}
	_, kb := FormatSmartReply(a)
	// must have at least one row per tunnel for restart
	want := map[string]bool{
		"tunnel_restart:7:tunnel_awg11:Wireguard1": true,
		"tunnel_restart:7:tunnel_awg12:Wireguard2": true,
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			delete(want, b.CallbackData)
		}
	}
	for k := range want {
		t.Errorf("multi-tunnel missing %s", k)
	}
}
