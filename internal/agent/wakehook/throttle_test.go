package wakehook

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)

func TestThrottleMinGap(t *testing.T) {
	th := &Throttle{}
	if !th.Allow(t0) || th.Allow(t0.Add(10*time.Second)) || !th.Allow(t0.Add(31*time.Second)) {
		t.Fatal("хотим: да, нет (ближе 30 с), да")
	}
}

func TestThrottleCapsPerHour(t *testing.T) {
	th := &Throttle{}
	allowed := 0
	var last time.Time
	for i := 0; i < 90; i++ { // раз в 40 с -- час флаппинга
		last = t0.Add(time.Duration(i*40) * time.Second)
		if th.Allow(last) {
			allowed++
		}
	}
	if allowed != 20 {
		t.Fatalf("за час пропущено %d, хотим потолок 20", allowed)
	}
	fired, suppressed := th.Stats(last)
	if fired != 20 || suppressed != 70 {
		t.Fatalf("stats: fired=%d suppressed=%d", fired, suppressed)
	}
}
