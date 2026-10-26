package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// orderSink -- рассылка, где карточка тревоги «висит» на Telegram до
// release, а каждый шаг записывается в журнал. Карточка, как настоящая
// рассылка, запоминает своё сообщение в alert_messages; «восстановилось»
// записывает, сколько сообщений было в нити на момент ответа.
type orderSink struct {
	d       *db.DB
	release chan struct{}
	entered chan struct{}

	mu       sync.Mutex
	events   []string
	nextMsg  int64
	threadAt []int // len(alert_messages) на момент каждого ответа
}

func (s *orderSink) log(e string) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *orderSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *orderSink) Send(context.Context, int64, string, string) (int, error) { return 1, nil }

func (s *orderSink) SendTracked(ctx context.Context, uid int64, checkName, _, _ string, _ *tg.InlineKeyboardMarkup, _ bool) (int, error) {
	s.mu.Lock()
	s.nextMsg++
	msgID := s.nextMsg
	s.mu.Unlock()
	s.log("hard-start")
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if err := s.d.AlertMessages().Put(uid, checkName, 100, 500+msgID); err != nil {
		return 0, err
	}
	s.log("hard-done")
	return 1, nil
}

func (s *orderSink) ReplyToEach(_ context.Context, uid int64, checkName, _, _ string, _ bool) error {
	msgs, _ := s.d.AlertMessages().List(uid, checkName)
	s.mu.Lock()
	s.threadAt = append(s.threadAt, len(msgs))
	s.mu.Unlock()
	s.log("recovery")
	return nil
}

func queueFixture(t *testing.T) (*db.DB, int64, *alerts.Dispatcher, *orderSink, Deps) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	uid, err := d.Users().Insert("queue", "f411f411f411f411f411f411f411f411f411f411f411f411f411f411f411f411", "198.51.100.45", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &orderSink{d: d, release: make(chan struct{}), entered: make(chan struct{}, 4)}
	disp := alerts.NewDispatcher(d, nil, alerts.Config{FailThreshold: 2, RecoveryThreshold: 2})
	disp.SetNotifySink(sink)
	deps := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background(), DB: d, Dispatcher: disp}
	return d, uid, disp, sink, deps
}

func queueHard(t *testing.T, deps Deps, disp *alerts.Dispatcher, uid int64) {
	t.Helper()
	now := time.Now().UTC()
	next := db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 2, HardSince: &now, LastAlertAt: &now}
	deliver, err := disp.Apply(uid, "queue", "dns", state.Transition{Kind: state.Hard, Next: next}, wire.Check{Name: "dns", Status: "fail"})
	if err != nil || deliver == nil {
		t.Fatalf("Apply hard: deliver=%v err=%v", deliver != nil, err)
	}
	spawnAlertDelivery(deps, "dispatch", uid, "dns", func(ctx context.Context) { _ = deliver(ctx) })
}

func queueRecovery(t *testing.T, deps Deps, disp *alerts.Dispatcher, uid int64) {
	t.Helper()
	next := db.IncidentState{CurrentStatus: "ok", ConsecutiveOKs: 2}
	deliver, err := disp.Apply(uid, "queue", "dns", state.Transition{Kind: state.Recovery, Next: next}, wire.Check{Name: "dns", Status: "ok"})
	if err != nil || deliver == nil {
		t.Fatalf("Apply recovery: deliver=%v err=%v", deliver != nil, err)
	}
	spawnAlertDelivery(deps, "dispatch", uid, "dns", func(ctx context.Context) { _ = deliver(ctx) })
}

