package backend

import (
	"context"
	"io"
	"log/slog"
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

// Доставка, ушедшая на месте при полном пуле, тоже пишет в базу: слив её
// ждёт, хотя места в пуле она не занимает.
func TestWaitRelayDrain_CountsInlineDelivery(t *testing.T) {
	waitRelayPoolEmpty(t)
	d := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	fill := make(chan struct{})
	for i := 0; i < relayConcurrencyLimit; i++ {
		spawnRelay(d, "fill", func(ctx context.Context) { <-fill })
	}
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
