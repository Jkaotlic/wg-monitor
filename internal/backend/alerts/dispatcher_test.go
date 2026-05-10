package alerts

import (
	"context"
	"path/filepath"
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
	mu              sync.Mutex
	sent            []sentMsg
	sentWithKeyboard []sentKBMsg
	topicID         int64
	topicErr        error
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

func (f *fakeTG) SendMessage(_ context.Context, chatID int64, threadID *int64, text, _ string, replyTo *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{chatID, threadID, text, replyTo})
	return int64(len(f.sent)) * 100, nil
}

func (f *fakeTG) SendMessageWithKeyboard(_ context.Context, chatID int64, threadID *int64, text, _ string, replyTo *int64, kb *tg.InlineKeyboardMarkup) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentWithKeyboard = append(f.sentWithKeyboard, sentKBMsg{chatID, threadID, text, replyTo, kb})
	return int64(len(f.sentWithKeyboard)+1000), nil
}

func (f *fakeTG) CreateForumTopic(_ context.Context, _ int64, _ string, _ int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.topicErr != nil {
		return 0, f.topicErr
	}
	if f.topicID == 0 {
		return 4242, nil
	}
	return f.topicID, nil
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
	if !strings.Contains(tg.sent[0].text, "RECOVERED") {
		t.Fatalf("text: %s", tg.sent[0].text)
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

// TestSendOffline_HappyPath: heartbeat watcher invokes SendOffline; topic
// must resolve via ensureTopic and a non-keyboard SendMessage fires with
// the OFFLINE text shape (TEST-04).
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
	if len(ftg.sent) != 1 {
		t.Fatalf("expected 1 plain SendMessage, got %d", len(ftg.sent))
	}
	if !strings.Contains(ftg.sent[0].text, "ROUTER OFFLINE") {
		t.Fatalf("text missing ROUTER OFFLINE: %q", ftg.sent[0].text)
	}
	if ftg.sent[0].thread == nil || *ftg.sent[0].thread != 8888 {
		t.Fatalf("thread mismatch: %v", ftg.sent[0].thread)
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
