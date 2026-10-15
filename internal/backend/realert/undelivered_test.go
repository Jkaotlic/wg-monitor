package realert

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// dispTG -- fakeTG, дорощенный до отправителя диспетчера тревог.
type dispTG struct{ *fakeTG }

func (f dispTG) SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, _ any) (int64, error) {
	return f.SendMessage(ctx, chatID, threadID, text, parseMode, replyTo)
}

var errTG502 = errors.New("tg sendMessage: Bad Gateway (code=502)")

var th = state.Thresholds{Fail: 3, Recovery: 2}

// BOT-01: первая тревога не дошла никому (Telegram лежал) -- она не
// помечается отправленной, и ближайший обход напоминаний её доставляет, а не
// через RealertEvery (6 ч).
func TestUndeliveredHardIsRealertedOnNextTick(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{sendErr: errTG502}
	now := time.Now().UTC().Truncate(time.Second)
	disp := alerts.NewDispatcher(d, dispTG{f}, alerts.Config{FailThreshold: 3, RecoveryThreshold: 2})
	disp.SetNow(func() time.Time { return now })

	prev := db.IncidentState{UserID: uid, CheckName: "awg_handshake", CurrentStatus: "fail", ConsecutiveFails: 2}
	tr := state.Apply(prev, "fail", now, th)
	if tr.Kind != state.Hard {
		t.Fatalf("переход %v, ждали hard", tr.Kind)
	}
	err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr,
		wire.Check{Name: "awg_handshake", Status: "fail"})
	if !errors.Is(err, notify.ErrNoneDelivered) {
		t.Fatalf("Handle = %v, ждали ErrNoneDelivered", err)
	}
	st, _ := d.State().Get(uid, "awg_handshake")
	if st.CurrentStatus != "hard" {
		t.Fatalf("состояние %q, ждали hard (иначе следующий fail даст дубль)", st.CurrentStatus)
	}
	if st.LastAlertAt != nil {
		t.Fatalf("недоставленная тревога помечена отправленной: LastAlertAt=%v", st.LastAlertAt)
	}

	f.sendErr = nil
	p := NewPoller(d, f, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.SetHardDeliverer(disp)
	disp.SetNow(func() time.Time { return now.Add(5 * time.Minute) })
	p.SetNow(func() time.Time { return now.Add(5 * time.Minute) })
	p.tick(context.Background())
	if n := f.count(); n != 2 {
		t.Fatalf("сообщений %d, ждали 2 (неудачная попытка + доставка на ближайшем обходе)", n)
	}
	// Первым до человека доходит сама тревога, а не «Всё ещё … #0», и она
	// записана: на неё ответит «восстановилось».
	if strings.Contains(f.sent[1], "Всё ещё") || !strings.Contains(f.sent[1], "🔴") {
		t.Fatalf("на обходе ушла не карточка тревоги: %q", f.sent[1])
	}
	msgs, _ := d.AlertMessages().List(uid, "awg_handshake")
	if msgs[6001] != 99 {
		t.Fatalf("доставленная тревога не записана в alert_messages: %v", msgs)
	}
	if text, _, ok := disp.Last(uid, "awg_handshake"); !ok || text != f.sent[1] {
		t.Fatalf("тревога не в памяти для нити починки: ok=%v", ok)
	}
	st, _ = d.State().Get(uid, "awg_handshake")
	if st.LastAlertAt == nil || !st.LastAlertAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("после доставки LastAlertAt=%v, ждали время обхода", st.LastAlertAt)
	}
	// Следующий обход -- уже обычная частота: повтора нет.
	p.SetNow(func() time.Time { return now.Add(10 * time.Minute) })
	p.tick(context.Background())
	if n := f.count(); n != 2 {
		t.Fatalf("сообщений %d после второго обхода, ждали 2", n)
	}
	// «Восстановилось» отвечает на доставленную на обходе тревогу.
	prev, _ = d.State().Get(uid, "awg_handshake")
	prev.ConsecutiveOKs = 1
	tr = state.Apply(prev, "ok", now.Add(15*time.Minute), th)
	if tr.Kind != state.Recovery {
		t.Fatalf("переход %v, ждали recovery", tr.Kind)
	}
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr,
		wire.Check{Name: "awg_handshake", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(); n != 3 || f.replyTos[2] == nil || *f.replyTos[2] != 99 {
		t.Fatalf("«восстановилось» не ответом на тревогу: сообщений %d, replyTo=%v", n, f.replyTos[len(f.replyTos)-1])
	}
}

