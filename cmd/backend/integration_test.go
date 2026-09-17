package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/actions"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/callbacks"
	bcmd "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/realert"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// TestStage2EndToEnd exercises: HARD with keyboard → callback Silence → edited message → state row updated.
// Then ages the row 7h, ensures realert poller picks up and sends a STILL-DOWN reminder.
func TestStage2EndToEnd(t *testing.T) {
	// 1. Spin up fake TG server that records calls
	var mu sync.Mutex
	sentMsgs := []map[string]any{}
	edits := []map[string]any{}
	answers := []map[string]any{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sentMsgs = append(sentMsgs, req)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1000 + len(sentMsgs)}})
		case strings.HasSuffix(r.URL.Path, "/createForumTopic"):
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_thread_id": 7}})
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			edits = append(edits, req)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			answers = append(answers, req)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
		}
	}))
	defer srv.Close()

	// 2. Set up DB + user
	tmp := t.TempDir() + "/test.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("vasya", "rawtoken", "1.2.3.4", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	// Уведомления идут в личку владельца: без привязки слать некому, и
	// тревога никуда не уйдёт -- это и есть новое поведение.
	if err := d.Users().SetTelegramUserID(uid, 4242); err != nil {
		t.Fatal(err)
	}

	// 3. Build TG client + dispatcher + callbacks router + realert poller.
	// BaseURL + Token + "/" + method → srv.URL + "/bot" + "t" + "/sendMessage"
	tgC := &tg.Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	disp := alerts.NewDispatcher(d, tgC, alerts.Config{
		FailThreshold:     3,
		RecoveryThreshold: 2,
	})
	router := callbacks.NewRouter(d, tgC, callbacks.Config{AdminUserID: 555})
	poller := realert.NewPoller(d, tgC, realert.Config{
		RealertEvery: 6 * time.Hour, TickEvery: time.Second,
	})

	// 4. Trigger 3 fails → HARD.
	// The FSM reads persisted state each iteration so ConsecutiveFails accumulates.
	ctx := context.Background()
	th := state.Thresholds{Fail: 3, Recovery: 2}
	for i := 0; i < 3; i++ {
		prev, _ := d.State().Get(uid, "awg_handshake")
		tr := state.Apply(prev, "fail", time.Now(), th)
		if err := disp.Handle(ctx, uid, "vasya", "awg_handshake", tr, wire.Check{Name: "awg_handshake", Status: "fail", Details: map[string]any{"error": "h=200s"}}); err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	if len(sentMsgs) != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 HARD message, got %d", len(sentMsgs))
	}
	if sentMsgs[0]["reply_markup"] == nil {
		t.Errorf("HARD message missing keyboard")
	}
	hardText := sentMsgs[0]["text"].(string)
	mu.Unlock()
	u, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	// Тему больше не создаём: тревога ушла в личку владельца, и нажатие
	// придёт оттуда же.
	if u.TelegramThreadID != nil {
		t.Fatalf("тема роутера больше не заводится, получили %v", *u.TelegramThreadID)
	}

	// 5. Simulate callback Silence(1h).
	// Нажатие приходит из лички владельца -- туда же, куда ушла тревога.
	q := &tg.CallbackQuery{
		ID:   "cbk-1",
		From: tg.User{ID: 4242},
		Message: tg.Message{
			MessageID: 1001,
			Chat:      tg.Chat{ID: 4242},
			Text:      hardText,
		},
		Data: fmt.Sprintf("silence:%d:awg_handshake:1h", uid),
	}
	router.HandleCallback(ctx, q)

	mu.Lock()
	if len(answers) != 1 {
		t.Errorf("expected 1 answerCallback, got %d", len(answers))
	}
	if len(edits) != 1 {
		t.Errorf("expected 1 edit, got %d", len(edits))
	} else if !strings.Contains(edits[0]["text"].(string), "Уведомления скрыты") {
		t.Errorf("edit missing silence status: %v", edits[0]["text"])
	}
	mu.Unlock()

	// 6. Verify state has SilencedUntil set.
	st, err := d.State().Get(uid, "awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if st.SilencedUntil == nil {
		t.Error("SilencedUntil nil after silence callback")
	}

	// 7. Age the state: clear silence, set hard_since/last_alert_at to 7h ago.
	aged := time.Now().Add(-7 * time.Hour)
	st.SilencedUntil = nil
	st.HardSince = &aged
	st.LastAlertAt = &aged
	if err := d.State().Save(uid, "awg_handshake", st); err != nil {
		t.Fatal(err)
	}

	// 8. Trigger realert tick — Run with short timeout to fire 1 tick.
	mu.Lock()
	initialMsgs := len(sentMsgs)
	mu.Unlock()
	runCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	_ = poller.Run(runCtx)
	cancel()
	poller.WaitForExit()

	mu.Lock()
	realertSent := len(sentMsgs) - initialMsgs
	if realertSent != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 realert message, got %d (total %d)", realertSent, len(sentMsgs))
	}
	last := sentMsgs[len(sentMsgs)-1]
	if !strings.Contains(last["text"].(string), "Всё ещё:") {
		t.Errorf("realert text missing realert marker: %v", last["text"])
	}
	if last["reply_markup"] == nil {
		t.Errorf("realert must include silence/ack keyboard")
	}
	mu.Unlock()
}

