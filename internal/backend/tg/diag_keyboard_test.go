package tg

import (
	"strings"
	"testing"
)

func TestDiagResultKeyboard_OK_PrimaryRaw(t *testing.T) {
	kb := DiagResultKeyboard("ok", 42, "deadbeef")
	flat := flattenCD(kb)
	if !strings.Contains(flat, "diag_raw:42:_panel_:deadbeef") {
		t.Errorf("missing diag_raw callback: %s", flat)
	}
	if !strings.Contains(flat, "diag_now:42:_menu") {
		t.Errorf("missing rerun callback: %s", flat)
	}
}

func TestDiagResultKeyboard_Err_HasHelpAndRetry(t *testing.T) {
	kb := DiagResultKeyboard("err", 42, "")
	flat := flattenCD(kb)
	if !strings.Contains(flat, "diag_now:42:_menu") {
		t.Errorf("missing retry callback: %s", flat)
	}
	if !strings.Contains(flat, "panel:0:help:diag") {
		t.Errorf("missing help callback: %s", flat)
	}
	if strings.Contains(flat, "diag_raw") {
		t.Errorf("err path should not include diag_raw: %s", flat)
	}
}

func flattenCD(kb InlineKeyboardMarkup) string {
	var sb strings.Builder
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			sb.WriteString(b.CallbackData)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func TestDiagResultKeyboard_FailingTestsButtons(t *testing.T) {
	failing := []DiagFailingTest{
		{ID: "mtu", Label: "MTU интерфейса"},
		{ID: "dns_leak", Label: "DNS leak проверка"},
	}
	kb := DiagResultKeyboardWithTests("ok", 42, "abcd1234", failing)
	if len(kb.InlineKeyboard) < 3 {
		t.Fatalf("expected ≥3 rows, got %d", len(kb.InlineKeyboard))
	}
	// First row should be the failing-test buttons
	row0 := kb.InlineKeyboard[0]
	if len(row0) != 2 {
		t.Errorf("expected 2 failing-test buttons, got %d", len(row0))
	}
	if row0[0].CallbackData != "diag_test:42:abcd1234:mtu" {
		t.Errorf("cb mismatch: %q", row0[0].CallbackData)
	}
}

func TestDiagResultKeyboard_NoFailing_NoExtraRow(t *testing.T) {
	kb := DiagResultKeyboardWithTests("ok", 42, "abcd1234", nil)
	// Should match the original 2-row layout (raw + rerun, then close)
	if len(kb.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows when no failing, got %d", len(kb.InlineKeyboard))
	}
}

func TestDiagResultKeyboard_TruncatesLongLabels(t *testing.T) {
	failing := []DiagFailingTest{{ID: "very_long_id", Label: "Очень длинный заголовок проверки превышает лимит"}}
	kb := DiagResultKeyboardWithTests("ok", 42, "abcd1234", failing)
	btn := kb.InlineKeyboard[0][0]
	// Cap text width — must contain ellipsis if truncated
	if len([]rune(btn.Text)) > 20 {
		t.Errorf("button text too long: %q (%d runes)", btn.Text, len([]rune(btn.Text)))
	}
}
