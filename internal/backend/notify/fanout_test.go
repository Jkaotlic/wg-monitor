package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

type fakeSender struct {
	mu   sync.Mutex
	sent []int64
	fail map[int64]error
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, _ *int64, _, _ string, _ *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.fail[chatID]; ok {
		return 0, err
	}
	f.sent = append(f.sent, chatID)
	return 1, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestFanout_SendsToEveryRecipient(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1002, 1001); err != nil {
		t.Fatal(err)
	}

	s := &fakeSender{}
	n, err := NewFanout(d, s, quietLogger()).Send(context.Background(), router, "линия упала", "HTML")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(s.sent) != 2 {
		t.Fatalf("доставлено %d (%v), ждали двоим", n, s.sent)
	}
}

// Один получатель недоступен -- остальные обязаны получить тревогу. Это
// главное требование веера: чужая закрытая личка не глушит чужую тревогу.
func TestFanout_OneFailureDoesNotStopTheRest(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1002, 1001); err != nil {
		t.Fatal(err)
	}

	s := &fakeSender{fail: map[int64]error{1001: &tg.APIError{
		Method: "sendMessage", Code: 403,
		Description: "Forbidden: bot can't initiate conversation with a user",
	}}}
	n, err := NewFanout(d, s, quietLogger()).Send(context.Background(), router, "линия упала", "HTML")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(s.sent) != 1 || s.sent[0] != 1002 {
		t.Fatalf("доставлено %d (%v), ждали одного -- 1002", n, s.sent)
	}

	// Недоступный помечен, чтобы оператор увидел его в дашборде.
	un, err := d.Unreachable().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 1 || un[0].TelegramUserID != 1001 {
		t.Fatalf("недоступные=%v, ждали 1001", un)
	}
}

// Человек заговорил с ботом -- отметка недоступности снимается сама.
func TestFanout_SuccessClearsUnreachable(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.Unreachable().Mark(1001, "bot can't initiate conversation"); err != nil {
		t.Fatal(err)
	}

	s := &fakeSender{}
	if _, err := NewFanout(d, s, quietLogger()).Send(context.Background(), router, "всё хорошо", "HTML"); err != nil {
		t.Fatal(err)
	}
	un, err := d.Unreachable().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 0 {
		t.Fatalf("недоступные=%v, успешная доставка обязана снять отметку", un)
	}
}

// Слать некому -- не ошибка. Возвращается ноль доставленных, и вызывающий
// волен показать это оператору.
func TestFanout_NobodyToNotify(t *testing.T) {
	d, router := newDB(t)
	s := &fakeSender{}
	n, err := NewFanout(d, s, quietLogger()).Send(context.Background(), router, "текст", "HTML")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(s.sent) != 0 {
		t.Fatalf("доставлено %d, ждали ноль", n)
	}
}

type fakeKeyboardSender struct {
	fakeSender
	nextID int64
}

func (f *fakeKeyboardSender) SendMessageWithKeyboard(_ context.Context, chatID int64, _ *int64, _, _ string, _ *int64, _ *tg.InlineKeyboardMarkup) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.fail[chatID]; ok {
		return 0, err
	}
	f.nextID++
	f.sent = append(f.sent, chatID)
	return f.nextID, nil
}

func TestFanout_SendTrackedRemembersEachMessage(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1002, 1001); err != nil {
		t.Fatal(err)
	}

	s := &fakeKeyboardSender{}
	n, err := NewFanout(d, s, quietLogger()).SendTracked(context.Background(), router, "tunnel_awg0", "линия упала", "HTML", &tg.InlineKeyboardMarkup{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("доставлено %d, ждали двоим", n)
	}

	got, err := d.AlertMessages().List(router, "tunnel_awg0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1001] == 0 || got[1002] == 0 {
		t.Fatalf("карта сообщений=%v, у каждого получателя должно быть своё", got)
	}
	if got[1001] == got[1002] {
		t.Fatal("id сообщений разных людей не могут совпадать")
	}
}

// «Починилось» приходит ответом на собственную тревогу каждого.
func TestFanout_ReplyToEachUsesOwnMessage(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1002, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.AlertMessages().Put(router, "tunnel_awg0", 1001, 555); err != nil {
		t.Fatal(err)
	}
	// У 1002 сообщения нет: он подключился позже. Он обязан получить
	// «починилось» обычным сообщением, а не остаться без него.

	s := &replyCapturingSender{replies: map[int64]*int64{}}
	if err := NewFanout(d, s, quietLogger()).ReplyToEach(context.Background(), router, "tunnel_awg0", "починилось", ""); err != nil {
		t.Fatal(err)
	}
	if len(s.replies) != 2 {
		t.Fatalf("получателей=%d, ждали двоих", len(s.replies))
	}
	if s.replies[1001] == nil || *s.replies[1001] != 555 {
		t.Fatalf("у 1001 replyTo=%v, ждали 555", s.replies[1001])
	}
	if s.replies[1002] != nil {
		t.Fatalf("у 1002 replyTo=%v, ждали без привязки", *s.replies[1002])
	}
}

type replyCapturingSender struct {
	mu      sync.Mutex
	replies map[int64]*int64
}

func (s *replyCapturingSender) SendMessage(_ context.Context, chatID int64, _ *int64, _, _ string, replyTo *int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[chatID] = replyTo
	return 1, nil
}

// Никому не дошло, хотя получатели были -- это неуспех: тревогу надо
// повторить. Отличается от «слать некому», где повторять её некому.
func TestFanout_AllFailedIsAnError(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}

	s := &fakeSender{fail: map[int64]error{1001: &tg.APIError{
		Method: "sendMessage", Code: 500, Description: "Internal Server Error",
	}}}
	n, err := NewFanout(d, s, quietLogger()).Send(context.Background(), router, "текст", "")
	if n != 0 {
		t.Fatalf("доставлено %d, ждали ноль", n)
	}
	if !errors.Is(err, ErrNoneDelivered) {
		t.Fatalf("ошибка=%v, ждали ErrNoneDelivered", err)
	}
}

// Исходная ошибка Telegram обязана дойти до вызывающего: по ней напоминания
// разбирают лимит частоты и решают, когда повторить.
func TestFanout_KeepsUnderlyingError(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	want := &tg.APIError{Method: "sendMessage", Code: 429, Description: "Too Many Requests", RetryAfter: 30}
	s := &fakeSender{fail: map[int64]error{1001: want}}

	_, err := NewFanout(d, s, quietLogger()).Send(context.Background(), router, "текст", "")
	var got *tg.APIError
	if !errors.As(err, &got) {
		t.Fatalf("ошибка=%v, из неё нельзя достать APIError", err)
	}
	if got.Code != 429 {
		t.Fatalf("код=%d, ждали 429", got.Code)
	}
}
