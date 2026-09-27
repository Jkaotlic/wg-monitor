package revive

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type fakeAdminNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (a *fakeAdminNotifier) SendAdmin(_ context.Context, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, text)
	return nil
}

func (a *fakeAdminNotifier) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

func (e *testEnv) seedWaitingBy(t *testing.T, requestedBy int64) {
	t.Helper()
	box, err := NewBox(e.key)
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := box.Seal(e.router, fixtureSecrets())
	if err != nil {
		t.Fatal(err)
	}
	now := e.clock.Now()
	if err := e.db.Revive().Put(db.ReviveIntent{
		RouterID: e.router, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour), RequestedBy: requestedBy,
	}, nonce, ct); err != nil {
		t.Fatal(err)
	}
}

// REV-02: отказ авто-оживления -- дело админа: владелец его не ставил и
// экрана «Парк» не видит. И раз в сутки одно и то же -- шум: одна весть на
// серию одинаковых причин.
func TestAutoFailure_OnlyAdminAndOncePerSameReason(t *testing.T) {
	env := newEnv(t)
	admin := &fakeAdminNotifier{}
	env.svc.cfg.AdminNotifier = admin
	env.clearAWGMURL(t) // отказ: у роутера нет адреса панели

	for day := 0; day < 3; day++ {
		env.seedWaitingBy(t, RequestedBySystem)
		env.tick(t)
		if in := env.intent(t); in.Status != StatusFailed {
			t.Fatalf("день %d: %+v", day, in)
		}
		env.clock.Advance(AutoRetryAfter)
	}
	if n := env.notifier.all(); len(n) != 0 {
		t.Fatalf("владельцу ушёл отказ авто-оживления: %+v", n)
	}
	got := admin.all()
	if len(got) != 1 || !strings.Contains(got[0], "«bronya»") {
		t.Fatalf("админу: %q, хотим одно сообщение на серию", got)
	}

	// Ручное оживление админа -- прежним путём, всем получателям роутера.
	env.seedWaitingBy(t, 42)
	env.tick(t)
	if n := env.notifier.all(); len(n) != 1 {
		t.Fatalf("ручной отказ: %+v", n)
	}
	// И серия после ручного начинается заново.
	env.seedWaitingBy(t, RequestedBySystem)
	env.tick(t)
	if got := admin.all(); len(got) != 2 {
		t.Fatalf("после ручного -- новая серия: %q", got)
	}
}

// Без канала админа отказ авто-оживления владельцу всё равно не уходит.
func TestAutoFailure_NoAdminChannelStaysSilent(t *testing.T) {
	env := newEnv(t)
	env.clearAWGMURL(t)
	env.seedWaitingBy(t, RequestedBySystem)
	env.tick(t)
	if n := env.notifier.all(); len(n) != 0 {
		t.Fatalf("владельцу ушёл отказ авто-оживления: %+v", n)
	}
}
