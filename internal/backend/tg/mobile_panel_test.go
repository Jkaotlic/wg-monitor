package tg

import (
	"strings"
	"testing"
	"time"
)

func TestMobileFleetPanelText(t *testing.T) {
	last := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	text := MobileFleetPanelText([]MobileFleetRow{
		{Nickname: "carvan", Kind: "mobile", LastSeenAt: &last, LastDeployedVersion: "v0.13.0", Ring: "canary", PendingVersion: "v0.14.0-rc1"},
		{Nickname: "home", Kind: "static"},
	}, last.Add(2*time.Minute))

	for _, want := range []string{"Мобильные роутеры", "carvan", "в сети", "canary", "ждёт обновление v0.14.0-rc1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("MobileFleetPanelText missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "home") {
		t.Fatalf("static routers must not appear in mobile panel:\n%s", text)
	}
}

func TestMobileFleetPanelText_FutureLastSeenIsClockSkew(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Minute)
	text := MobileFleetPanelText([]MobileFleetRow{
		{Nickname: "carvan", Kind: "mobile", LastSeenAt: &future},
	}, now)

	if strings.Contains(text, "Ð² Ñ\u0081ÐµÑ‚Ð¸") {
		t.Fatalf("future last_seen must not render as online:\n%s", text)
	}
	if !strings.Contains(text, "clock") {
		t.Fatalf("future last_seen should be explicit clock skew:\n%s", text)
	}
}
