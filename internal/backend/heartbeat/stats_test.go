package heartbeat

import (
	"testing"
	"time"
)

func TestJudge_NeverScannedIsNotAlive(t *testing.T) {
	v := Judge(Stats{}, time.Now())
	if v.Alive {
		t.Fatal("сторож без единого обхода не может считаться живым")
	}
	if v.Reason == "" {
		t.Fatal("вердикт без объяснения бесполезен")
	}
}

// Отставание меряется шагами обхода, а не секундами: тридцать секунд -- норма
// при шаге в минуту и тревога при шаге в пять секунд.
func TestJudge_LatenessIsMeasuredInScanSteps(t *testing.T) {
	now := time.Date(2026, 8, 21, 23, 0, 0, 0, time.UTC)
	fresh := Stats{ScansTotal: 10, LastScanAt: now.Add(-40 * time.Second), ScanEvery: 30 * time.Second}
	if !Judge(fresh, now).Alive {
		t.Fatal("один пропущенный шаг -- ещё не смерть")
	}
	stale := Stats{ScansTotal: 10, LastScanAt: now.Add(-4 * time.Minute), ScanEvery: 30 * time.Second}
	v := Judge(stale, now)
	if v.Alive {
		t.Fatal("четыре минуты без обхода при шаге в тридцать секунд -- это не жизнь")
	}
	if v.Reason == "" {
		t.Fatal("вердикт без объяснения бесполезен")
	}
}

func TestJudge_ZeroScanEveryFallsBackToDefault(t *testing.T) {
	now := time.Date(2026, 8, 21, 23, 0, 0, 0, time.UTC)
	s := Stats{ScansTotal: 1, LastScanAt: now.Add(-time.Minute)}
	if !Judge(s, now).Alive {
		t.Fatal("минута при шаге по умолчанию (30с) укладывается в три шага")
	}
}

func TestSnapshotReadsCounters(t *testing.T) {
	w := NewWatcher(nil, nil, Config{ScanEvery: 45 * time.Second})
	metricScans.Set(7)
	metricLastScanUnix.Set(time.Date(2026, 8, 21, 22, 0, 0, 0, time.UTC).Unix())
	metricStaleUsers.Set(2)
	s := w.Snapshot()
	if s.ScansTotal != 7 || s.StaleUsers != 2 {
		t.Fatalf("счётчики не прочитались: %+v", s)
	}
	if s.ScanEvery != 45*time.Second {
		t.Fatalf("шаг обхода: %v", s.ScanEvery)
	}
	if s.LastScanAt.IsZero() {
		t.Fatal("время последнего обхода потерялось")
	}
}
