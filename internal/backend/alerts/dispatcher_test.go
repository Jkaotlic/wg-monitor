package alerts

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

func chk(name, status string, details map[string]any) wire.Check {
	return wire.Check{Name: name, Status: status, Details: details}
}

type fakeTG struct {
	mu               sync.Mutex
	sent             []sentMsg
	sentWithKeyboard []sentKBMsg
	welcomeSends     []welcomeSend
	topicCalls       []topicCall
	topicID          int64
	topicErr         error
	// sendErrOnce, when non-nil, is returned by the next Send*; it is then
	// cleared so the subsequent call succeeds. Lets tests simulate a
	// transient TG failure (e.g. "thread not found") with retry.
	sendErrOnce    error
	topicCallCount int
}

type sentMsg struct {
	chat    int64
	thread  *int64
	text    string
	replyTo *int64
}

type sentKBMsg struct {
	chatID   int64
	threadID *int64
	text     string
	replyTo  *int64
	keyboard *tg.InlineKeyboardMarkup
}

type welcomeSend struct {
	chatID   int64
	threadID *int64
	text     string
	markup   any
}

type topicCall struct {
	chatID int64
	name   string
}

func (f *fakeTG) SendMessage(_ context.Context, chatID int64, threadID *int64, text, _ string, replyTo *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{chatID, threadID, text, replyTo})
	if f.sendErrOnce != nil {
		err := f.sendErrOnce
		f.sendErrOnce = nil
		return 0, err
	}
	return int64(len(f.sent)) * 100, nil
}

func (f *fakeTG) SendMessageWithKeyboard(_ context.Context, chatID int64, threadID *int64, text, _ string, replyTo *int64, kb *tg.InlineKeyboardMarkup) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentWithKeyboard = append(f.sentWithKeyboard, sentKBMsg{chatID, threadID, text, replyTo, kb})
	if f.sendErrOnce != nil {
		err := f.sendErrOnce
		f.sendErrOnce = nil
		return 0, err
	}
	return int64(len(f.sentWithKeyboard) + 1000), nil
}

func (f *fakeTG) CreateForumTopic(_ context.Context, chatID int64, name string, _ int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.topicErr != nil {
		return 0, f.topicErr
	}
	f.topicCallCount++
	f.topicCalls = append(f.topicCalls, topicCall{chatID: chatID, name: name})
	if f.topicID == 0 {
		return 4242, nil
	}
	return f.topicID, nil
}

func (f *fakeTG) SendMessageWithReplyKeyboard(_ context.Context, chatID int64, threadID *int64, text, _ string, _ *int64, markup any) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.welcomeSends = append(f.welcomeSends, welcomeSend{chatID, threadID, text, markup})
	if f.sendErrOnce != nil {
		err := f.sendErrOnce
		f.sendErrOnce = nil
		return 0, err
	}
	return int64(len(f.welcomeSends) + 2000), nil
}

func newDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestDispatcherCreatesTopicLazily(t *testing.T) {
	d := newDB(t)
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	tg := &fakeTG{topicID: 7777}
	disp := NewDispatcher(d, tg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{
		Kind: state.Hard,
		Next: db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: ptrT(time.Now())},
	}
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, chk("awg_handshake", "fail", map[string]any{"error": "details"})); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(tg.sentWithKeyboard) != 1 {
		t.Fatalf("sentWithKeyboard %d messages", len(tg.sentWithKeyboard))
	}
	if tg.sentWithKeyboard[0].threadID == nil || *tg.sentWithKeyboard[0].threadID != 7777 {
		t.Fatalf("thread: %v", tg.sentWithKeyboard[0].threadID)
	}
	u, _ := d.Users().GetByNickname("vasya")
	if u.TelegramThreadID == nil || *u.TelegramThreadID != 7777 {
		t.Fatalf("thread id not persisted: %+v", u.TelegramThreadID)
	}
}

