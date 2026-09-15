package notify

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// kbRecordingSender запоминает, с какими кнопками ушло каждое сообщение.
// Умеет все три способа отправки, чтобы тест видел ровно тот путь, который
// выбрал веер.
type kbRecordingSender struct {
	mu      sync.Mutex
	nextID  int64
	count   map[int64]int
	inline  map[int64]*tg.InlineKeyboardMarkup // nil -- ушло без кнопок
	replyTo map[int64]*int64
	reply   map[int64]any // разметка, ушедшая через SendMessageWithReplyKeyboard
}

func newKBRecorder() *kbRecordingSender {
	return &kbRecordingSender{
		count:   map[int64]int{},
		inline:  map[int64]*tg.InlineKeyboardMarkup{},
		replyTo: map[int64]*int64{},
		reply:   map[int64]any{},
	}
}

func (s *kbRecordingSender) SendMessage(_ context.Context, chatID int64, _ *int64, _, _ string, replyTo *int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.count[chatID]++
	s.inline[chatID] = nil
	s.replyTo[chatID] = replyTo
	return s.nextID, nil
}

func (s *kbRecordingSender) SendMessageWithKeyboard(_ context.Context, chatID int64, _ *int64, _, _ string, replyTo *int64, markup *tg.InlineKeyboardMarkup) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.count[chatID]++
	s.inline[chatID] = markup
	s.replyTo[chatID] = replyTo
	return s.nextID, nil
}

func (s *kbRecordingSender) SendMessageWithReplyKeyboard(_ context.Context, chatID int64, _ *int64, _, _ string, replyTo *int64, markup any) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.count[chatID]++
	s.reply[chatID] = markup
	s.replyTo[chatID] = replyTo
	return s.nextID, nil
}

// muteRowsIn -- сколько раз в разметке встречается кнопка выключения.
func muteRowsIn(kb *tg.InlineKeyboardMarkup) int {
	if kb == nil {
		return 0
	}
	n := 0
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.Text == AdminMuteButtonText || len(b.CallbackData) > 6 && b.CallbackData[:6] == "nmute:" {
				n++
			}
		}
	}
	return n
}

// lastRowIsMute -- кнопка выключения стоит последним рядом и ведёт на этот роутер.
func lastRowIsMute(kb *tg.InlineKeyboardMarkup, router int64) bool {
	if kb == nil || len(kb.InlineKeyboard) == 0 {
		return false
	}
	last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
	return len(last) == 1 && last[0].Text == AdminMuteButtonText &&
		last[0].CallbackData == fmt.Sprintf("nmute:%d", router)
}

func TestParseAdminMuteCallback(t *testing.T) {
	if id, ok := ParseAdminMuteCallback("nmute:42"); !ok || id != 42 {
		t.Fatalf("nmute:42 -> %d,%v; ждали 42,true", id, ok)
	}
	for _, bad := range []string{"nmute:", "nmute:abc", "nmute:-1", "nmute:0", "mute:42", "nmute:42:1", ""} {
		if _, ok := ParseAdminMuteCallback(bad); ok {
			t.Errorf("%q разобран как годный", bad)
		}
	}
	if got := AdminMuteCallbackData(7); got != "nmute:7" {
		t.Fatalf("AdminMuteCallbackData(7)=%q", got)
	}
}

// Каждый метод веера: у админа последний ряд -- «Не писать мне про этот
// роутер», у владельца такой кнопки нет нигде. Решение оператора: админу
// приходит всё подряд, значит и кнопка под всем подряд.
func TestFanout_EveryMethodGivesAdminMuteRowOnly(t *testing.T) {
	baseKB := func() *tg.InlineKeyboardMarkup {
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "📋 История", CallbackData: "history:1:tunnel_awg0"}},
		}}
	}
	cases := []struct {
		name string
		run  func(f *Fanout, router int64) error
		// reply -- метод шлёт через нижнюю клавиатуру, разметку смотреть там.
		reply bool
	}{
		{"Send", func(f *Fanout, r int64) error {
			_, err := f.Send(context.Background(), r, "текст", "")
			return err
		}, false},
		{"SendKeyboard/nil", func(f *Fanout, r int64) error {
			_, err := f.SendKeyboard(context.Background(), r, "текст", "", nil)
			return err
		}, false},
		{"SendKeyboard/kb", func(f *Fanout, r int64) error {
			_, err := f.SendKeyboard(context.Background(), r, "текст", "", baseKB())
			return err
		}, false},
		{"SendTracked", func(f *Fanout, r int64) error {
			_, err := f.SendTracked(context.Background(), r, "tunnel_awg0", "текст", "", baseKB())
			return err
		}, false},
		{"ReplyToEach", func(f *Fanout, r int64) error {
			return f.ReplyToEach(context.Background(), r, "tunnel_awg0", "починилось", "")
		}, false},
		{"SendWithReplyKeyboard/inline", func(f *Fanout, r int64) error {
			_, err := f.SendWithReplyKeyboard(context.Background(), r, "текст", "", *baseKB())
			return err
		}, true},
		// B7b: mobileWakeKeyboard возвращает nil, когда miniAppBaseURL не
		// настроен (lifecycle_notifier.go) -- без своей кнопки. Админ обязан
		// всё равно получить ряд выключения: replyMarkupFor не находит для
		// nil-разметки ни один case и отдаёт markup как есть (тоже nil), и
		// тогда SendWithReplyKeyboard падает в sendOne, который дописывает
		// ряд админу независимо от того, что было в исходной разметке (см.
		// комментарий "Разметки нет или отправитель её не умеет" в fanout.go).
		// Путь другой, чем у SendWithReplyKeyboard/inline (там m != nil, и
		// разметка уходит через SendMessageWithReplyKeyboard) -- поэтому итог
		// смотрим в s.inline, а не в s.reply, reply=false.
		{"SendWithReplyKeyboard/nil", func(f *Fanout, r int64) error {
			_, err := f.SendWithReplyKeyboard(context.Background(), r, "текст", "", nil)
			return err
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, router := newDB(t)
			if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
				t.Fatal(err)
			}
			s := newKBRecorder()
			if err := c.run(NewFanout(d, s, quietLogger(), adminTG), router); err != nil {
				t.Fatal(err)
			}
			if s.count[1001] != 1 || s.count[adminTG] != 1 {
				t.Fatalf("сообщений: владельцу %d, админу %d; ждали по одному", s.count[1001], s.count[adminTG])
			}
			adminKB, ownerKB := s.inline[adminTG], s.inline[1001]
			if c.reply {
				adminKB, ownerKB = asInline(t, s.reply[adminTG]), asInline(t, s.reply[1001])
			}
			if !lastRowIsMute(adminKB, router) {
				t.Fatalf("у админа нет кнопки выключения последним рядом: %+v", adminKB)
			}
			if muteRowsIn(adminKB) != 1 {
				t.Fatalf("у админа кнопок выключения %d, ждали одну", muteRowsIn(adminKB))
			}
			if muteRowsIn(ownerKB) != 0 {
				t.Fatalf("у владельца появилась кнопка выключения админа: %+v", ownerKB)
			}
		})
	}
}