// BOT-01: «восстановилось», которое не дошло никому, не теряется: ближайший
// обход доставляет его ответом на исходную тревогу, и только один раз.
func TestUndeliveredRecoveryIsRetriedOnNextTick(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	now := time.Now().UTC().Truncate(time.Second)
	disp := alerts.NewDispatcher(d, dispTG{f}, alerts.Config{FailThreshold: 3, RecoveryThreshold: 2})
	disp.SetNow(func() time.Time { return now })

	hardSince := now.Add(-time.Hour)
	lastAlert := now.Add(-time.Hour)
	if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		ConsecutiveFails: 5, ConsecutiveOKs: 1, HardSince: &hardSince, LastAlertAt: &lastAlert,
	}); err != nil {
		t.Fatal(err)
	}
	const rootMsg = int64(555)
	if err := d.AlertMessages().Put(uid, "awg_handshake", 6001, rootMsg); err != nil {
		t.Fatal(err)
	}
	// Корневая тревога ушла час назад (sent_at -- как пишет CURRENT_TIMESTAMP).
	if _, err := d.SQL().Exec(`UPDATE alert_messages SET sent_at = ? WHERE user_id = ?`,
		now.Add(-time.Hour).Format("2006-01-02 15:04:05"), uid); err != nil {
		t.Fatal(err)
	}
	prev, _ := d.State().Get(uid, "awg_handshake")
	tr := state.Apply(prev, "ok", now, th)
	if tr.Kind != state.Recovery {
		t.Fatalf("переход %v, ждали recovery", tr.Kind)
	}
	f.sendErr = errTG502
	err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr,
		wire.Check{Name: "awg_handshake", Status: "ok"})
	if !errors.Is(err, notify.ErrNoneDelivered) {
		t.Fatalf("Handle = %v, ждали ErrNoneDelivered", err)
	}

	f.sendErr = nil
	p := NewPoller(d, f, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.SetNow(func() time.Time { return now.Add(5 * time.Minute) })
	p.tick(context.Background())
	if n := f.count(); n != 2 {
		t.Fatalf("сообщений %d, ждали 2 (неудачная попытка + повтор)", n)
	}
	if !strings.Contains(f.sent[1], "Простой") {
		t.Fatalf("повтор не похож на «восстановилось»: %q", f.sent[1])
	}
	if f.replyTos[1] == nil || *f.replyTos[1] != rootMsg {
		t.Fatalf("повтор ушёл не ответом на тревогу: replyTo=%v", f.replyTos[1])
	}
	p.SetNow(func() time.Time { return now.Add(10 * time.Minute) })
	p.tick(context.Background())
	if n := f.count(); n != 2 {
		t.Fatalf("сообщений %d после второго обхода, ждали 2 (повтор только один)", n)
	}
}

