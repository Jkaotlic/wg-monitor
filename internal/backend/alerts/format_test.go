package alerts

import (
	"strings"
	"testing"
	"time"
)

func TestFormatHard(t *testing.T) {
	hardSince := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatHard(HardArgs{
		Nickname:    "vasya",
		CheckName:   "awg_handshake",
		ConsecFails: 3,
		HardSince:   hardSince,
		Detail:      "handshake age 312s > 180s",
	})
	for _, want := range []string{"🔴", "vasya", "awg_handshake", "DOWN", "handshake age 312s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
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
	for _, want := range []string{"✅", "vasya", "RECOVERED", "7m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFormatRouterOffline(t *testing.T) {
	got := FormatRouterOffline("vasya", 11*time.Minute)
	if !strings.Contains(got, "OFFLINE") || !strings.Contains(got, "vasya") || !strings.Contains(got, "11m") {
		t.Fatalf("got: %s", got)
	}
}
