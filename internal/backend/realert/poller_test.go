package realert

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type fakeTG struct {
	mu      sync.Mutex
	sent    []string
	sendErr error
}

func (f *fakeTG) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return 99, f.sendErr
}

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	tmp := t.TempDir() + "/test.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	uid, err := d.Users().Insert("vasya", "rawtoken", "1.1.1.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	return d, uid
}

func TestTickEmptyNoCalls(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.tick(context.Background())
	if len(f.sent) != 0 {
		t.Errorf("expected 0 sends, got %d", len(f.sent))
	}
}

func TestTickStaleHardSendsRealert(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

	hardSince := time.Now().Add(-7 * time.Hour)
	lastAlert := time.Now().Add(-7 * time.Hour)
	err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		ConsecutiveFails: 3, HardSince: &hardSince, LastAlertAt: &lastAlert,
	})
	if err != nil {
		t.Fatal(err)
	}

	p.tick(context.Background())

	if len(f.sent) != 1 {
		t.Fatalf("expected 1 realert, got %d", len(f.sent))
	}
	if !strings.Contains(f.sent[0], "STILL DOWN") {
		t.Errorf("missing 'STILL DOWN' in: %q", f.sent[0])
	}
	if !strings.Contains(f.sent[0], "Re-alert #1") {
		t.Errorf("expected 'Re-alert #1' (7h since), got: %q", f.sent[0])
	}

	st, _ := d.State().Get(uid, "awg_handshake")
	if st.LastAlertAt == nil || time.Since(*st.LastAlertAt) > time.Minute {
		t.Errorf("LastAlertAt should be updated to recent, got %v", st.LastAlertAt)
	}
}

func TestTickSilencedSkipped(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

	hardSince := time.Now().Add(-7 * time.Hour)
	lastAlert := time.Now().Add(-7 * time.Hour)
	silenced := time.Now().Add(2 * time.Hour)
	err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		HardSince: &hardSince, LastAlertAt: &lastAlert, SilencedUntil: &silenced,
	})
	if err != nil {
		t.Fatal(err)
	}

	p.tick(context.Background())
	if len(f.sent) != 0 {
		t.Errorf("silenced HARD should not realert, got %d sends", len(f.sent))
	}
}

func TestTickAckedSkipped(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

	hardSince := time.Now().Add(-7 * time.Hour)
	lastAlert := time.Now().Add(-7 * time.Hour)
	err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		HardSince: &hardSince, LastAlertAt: &lastAlert, Acked: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	p.tick(context.Background())
	if len(f.sent) != 0 {
		t.Errorf("acked HARD should not realert, got %d sends", len(f.sent))
	}
}

func TestTickSendErrorPreservesLastAlertAt(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{sendErr: errors.New("tg flap")}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

	hardSince := time.Now().Add(-7 * time.Hour)
	origLastAlert := time.Now().Add(-7 * time.Hour)
	err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		HardSince: &hardSince, LastAlertAt: &origLastAlert,
	})
	if err != nil {
		t.Fatal(err)
	}

	p.tick(context.Background())

	st, _ := d.State().Get(uid, "awg_handshake")
	if st.LastAlertAt == nil {
		t.Fatal("LastAlertAt nil")
	}
	if time.Since(*st.LastAlertAt) < 6*time.Hour {
		t.Errorf("LastAlertAt should NOT have advanced after send error, but it did: %v", st.LastAlertAt)
	}
}
