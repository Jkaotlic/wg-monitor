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
		ChatID:            -100,
		FailThreshold:     3,
		RecoveryThreshold: 2,
	})
	router := callbacks.NewRouter(d, tgC, callbacks.Config{
		ChatID: -100, AdminUserID: 555, MuteCutoffHour: 9,
	})
	poller := realert.NewPoller(d, tgC, realert.Config{
		ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second,
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

	router := callbacks.NewRouterWithSink(d, noopTG{}, queue, callbacks.Config{
		ChatID: -100, AdminUserID: 555, MuteCutoffHour: 9,
	})

	q := &tg.CallbackQuery{
		ID:      "cbk-force-recheck",
		From:    tg.User{ID: 555},
		Message: tg.Message{MessageID: 4242, Chat: tg.Chat{ID: -100}, Text: "🔴 alert"},
		Data:    fmt.Sprintf("force_recheck:%d:agent_heartbeat", uid),
	}
	router.HandleCallback(context.Background(), q)

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

// TestCommandResultRelayEndToEnd extends TestCommandChannelEndToEnd to cover
// the v0.6.0 relay path: after the agent POSTs result, the backend must invoke
// the configured TGNotifier with a MessageRef whose MessageID equals the
// original alert message id. This is the test that closes the "жму diag —
// молчит" symptom against regression.
func TestCommandResultRelayEndToEnd(t *testing.T) {
	tmp := t.TempDir() + "/relay.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const tok = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	uid, err := d.Users().Insert("vasya", tok, "1.2.3.4", "nwg0")
	if err != nil {
		t.Fatal(err)
	}

	queue := bcmd.New()

	rec := &recordingNotifier{}
	mux := backend.NewMux(backend.Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  noopDispatcher{},
		CommandSink: queue,
		TGNotifier:  rec,
		UI:          backend.UIConfig{DiagMaxChars: 3500},
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	backendSrv := httptest.NewServer(mux)
	defer backendSrv.Close()

	awgFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/diagnostics/result" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":"diagnostics: all OK"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer awgFake.Close()

	router := callbacks.NewRouterWithSink(d, noopTG{}, queue, callbacks.Config{
		ChatID: -100, AdminUserID: 555, MuteCutoffHour: 9,
	})

	// 1. Operator taps Diag on the original alert message_id=4242.
	tid := int64(11)
	q := &tg.CallbackQuery{
		ID:   "cbk-diag",
		From: tg.User{ID: 555},
		Message: tg.Message{
			MessageID: 4242, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
			Text: "🔴 [vasya] tunnel_amnezia — DOWN",
		},
		Data: fmt.Sprintf("diag_now:%d:tunnel_amnezia_for_awg2", uid),
	}
	router.HandleCallback(context.Background(), q)

	// 2. Agent polls + runs + posts.
	agentClient := agent.NewClient(backendSrv.URL, tok, "test-agent", 5*time.Second)
	cmd, err := agentClient.PollCommand(context.Background(), 2)
	if err != nil || cmd == nil {
		t.Fatalf("PollCommand: %v cmd=%+v", err, cmd)
	}
	runner := &actions.Runner{AwgClient: awgmgr.New(awgFake.URL)}
	res := runner.Execute(context.Background(), *cmd)
	if res.Status != "ok" {
		t.Fatalf("expected ok, got %+v", res)
	}
	if err := agentClient.PostResult(context.Background(), res); err != nil {
		t.Fatalf("PostResult: %v", err)
	}

	// 3. Wait for the async relay to fire (handler kicks goroutine).
	if got := waitForRelay(t, rec.calls, 1, 2*time.Second); got != 1 {
		t.Fatalf("expected 1 NotifyCommandResult call, got %d", got)
	}
	got := rec.last()
	if got.ref.ChatID != -100 || got.ref.MessageID != 4242 {
		t.Errorf("ref mis-routed: chat=%d msg=%d (want -100, 4242)", got.ref.ChatID, got.ref.MessageID)
	}
	if got.ref.ThreadID == nil || *got.ref.ThreadID != 11 {
		t.Errorf("thread id lost: %+v", got.ref.ThreadID)
	}
	if got.action != "diag_now" {
		t.Errorf("action: got %q want diag_now", got.action)
	}
}

type recordingNotifier struct {
	mu      sync.Mutex
	records []notifyCall
}

type notifyCall struct {
	ref      bcmd.MessageRef
	action   string
	result   wire.CommandResult
	maxChars int
}

func (r *recordingNotifier) NotifyCommandResult(ctx context.Context, ref bcmd.MessageRef, action string, result wire.CommandResult, userID int64, maxChars int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, notifyCall{ref: ref, action: action, result: result, maxChars: maxChars})
	return nil
}

func (r *recordingNotifier) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

func (r *recordingNotifier) last() notifyCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.records[len(r.records)-1]
}

// noopDispatcher: minimal Dispatcher impl (this test doesn't exercise the FSM).
type noopDispatcher struct{}

func (noopDispatcher) Handle(_ context.Context, _ int64, _, _ string, _ state.Transition, _ wire.Check) error {
	return nil
}

// noopTG fulfils callbacks.TGClient. HandleCallback calls AnswerCallbackQuery
// and EditMessageText after a successful action; we ignore both for this test.
type noopTG struct{}

func (noopTG) SendMessage(_ context.Context, _ int64, _ *int64, _ string, _ string, _ *int64) (int64, error) {
	return 1, nil
}
func (noopTG) SendMessageWithReplyKeyboard(_ context.Context, _ int64, _ *int64, _ string, _ string, _ *int64, _ any) (int64, error) {
	return 1, nil
}
func (noopTG) DeleteMessage(_ context.Context, _, _ int64) error        { return nil }
func (noopTG) AnswerCallbackQuery(_ context.Context, _, _ string) error { return nil }
func (noopTG) EditMessageText(_ context.Context, _, _ int64, _ string, _ string, _ *tg.InlineKeyboardMarkup) error {
	return nil
}
func (noopTG) GetUpdates(_ context.Context, _ int64, _ int) ([]tg.Update, error) {
	return nil, nil
}
func (noopTG) GetFile(_ context.Context, _ string) (string, error)      { return "", nil }
func (noopTG) DownloadFile(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (noopTG) CreateForumTopic(_ context.Context, _ int64, _ string, _ int) (int64, error) {
	return 0, nil
}

// capturingTG is a TGClient that records SendMessageWithReplyKeyboard calls
// so tests can assert on rendered text and markup.
type capturingTG struct {
	mu   sync.Mutex
	sent []tgSentMsg
}

type tgSentMsg struct {
	text   string
	markup any
}

func (c *capturingTG) SendMessage(_ context.Context, _ int64, _ *int64, text, _ string, _ *int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, tgSentMsg{text: text})
	return int64(len(c.sent)), nil
}
func (c *capturingTG) SendMessageWithReplyKeyboard(_ context.Context, _ int64, _ *int64, text, _ string, _ *int64, markup any) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, tgSentMsg{text: text, markup: markup})
	return int64(len(c.sent)), nil
}
func (c *capturingTG) DeleteMessage(_ context.Context, _, _ int64) error        { return nil }
func (c *capturingTG) AnswerCallbackQuery(_ context.Context, _, _ string) error { return nil }
func (c *capturingTG) EditMessageText(_ context.Context, _, _ int64, _ string, _ string, _ *tg.InlineKeyboardMarkup) error {
	return nil
}
func (c *capturingTG) GetUpdates(_ context.Context, _ int64, _ int) ([]tg.Update, error) {
	return nil, nil
}
func (c *capturingTG) GetFile(_ context.Context, _ string) (string, error)      { return "", nil }
func (c *capturingTG) DownloadFile(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (c *capturingTG) CreateForumTopic(_ context.Context, _ int64, _ string, _ int) (int64, error) {
	return 0, nil
}

func (c *capturingTG) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func (c *capturingTG) lastSent() (tgSentMsg, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return tgSentMsg{}, false
	}
	return c.sent[len(c.sent)-1], true
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

	// Real callbacks.Notifier with a capturing TG so we can inspect rendered text + markup.
	capTG := &capturingTG{}
	notifier := callbacks.NewNotifier(capTG)

	router := callbacks.NewRouterWithSink(d, noopTG{}, queue, callbacks.Config{
		ChatID: -100, AdminUserID: 555, MuteCutoffHour: 9,
	})
	// Share the diagCache between router and notifier so the "Полный отчёт"
	// token written by the notifier is retrievable via the router (standard main.go wiring).
	notifier.DiagCache = router.DiagCache()

	mux := backend.NewMux(backend.Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  noopDispatcher{},
		CommandSink: queue,
		TGNotifier:  notifier,
		UI:          backend.UIConfig{DiagMaxChars: 3500},
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	backendSrv := httptest.NewServer(mux)
	defer backendSrv.Close()

	// 1. Operator taps Diag on the original alert message.
	tid := int64(22)
	q := &tg.CallbackQuery{
		ID:   "cbk-diag-autotrigger",
		From: tg.User{ID: 555},
		Message: tg.Message{
			MessageID: 5050, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
			Text: "🔴 [natasha] tunnel_amnezia — DOWN",
		},
		Data: fmt.Sprintf("diag_now:%d:tunnel_amnezia_for_awg2", uid),
	}
	router.HandleCallback(context.Background(), q)

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

	// 3. Wait for the async relay to fire.
	if got := waitForRelay(t, capTG.calls, 1, 3*time.Second); got < 1 {
		t.Fatalf("expected at least 1 TG message, got %d", got)
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

	// 5. Assert rendered TG message carries the parsed summary. The panel
	// version is engineering: the owner's summary no longer shows it — it is
	// enough that the parsed card arrived rather than raw JSON.
	msg, ok := capTG.lastSent()
	if !ok {
		t.Fatal("no TG message captured")
	}
	if !strings.Contains(msg.text, "📊 Диагностика") {
		t.Errorf("TG text missing '📊 Диагностика': %q", msg.text)
	}
	if !strings.Contains(msg.text, "отчёт получен") {
		t.Errorf("TG text missing parsed summary 'отчёт получен': %q", msg.text)
	}

	// 6. Assert inline keyboard has "📄 Полный отчёт" with diag_raw callback.
	kb, ok := msg.markup.(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("expected *tg.InlineKeyboardMarkup, got %T (%v)", msg.markup, msg.markup)
	}
	if len(kb.InlineKeyboard) == 0 {
		t.Fatal("inline keyboard has no rows")
	}
	firstRow := kb.InlineKeyboard[0]
	foundRaw := false
	for _, btn := range firstRow {
		if strings.Contains(btn.Text, "Полный отчёт") || strings.HasPrefix(btn.CallbackData, "diag_raw:") {
			foundRaw = true
			break
		}
	}
	if !foundRaw {
		t.Errorf("inline keyboard first row missing 'Полный отчёт' / diag_raw: button; got %+v", firstRow)
	}
}
