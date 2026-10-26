package backend

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// TestWaitRelayDrain_BlocksWhileDeliveryInFlight: остановка бэкенда ждёт
// фоновые доставки тревог до d.Close() -- иначе они пишут в закрытую базу.
func TestWaitRelayDrain_BlocksWhileDeliveryInFlight(t *testing.T) {
	waitRelayPoolEmpty(t)
	d := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	release := make(chan struct{})
	spawnAlertDelivery(d, "dispatch", 1, "drain", func(ctx context.Context) { <-release })
	done := make(chan struct{})
	go func() {
		WaitRelayDrain()
		close(done)
	}()
	select {
	case <-done:
		close(release)
		t.Fatal("WaitRelayDrain вернулся, пока доставка в полёте")
	case <-time.After(50 * time.Millisecond):
	}
	close(release) // доставка закончилась
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitRelayDrain не вернулся после конца доставки")
	}
}

// Доставка, ушедшая на месте (пул и потолок фоновых без пула заняты), тоже
// пишет в базу: слив её ждёт, хотя места в пуле она не занимает.
func TestWaitRelayDrain_CountsInlineDelivery(t *testing.T) {
	waitRelayPoolEmpty(t)
	d := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	fill := fillAlertSlotless(t, d)
	inlineStarted := make(chan struct{})
	inlineRelease := make(chan struct{})
	go spawnAlertDelivery(d, "dispatch", 1, "drain-inline", func(ctx context.Context) {
		close(inlineStarted)
		<-inlineRelease
	})
	select {
	case <-inlineStarted:
	case <-time.After(2 * time.Second):
		close(fill)
		t.Fatal("доставка на месте не началась")
	}
	close(fill)
	waitRelayPoolEmpty(t) // пул пуст, доставка на месте ещё идёт

	done := make(chan struct{})
	go func() { WaitRelayDrain(); close(done) }()
	select {
	case <-done:
		close(inlineRelease)
		t.Fatal("WaitRelayDrain вернулся, пока доставка на месте ещё пишет")
	case <-time.After(50 * time.Millisecond):
	}
	close(inlineRelease)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitRelayDrain не вернулся после конца доставки на месте")
	}
}

func TestWaitRelayDrain_EmptyPoolReturnsAtOnce(t *testing.T) {
	waitRelayPoolEmpty(t)
	done := make(chan struct{})
	go func() { WaitRelayDrain(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("пустой пул: WaitRelayDrain должен вернуться сразу")
	}
}

// waitDraining ждёт, пока WaitRelayDrain поднимет флаг слива.
func waitDraining(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !relayDraining() {
		if time.Now().After(deadline) {
			t.Fatal("слив не начался")
		}
		time.Sleep(time.Millisecond)
	}
}

// Обработчик, переживший srv.Shutdown, запускает доставку, когда слив уже
// ждёт: relayWG.Add параллельно с Wait -- запрещённое использование
// WaitGroup (паника «Add called concurrently with Wait» / гонка). Во время
// слива тревога доставляется без учёта, прочие уведомления бросаются, и слив
// поздних не ждёт. Прогонять с -race.
func TestWaitRelayDrain_LateSpawnDuringDrain(t *testing.T) {
	waitRelayPoolEmpty(t)
	d := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	first := make(chan struct{})
	spawnAlertDelivery(d, "dispatch", 1, "drain-first", func(ctx context.Context) { <-first })
	drained := make(chan struct{})
	go func() { WaitRelayDrain(); close(drained) }()
	waitDraining(t)

	lateStarted := make(chan struct{})
	lateRelease := make(chan struct{})
	defer close(lateRelease)
	spawnAlertDelivery(d, "dispatch", 1, "drain-late", func(ctx context.Context) {
		close(lateStarted)
		<-lateRelease
	})
	select {
	case <-lateStarted:
	case <-time.After(2 * time.Second):
		close(first)
		t.Fatal("поздняя тревога во время слива не доставлена")
	}
	var noticeRan atomic.Bool
	spawnRelay(d, "late-notice", func(ctx context.Context) { noticeRan.Store(true) })

	close(first)
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("слив ждёт позднюю доставку, начатую после его старта")
	}
	if noticeRan.Load() {
		t.Fatal("уведомление во время слива запущено, ждали отказ")
	}
}
