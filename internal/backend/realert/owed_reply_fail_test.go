package realert

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// spySink -- рассылка напоминаний, которая только записывает вызовы.
type spySink struct {
	mu      sync.Mutex
	replies []string
	err     error
	sendErr error
}

func (s *spySink) SendKeyboard(context.Context, int64, string, string, *tg.InlineKeyboardMarkup) (int, error) {
	return 1, s.sendErr
}

func (s *spySink) ReplyToEach(_ context.Context, _ int64, checkName, text, _ string, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, checkName+": "+text)
	return s.err
}

func TestOwedReplyAmbiguousDeliveryIsConsumed(t *testing.T) {
	d, uid := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := d.State().Save(uid, "dns", db.IncidentState{
		UserID: uid, CheckName: "dns", CurrentStatus: "ok", ConsecutiveOKs: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.OwedReplies().Put(uid, "dns", "🟢 восстановилось", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	sink := &spySink{err: fmt.Errorf("%w: %w", notify.ErrNoneDelivered, context.DeadlineExceeded)}
	p := NewPoller(d, &fakeTG{}, Config{})
	p.SetNotifySink(sink)
	p.SetNow(func() time.Time { return now })
	p.tick(context.Background())

	owed, err := d.OwedReplies().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(owed) != 0 {
		t.Fatalf("ambiguous owed reply remains queued: %+v", owed)
	}
}

func TestOwedReplyAmbiguousRateLimitStillPausesBatch(t *testing.T) {
	d, uid := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := d.State().Save(uid, "dns", db.IncidentState{
		UserID: uid, CheckName: "dns", CurrentStatus: "ok", ConsecutiveOKs: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.OwedReplies().Put(uid, "dns", "восстановилось", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	rateErr := &tg.APIError{Method: "sendMessage", Code: 429, RetryAfter: 30 * time.Second}
	sink := &spySink{err: fmt.Errorf("%w: %w", notify.ErrNoneDelivered, errors.Join(context.DeadlineExceeded, fmt.Errorf("%w: %w", tg.ErrMaybeDelivered, rateErr)))}
	p := NewPoller(d, &fakeTG{}, Config{})
	p.SetNotifySink(sink)
	p.SetNow(func() time.Time { return now })
	p.tick(context.Background())
	if !p.rateLimitActive(now.Add(10 * time.Second)) {
		t.Fatal("mixed ambiguous+429 result did not activate rate-limit pause")
	}
}

func (s *spySink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.replies)
}

// FRESH-03: долг «восстановилось» повторяется, только пока проверка в
// порядке. Снова падает (fail, ещё не hard) -- «восстановилось» соврало бы
// человеку, у которого роутер опять ломается; долг снимается без отправки.
func TestOwedReplyByStatus(t *testing.T) {
	for _, c := range []struct {
		status    string
		wantSent  int
		wantOwed  int
		fails     int
		consecOKs int
	}{
		{status: "fail", wantSent: 0, wantOwed: 0, fails: 1},
		{status: "ok", wantSent: 1, wantOwed: 0, consecOKs: 2},
	} {
		t.Run(c.status, func(t *testing.T) {
			d, uid := newTestDB(t)
			now := time.Now().UTC().Truncate(time.Second)
			if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
				UserID: uid, CheckName: "awg_handshake", CurrentStatus: c.status,
				ConsecutiveFails: c.fails, ConsecutiveOKs: c.consecOKs,
			}); err != nil {
				t.Fatal(err)
			}
			if err := d.OwedReplies().Put(uid, "awg_handshake", "🟢 восстановилось", now.Add(-10*time.Minute)); err != nil {
				t.Fatal(err)
			}
			spy := &spySink{}
			p := NewPoller(d, &fakeTG{}, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
			p.SetNotifySink(spy)
			p.SetNow(func() time.Time { return now })
			p.tick(context.Background())
			if n := spy.count(); n != c.wantSent {
				t.Fatalf("status=%s: ReplyToEach вызван %d раз, ждали %d: %v", c.status, n, c.wantSent, spy.replies)
			}
			owed, err := d.OwedReplies().List()
			if err != nil {
				t.Fatal(err)
			}
			if len(owed) != c.wantOwed {
				t.Fatalf("status=%s: долгов %d, ждали %d", c.status, len(owed), c.wantOwed)
			}
		})
	}
}
