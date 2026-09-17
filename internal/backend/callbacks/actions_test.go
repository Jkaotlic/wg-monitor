package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	wire1 "github.com/Jkaotlic/wg-monitor/pkg/wire"
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
	userID  int64
	cmdID   string
	action  string
	check   string
	ndms    string
	tunnel  string
	backend string
	force   bool
	args    map[string]any
}

type enqueueRefCall struct {
	userID    int64
	cmdID     string
	chatID    int64
	messageID int64
}

func (f *fakeEnqueuer) Enqueue(userID int64, cmd wire1.Command) error {
	if f.err != nil {
		return f.err
	}
	check, _ := cmd.Args["check_name"].(string)
	ndms, _ := cmd.Args["ndms_name"].(string)
	tunnel, _ := cmd.Args["tunnel_id"].(string)
	backend, _ := cmd.Args["backend"].(string)
	force, _ := cmd.Args["force_legacy_cleanup"].(bool)
	f.calls = append(f.calls, enqueueCall{
		userID: userID, cmdID: cmd.ID, action: cmd.Action, check: check, ndms: ndms, tunnel: tunnel, backend: backend, force: force,
		args: cmd.Args,
	})
	return nil
}

func (f *fakeEnqueuer) EnqueueWithRef(userID int64, cmd wire1.Command, ref cmdpkg.MessageRef) error {
	if err := f.Enqueue(userID, cmd); err != nil {
		return err
	}
	f.refs = append(f.refs, enqueueRefCall{
		userID: userID, cmdID: cmd.ID, chatID: ref.ChatID, messageID: ref.MessageID,
	})
	return nil
}

func TestCommandAction_RouterDoctorEnqueues(t *testing.T) {
	sink := &fakeEnqueuer{}
	a := NewCommandAction(sink, func() string { return "fixed-id-1" })
	statusLine, err := a.Apply(context.Background(), nil, Args{
		Action: "router_doctor", UserID: 7, CheckName: "tunnel_amnezia_for_awg2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.calls))
	}
	c := sink.calls[0]
	if c.userID != 7 || c.cmdID != "fixed-id-1" || c.action != "router_doctor" || c.check != "tunnel_amnezia_for_awg2" {
		t.Errorf("got %+v", c)
	}
	// Строка дописывается под тревогой в личке владельца: «Отправлено роутеру: …».
	if !strings.Contains(statusLine, "Проверка") || !strings.Contains(statusLine, "Отправлено роутеру") {
		t.Errorf("unexpected status line: %q", statusLine)
	}
}

// v0.30 задача 2: диагностика всегда гоняет полную проверку заново
// (/api/diagnostics/stream?restart=false) без IncludeRestart, так что ни
// один VPN-туннель не перезапускается. Владелец, нажавший кнопку под
// тревогой, должен узнать, что проверка идёт заново и связь при этом не
// моргнёт — а не читать обещание перезапуска, которого больше не будет.
func TestCommandAction_DiagSaysFreshWithoutRestart(t *testing.T) {
	sink := &fakeEnqueuer{}
	a := NewCommandAction(sink, func() string { return "id-diag" })
	s, err := a.Apply(context.Background(), nil, Args{Action: "diag_now", UserID: 1, CheckName: "tunnel_awg10"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "Диагностика") {
		t.Fatalf("строка не называет действие диагностикой: %q", s)
	}
	if !strings.Contains(s, "заново") || !strings.Contains(s, "не перезапускаются") {
		t.Fatalf("строка должна сказать «заново» и «не перезапускаются»: %q", s)
	}
	if strings.Contains(s, "перезапустится") {
		t.Fatalf("строка не должна обещать перезапуск: %q", s)
	}
}

func TestCommandAction_CommandActions(t *testing.T) {
	cases := []struct {
		action  string
		wantSub string
	}{
		{"diag_now", "Диагностика"},
		{"pingcheck_now", "Тест связи"},
		{"force_recheck", "Запрос отчёта"},
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

func TestActionHistoryAnswersWhereTapped(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateTelegramTopic(uid, -200, 555); err != nil {
		t.Fatal(err)
	}
	const tapper = int64(8001)

	f := &fakeTGForHistory{}
	a := NewHistoryAction(d, f, -100)
	// Нажали в личке -- ответ обязан прийти туда же, а не в тему роутера.
	q := &tg.CallbackQuery{
		ID:      "cbk-history-dm",
		From:    tg.User{ID: tapper},
		Message: tg.Message{MessageID: 9, Chat: tg.Chat{ID: tapper}},
	}
	if _, err := a.Apply(context.Background(), q, Args{Action: "history", UserID: uid, CheckName: "awg_handshake"}); err != nil {
		t.Fatal(err)
	}
	if len(f.chats) != 1 || f.chats[0] != tapper {
		t.Fatalf("ответ ушёл в %v, ждали личку нажавшего %d", f.chats, tapper)
	}
	if f.threads[0] != nil {
		t.Fatalf("в личку отвечают без темы, получили %v", *f.threads[0])
	}
}

// Minimal mock for History tests (no SendMessageWithKeyboard needed)
type fakeTGForHistory struct {
	onSend  func(text string)
	chats   []int64
	threads []*int64
}

func (f *fakeTGForHistory) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	if f.onSend != nil {
		f.onSend(text)
	}
	f.chats = append(f.chats, chatID)
	f.threads = append(f.threads, threadID)
	return 1, nil
}

// Ensure db import is used (newTestDB already uses it, but keep explicit).
var _ *db.DB
