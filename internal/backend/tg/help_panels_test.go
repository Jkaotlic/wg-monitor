package tg

import (
	"strings"
	"testing"
)

func TestHelpForScreen_KnownScreens(t *testing.T) {
	for _, screen := range []string{"maint", "routes", "tunnels", "access", "diag", "status", "doctor"} {
		body := HelpForScreen(screen)
		if body == "" {
			t.Errorf("screen %q: empty help body", screen)
		}
		if len(body) > 3500 {
			t.Errorf("screen %q: help body too long (%d > 3500)", screen, len(body))
		}
	}
}

func TestHelpForScreen_UnknownReturnsGeneric(t *testing.T) {
	body := HelpForScreen("totally_made_up")
	if !strings.Contains(body, "Помощь") {
		t.Errorf("unknown screen should still return some help text, got: %q", body)
	}
}

func TestHelpForScreen_PingCheck(t *testing.T) {
	got := HelpForScreen("pingcheck")
	for _, want := range []string{"PingCheck", "watchdog", "Restart×"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in help body", want)
		}
	}
}

func TestHelpForScreen_Doctor(t *testing.T) {
	got := HelpForScreen("doctor")
	for _, want := range []string{"Read-only", "awg-manager", "PingCheck"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in help body", want)
		}
	}
}

func TestHelpRowFor_Maint(t *testing.T) {
	row := HelpRowFor("maint")
	if len(row) != 1 {
		t.Fatalf("want 1 button, got %d", len(row))
	}
	if row[0].CallbackData != "panel:0:help:maint" {
		t.Errorf("bad callback data: %q", row[0].CallbackData)
	}
	if row[0].Text != "ℹ Помощь" {
		t.Errorf("bad text: %q", row[0].Text)
	}
}
