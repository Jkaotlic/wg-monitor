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

	"github.com/anex/wg-monitor/internal/agent"
	"github.com/anex/wg-monitor/internal/agent/actions"
	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/internal/backend"
	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/callbacks"
	bcmd "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/realert"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
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

	// 5. Simulate callback Silence(1h).
	q := &tg.CallbackQuery{
		ID:   "cbk-1",
		From: tg.User{ID: 555},
		Message: tg.Message{
			MessageID: 1001, Chat: tg.Chat{ID: -100},
			Text: hardText,
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
	} else if !strings.Contains(edits[0]["text"].(string), "Silenced") {
		t.Errorf("edit missing 'Silenced': %v", edits[0]["text"])
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
	if !strings.Contains(last["text"].(string), "STILL DOWN") {
		t.Errorf("realert text missing 'STILL DOWN': %v", last["text"])
	}
	if last["reply_markup"] != nil {
		t.Errorf("realert must not have keyboard")
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

	var restartHits int
	awgFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/control/restart-all" {
			restartHits++
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer awgFake.Close()

	router := callbacks.NewRouterWithSink(d, noopTG{}, queue, callbacks.Config{
		ChatID: -100, AdminUserID: 555, MuteCutoffHour: 9,
	})

	q := &tg.CallbackQuery{
		ID:      "cbk-restart",
		From:    tg.User{ID: 555},
		Message: tg.Message{MessageID: 4242, Chat: tg.Chat{ID: -100}, Text: "🔴 alert"},
		Data:    fmt.Sprintf("restart_tunnel:%d:tunnel_amnezia_for_awg2", uid),
	}
	router.HandleCallback(context.Background(), q)

	agentClient := agent.NewClient(backendSrv.URL, tok, "test-agent", 5*time.Second)
	cmd, err := agentClient.PollCommand(context.Background(), 2)
	if err != nil {
		t.Fatalf("PollCommand: %v", err)
	}
	if cmd == nil {
		t.Fatal("PollCommand returned nil — queue should have a restart_tunnel command")
	}
	if cmd.Action != "restart_tunnel" {
		t.Errorf("got action %q want restart_tunnel", cmd.Action)
	}

	runner := &actions.Runner{AwgClient: awgmgr.New(awgFake.URL)}
	res := runner.Execute(context.Background(), *cmd)
	if res.Status != "ok" {
		t.Errorf("expected ok status, got %q output=%q", res.Status, res.Output)
	}
	if restartHits != 1 {
		t.Errorf("expected awg-manager RestartAll hit once, got %d", restartHits)
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

func (r *recordingNotifier) NotifyCommandResult(ctx context.Context, ref bcmd.MessageRef, action string, result wire.CommandResult, maxChars int) error {
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
func (noopTG) DeleteMessage(_ context.Context, _, _ int64) error                              { return nil }
func (noopTG) AnswerCallbackQuery(_ context.Context, _, _ string) error { return nil }
func (noopTG) EditMessageText(_ context.Context, _, _ int64, _ string, _ string, _ *tg.InlineKeyboardMarkup) error {
	return nil
}
func (noopTG) GetUpdates(_ context.Context, _ int64, _ int) ([]tg.Update, error) {
	return nil, nil
}
func (noopTG) GetFile(_ context.Context, _ string) (string, error)      { return "", nil }
func (noopTG) DownloadFile(_ context.Context, _ string) ([]byte, error) { return nil, nil }