func TestDispatcherRecoveryRepliesToHardMessage(t *testing.T) {
	d := newDB(t)
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	d.Users().UpdateThreadID(uid, 4242)
	hardMsgID := int64(999)
	d.State().Save(uid, "awg_handshake", db.IncidentState{
		CurrentStatus: "hard", LastAlertMsgID: &hardMsgID, HardSince: ptrT(time.Now().Add(-7 * time.Minute)),
	})
	tg := &fakeTG{}
	disp := NewDispatcher(d, tg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{
		Kind: state.Recovery,
		Next: db.IncidentState{CurrentStatus: "ok"},
	}
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, chk("awg_handshake", "ok", nil)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(tg.sent) != 1 {
		t.Fatalf("sent: %d", len(tg.sent))
	}
	if tg.sent[0].replyTo == nil || *tg.sent[0].replyTo != 999 {
		t.Fatalf("replyTo: %v", tg.sent[0].replyTo)
	}
	if !strings.Contains(tg.sent[0].text, "снова в норме") && !strings.Contains(tg.sent[0].text, "снова на связи") && !strings.Contains(tg.sent[0].text, "снова работает") && !strings.Contains(tg.sent[0].text, "восстановился") && !strings.Contains(tg.sent[0].text, "снова отвечает") && !strings.Contains(tg.sent[0].text, "снова доступен") && !strings.Contains(tg.sent[0].text, "снова доступны") {
		t.Fatalf("text missing recovery headline: %s", tg.sent[0].text)
	}
	savedState, err := d.State().Get(uid, "awg_handshake")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if savedState.LastAlertMsgID != nil {
		t.Fatalf("LastAlertMsgID should be nil after recovery, got %d", *savedState.LastAlertMsgID)
	}
	if savedState.CurrentStatus != "ok" {
		t.Fatalf("status should be ok after recovery, got %s", savedState.CurrentStatus)
	}
}

func TestDispatcherSoftFlapNoTGButCounted(t *testing.T) {
	d := newDB(t)
	tok := "2222222222222222222222222222222222222222222222222222222222222222"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	tg := &fakeTG{}
	disp := NewDispatcher(d, tg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{Kind: state.SoftFlap, Next: db.IncidentState{CurrentStatus: "ok"}}
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, chk("awg_handshake", "fail", nil)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(tg.sent) != 0 {
		t.Fatalf("soft flap must not send tg")
	}
	today := time.Now().UTC().Format("2006-01-02")
	n, _ := d.State().GetSoftFlap(uid, "awg_handshake", today)
	if n != 1 {
		t.Fatalf("flap count: %d", n)
	}
}

func TestDispatcherHARDIncludesKeyboard(t *testing.T) {
	d := newDB(t)
	tok := "3333333333333333333333333333333333333333333333333333333333333333"
	uid, _ := d.Users().Insert("bob", tok, "2.2.2.2", "awg0")
	ftg := &fakeTG{topicID: 5555}
	disp := NewDispatcher(d, ftg, Config{ChatID: -200, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{
		Kind: state.Hard,
		Next: db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: ptrT(time.Now())},
	}
	if err := disp.Handle(context.Background(), uid, "bob", "awg_handshake", tr, chk("awg_handshake", "fail", map[string]any{"error": "timeout"})); err != nil {
		t.Fatalf("handle: %v", err)
	}

	// Must use SendMessageWithKeyboard, not SendMessage
	if len(ftg.sentWithKeyboard) != 1 {
		t.Fatalf("expected 1 keyboard message, got %d", len(ftg.sentWithKeyboard))
	}
	if len(ftg.sent) != 0 {
		t.Fatalf("plain SendMessage should not be called for HARD, got %d", len(ftg.sent))
	}

	kb := ftg.sentWithKeyboard[0].keyboard
	if kb == nil {
		t.Fatal("keyboard is nil")
	}
	// Spec §6.2: 2 rows, 4 buttons in row 1, 2 buttons in row 2 = 6 total
	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 keyboard rows, got %d", len(kb.InlineKeyboard))
	}
	totalButtons := 0
	for _, row := range kb.InlineKeyboard {
		totalButtons += len(row)
	}
	if totalButtons != 6 {
		t.Fatalf("expected 6 buttons total, got %d", totalButtons)
	}

	// Verify callback_data contains userID and checkName
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if !strings.Contains(btn.CallbackData, "awg_handshake") {
				t.Errorf("button %q callback_data missing checkName: %s", btn.Text, btn.CallbackData)
			}
		}
	}
}

