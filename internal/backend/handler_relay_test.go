package backend

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// waitRelayPoolEmpty blocks until the shared relay semaphore has drained, so
// in-flight relays from other tests don't skew the saturation assertions.
func waitRelayPoolEmpty(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(relaySem) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("relay pool not empty: %d slots held", len(relaySem))
		}
		time.Sleep(time.Millisecond)
	}
}

// waitRelayDrained ждёт конца всех фоновых доставок, включая ушедшие на
// месте при полном пуле (WaitRelayDrain на relayWG).
func waitRelayDrained(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() { WaitRelayDrain(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("фоновые доставки не закончились за 2 с")
	}
}

// TestSpawnRelay_DropsWhenPoolSaturated pins BE-02: relay spawns are bounded by
// relayConcurrencyLimit. spawnRelay acquires the semaphore synchronously, so
// once every slot is held the next relay is dropped (its fn never runs) instead
// of spawning an unbounded goroutine.
func TestSpawnRelay_DropsWhenPoolSaturated(t *testing.T) {
	waitRelayPoolEmpty(t)
	d := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	release := make(chan struct{})

	for i := 0; i < relayConcurrencyLimit; i++ {
		spawnRelay(d, "fill", func(ctx context.Context) { <-release })
	}

	var overflowRan atomic.Bool
	spawnRelay(d, "overflow", func(ctx context.Context) { overflowRan.Store(true) })
	if overflowRan.Load() {
		t.Fatal("overflow relay ran despite a saturated relay pool")
	}

	close(release)
	waitRelayPoolEmpty(t)
}

// TestSpawnRelay_RunsWhenPoolHasRoom confirms the normal path still executes fn.
func TestSpawnRelay_RunsWhenPoolHasRoom(t *testing.T) {
	waitRelayPoolEmpty(t)
	d := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	done := make(chan struct{})
	spawnRelay(d, "normal", func(ctx context.Context) { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay fn did not run with an empty pool")
	}
	waitRelayPoolEmpty(t)
}

// FRESH-04: тревога из /v1/report при занятом пуле не бросается, как прочие
// уведомления: отметка last_alert_at уже стоит, и брошенная тревога пришла бы
// лишь напоминанием через 6 часов. При полном пуле и пустой очереди проверки
// она уходит в фон без места в пуле -- ответ на отчёт не ждёт Telegram.
func TestSpawnAlertDelivery_BackgroundWhenPoolSaturated(t *testing.T) {
	waitRelayPoolEmpty(t)
	d := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	release := make(chan struct{})
	for i := 0; i < relayConcurrencyLimit; i++ {
		spawnRelay(d, "fill", func(ctx context.Context) { <-release })
	}

	delivered := make(chan bool, 1)
	hold := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		spawnAlertDelivery(d, "dispatch", 1, "dns-bg", func(ctx context.Context) {
			<-hold // Telegram отвечает медленно
			_, ok := ctx.Deadline()
			delivered <- ok
		})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		close(hold)
		close(release)
		t.Fatal("ответ на отчёт ждёт доставку тревоги при полном пуле")
	}
	close(hold)
	select {
	case ok := <-delivered:
		if !ok {
			t.Fatal("доставка в фоне без срока")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("тревога при занятом пуле не доставлена")
	}
	close(release)
	waitRelayPoolEmpty(t)
	waitRelayDrained(t)
}

// fillAlertSlotless занимает пул и все места фоновых доставок без пула;
// закрытие возвращённого канала их отпускает.
func fillAlertSlotless(t *testing.T, d Deps) chan struct{} {
	t.Helper()
	release := make(chan struct{})
	for i := 0; i < relayConcurrencyLimit; i++ {
		spawnRelay(d, "fill", func(ctx context.Context) { <-release })
	}
	for i := 0; i < alertSlotlessLimit; i++ {
		spawnAlertDelivery(d, "dispatch", 1, fmt.Sprintf("fill-%d", i), func(ctx context.Context) { <-release })
	}
	return release
}

// Выше потолка фоновых доставок без пула -- доставка на месте, под сроком:
// горутины не растут без предела, тревога всё равно доходит.
func TestSpawnAlertDelivery_InlineAboveSlotlessCap(t *testing.T) {
	waitRelayPoolEmpty(t)
	waitRelayDrained(t)
	d := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	release := fillAlertSlotless(t, d)
	defer func() {
		close(release)
		waitRelayPoolEmpty(t)
		waitRelayDrained(t)
	}()

	var ran, hadDeadline atomic.Bool
	spawnAlertDelivery(d, "dispatch", 1, "dns-inline", func(ctx context.Context) {
		_, ok := ctx.Deadline()
		hadDeadline.Store(ok)
		ran.Store(true)
	})
	if !ran.Load() {
		t.Fatal("выше потолка тревога не доставлена на месте")
	}
	if !hadDeadline.Load() {
		t.Fatal("доставка на месте без срока: зависший Telegram держал бы отчёт вечно")
	}
}
