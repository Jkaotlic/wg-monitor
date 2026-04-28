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

func TestFormatRealert(t *testing.T) {
	hardSince := time.Date(2026, 4, 28, 9, 3, 0, 0, time.UTC)
	msg := FormatRealert(RealertArgs{
		Nickname:     "vasya",
		CheckName:    "awg_handshake",
		HardSince:    hardSince,
		RealertCount: 2,
	})
	if !strings.Contains(msg, "STILL DOWN") {
		t.Errorf("missing STILL DOWN: %q", msg)
	}
	if !strings.Contains(msg, "vasya") {
		t.Errorf("missing nickname: %q", msg)
	}
	if !strings.Contains(msg, "awg_handshake") {
		t.Errorf("missing check name: %q", msg)
	}
	if !strings.Contains(msg, "Re-alert #2") {
		t.Errorf("missing re-alert counter: %q", msg)
	}
	if !strings.Contains(msg, "🔁") {
		t.Errorf("missing 🔁 emoji: %q", msg)
	}
}