// Долг «восстановилось» снимается без отправки, если проверка снова в
// аварии: «восстановилось» поверх новой тревоги соврало бы.
func TestOwedReplyDroppedWhenHardAgain(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	now := time.Now().UTC().Truncate(time.Second)
	hardSince := now.Add(-time.Minute)
	lastAlert := now.Add(-time.Minute)
	if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		ConsecutiveFails: 3, HardSince: &hardSince, LastAlertAt: &lastAlert,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.OwedReplies().Put(uid, "awg_handshake", "🟢 восстановилось", now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(d, f, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.SetNow(func() time.Time { return now })
	p.tick(context.Background())
	if n := f.count(); n != 0 {
		t.Fatalf("сообщений %d, ждали 0: %v", n, f.sent)
	}
	if owed, _ := d.OwedReplies().List(); len(owed) != 0 {
		t.Fatalf("долг не снят: %+v", owed)
	}
}

// silentFakeTG -- fakeTG, умеющий тихие сообщения; считает их отдельно.
type silentFakeTG struct {
	*fakeTG
	silent int
}

func (f *silentFakeTG) SendSilentMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, _ *tg.InlineKeyboardMarkup) (int64, error) {
	f.silent++
	return f.SendMessage(ctx, chatID, threadID, text, parseMode, replyTo)
}

// BOT-02: повтор «восстановилось» внутри «Тише на час» тоже без звука.
func TestOwedReplyRetrySilentWhileSilenced(t *testing.T) {
	d, uid := newTestDB(t)
	f := &silentFakeTG{fakeTG: &fakeTG{}}
	now := time.Now().UTC().Truncate(time.Second)
	until := now.Add(time.Hour)
	if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "ok", ConsecutiveOKs: 2, SilencedUntil: &until,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.OwedReplies().Put(uid, "awg_handshake", "🟢 восстановилось", now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(d, f, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.SetNow(func() time.Time { return now })
	p.tick(context.Background())
	if f.count() != 1 || f.silent != 1 {
		t.Fatalf("сообщений %d, из них тихих %d; ждали одно тихое", f.count(), f.silent)
	}
}

// Очистка переписки после повтора «восстановилось» стирает только сообщения
// старой поломки: сообщение новой тревоги, ушедшее после долга, остаётся.
func TestOwedReplyClearKeepsNewerAlertMessages(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	now := time.Now().UTC().Truncate(time.Second)
	if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "ok", ConsecutiveOKs: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.OwedReplies().Put(uid, "awg_handshake", "🟢 восстановилось", now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for chat, at := range map[int64]time.Time{6001: now.Add(-time.Hour), 7001: now.Add(-time.Minute)} {
		if err := d.AlertMessages().Put(uid, "awg_handshake", chat, 500+chat); err != nil {
			t.Fatal(err)
		}
		if _, err := d.SQL().Exec(`UPDATE alert_messages SET sent_at = ? WHERE user_id = ? AND telegram_user_id = ?`,
			at.Format("2006-01-02 15:04:05"), uid, chat); err != nil {
			t.Fatal(err)
		}
	}
	p := NewPoller(d, f, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.SetNow(func() time.Time { return now })
	p.tick(context.Background())
	if f.count() != 1 {
		t.Fatalf("сообщений %d, ждали 1", f.count())
	}
	msgs, _ := d.AlertMessages().List(uid, "awg_handshake")
	if _, old := msgs[6001]; old {
		t.Fatalf("переписка старой поломки не забыта: %v", msgs)
	}
	if msgs[7001] != 7501 {
		t.Fatalf("стёрто сообщение новой тревоги: %v", msgs)
	}
}

// countingDeliverer -- HardDeliverer, который только считает вызовы.
type countingDeliverer struct{ n int }

func (c *countingDeliverer) DeliverHard(context.Context, int64, string, string, db.IncidentState, wire.Check) (int, error) {
	c.n++
	return 1, nil
}

// Единственный владелец тревоги «не на связи» -- сторож молчания (BOT-05):
// даже если строка agent_heartbeat = hard оказалась без отметки отправки
// (старые данные), realert не шлёт полную карточку FormatHard, а только
// короткое напоминание.
func TestRealertNeverDeliversHardCardForHeartbeat(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	now := time.Now().UTC().Truncate(time.Second)
	hs := now.Add(-time.Hour)
	if err := d.State().Save(uid, "agent_heartbeat", db.IncidentState{
		UserID: uid, CheckName: "agent_heartbeat", CurrentStatus: "hard", ConsecutiveFails: 1, HardSince: &hs,
	}); err != nil {
		t.Fatal(err)
	}
	cd := &countingDeliverer{}
	p := NewPoller(d, f, Config{RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.SetNow(func() time.Time { return now })
	p.SetHardDeliverer(cd)
	p.tick(context.Background())
	if cd.n != 0 {
		t.Fatalf("realert отправил карточку тревоги по agent_heartbeat: %d", cd.n)
	}
	if f.count() != 1 || !strings.Contains(f.sent[0], "Всё ещё") {
		t.Fatalf("ждали одно короткое напоминание, ушло %q", f.sent)
	}
}