func TestDispatcherHardUsesRouterTelegramChatID(t *testing.T) {
	d := newDB(t)
	uid, err := d.Users().Insert("tenant", "tok", "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateTelegramTopic(uid, -200, 555); err != nil {
		t.Fatal(err)
	}
	ftg := &fakeTG{}
	disp := NewDispatcher(d, ftg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{
		Kind: state.Hard,
		Next: db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: ptrT(time.Now())},
	}
	if err := disp.Handle(context.Background(), uid, "tenant", "awg_handshake", tr, chk("awg_handshake", "fail", nil)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	if len(ftg.sentWithKeyboard) != 1 {
		t.Fatalf("sentWithKeyboard=%d, want 1", len(ftg.sentWithKeyboard))
	}
	if ftg.sentWithKeyboard[0].chatID != -200 {
		t.Fatalf("chatID=%d, want -200", ftg.sentWithKeyboard[0].chatID)
	}
	if tid := ftg.sentWithKeyboard[0].threadID; tid == nil || *tid != 555 {
		t.Fatalf("threadID=%v, want 555", tid)
	}
}

func TestEnsureTopicCreatesInRouterTelegramChatID(t *testing.T) {
	d := newDB(t)
	uid, err := d.Users().Insert("tenant", "tok", "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateTelegramTopic(uid, -200, 0); err != nil {
		t.Fatal(err)
	}
	ftg := &fakeTG{topicID: 9999}
	disp := NewDispatcher(d, ftg, Config{ChatID: -100})
	disp.WelcomeKeyboard = func() any { return "stub-kb" }

	ref, err := disp.ensureTopic(context.Background(), uid, "tenant")
	if err != nil {
		t.Fatalf("ensureTopic: %v", err)
	}
	if ref.ChatID != -200 || ref.ThreadID != 9999 {
		t.Fatalf("ref=%+v, want chat=-200 thread=9999", ref)
	}
	if len(ftg.topicCalls) != 1 || ftg.topicCalls[0].chatID != -200 {
		t.Fatalf("topic calls=%+v, want one call to -200", ftg.topicCalls)
	}
	if len(ftg.welcomeSends) != 1 || ftg.welcomeSends[0].chatID != -200 {
		t.Fatalf("welcome sends=%+v, want chat -200", ftg.welcomeSends)
	}
	got, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.TelegramChatID == nil || *got.TelegramChatID != -200 || got.TelegramThreadID == nil || *got.TelegramThreadID != 9999 {
		t.Fatalf("topic binding not persisted: %+v", got)
	}
}

func TestDispatcherRecoveryZeroesAcked(t *testing.T) {
	d := newDB(t)
	tok := "4444444444444444444444444444444444444444444444444444444444444444"
	uid, _ := d.Users().Insert("carol", tok, "3.3.3.3", "awg0")
	d.Users().UpdateThreadID(uid, 4242)

	hardMsgID := int64(123)
	hardSince := time.Now().Add(-5 * time.Minute)
	d.State().Save(uid, "awg_handshake", db.IncidentState{
		CurrentStatus:  "hard",
		Acked:          true,
		HardSince:      &hardSince,
		LastAlertMsgID: &hardMsgID,
	})

	ftg := &fakeTG{}
	disp := NewDispatcher(d, ftg, Config{ChatID: -300, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{
		Kind: state.Recovery,
		Next: db.IncidentState{CurrentStatus: "ok"},
	}
	if err := disp.Handle(context.Background(), uid, "carol", "awg_handshake", tr, chk("awg_handshake", "ok", nil)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	saved, err := d.State().Get(uid, "awg_handshake")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if saved.Acked {
		t.Fatal("Acked should be false after Recovery, but got true")
	}
	if saved.LastAlertMsgID != nil {
		t.Fatalf("LastAlertMsgID should be nil after Recovery, got %d", *saved.LastAlertMsgID)
	}
}

func ptrT(t time.Time) *time.Time { return &t }

func keyboardHasCallback(kb *tg.InlineKeyboardMarkup, callback string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == callback {
				return true
			}
		}
	}
	return false
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestSendOffline_HappyPath: heartbeat watcher invokes SendOffline; topic
// must resolve via ensureTopic and a keyboard message fires with the OFFLINE
// text shape plus silence controls.
func TestSendOffline_HappyPath(t *testing.T) {
	d := newDB(t)
	tok := "5555555555555555555555555555555555555555555555555555555555555555"
	uid, _ := d.Users().Insert("dora", tok, "1.1.1.1", "awg0")
	d.Users().UpdateThreadID(uid, 8888)
	ftg := &fakeTG{}
	disp := NewDispatcher(d, ftg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})
	if err := disp.SendOffline(context.Background(), uid, "dora", 12*time.Minute); err != nil {
		t.Fatalf("SendOffline: %v", err)
	}
	if len(ftg.sentWithKeyboard) != 1 {
		t.Fatalf("expected 1 keyboard message, got %d", len(ftg.sentWithKeyboard))
	}
	if len(ftg.sent) != 0 {
		t.Fatalf("plain SendMessage should not be called for offline, got %d", len(ftg.sent))
	}
	if !strings.Contains(ftg.sentWithKeyboard[0].text, "Роутер не на связи") {
		t.Fatalf("text missing offline headline: %q", ftg.sentWithKeyboard[0].text)
	}
	if ftg.sentWithKeyboard[0].threadID == nil || *ftg.sentWithKeyboard[0].threadID != 8888 {
		t.Fatalf("thread mismatch: %v", ftg.sentWithKeyboard[0].threadID)
	}
	kb := ftg.sentWithKeyboard[0].keyboard
	if kb == nil {
		t.Fatal("offline keyboard is nil")
	}
	if !keyboardHasCallback(kb, "silence:"+itoa(uid)+":agent_heartbeat:24h") ||
		!keyboardHasCallback(kb, "mute:"+itoa(uid)+":agent_heartbeat") {
		t.Fatalf("offline keyboard missing silence/mute controls: %#v", kb)
	}
}

// TestSendOffline_TopicCreateFailure: ensureTopic surfaces fakeTG.topicErr;
// SendOffline must propagate, not swallow.
func TestSendOffline_TopicCreateFailure(t *testing.T) {
	d := newDB(t)
	tok := "6666666666666666666666666666666666666666666666666666666666666666"
	uid, _ := d.Users().Insert("eve", tok, "1.1.1.1", "awg0")
	// no UpdateThreadID — ensureTopic will hit CreateForumTopic
	ftg := &fakeTG{topicErr: errStub("rate limited")}
	disp := NewDispatcher(d, ftg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})
	if err := disp.SendOffline(context.Background(), uid, "eve", time.Hour); err == nil {
		t.Fatalf("expected error from SendOffline when topic create fails")
	}
	if len(ftg.sent) != 0 {
		t.Fatalf("no message should have been sent on topic-create-failure, got %d", len(ftg.sent))
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

// TestDispatcherSelfHealsStaleTopic: when the cached telegram_thread_id
// points at a topic that no longer exists in TG (operator deleted it),
// the first SendMessage* returns a 400 "message thread not found" and the
// dispatcher must clear the id, recreate the topic, and resend once
// against the fresh id. Covers scenario "тема была и пропала".
func TestDispatcherSelfHealsStaleTopic(t *testing.T) {
	d := newDB(t)
	tok := "7777777777777777777777777777777777777777777777777777777777777777"
	uid, _ := d.Users().Insert("frank", tok, "1.1.1.1", "awg0")
	const staleID, freshID = int64(1111), int64(9999)
	if err := d.Users().UpdateThreadID(uid, staleID); err != nil {
		t.Fatal(err)
	}
	ftg := &fakeTG{
		topicID:     freshID,
		sendErrOnce: &tg.APIError{Method: "sendMessage", Description: "Bad Request: message thread not found", Code: 400},
	}
	disp := NewDispatcher(d, ftg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{
		Kind: state.Hard,
		Next: db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: ptrT(time.Now())},
	}
	if err := disp.Handle(context.Background(), uid, "frank", "awg_handshake", tr, chk("awg_handshake", "fail", map[string]any{"error": "x"})); err != nil {
		t.Fatalf("handle: %v", err)
	}

	if got := len(ftg.sentWithKeyboard); got != 2 {
		t.Fatalf("expected 2 send attempts (stale + retry), got %d", got)
	}
	if tid := ftg.sentWithKeyboard[0].threadID; tid == nil || *tid != staleID {
		t.Fatalf("first send threadID: want %d, got %v", staleID, tid)
	}
	if tid := ftg.sentWithKeyboard[1].threadID; tid == nil || *tid != freshID {
		t.Fatalf("retry threadID: want %d, got %v", freshID, tid)
	}
	u, _ := d.Users().GetByNickname("frank")
	if u.TelegramThreadID == nil || *u.TelegramThreadID != freshID {
		t.Fatalf("DB thread_id after heal: want %d, got %v", freshID, u.TelegramThreadID)
	}
	if ftg.topicCallCount != 1 {
		t.Fatalf("CreateForumTopic call count: want 1 (only the heal), got %d", ftg.topicCallCount)
	}
}

// TestDispatcherSurfacesNonHealableTGError: a TG error that is NOT a
// stale-topic signal (e.g. 403 forbidden, 429 rate-limit, malformed) must
// propagate untouched — no retry, no thread_id reset.
func TestDispatcherSurfacesNonHealableTGError(t *testing.T) {
	d := newDB(t)
	tok := "8888888888888888888888888888888888888888888888888888888888888888"
	uid, _ := d.Users().Insert("grace", tok, "1.1.1.1", "awg0")
	const stableID = int64(5555)
	_ = d.Users().UpdateThreadID(uid, stableID)
	ftg := &fakeTG{
		sendErrOnce: &tg.APIError{Method: "sendMessage", Description: "Forbidden: bot was kicked", Code: 403},
	}
	disp := NewDispatcher(d, ftg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})
	tr := state.Transition{
		Kind: state.Hard,
		Next: db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: ptrT(time.Now())},
	}
	err := disp.Handle(context.Background(), uid, "grace", "awg_handshake", tr, chk("awg_handshake", "fail", nil))
	if err == nil {
		t.Fatal("expected error to surface, got nil")
	}
	if len(ftg.sentWithKeyboard) != 1 {
		t.Fatalf("expected 1 send attempt (no retry on non-stale err), got %d", len(ftg.sentWithKeyboard))
	}
	u, _ := d.Users().GetByNickname("grace")
	if u.TelegramThreadID == nil || *u.TelegramThreadID != stableID {
		t.Fatalf("thread_id must be untouched on non-stale err: got %v", u.TelegramThreadID)
	}
}

// TestBuildNeighborSummaries: shared helper used by both dispatcher and
// realert; covers the four interesting paths — empty, exclusion of self,
// ping_check_status override, malformed JSON tolerated (LOGIC-08, TEST-04).
func TestBuildNeighborSummaries(t *testing.T) {
	rows := []db.EventRow{
		{CheckName: "tunnel_self", Status: "fail", DetailsJSON: `{"tunnel_name":"self"}`},
		{CheckName: "tunnel_a", Status: "ok", DetailsJSON: `{"tunnel_name":"alpha","interface":"nwg0","handshake_age_sec":42}`},
		{CheckName: "tunnel_b", Status: "fail", DetailsJSON: `{"tunnel_name":"beta","ping_check_status":"dead"}`},
		{CheckName: "tunnel_c", Status: "ok", DetailsJSON: `not-json`},
	}
	out := BuildNeighborSummaries(rows, "tunnel_self")
	if len(out) != 3 {
		t.Fatalf("expected 3 entries (self excluded), got %d", len(out))
	}
	got := map[string]NeighborSummary{}
	for _, ns := range out {
		got[ns.CheckName] = ns
	}
	if got["tunnel_a"].TunnelName != "alpha" || got["tunnel_a"].HandshakeAge != 42 {
		t.Errorf("alpha row: %+v", got["tunnel_a"])
	}
	// ping_check_status override: stored Status was "fail", details promoted to "dead".
	if got["tunnel_b"].Status != "dead" {
		t.Errorf("beta status override: %+v", got["tunnel_b"])
	}
	// malformed JSON tolerated: stored Status preserved.
	if got["tunnel_c"].Status != "ok" {
		t.Errorf("tunnel_c with bad JSON should keep stored Status: %+v", got["tunnel_c"])
	}
}

// TestBuildNeighborSummaries_Empty: nil input → nil output (no allocation).
func TestBuildNeighborSummaries_Empty(t *testing.T) {
	if got := BuildNeighborSummaries(nil, ""); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestCollectNeighbors_OmitsStaleTunnelRows(t *testing.T) {
	d := newDB(t)
	uid, err := d.Users().Insert("vasya", "tok", "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := d.Events().Insert(uid, "tunnel_fresh", "ok", `{"tunnel_name":"fresh","interface":"nwg0"}`, now); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "tunnel_old", "ok", `{"tunnel_name":"old","interface":"nwg1"}`, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}

	disp := NewDispatcher(d, &fakeTG{}, Config{})
	got := disp.collectNeighbors(uid, "tunnel_failed")
	if len(got) != 1 {
		t.Fatalf("want only fresh neighbor, got %+v", got)
	}
	if got[0].CheckName != "tunnel_fresh" {
		t.Fatalf("stale neighbor leaked into alert context: %+v", got)
	}
}

func TestEnsureTopic_SendsWelcomeOnFreshCreate(t *testing.T) {
	d := newDB(t)
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, err := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	ftg := &fakeTG{topicID: 9999}
	disp := NewDispatcher(d, ftg, Config{ChatID: -100})
	disp.WelcomeKeyboard = func() any { return "stub-kb" }

	ref, err := disp.ensureTopic(context.Background(), uid, "vasya")
	if err != nil {
		t.Fatalf("ensureTopic fresh: %v", err)
	}
	if ref.ChatID != -100 || ref.ThreadID != 9999 {
		t.Errorf("want chat -100 tid 9999, got %+v", ref)
	}
	if len(ftg.welcomeSends) != 1 {
		t.Fatalf("want 1 welcome send on fresh create, got %d", len(ftg.welcomeSends))
	}
	if !strings.Contains(ftg.welcomeSends[0].text, "vasya") {
		t.Errorf("welcome text missing nickname: %s", ftg.welcomeSends[0].text)
	}
	if ftg.welcomeSends[0].markup != "stub-kb" {
		t.Errorf("welcome markup not propagated: %v", ftg.welcomeSends[0].markup)
	}

	// Second call — thread already exists, no new welcome.
	if _, err := disp.ensureTopic(context.Background(), uid, "vasya"); err != nil {
		t.Fatalf("ensureTopic no-op: %v", err)
	}
	if len(ftg.welcomeSends) != 1 {
		t.Errorf("want still 1 welcome (no-op), got %d", len(ftg.welcomeSends))
	}
}

func TestEnsureTopic_NoWelcomeWhenKeyboardNil(t *testing.T) {
	d := newDB(t)
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	ftg := &fakeTG{topicID: 9999}
	disp := NewDispatcher(d, ftg, Config{ChatID: -100})
	// disp.WelcomeKeyboard intentionally nil.
	if _, err := disp.ensureTopic(context.Background(), uid, "vasya"); err != nil {
		t.Fatal(err)
	}
	if len(ftg.welcomeSends) != 0 {
		t.Errorf("want 0 welcome (nil keyboard), got %d", len(ftg.welcomeSends))
	}
}
