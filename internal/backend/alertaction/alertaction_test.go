package alertaction

import (
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestApplySilenceSetsSilencedUntil(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	st := db.IncidentState{UserID: 1, CheckName: "x", CurrentStatus: "hard"}
	out, line := ApplySilence(st, time.Hour, now)
	if out.SilencedUntil == nil || !out.SilencedUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("SilencedUntil = %v, want %v", out.SilencedUntil, now.Add(time.Hour))
	}
	if line == "" {
		t.Error("expected a non-empty status line")
	}
	if out.Acked {
		t.Error("silence must not set Acked")
	}
}

func TestApplyAckSetsAcked(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	st := db.IncidentState{UserID: 1, CheckName: "x", CurrentStatus: "hard"}
	out, line := ApplyAck(st, now)
	if !out.Acked {
		t.Fatal("ApplyAck must set Acked=true")
	}
	if out.SilencedUntil != nil {
		t.Error("ack must not set SilencedUntil")
	}
	if line != "✅ Отмечено: вижу проблему, напомню после восстановления" {
		t.Errorf("ack status line = %q", line)
	}
}

func TestApplyMuteSetsNextCutoff(t *testing.T) {
	// 2026-07-14 08:00 UTC == 11:00 MSK; cutoffHour 9 (MSK) already passed today,
	// so the next cutoff is tomorrow 09:00 MSK.
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	st := db.IncidentState{UserID: 1, CheckName: "x", CurrentStatus: "hard"}
	out, line := ApplyMute(st, 9, now)
	if out.SilencedUntil == nil {
		t.Fatal("mute must set SilencedUntil")
	}
	loc := MoscowLoc()
	got := out.SilencedUntil.In(loc)
	if got.Hour() != 9 || got.Minute() != 0 || got.Day() != 15 {
		t.Fatalf("mute cutoff = %v (MSK), want 2026-07-15 09:00 MSK", got)
	}
	if line == "" {
		t.Error("expected a non-empty status line")
	}
}

func TestApplyMuteCutoffLaterToday(t *testing.T) {
	// 2026-07-14 03:00 UTC == 06:00 MSK; cutoffHour 9 MSK is still ahead today.
	now := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)
	st := db.IncidentState{UserID: 1, CheckName: "x"}
	out, _ := ApplyMute(st, 9, now)
	got := out.SilencedUntil.In(MoscowLoc())
	if got.Day() != 14 || got.Hour() != 9 {
		t.Fatalf("mute cutoff = %v, want 2026-07-14 09:00 MSK", got)
	}
}

func TestTransitionsEmitsOnlyOnChange(t *testing.T) {
	base := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	events := []db.EventRow{
		{CheckName: "x", Status: "ok", TS: base},
		{CheckName: "x", Status: "ok", TS: base.Add(1 * time.Minute)},
		{CheckName: "x", Status: "fail", TS: base.Add(2 * time.Minute)},
		{CheckName: "x", Status: "fail", TS: base.Add(3 * time.Minute)},
		{CheckName: "x", Status: "ok", TS: base.Add(4 * time.Minute)},
	}
	tr, truncated := Transitions(events)
	if truncated {
		t.Error("did not expect truncation for 3 transitions")
	}
	if len(tr) != 3 {
		t.Fatalf("got %d transitions, want 3", len(tr))
	}
	if tr[0].Status != "ok" || tr[1].Status != "fail" || tr[2].Status != "ok" {
		t.Errorf("transition statuses = %v", tr)
	}
}
