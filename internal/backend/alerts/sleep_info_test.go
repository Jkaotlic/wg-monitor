package alerts

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSleepInfo(t *testing.T) {
	last := time.Date(2026, 5, 15, 14, 32, 7, 0, time.Local)
	card := RenderSleepInfo("carvan", last)
	if card.Badge != "🌙" {
		t.Errorf("badge: want 🌙, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "carvan") {
		t.Errorf("summary missing nick: %q", card.Summary)
	}
	if !strings.Contains(card.Summary, "14:32") {
		t.Errorf("summary must include HH:MM of lastSeen, got %q", card.Summary)
	}
	if card.Details != "" || card.Hint != "" {
		t.Errorf("sleep info must be one-liner; details=%q hint=%q", card.Details, card.Hint)
	}
}
