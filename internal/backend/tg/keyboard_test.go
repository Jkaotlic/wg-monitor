package tg

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHardAlertKeyboardShape(t *testing.T) {
	kb := HardAlertKeyboard(42, "awg_handshake")
	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(kb.InlineKeyboard))
	}
	if len(kb.InlineKeyboard[0]) != 4 {
		t.Errorf("row 0: expected 4 buttons (silence x3 + ack), got %d", len(kb.InlineKeyboard[0]))
	}
	if len(kb.InlineKeyboard[1]) != 2 {
		t.Errorf("row 1: expected 2 buttons (history + mute), got %d", len(kb.InlineKeyboard[1]))
	}
}

func TestHardAlertKeyboardCallbackData(t *testing.T) {
	kb := HardAlertKeyboard(42, "awg_handshake")
	expected := map[string]bool{
		"silence:42:awg_handshake:1h":  true,
		"silence:42:awg_handshake:4h":  true,
		"silence:42:awg_handshake:24h": true,
		"ack:42:awg_handshake":         true,
		"history:42:awg_handshake":     true,
		"mute:42:awg_handshake":        true,
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if !expected[btn.CallbackData] {
				t.Errorf("unexpected callback_data: %q", btn.CallbackData)
			}
			delete(expected, btn.CallbackData)
		}
	}
	for k := range expected {
		t.Errorf("missing button: %q", k)
	}
}

func TestHardAlertKeyboardJSONShape(t *testing.T) {
	kb := HardAlertKeyboard(42, "awg_handshake")
	raw, err := json.Marshal(kb)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"inline_keyboard"`) {
		t.Errorf("json must have `inline_keyboard` key, got %s", s)
	}
	if !strings.Contains(s, `"callback_data"`) {
		t.Errorf("json must have `callback_data` field, got %s", s)
	}
}

func TestHardAlertKeyboardCallbackData64ByteLimit(t *testing.T) {
	kb := HardAlertKeyboard(999999999, "awg_handshake_with_long_name")
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if len(btn.CallbackData) > 64 {
				t.Errorf("callback_data exceeds TG 64-byte limit: %d bytes (%q)", len(btn.CallbackData), btn.CallbackData)
			}
		}
	}
}

func TestHardAlertKeyboardWithTunnelActions(t *testing.T) {
	kb := HardAlertKeyboard(42, "tunnel_amnezia_for_awg2", WithTunnelActions())
	if len(kb.InlineKeyboard) != 3 {
		t.Fatalf("expected 3 rows with tunnel actions, got %d", len(kb.InlineKeyboard))
	}
	want := map[string]bool{
		"restart_tunnel:42:tunnel_amnezia_for_awg2": true,
		"diag_now:42:tunnel_amnezia_for_awg2":       true,
		"pingcheck_now:42:tunnel_amnezia_for_awg2":  true,
	}
	for _, btn := range kb.InlineKeyboard[2] {
		if !want[btn.CallbackData] {
			t.Errorf("unexpected tunnel-action callback: %q", btn.CallbackData)
		}
		delete(want, btn.CallbackData)
	}
	for k := range want {
		t.Errorf("missing tunnel-action button: %q", k)
	}
}

func TestHardAlertKeyboardWithMobileActions(t *testing.T) {
	kb := HardAlertKeyboard(42, "agent_heartbeat", WithMobileActions())
	if len(kb.InlineKeyboard) != 3 {
		t.Fatalf("expected 3 rows with mobile actions, got %d", len(kb.InlineKeyboard))
	}
	if len(kb.InlineKeyboard[2]) != 1 {
		t.Fatalf("expected 1 mobile-action button, got %d", len(kb.InlineKeyboard[2]))
	}
	got := kb.InlineKeyboard[2][0].CallbackData
	if got != "force_recheck:42:agent_heartbeat" {
		t.Errorf("got %q want force_recheck:42:agent_heartbeat", got)
	}
}

func TestHardAlertKeyboardWithHydraRouteActions(t *testing.T) {
	kb := HardAlertKeyboard(42, "hydraroute", WithHydraRouteActions())
	if len(kb.InlineKeyboard) != 3 {
		t.Fatalf("expected 3 rows with hydraroute actions, got %d", len(kb.InlineKeyboard))
	}
	want := map[string]bool{
		"maint_restart:42:hrneo_start": true,
		"maint_restart:42:hrneo":       true,
	}
	for _, btn := range kb.InlineKeyboard[2] {
		if !want[btn.CallbackData] {
			t.Errorf("unexpected hydraroute-action callback: %q", btn.CallbackData)
		}
		delete(want, btn.CallbackData)
	}
	for k := range want {
		t.Errorf("missing hydraroute-action button: %q", k)
	}
}

func TestHardAlertKeyboardCombinedTunnelAndMobile(t *testing.T) {
	kb := HardAlertKeyboard(42, "tunnel_amnezia_for_awg2", WithTunnelActions(), WithMobileActions())
	if len(kb.InlineKeyboard) != 4 {
		t.Errorf("expected 4 rows (base 2 + tunnel + mobile), got %d", len(kb.InlineKeyboard))
	}
}

func TestHardAlertKeyboardHumanisedLabels(t *testing.T) {
	kb := HardAlertKeyboard(42, "tunnel_amnezia", WithTunnelActions(), WithMobileActions())
	want := map[string]string{
		"silence:42:tunnel_amnezia:1h":     "⏸ Тише на 1ч",
		"silence:42:tunnel_amnezia:4h":     "⏸ Тише на 4ч",
		"silence:42:tunnel_amnezia:24h":    "⏸ Тише на 24ч",
		"ack:42:tunnel_amnezia":            "✅ Понял",
		"history:42:tunnel_amnezia":        "📋 История за 24ч",
		"mute:42:tunnel_amnezia":           "🔇 Тихо до утра",
		"restart_tunnel:42:tunnel_amnezia": "🔁 Перезапуск туннеля",
		"diag_now:42:tunnel_amnezia":       "📊 Диагностика",
		"pingcheck_now:42:tunnel_amnezia":  "▶ Тест связи",
		"force_recheck:42:tunnel_amnezia":  "🔄 Дай отчёт сейчас",
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if expected, ok := want[b.CallbackData]; ok {
				if b.Text != expected {
					t.Errorf("cd=%q got text %q want %q", b.CallbackData, b.Text, expected)
				}
				delete(want, b.CallbackData)
			}
		}
	}
	for cd := range want {
		t.Errorf("missing callback_data: %s", cd)
	}
}

func TestHardAlertKeyboardCommandActionsRespect64ByteLimit(t *testing.T) {
	kb := HardAlertKeyboard(999999999, "tunnel_amnezia_for_awg2_long", WithTunnelActions(), WithMobileActions())
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if len(btn.CallbackData) > 64 {
				t.Errorf("callback_data exceeds 64-byte TG limit: %d bytes (%q)", len(btn.CallbackData), btn.CallbackData)
			}
		}
	}
}

func TestPerRouterKeyboardContainsHideMyName(t *testing.T) {
	kb := ReplyKeyboardForTopic("per_router").(*ReplyKeyboardMarkup)
	found := false
	for _, row := range kb.Keyboard {
		for _, btn := range row {
			if btn.Text == "HideMy.name" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("HideMy.name button not found")
	}
	if got := CompatBtnTextByCode("hidemyname"); got != "HideMy.name" {
		t.Fatalf("compat reverse = %q", got)
	}
}
