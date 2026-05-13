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