func asInline(t *testing.T, m any) *tg.InlineKeyboardMarkup {
	t.Helper()
	switch v := m.(type) {
	case nil:
		return nil
	case tg.InlineKeyboardMarkup:
		return &v
	case *tg.InlineKeyboardMarkup:
		return v
	}
	t.Fatalf("разметка неожиданного типа %T", m)
	return nil
}

// Клавиатура вызывающего общая для всех адресатов и для следующих тревог
// (realert шлёт один и тот же kb на каждом тике). Дописать админский ряд в
// неё саму -- значит через час показать кнопку админа владельцу.
func TestFanout_AdminRowDoesNotMutateCallerKeyboard(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	kb := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{{Text: "📋 История", CallbackData: "history:1:tunnel_awg0"}},
	}}
	f := NewFanout(d, newKBRecorder(), quietLogger(), adminTG)
	for i := 0; i < 3; i++ {
		if _, err := f.SendKeyboard(context.Background(), router, "напоминание", "", kb); err != nil {
			t.Fatal(err)
		}
	}
	if len(kb.InlineKeyboard) != 1 {
		t.Fatalf("клавиатура вызывающего изменилась: %+v", kb.InlineKeyboard)
	}
}

// «Починилось» админу -- ответом на его тревогу и с кнопкой.
func TestFanout_ReplyToEachAdminKeepsReplyTo(t *testing.T) {
	d, router := newDB(t)
	if err := d.AlertMessages().Put(router, "tunnel_awg0", adminTG, 555); err != nil {
		t.Fatal(err)
	}
	s := newKBRecorder()
	if err := NewFanout(d, s, quietLogger(), adminTG).ReplyToEach(context.Background(), router, "tunnel_awg0", "починилось", ""); err != nil {
		t.Fatal(err)
	}
	if s.replyTo[adminTG] == nil || *s.replyTo[adminTG] != 555 {
		t.Fatalf("replyTo админа=%v, ждали 555", s.replyTo[adminTG])
	}
	if !lastRowIsMute(s.inline[adminTG], router) {
		t.Fatalf("у админа нет кнопки выключения: %+v", s.inline[adminTG])
	}
}

// Админ не настроен -- ни у кого кнопки нет, отправка прежняя.
func TestFanout_ZeroAdminNoMuteRow(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, adminTG); err != nil {
		t.Fatal(err)
	}
	s := newKBRecorder()
	if _, err := NewFanout(d, s, quietLogger(), 0).Send(context.Background(), router, "текст", ""); err != nil {
		t.Fatal(err)
	}
	if s.count[adminTG] != 1 || s.inline[adminTG] != nil {
		t.Fatalf("при adminID=0 ждали одно сообщение без кнопок, got count=%d kb=%+v", s.count[adminTG], s.inline[adminTG])
	}
}

// Админ-владелец -- ровно одно сообщение (и одна кнопка).
func TestFanout_AdminOwnerGetsExactlyOneMessage(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, adminTG); err != nil {
		t.Fatal(err)
	}
	s := newKBRecorder()
	n, err := NewFanout(d, s, quietLogger(), adminTG).Send(context.Background(), router, "текст", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || s.count[adminTG] != 1 || muteRowsIn(s.inline[adminTG]) != 1 {
		t.Fatalf("доставлено %d, админу %d, кнопок %d; ждали 1/1/1", n, s.count[adminTG], muteRowsIn(s.inline[adminTG]))
	}
}

// Выключивший роутер админ не получает ничего.
func TestFanout_MutedAdminGetsNone(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.NotifyMutes().SetMuted(adminTG, router, true); err != nil {
		t.Fatal(err)
	}
	s := newKBRecorder()
	if _, err := NewFanout(d, s, quietLogger(), adminTG).SendTracked(context.Background(), router, "tunnel_awg0", "текст", "", &tg.InlineKeyboardMarkup{}); err != nil {
		t.Fatal(err)
	}
	if s.count[adminTG] != 0 {
		t.Fatalf("выключивший админ получил %d сообщений", s.count[adminTG])
	}
	if s.count[1001] != 1 {
		t.Fatalf("владелец получил %d, ждали 1", s.count[1001])
	}
}
