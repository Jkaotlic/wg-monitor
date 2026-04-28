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
