package notify

import (
	"context"
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
