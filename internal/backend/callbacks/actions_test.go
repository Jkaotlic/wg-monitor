package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
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
	if !strings.HasPrefix(statusLine, "⏸ Silenced") {
		t.Errorf("status line should start with '⏸ Silenced', got: %q", statusLine)
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

func TestActionAckSetsAcked(t *testing.T) {
	d, uid := newTestDB(t)
	a := NewAckAction(d)
	statusLine, err := a.Apply(context.Background(), nil, Args{
		Action: "ack", UserID: uid, CheckName: "awg_handshake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statusLine, "✅ Ack'ed") {
		t.Errorf("status line should start with '✅ Ack'ed', got: %q", statusLine)
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
