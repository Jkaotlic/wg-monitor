package realert

import (
	"context"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// undeliveredHard -- тревога в hard, карточка которой не дошла никому
// (last_alert_at NULL): обход напоминаний доставит её сам.
func undeliveredHard(t *testing.T) (*db.DB, int64, *fakeTG, *Poller, time.Time) {
	t.Helper()
	d, uid := newTestDB(t)
	f := &fakeTG{}
	now := time.Now().UTC().Truncate(time.Second)
	hardSince := now.Add(-time.Hour)
	if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		ConsecutiveFails: 5, HardSince: &hardSince,
	}); err != nil {
		t.Fatal(err)
	}
	disp := alerts.NewDispatcher(d, dispTG{f}, alerts.Config{FailThreshold: 3, RecoveryThreshold: 2})
	disp.SetNow(func() time.Time { return now })
	p := NewPoller(d, f, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.SetHardDeliverer(disp)
	p.SetNow(func() time.Time { return now })
	return d, uid, f, p, now
}

func tickBounded(t *testing.T, p *Poller) {
	t.Helper()
	done := make(chan struct{})
	go func() { p.tick(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("обход напоминаний завис на очереди доставки: остальные тревоги ждали бы его")
	}
}

// Обход напоминаний доставляет карточку той же проверки в общей очереди:
// пока идёт доставка из отчёта (здесь -- «восстановилось»), карточка не
// уходит, и обход её не ждёт -- повтор на следующем обходе.
func TestRealertDeliverHardWaitsQueuedDelivery(t *testing.T) {
	_, uid, f, p, now := undeliveredHard(t)
	_, release := alerts.EnqueueDelivery(uid, "awg_handshake") // доставка в полёте
	tickBounded(t, p)
	if n := f.count(); n != 0 {
		release()
		t.Fatalf("карточка ушла (%d сообщений), пока доставка той же проверки ещё идёт", n)
	}
	release()
	p.SetNow(func() time.Time { return now.Add(time.Minute) })
	tickBounded(t, p)
	if n := f.count(); n != 1 {
		t.Fatalf("после очереди сообщений %d, ждали карточку", n)
	}
	if n := alerts.DeliveryQueueLen(); n != 0 {
		t.Fatalf("очередь не освобождена: %d", n)
	}
}

// Доставка впереди была «восстановилось»: проверка ожила, пока карточка
// ждала очереди. Карточку уже исправленной поломки не шлём.
func TestRealertDeliverHardSkipsWhenRecoveredWhileQueued(t *testing.T) {
	d, uid, f, p, now := undeliveredHard(t)
	_, release := alerts.EnqueueDelivery(uid, "awg_handshake")
	tickBounded(t, p)
	if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "ok", ConsecutiveOKs: 2,
	}); err != nil {
		t.Fatal(err)
	}
	release()
	p.SetNow(func() time.Time { return now.Add(time.Minute) })
	tickBounded(t, p)
	if n := f.count(); n != 0 {
		t.Fatalf("карточка ожившей проверки ушла: %d сообщений", n)
	}
}

// Пока обход сам доставляет карточку, доставка из отчёта той же проверки
// встаёт за ним в очередь.
func TestRealertDeliverHardHoldsQueue(t *testing.T) {
	_, uid, f, p, _ := undeliveredHard(t)
	f.mu.Lock()
	f.onSend = func() {
		prev, release := alerts.EnqueueDelivery(uid, "awg_handshake")
		defer release()
		if prev == nil {
			f.queueFreeDuringSend = true
		}
	}
	f.mu.Unlock()
	tickBounded(t, p)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queueFreeDuringSend {
		t.Fatal("карточка из обхода шла мимо очереди: доставка из отчёта её не ждала бы")
	}
}

// Повтор недошедшего «восстановилось» -- тоже в общей очереди проверки:
// пока по ней идёт доставка (здесь -- карточка из отчёта), повтор не уходит
// и долг остаётся; на следующем обходе после неё -- уходит.
func TestRealertOwedReplyWaitsQueuedDelivery(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	now := time.Now().UTC().Truncate(time.Second)
	if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "ok", ConsecutiveOKs: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.AlertMessages().Put(uid, "awg_handshake", 6001, 555); err != nil {
		t.Fatal(err)
	}
	if err := d.OwedReplies().Put(uid, "awg_handshake", "✅ Простой 1 ч", now); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(d, f, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.SetNow(func() time.Time { return now.Add(time.Minute) })

	_, release := alerts.EnqueueDelivery(uid, "awg_handshake") // доставка в полёте
	tickBounded(t, p)
	if n := f.count(); n != 0 {
		release()
		t.Fatalf("повтор «восстановилось» ушёл (%d сообщений), пока доставка той же проверки ещё идёт", n)
	}
	owed, err := d.OwedReplies().List()
	if err != nil || len(owed) != 1 {
		release()
		t.Fatalf("долг «восстановилось» потерян при занятой очереди: %v %v", owed, err)
	}
	release()

	p.SetNow(func() time.Time { return now.Add(2 * time.Minute) })
	tickBounded(t, p)
	if n := f.count(); n != 1 {
		t.Fatalf("после очереди сообщений %d, ждали повтор «восстановилось»", n)
	}
	if owed, _ := d.OwedReplies().List(); len(owed) != 0 {
		t.Fatalf("долг не снят после доставки: %v", owed)
	}
	if n := alerts.DeliveryQueueLen(); n != 0 {
		t.Fatalf("очередь не освобождена: %d", n)
	}
}

// Между чтением состояния и местом в очереди доставка из отчёта успела
// закончиться: проверка ожила (или карточка уже ушла). Снимок устарел --
// карточку не шлём, иначе пришёл бы дубль или карточка ожившей проверки.
func TestRealertDeliverHardRereadsStateInQueue(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   func(now time.Time) db.IncidentState
	}{
		{"ожила", func(time.Time) db.IncidentState {
			return db.IncidentState{CurrentStatus: "ok", ConsecutiveOKs: 2}
		}},
		{"карточка уже ушла", func(now time.Time) db.IncidentState {
			hs := now.Add(-time.Hour)
			return db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 5, HardSince: &hs, LastAlertAt: &now}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, uid, f, p, now := undeliveredHard(t)
			p.beforeHardQueue = func() {
				st := tc.st(now)
				st.UserID, st.CheckName = uid, "awg_handshake"
				if err := d.State().Save(uid, "awg_handshake", st); err != nil {
					t.Error(err)
				}
			}
			tickBounded(t, p)
			if n := f.count(); n != 0 {
				t.Fatalf("карточка по устаревшему снимку ушла: %d сообщений", n)
			}
			if n := alerts.DeliveryQueueLen(); n != 0 {
				t.Fatalf("очередь не освобождена: %d", n)
			}
		})
	}
}
