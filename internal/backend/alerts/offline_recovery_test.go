package alerts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// silentTG -- fakeTG, который умеет и тихую отправку (disable_notification)
// и помнит, какие сообщения ушли без звука.
type silentTG struct {
	fakeTG
	smu    sync.Mutex
	silent []sentKBMsg
}

func (s *silentTG) SendSilentMessageWithKeyboard(_ context.Context, chatID int64, threadID *int64, text, _ string, replyTo *int64, kb *tg.InlineKeyboardMarkup) (int64, error) {
	s.smu.Lock()
	defer s.smu.Unlock()
	s.silent = append(s.silent, sentKBMsg{chatID, threadID, text, replyTo, kb})
	return int64(len(s.silent) + 5000), nil
}

// offlineRig -- настоящая цепочка «сторож -> диспетчер -> рассылка» на
// подставном Telegram с управляемыми часами.
type offlineRig struct {
	d     *db.DB
	uid   int64
	clock time.Time
	w     *heartbeat.Watcher
}

func newOfflineRig(t *testing.T, sender TGSender) *offlineRig {
	t.Helper()
	d := newDB(t)
	tok := "abab000000000000000000000000000000000000000000000000000000000000"
	uid, err := d.Users().Insert("router-r", tok, "198.51.100.20", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().SetTelegramUserID(uid, 1201); err != nil {
		t.Fatal(err)
	}
	r := &offlineRig{d: d, uid: uid, clock: time.Now().UTC()}
	if err := d.Events().Insert(uid, "agent_heartbeat", "ok", "", r.clock.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	disp := NewDispatcher(d, sender, Config{FailThreshold: 3, RecoveryThreshold: 2})
	disp.SetNow(func() time.Time { return r.clock })
	r.w = heartbeat.NewWatcher(d, disp, heartbeat.Config{StaleAfter: 5 * time.Minute, ScanEvery: 30 * time.Second})
	r.w.SetNow(func() time.Time { return r.clock })
	return r
}

func (r *offlineRig) scan() { r.w.ScanForTest(context.Background()) }

// comeBack -- роутер прислал отчёт; следующий обход видит его свежим.
func (r *offlineRig) comeBack(t *testing.T) {
	t.Helper()
	r.clock = r.clock.Add(20 * time.Minute)
	if err := r.d.Events().Insert(r.uid, "agent_heartbeat", "ok", "", r.clock); err != nil {
		t.Fatal(err)
	}
	r.clock = r.clock.Add(30 * time.Second)
	r.scan()
}

// BOT-03: тревога «Роутер не на связи» обещает «Когда роутер вернётся, бот
// напишет сам». Роутер вернулся -- бот обязан написать «Роутер снова на
// связи» ответом на ту самую тревогу, ровно один раз, и забыть переписку.
func TestOfflineRouterBackSendsRecoveryReply(t *testing.T) {
	ftg := &fakeTG{}
	r := newOfflineRig(t, ftg)

	r.scan()
	if len(ftg.sentWithKeyboard) != 1 || !strings.Contains(ftg.sentWithKeyboard[0].text, "Роутер не на связи") {
		t.Fatalf("первая тревога: %+v", ftg.sentWithKeyboard)
	}
	alertID := int64(1001) // fakeTG: первое сообщение с кнопками

	r.comeBack(t)

	var back []sentMsg
	for _, m := range ftg.sent {
		if strings.Contains(m.text, "Роутер снова на связи") {
			back = append(back, m)
		}
	}
	if len(back) != 1 {
		t.Fatalf("«Роутер снова на связи»: %d сообщений, ждали одно; ушло %+v", len(back), ftg.sent)
	}
	if back[0].chat != 1201 {
		t.Fatalf("адресат %d, ждали владельца 1201", back[0].chat)
	}
	if back[0].replyTo == nil || *back[0].replyTo != alertID {
		t.Fatalf("«снова на связи» не ответ на тревогу %d: replyTo=%v", alertID, back[0].replyTo)
	}
	if !strings.Contains(back[0].text, "Простой") {
		t.Fatalf("нет простоя в тексте: %q", back[0].text)
	}
	msgs, err := r.d.AlertMessages().List(r.uid, "agent_heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("переписка по молчанию роутера не забыта: %v", msgs)
	}

	// Следующий обход -- роутер по-прежнему на связи -- второго сообщения нет.
	r.clock = r.clock.Add(30 * time.Second)
	r.scan()
	if n := len(ftg.sent); n != 1 {
		t.Fatalf("повторный обход прислал ещё сообщения: %+v", ftg.sent)
	}
}

// Роутер, о котором бот не предупреждал (тревоги не было), возвращается
// молча: писать «снова на связи» на пустом месте -- шум.
func TestOfflineRouterBackWithoutAlertIsQuiet(t *testing.T) {
	ftg := &fakeTG{}
	r := newOfflineRig(t, ftg)
	r.comeBack(t)
	if len(ftg.sent)+len(ftg.sentWithKeyboard) != 0 {
		t.Fatalf("без тревоги ушло: %+v %+v", ftg.sent, ftg.sentWithKeyboard)
	}
}

// «Снова на связи», не дошедшее никому, не теряется: долг ложится в
// owed_replies и уйдёт на обходе напоминаний ответом на тревогу (BOT-01).
func TestOfflineRouterBackUndeliveredIsOwed(t *testing.T) {
	ftg := &fakeTG{}
	r := newOfflineRig(t, ftg)
	r.scan()
	ftg.sendErrOnce = errors.New("telegram: 502 Bad Gateway")
	r.comeBack(t)

	owed, err := r.d.OwedReplies().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(owed) != 1 || owed[0].CheckName != "agent_heartbeat" || !strings.Contains(owed[0].Text, "Роутер снова на связи") {
		t.Fatalf("долг «снова на связи» не записан: %+v", owed)
	}
	st, err := r.d.State().Get(r.uid, "agent_heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentStatus == "hard" {
		t.Fatal("строка осталась hard -- долг будет снят без отправки")
	}
	msgs, _ := r.d.AlertMessages().List(r.uid, "agent_heartbeat")
	if len(msgs) != 1 {
		t.Fatalf("переписка стёрта до доставки долга: %v", msgs)
	}
}

// Под «Тише на час» и «снова на связи» приходит без звука (как BOT-02 для
// прочих проверок).
func TestOfflineRouterBackSilencedIsQuietSound(t *testing.T) {
	stg := &silentTG{}
	r := newOfflineRig(t, stg)
	r.scan()
	st, err := r.d.State().Get(r.uid, "agent_heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	until := r.clock.Add(time.Hour)
	st.SilencedUntil = &until
	if err := r.d.State().Save(r.uid, "agent_heartbeat", st); err != nil {
		t.Fatal(err)
	}
	r.comeBack(t)

	if len(stg.silent) != 1 || !strings.Contains(stg.silent[0].text, "Роутер снова на связи") {
		t.Fatalf("тихих «снова на связи»: %+v; со звуком: %+v", stg.silent, stg.sent)
	}
	for _, m := range stg.sent {
		if strings.Contains(m.text, "Роутер снова на связи") {
			t.Fatalf("под тишиной ушло со звуком: %q", m.text)
		}
	}
}