// TestCommandChannelEndToEnd: TG callback enqueues a wire.Command, the agent
// HTTP client polls /v1/cmd, the actions.Runner executes (with a fake
// awg-manager), the result is POSTed back, and the backend's queue records it.
//
// Wire-up here mirrors cmd/backend/main.go and cmd/agent/main.go closely so
// it catches integration regressions in either side.
func TestCommandChannelEndToEnd(t *testing.T) {
	tmp := t.TempDir() + "/cmdchan.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const tok = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	uid, err := d.Users().Insert("victor", tok, "1.2.3.4", "nwg0")
	if err != nil {
		t.Fatal(err)
	}

	queue := bcmd.New()

	mux := backend.NewMux(backend.Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  noopDispatcher{},
		CommandSink: queue,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	backendSrv := httptest.NewServer(mux)
	defer backendSrv.Close()

	// Команды ставит мини-апп (кнопок в боте больше нет, цикл 5): очередь
	// напрямую -- тот же путь, что у /v1/miniapp/routers/{id}/commands.
	if err := queue.Enqueue(uid, wire.Command{ID: "cmd-force-recheck", Action: "force_recheck", Args: map[string]any{}, IssuedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	agentClient := agent.NewClient(backendSrv.URL, tok, "test-agent", 5*time.Second)
	cmd, err := agentClient.PollCommand(context.Background(), 2)
	if err != nil {
		t.Fatalf("PollCommand: %v", err)
	}
	if cmd == nil {
		t.Fatal("PollCommand returned nil — queue should have a force_recheck command")
	}
	if cmd.Action != "force_recheck" {
		t.Errorf("got action %q want force_recheck", cmd.Action)
	}

	var rechecked bool
	runner := &actions.Runner{ForceRecheck: func(context.Context) { rechecked = true }}
	res := runner.Execute(context.Background(), *cmd)
	if res.Status != "ok" {
		t.Errorf("expected ok status, got %q output=%q", res.Status, res.Output)
	}
	if !rechecked {
		t.Error("expected ForceRecheck callback to run")
	}

	if err := agentClient.PostResult(context.Background(), res); err != nil {
		t.Fatalf("PostResult: %v", err)
	}

	got, ok := queue.AwaitResult(context.Background(), uid, cmd.ID, 100*time.Millisecond)
	if !ok || got == nil {
		t.Fatal("queue.AwaitResult: command result not recorded")
	}
	if got.Status != "ok" || got.ID != cmd.ID {
		t.Errorf("recorded result mismatch: %+v", got)
	}
}

// noopDispatcher: minimal Dispatcher impl (this test doesn't exercise the FSM).
type noopDispatcher struct{}

func (noopDispatcher) Handle(_ context.Context, _ int64, _, _ string, _ state.Transition, _ wire.Check) error {
	return nil
}

// TestIntegration_DiagNow_FreshRunNoRestart verifies the diag_now state
// machine end-to-end after v0.30's task 2: every tap runs a fresh diagnostic
// pass and never restarts a VPN tunnel.
//  1. Runner calls GET /api/diagnostics/stream?restart=false → SSE "done".
//  2. Runner reads GET /api/diagnostics/result → 200 with parseable body.
//  3. POST /api/diagnostics/run — the endpoint that always restarts every
//     tunnel — is never hit (runHits == 0).
//  4. Backend renders a Card with "📊 Диагностика" / "2.8.2" and an inline
//     keyboard whose first row contains "📄 Полный отчёт" (callback diag_raw:…).
func TestIntegration_DiagNow_FreshRunNoRestart(t *testing.T) {
	var (
		resultHits int
		streamHits int
		runHits    int
		mu         sync.Mutex
	)
	awgFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/diagnostics/stream":
			streamHits++
			if got := r.URL.Query().Get("restart"); got != "false" {
				t.Errorf("stream restart param: got %q, want %q", got, "false")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("event: done\ndata: {\"type\":\"done\"}\n\n"))
		case "/api/diagnostics/result":
			resultHits++
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"system":{"appVersion":"2.8.2","uptime":"5m"}}`))
		case "/api/diagnostics/run":
			runHits++
			t.Errorf("POST /api/diagnostics/run must never be called — it always restarts every VPN tunnel")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"status":"running"}}`))
		default:
			t.Errorf("unexpected awg-mgr path: %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer awgFake.Close()

	// --- Harness wiring ---
	tmp := t.TempDir() + "/diag_autotrigger.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	const tok = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	uid, err := d.Users().Insert("natasha", tok, "1.2.3.4", "nwg0")
	if err != nil {
		t.Fatal(err)
	}

	queue := bcmd.New()

	mux := backend.NewMux(backend.Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  noopDispatcher{},
		CommandSink: queue,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	backendSrv := httptest.NewServer(mux)
	defer backendSrv.Close()

	// 1. Команду ставит мини-апп своим маршрутом -- очередь напрямую.
	if err := queue.Enqueue(uid, wire.Command{ID: "cmd-diag-now", Action: "diag_now", Args: map[string]any{}, IssuedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	// 2. Agent polls command and runs it through a fresh Runner (no
	// poll-tuning fields left to set — DiagFresh's own budget is the 75s
	// diag_now action-timeout override, way more than this test needs).
	agentClient := agent.NewClient(backendSrv.URL, tok, "test-agent-diag", 5*time.Second)
	cmd, err := agentClient.PollCommand(context.Background(), 2)
	if err != nil || cmd == nil {
		t.Fatalf("PollCommand: %v cmd=%+v", err, cmd)
	}
	if cmd.Action != "diag_now" {
		t.Fatalf("expected diag_now, got %q", cmd.Action)
	}

	runner := &actions.Runner{
		AwgClient: awgmgr.New(awgFake.URL),
	}
	res := runner.Execute(context.Background(), *cmd)
	if res.Status != "ok" {
		t.Fatalf("runner.Execute: status=%q output=%q", res.Status, res.Output)
	}

	if err := agentClient.PostResult(context.Background(), res); err != nil {
		t.Fatalf("PostResult: %v", err)
	}

	// 3. Итог записан в очередь -- его заберёт опрос мини-аппа.
	if got, ok := queue.AwaitResult(context.Background(), uid, cmd.ID, time.Second); !ok || got == nil || got.Status != "ok" {
		t.Fatalf("итог diag_now не записан: %+v", got)
	}

	// 4. Assert hit counters: exactly one fresh stream run, one result read,
	// and the tunnel-restarting /run endpoint untouched.
	mu.Lock()
	rh := resultHits
	sh := streamHits
	rnh := runHits
	mu.Unlock()
	if sh != 1 {
		t.Errorf("streamHits: want 1, got %d", sh)
	}
	if rh != 1 {
		t.Errorf("resultHits: want 1, got %d", rh)
	}
	if rnh != 0 {
		t.Errorf("runHits: want 0 (must never call /api/diagnostics/run), got %d", rnh)
	}
}
