package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	wire1 "github.com/anex/wg-monitor/pkg/wire"
)

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	tmp := t.TempDir() + "/test.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	// Users().Insert takes (nickname, rawToken, expectedExitIP, awgIface string)
	uid, err := d.Users().Insert("vasya", "rawtoken", "1.1.1.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	return d, uid
}

func TestActionSilenceWritesUntil(t *testing.T) {
	d, uid := newTestDB(t)
	a := NewSilenceAction(d)
	statusLine, err := a.Apply(context.Background(), nil, Args{
		Action: "silence", UserID: uid, CheckName: "awg_handshake", TTL: 4 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statusLine, "⏸ Уведомления скрыты") {
		t.Errorf("status line should start with human silence marker, got: %q", statusLine)
	}
	st, _ := d.State().Get(uid, "awg_handshake")
	if st.SilencedUntil == nil {
		t.Fatal("SilencedUntil nil")
	}
	elapsed := time.Until(*st.SilencedUntil)
	if elapsed < 3*time.Hour+30*time.Minute || elapsed > 4*time.Hour+30*time.Minute {
		t.Errorf("expected silence ~4h, got %v", elapsed)
	}
}

type fakeEnqueuer struct {
	calls []enqueueCall
	refs  []enqueueRefCall
	err   error
}

type enqueueCall struct {
	userID int64
	cmdID  string
	action string
	check  string
}

type enqueueRefCall struct {
	userID    int64
	cmdID     string
	chatID    int64
	messageID int64
	bulkID    string
}

func (f *fakeEnqueuer) Enqueue(userID int64, cmd wire1.Command) error {
	if f.err != nil {
		return f.err
	}
	check, _ := cmd.Args["check_name"].(string)
	f.calls = append(f.calls, enqueueCall{
		userID: userID, cmdID: cmd.ID, action: cmd.Action, check: check,
	})
	return nil
}

func (f *fakeEnqueuer) EnqueueWithRef(userID int64, cmd wire1.Command, ref cmdpkg.MessageRef) error {
	if err := f.Enqueue(userID, cmd); err != nil {
		return err
	}
	f.refs = append(f.refs, enqueueRefCall{
		userID: userID, cmdID: cmd.ID, chatID: ref.ChatID, messageID: ref.MessageID, bulkID: ref.BulkID,
	})
	return nil
}

func TestCommandAction_RestartTunnelEnqueues(t *testing.T) {
	sink := &fakeEnqueuer{}
	a := NewCommandAction(sink, func() string { return "fixed-id-1" })
	statusLine, err := a.Apply(context.Background(), nil, Args{
		Action: "restart_tunnel", UserID: 7, CheckName: "tunnel_amnezia_for_awg2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.calls))
	}
	c := sink.calls[0]
	if c.userID != 7 || c.cmdID != "fixed-id-1" || c.action != "restart_tunnel" || c.check != "tunnel_amnezia_for_awg2" {
		t.Errorf("got %+v", c)
	}
	if !strings.Contains(statusLine, "Перезапуск туннеля") || !strings.Contains(statusLine, "очередь") {
		t.Errorf("unexpected status line: %q", statusLine)
	}
}

func TestCommandAction_RestartPanelLabelsAwgManager(t *testing.T) {
	sink := &fakeEnqueuer{}
	a := NewCommandAction(sink, func() string { return "fixed-id-1" })
	statusLine, err := a.Apply(context.Background(), nil, Args{
		Action: "restart_tunnel", UserID: 7, CheckName: panelSentinel,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusLine, "awg-manager") {
		t.Fatalf("panel restart should name awg-manager, got %q", statusLine)
	}
	if strings.Contains(statusLine, "Перезапуск туннеля") {
		t.Fatalf("panel restart must not look like a per-tunnel restart: %q", statusLine)
	}
}

func TestCommandAction_CommandActions(t *testing.T) {
	cases := []struct {
		action  string
		wantSub string
	}{
		{"restart_tunnel", "Перезапуск туннеля"},
		{"diag_now", "Диагностика"},
		{"pingcheck_now", "Проверка связи"},
		{"force_recheck", "Повторная проверка"},
		{"opkg_upgrade", "opkg"},
		{"router_doctor", "Проверка"},
	}
	for _, c := range cases {
		sink := &fakeEnqueuer{}
		a := NewCommandAction(sink, func() string { return "id-" + c.action })
		s, err := a.Apply(context.Background(), nil, Args{
			Action: c.action, UserID: 1, CheckName: "x",
		})
		if err != nil {
			t.Errorf("%s: err=%v", c.action, err)
			continue
		}
		if !strings.Contains(s, c.wantSub) {
			t.Errorf("%s: status %q missing %q", c.action, s, c.wantSub)
		}
		if len(sink.calls) != 1 || sink.calls[0].action != c.action {
			t.Errorf("%s: expected 1 enqueue with action=%s, got calls=%+v", c.action, c.action, sink.calls)
		}
	}
}

func TestCommandAction_PropagatesEnqueueError(t *testing.T) {
	sink := &fakeEnqueuer{err: errSentinel}
	a := NewCommandAction(sink, func() string { return "x" })
	_, err := a.Apply(context.Background(), nil, Args{
		Action: "diag_now", UserID: 1, CheckName: "x",
	})
	if err == nil {
		t.Fatal("expected error propagated")
	}
}

func TestCommandAction_NilSinkReturnsError(t *testing.T) {
	a := NewCommandAction(nil, func() string { return "x" })
	_, err := a.Apply(context.Background(), nil, Args{
		Action: "diag_now", UserID: 1, CheckName: "x",
	})
	if err == nil {
		t.Fatal("expected error when sink is nil (command channel disabled)")
	}
}

var errSentinel = newSentinelErr()

func newSentinelErr() error { return &sentinel{} }

type sentinel struct{}

func (*sentinel) Error() string { return "boom" }

func TestActionAckSetsAcked(t *testing.T) {
	d, uid := newTestDB(t)
	a := NewAckAction(d)
	statusLine, err := a.Apply(context.Background(), nil, Args{
		Action: "ack", UserID: uid, CheckName: "awg_handshake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statusLine, "✅ Отмечено") {
		t.Errorf("status line should start with human ack marker, got: %q", statusLine)
	}
	st, _ := d.State().Get(uid, "awg_handshake")
	if !st.Acked {
		t.Error("Acked not set to true")
	}
}

func TestNextCutoff(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Moscow")
	cases := []struct {
		now      time.Time
		cutoff   int
		expected time.Time
	}{
		{now: time.Date(2026, 4, 28, 14, 0, 0, 0, loc), cutoff: 9, expected: time.Date(2026, 4, 29, 9, 0, 0, 0, loc)},
		{now: time.Date(2026, 4, 28, 5, 0, 0, 0, loc), cutoff: 9, expected: time.Date(2026, 4, 28, 9, 0, 0, 0, loc)},
		{now: time.Date(2026, 4, 28, 8, 55, 0, 0, loc), cutoff: 9, expected: time.Date(2026, 4, 28, 9, 0, 0, 0, loc)},
		{now: time.Date(2026, 4, 28, 9, 0, 0, 0, loc), cutoff: 9, expected: time.Date(2026, 4, 29, 9, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		got := nextCutoff(c.now, c.cutoff, loc)
		if !got.Equal(c.expected) {
			t.Errorf("now=%v cutoff=%d: got %v, want %v", c.now, c.cutoff, got, c.expected)
		}
	}
}

func TestActionMuteWritesUntil(t *testing.T) {
	d, uid := newTestDB(t)
	a := NewMuteAction(d, 9)
	_, err := a.Apply(context.Background(), nil, Args{Action: "mute", UserID: uid, CheckName: "awg_handshake"})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := d.State().Get(uid, "awg_handshake")
	if st.SilencedUntil == nil {
		t.Fatal("SilencedUntil nil")
	}
	delta := time.Until(*st.SilencedUntil)
	if delta < 0 || delta > 25*time.Hour {
		t.Errorf("delta out of range [0, 25h]: %v", delta)
	}
}

func TestActionHistoryNoEvents(t *testing.T) {
	d, uid := newTestDB(t)
	var sent []string
	fakeTG := &fakeTGForHistory{onSend: func(text string) { sent = append(sent, text) }}
	a := NewHistoryAction(d, fakeTG, -100)
	_, err := a.Apply(context.Background(), &tg.CallbackQuery{}, Args{
		Action: "history", UserID: uid, CheckName: "awg_handshake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected 1 history message, got %d", len(sent))
	}
	if !strings.Contains(sent[0], "нет событий") {
		t.Errorf("expected 'нет событий', got %q", sent[0])
	}
}

func TestActionHistoryWithTransitions(t *testing.T) {
	d, uid := newTestDB(t)
	now := time.Now()
	// Insert 5 events: ok, fail, fail, fail, ok (one HARD transition)
	_ = d.Events().Insert(uid, "awg_handshake", "ok", "", now.Add(-30*time.Minute))
	_ = d.Events().Insert(uid, "awg_handshake", "fail", "h=200s", now.Add(-25*time.Minute))
	_ = d.Events().Insert(uid, "awg_handshake", "fail", "h=250s", now.Add(-20*time.Minute))
	_ = d.Events().Insert(uid, "awg_handshake", "fail", "h=300s", now.Add(-15*time.Minute))
	_ = d.Events().Insert(uid, "awg_handshake", "ok", "", now.Add(-10*time.Minute))

	var sent []string
	fakeTG := &fakeTGForHistory{onSend: func(text string) { sent = append(sent, text) }}
	a := NewHistoryAction(d, fakeTG, -100)
	_, err := a.Apply(context.Background(), &tg.CallbackQuery{}, Args{
		Action: "history", UserID: uid, CheckName: "awg_handshake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("got %d msgs", len(sent))
	}
	msg := sent[0]
	// Expect >=2 transitions: ok->fail and fail->ok
	if !strings.Contains(msg, "✅") || !strings.Contains(msg, "❌") {
		t.Errorf("expected ✅ and ❌ in transitions, got %q", msg)
	}
}

// Minimal mock for History tests (no SendMessageWithKeyboard needed)
type fakeTGForHistory struct {
	onSend func(text string)
}

func (f *fakeTGForHistory) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	if f.onSend != nil {
		f.onSend(text)
	}
	return 1, nil
}

// Ensure db import is used (newTestDB already uses it, but keep explicit).
var _ *db.DB

func TestImportAction_Apply_Replace(t *testing.T) {
	pending := map[int64]*pendingUpload{
		42: {ConfB64: "abc", Name: "awg11", Token: "tok1", ExpiresAt: time.Now().Add(time.Minute)},
	}
	sink := &fakeEnqueuer{}
	a := &ImportAction{
		sink: sink,
		consumeFn: func(uid int64, token string) (*pendingUpload, bool) {
			if uid == 42 && token == "tok1" {
				up := pending[uid]
				delete(pending, uid)
				return up, true
			}
			return nil, false
		},
		idGen: func() string { return "fixed-id" },
	}
	q := &tg.CallbackQuery{
		ID:      "cbq1",
		From:    tg.User{ID: 42},
		Message: tg.Message{MessageID: 10, Chat: tg.Chat{ID: -100}},
		Data:    "tunnel_import_replace:42:awg11:tok1",
	}
	args := Args{Action: "tunnel_import_replace", UserID: 42, CheckName: "awg11", ImportToken: "tok1"}
	status, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "awg11") {
		t.Errorf("status: %q", status)
	}
	// EnqueueWithRef was called — check via refs slice
	if len(sink.refs) == 0 {
		t.Fatal("no EnqueueWithRef call")
	}
	// Action and args stored in calls (EnqueueWithRef delegates to Enqueue)
	if len(sink.calls) == 0 {
		t.Fatal("no enqueue call recorded")
	}
	lastCall := sink.calls[len(sink.calls)-1]
	if lastCall.action != "tunnel_import" {
		t.Errorf("cmd action: %q", lastCall.action)
	}
	if len(pending) != 0 {
		t.Error("pending should be consumed")
	}
}

func TestImportAction_Apply_Expired(t *testing.T) {
	sink := &fakeEnqueuer{}
	a := &ImportAction{
		sink:      sink,
		consumeFn: func(uid int64, token string) (*pendingUpload, bool) { return nil, false },
		idGen:     func() string { return "x" },
	}
	q := &tg.CallbackQuery{Message: tg.Message{Chat: tg.Chat{ID: -100}}}
	_, err := a.Apply(context.Background(), q, Args{Action: "tunnel_import_replace", UserID: 99})
	if err == nil || !strings.Contains(err.Error(), "истекла") {
		t.Errorf("expected expiry error, got %v", err)
	}
}

// fakeRebindEnqueuer is a minimal CommandEnqueuer that records the last
// enqueued action and args. Used by RebindConfirmAction tests only.
type fakeRebindEnqueuer struct {
	lastAction string
	lastArgs   map[string]any
}

func (f *fakeRebindEnqueuer) Enqueue(userID int64, cmd wire1.Command) error {
	f.lastAction = cmd.Action
	f.lastArgs = cmd.Args
	return nil
}

func (f *fakeRebindEnqueuer) EnqueueWithRef(userID int64, cmd wire1.Command, ref cmdpkg.MessageRef) error {
	f.lastAction = cmd.Action
	f.lastArgs = cmd.Args
	return nil
}

func TestRebindConfirmAction_TokenMissing(t *testing.T) {
	a := &RebindConfirmAction{
		consumeFn: func(int64, string) (*pendingRebind, bool) { return nil, false },
		idGen:     func() string { return "id1" },
	}
	q := &tg.CallbackQuery{Message: tg.Message{Chat: tg.Chat{ID: 1}}}
	_, err := a.Apply(context.Background(), q, Args{Action: "routes_confirm", UserID: 42, RebindToken: "x"})
	if err == nil {
		t.Errorf("expected error when token unknown")
	}
}

func TestRebindConfirmAction_HappyPath(t *testing.T) {
	pr := &pendingRebind{
		SrcID: "t1", DstID: "t2",
		Token:     "tok1",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	consumed := false
	sink := &fakeRebindEnqueuer{}
	a := &RebindConfirmAction{
		consumeFn: func(uid int64, token string) (*pendingRebind, bool) {
			if uid == 42 && token == "tok1" && !consumed {
				consumed = true
				return pr, true
			}
			return nil, false
		},
		idGen: func() string { return "id1" },
		sink:  sink,
	}
	q := &tg.CallbackQuery{Message: tg.Message{Chat: tg.Chat{ID: 1}, MessageID: 7}}
	_, err := a.Apply(context.Background(), q, Args{Action: "routes_confirm", UserID: 42, RebindToken: "tok1", RebindSrcID: "t1", RebindDstID: "t2"})
	if err != nil {
		t.Fatal(err)
	}
	if sink.lastAction != "route_rebind" {
		t.Errorf("enqueued %q, want route_rebind", sink.lastAction)
	}
	if sink.lastArgs["src_tunnel_id"] != "t1" || sink.lastArgs["dst_tunnel_id"] != "t2" {
		t.Errorf("args: %+v", sink.lastArgs)
	}
}
