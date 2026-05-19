package tg

import (
	"strings"
	"testing"
	"time"
)

func TestMobileFleetPanelText(t *testing.T) {
	last := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	text := MobileFleetPanelText([]MobileFleetRow{
		{Nickname: "client-h", Kind: "mobile", LastSeenAt: &last, LastDeployedVersion: "v0.13.0", Ring: "canary", PendingVersion: "v0.14.0-rc1"},
		{Nickname: "home", Kind: "static"},
	}, last.Add(2*time.Minute))

	for _, want := range []string{"Mobile fleet", "client-h", "awake", "canary", "pending v0.14.0-rc1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("MobileFleetPanelText missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "home") {
		t.Fatalf("static routers must not appear in mobile panel:\n%s", text)
	}
}
