package tg

import (
	"strings"
	"testing"
)

func TestHelpForScreen_KnownScreens(t *testing.T) {
	for _, screen := range []string{
		"operator", "alerts", "fleet", "premium", "mobile",
		"maint", "routes", "tunnels", "access", "diag", "status", "doctor", "pingcheck",
	} {
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
	for _, want := range []string{"PingCheck", "сторожевой", "restart×"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in help body", want)
		}
	}
}

func TestHelpForScreen_Doctor(t *testing.T) {
	got := HelpForScreen("doctor")
	for _, want := range []string{"ничего не меняет", "awg-manager", "PingCheck"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in help body", want)
		}
	}
}

func TestHelpForScreen_OperatorOverview(t *testing.T) {
	got := HelpForScreen("operator")
	for _, want := range []string{"Operator", "queue", "Premium", "self_update", "help:premium"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in operator help body:\n%s", want, got)
		}
	}
}

func TestHelpForScreen_Premium(t *testing.T) {
	got := HelpForScreen("premium")
	for _, want := range []string{"Amnezia Premium", "HideMy.name", ".conf", "AmneziaWG 2.0", "secret"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in premium help body:\n%s", want, got)
		}
	}
}

func TestHelpForScreen_RoutesMatchesWANSystemRebind(t *testing.T) {
	got := HelpForScreen("routes")
	for _, want := range []string{"NDMS", "HydraRoute-Neo", "WAN/system", "отдельной кнопкой", "preview"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in routes help body:\n%s", want, got)
		}
	}
	if strings.Contains(got, "не трогаются") {
		t.Errorf("routes help must not say WAN/system is untouchable anymore:\n%s", got)
	}
}

func TestHelpForScreen_TunnelsDistinguishesRestartButtons(t *testing.T) {
	got := HelpForScreen("tunnels")
	for _, want := range []string{"🔁 <имя>", "конкретный тоннель", "выкл→вкл", "🔁 awg-mgr", "менеджер awg-manager"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tunnels help should explain restart distinction, missing %q in:\n%s", want, got)
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