func waitEvents(t *testing.T, s *orderSink, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ev := s.snapshot()
		if len(ev) >= n {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatalf("ждали %d шагов доставки, есть %v", n, ev)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Доставки одной проверки идут строго по очереди: «восстановилось», пришедшее,
// пока карточка тревоги ещё висит на Telegram, ждёт её и отвечает в нить,
// а не мимо (раньше оно уходило первым и не находило, на что ответить).
func TestAlertDeliveryQueue_RecoveryWaitsForHard(t *testing.T) {
	waitRelayPoolEmpty(t)
	_, uid, disp, sink, deps := queueFixture(t)

	queueHard(t, deps, disp, uid)
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("карточка тревоги не начала отправку")
	}
	queueRecovery(t, deps, disp, uid)

	time.Sleep(100 * time.Millisecond)
	for _, e := range sink.snapshot() {
		if e == "recovery" {
			close(sink.release)
			t.Fatal("«восстановилось» ушло, пока карточка тревоги ещё не отправлена")
		}
	}
	close(sink.release)

	ev := waitEvents(t, sink, 3)
	want := []string{"hard-start", "hard-done", "recovery"}
	for i := range want {
		if ev[i] != want[i] {
			t.Fatalf("порядок доставки %v, ждали %v", ev, want)
		}
	}
	sink.mu.Lock()
	thread := sink.threadAt[0]
	sink.mu.Unlock()
	if thread != 1 {
		t.Fatalf("«восстановилось» отвечало при %d сообщениях в нити, ждали 1 (ответ на тревогу)", thread)
	}
	waitRelayPoolEmpty(t)
	if n := alertQueueLen(); n != 0 {
		t.Fatalf("очередь доставки не освобождена: %d записей", n)
	}
}

// Новая тревога, пришедшая следом за «восстановилось», не теряет свою нить:
// «восстановилось» прошлой поломки стирает переписку до неё, а не после.
func TestAlertDeliveryQueue_RecoveryDoesNotClearNextHard(t *testing.T) {
	waitRelayPoolEmpty(t)
	d, uid, disp, sink, deps := queueFixture(t)

	queueHard(t, deps, disp, uid)
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("карточка тревоги не начала отправку")
	}
	queueRecovery(t, deps, disp, uid)
	queueHard(t, deps, disp, uid)
	time.Sleep(50 * time.Millisecond)
	close(sink.release)

	ev := waitEvents(t, sink, 5)
	want := []string{"hard-start", "hard-done", "recovery", "hard-start", "hard-done"}
	for i := range want {
		if ev[i] != want[i] {
			t.Fatalf("порядок доставки %v, ждали %v", ev, want)
		}
	}
	waitRelayPoolEmpty(t)
	msgs, err := d.AlertMessages().List(uid, "dns")
	if err != nil {
		t.Fatal(err)
	}
	if msgs[100] != 502 {
		t.Fatalf("нить новой тревоги = %v, ждали сообщение 502", msgs)
	}
	if n := alertQueueLen(); n != 0 {
		t.Fatalf("очередь доставки не освобождена: %d записей", n)
	}
}

// Пул relay полон, а по проверке уже стоит доставка: ждать её на месте
// значило бы держать ответ на отчёт всю цепочку. Доставка уходит в фон --
// число таких горутин ограничено самой цепочкой.
func TestReportNotHeldByQueueWhenPoolFull(t *testing.T) {
	waitRelayPoolEmpty(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "f412f412f412f412f412f412f412f412f412f412f412f412f412f412f412f412"
	uid, err := d.Users().Insert("fullpool", tok, "198.51.100.46", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.State().Save(uid, "dns", db.IncidentState{CurrentStatus: "fail", ConsecutiveFails: 1}); err != nil {
		t.Fatal(err)
	}
	sink := &stuckTelegram{release: make(chan struct{}), delivered: make(chan string, 1)}
	close(sink.release) // Telegram отвечает сразу
	disp := alerts.NewDispatcher(d, nil, alerts.Config{FailThreshold: 2, RecoveryThreshold: 2})
	disp.SetNotifySink(sink)
	deps := Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  disp,
		CommandSink: &fakeCmdSink{},
		Thresholds:  state.Thresholds{Fail: 2, Recovery: 2},
	}
	srv := httptest.NewServer(NewMux(deps))
	defer srv.Close()

	_, releaseQueued := alerts.EnqueueDelivery(uid, "dns") // доставка по dns уже в очереди
	fill := make(chan struct{})
	for i := 0; i < relayConcurrencyLimit; i++ {
		spawnRelay(deps, "fill", func(ctx context.Context) { <-fill })
	}
	cleanup := func() { releaseQueued(); close(fill) }

	body, _ := json.Marshal(wire.Report{Timestamp: time.Now().UTC(), AgentVersion: "test",
		Checks: []wire.Check{{Name: "dns", Status: "fail"}}})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- -1
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case code := <-done:
		if code != http.StatusOK {
			cleanup()
			t.Fatalf("ответ на отчёт: %d", code)
		}
	case <-time.After(2 * time.Second):
		cleanup()
		<-done
		t.Fatal("ответ на отчёт ждёт доставку, стоящую в очереди проверки")
	}
	cleanup()
	select {
	case <-sink.delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("тревога после очереди не доставлена")
	}
	waitRelayPoolEmpty(t)
	waitRelayDrained(t)
}
