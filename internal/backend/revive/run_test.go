package revive

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
)

func TestSchedule_ReachableSilentRouterLaunchesRightAway(t *testing.T) {
	env := newEnv(t)
	panel := env.panel(t, http.StatusOK)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	if panel.hits() != 2 {
		t.Fatalf("опросов %d, ждали два подряд", panel.hits())
	}
	if calls := env.engine.calls(); len(calls) != 1 || !calls[0].Secrets.Equal(fixtureSecrets()) {
		t.Fatalf("запусков %d", len(calls))
	}
	if in := env.intent(t); in.Status != StatusRunning || in.Attempts != 1 {
		t.Fatalf("%+v", in)
	}
}

func TestSchedule_OfflineRouterStaysWaiting(t *testing.T) {
	env := newEnv(t)
	panel := env.panel(t, http.StatusServiceUnavailable)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	if panel.hits() != 1 {
		t.Fatalf("спящий роутер: опросов %d, ждали один", panel.hits())
	}
	if in := env.intent(t); in.Status != StatusWaiting || len(env.engine.calls()) != 0 {
		t.Fatalf("%+v", in)
	}
}

// Рестарт бэкенда посреди переустановки: задания в памяти нет.
func TestRecover_RunningBecomesWaitingWithAttemptCounted(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	if ok, _ := env.db.Revive().MarkRunning(env.router, env.clock.Now(), 0); !ok {
		t.Fatal("mark running")
	}
	env.svc = env.newService(t, env.key) // «новый процесс»: карта заданий пуста
	n, err := env.svc.Recover()
	if err != nil || n != 1 {
		t.Fatalf("recover: %d %v", n, err)
	}
	in := env.intent(t)
	if in.Status != StatusWaiting || in.Attempts != 1 || in.LastError != reasonJobLost {
		t.Fatalf("%+v", in)
	}
	env.tick(t)
	env.tick(t)
	if in := env.intent(t); in.Status != StatusRunning || in.Attempts != 2 {
		t.Fatalf("повторный запуск после рестарта: %+v", in)
	}
}

func TestRecover_ExhaustedAttemptsFailOnNextTick(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	if _, err := env.db.SQL().Exec(`UPDATE revive_intents SET status = 'running', attempts = 5, last_error = 'роутер не смог скачать агент' WHERE user_id = ?`, env.router); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	env.tick(t)
	in := env.intent(t)
	if in.Status != StatusFailed || len(env.engine.calls()) != 0 || env.hasSecret(t) {
		t.Fatalf("%+v, запусков %d", in, len(env.engine.calls()))
	}
	if n := env.notifier.all(); len(n) != 1 || !strings.Contains(n[0].Text, "попыток: 5") {
		t.Fatalf("%+v", n)
	}
}

func TestRun_TicksUntilCancelled(t *testing.T) {
	env := newEnv(t)
	env.probe.script(awgmstate.Offline)
	env.seedWaiting(t)
	svc, err := New(Config{
		DB: env.db, Key: env.key, Engine: env.engine, Probe: env.probe.Probe, Now: env.clock.Now,
		ProbeEvery: 5 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for env.probe.count() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("за 2 с опросов %d", env.probe.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не остановился после отмены")
	}
}

func TestRecoverAndRun_NilSafe(t *testing.T) {
	var s *Service
	if n, err := s.Recover(); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	s.Run(context.Background()) // выключено -- сразу возвращается
}
