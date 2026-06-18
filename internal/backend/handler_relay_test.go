package backend

import (
	"context"
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
