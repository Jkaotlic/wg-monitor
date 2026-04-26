package alerts

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
)

type fakeTG struct {
	mu       sync.Mutex
	sent     []sentMsg
	topicID  int64
	topicErr error
}

type sentMsg struct {
	chat     int64
	thread   *int64
	text     string
	replyTo  *int64
}

func (f *fakeTG) SendMessage(_ context.Context, chatID int64, threadID *int64, text, _ string, replyTo *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{chatID, threadID, text, replyTo})
	return int64(len(f.sent)) * 100, nil
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
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, "details"); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(tg.sent) != 1 {
		t.Fatalf("sent %d messages", len(tg.sent))
	}
	if tg.sent[0].thread == nil || *tg.sent[0].thread != 7777 {
		t.Fatalf("thread: %v", tg.sent[0].thread)
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
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, ""); err != nil {
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
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, ""); err != nil {
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

func ptrT(t time.Time) *time.Time { return &t }
