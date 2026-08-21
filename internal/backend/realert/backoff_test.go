// internal/backend/realert/backoff_test.go
package realert

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// hardSince/lastAlert в прошлом относительно фиксированного «сейчас».
func stageIncident(t *testing.T, d *db.DB, uid int64, now time.Time, age, sinceLastAlert time.Duration) {
	t.Helper()
	hardSince := now.Add(-age)
	lastAlert := now.Add(-sinceLastAlert)
	if err := d.State().Save(uid, "agent_heartbeat", db.IncidentState{
		UserID: uid, CheckName: "agent_heartbeat", CurrentStatus: "hard",
		ConsecutiveFails: 3, HardSince: &hardSince, LastAlertAt: &lastAlert,
	}); err != nil {
		t.Fatal(err)
	}
}

func pollerAt(d *db.DB, f *fakeTG, now time.Time) *Poller {
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: time.Hour, TickEvery: time.Second})
	p.SetNow(func() time.Time { return now })
	return p
}

// Роутер, лежащий 19 дней, присылал напоминание каждый час -- четыре с лишним
// сотни сообщений в один топик. Это не мониторинг, а фон, который перестают
// читать; и когда рядом ляжет второй роутер, его сообщение утонет в этом фоне.
// Первые сутки частота прежняя: пока инцидент свежий, напоминание -- это шанс
// на быструю починку.
func TestRealertKeepsBaseCadenceOnFirstDay(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	stageIncident(t, d, uid, now, 5*time.Hour, 70*time.Minute)

	pollerAt(d, f, now).tick(context.Background())

	if f.count() != 1 {
		t.Fatalf("на первых сутках ждали напоминание, отправлено %d", f.count())
	}
}

func TestRealertSlowsDownAfterFirstDay(t *testing.T) {
	d, uid := newTestDB(t)
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	f := &fakeTG{}
	stageIncident(t, d, uid, now, 30*time.Hour, 2*time.Hour)
	pollerAt(d, f, now).tick(context.Background())
	if f.count() != 0 {
		t.Fatalf("сутки спустя час молчания -- ещё не повод писать, отправлено %d", f.count())
	}

	f2 := &fakeTG{}
	stageIncident(t, d, uid, now, 30*time.Hour, 7*time.Hour)
	pollerAt(d, f2, now).tick(context.Background())
	if f2.count() != 1 {
		t.Fatalf("через шесть часов напоминание должно уйти, отправлено %d", f2.count())
	}
	if !strings.Contains(f2.sent[0], "напомню снова через 6h") {
		t.Errorf("карточка должна обещать следующий приход через 6ч: %q", f2.sent[0])
	}
}

func TestRealertGoesDailyAfterAWeek(t *testing.T) {
	d, uid := newTestDB(t)
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	f := &fakeTG{}
	stageIncident(t, d, uid, now, 19*24*time.Hour, 8*time.Hour)
	pollerAt(d, f, now).tick(context.Background())
	if f.count() != 0 {
		t.Fatalf("роутер лежит третью неделю -- восьми часов молчания мало, отправлено %d", f.count())
	}

	f2 := &fakeTG{}
	stageIncident(t, d, uid, now, 19*24*time.Hour, 25*time.Hour)
	pollerAt(d, f2, now).tick(context.Background())
	if f2.count() != 1 {
		t.Fatalf("раз в сутки напоминать всё же нужно, отправлено %d", f2.count())
	}
}

// Затухание только удлиняет паузу. Если оператор сам выставил редкий realert
// (скажем, раз в двое суток), «раз в 6 часов» не имеет права его ускорить.
func TestRealertBackoffNeverShortensConfiguredCadence(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 48 * time.Hour, TickEvery: time.Second})
	p.SetNow(func() time.Time { return now })
	stageIncident(t, d, uid, now, 30*time.Hour, 7*time.Hour)

	p.tick(context.Background())

	if f.count() != 0 {
		t.Fatalf("настроенные 48ч важнее шестичасового пола, отправлено %d", f.count())
	}
}
