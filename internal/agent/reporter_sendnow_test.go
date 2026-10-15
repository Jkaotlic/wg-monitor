package agent

import (
	"context"
	"testing"
	"time"
)

// AG-05: внеплановый отчёт после действия с туннелем -- не «вернулся после
// отвала». С Resumed=true бэкенд у мобильного роутера выбрасывает упавшие
// проверки туннелей из тревог, и свежая «туннель всё ещё лежит» терялась.
func TestReporterSendNowIsNotResumed(t *testing.T) {
	s := &recSender{}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
	r.lastReportAt = time.Now()
	r.SendNow(context.Background())
	reps := s.snapshot()
	if len(reps) != 1 {
		t.Fatalf("отчётов %d, want 1", len(reps))
	}
	if reps[0].Resumed {
		t.Fatal("внеплановый отчёт ушёл с Resumed=true")
	}
	if reps[0].Trigger != "" {
		t.Fatalf("trigger = %q, want плановый", reps[0].Trigger)
	}

	// force_recheck по-прежнему Resumed=true.
	r.ForceResumed(context.Background())
	if reps = s.snapshot(); len(reps) != 2 || !reps[1].Resumed {
		t.Fatalf("ForceResumed без Resumed: %+v", reps)
	}
}
