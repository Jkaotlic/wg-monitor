package alerts

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// doorTG -- Telegram, который на каждую личку отвечает одной и той же ошибкой,
// пока её не снимут, и считает попытки.
type doorTG struct {
	mu    sync.Mutex
	err   error
	tries int
}

func (s *doorTG) send() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tries++
	if s.err != nil {
		return 0, s.err
	}
	return int64(s.tries), nil
}

func (s *doorTG) SendMessage(context.Context, int64, *int64, string, string, *int64) (int64, error) {
	return s.send()
}

func (s *doorTG) SendMessageWithKeyboard(context.Context, int64, *int64, string, string, *int64, *tg.InlineKeyboardMarkup) (int64, error) {
	return s.send()
}

func (s *doorTG) SendMessageWithReplyKeyboard(context.Context, int64, *int64, string, string, *int64, any) (int64, error) {
	return s.send()
}

func (s *doorTG) CreateForumTopic(context.Context, int64, string, int) (int64, error) {
	return 0, nil
}

func (s *doorTG) open() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = nil
}

func (s *doorTG) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tries
}

// Боевой случай 11.09.2026: единственный получатель роутера за «chat not
// found», и сторож слал ему «роутер не на связи» на КАЖДОМ обходе -- 1112
// ошибок из 1112 обходов. Недоступный получатель -- это «слать некому», а не
// сбой Telegram: сторож обязан отстать до срока повторного напоминания, не
// копить offline_errors_total и не выдавать это за поломку отправки.
//
// Тест гоняет настоящую цепочку: сторож -> диспетчер -> рассылка.
func TestOfflineToUnreachableOnlyRecipientDoesNotHammer(t *testing.T) {
	d := newDB(t)
	tok := "7777777777777777777777777777777777777777777777777777777777777777"
	uid, err := d.Users().Insert("router-c", tok, "198.51.100.12", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().SetTelegramUserID(uid, 1001); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	if err := d.Events().Insert(uid, "agent_heartbeat", "ok", "", start.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	door := &doorTG{err: &tg.APIError{Method: "sendMessage", Code: 400, Description: "Bad Request: chat not found"}}
	disp := NewDispatcher(d, door, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})
	const scanEvery = 30 * time.Second
	w := heartbeat.NewWatcher(d, disp, heartbeat.Config{
		StaleAfter:    5 * time.Minute,
		ScanEvery:     scanEvery,
		RenotifyEvery: time.Hour,
	})
	clock := start
	w.SetNow(func() time.Time { return clock })
	errorsBefore := w.Snapshot().OfflineErrors

	const scans = 10
	for i := 0; i < scans; i++ {
		w.ScanForTest(context.Background())
		clock = clock.Add(scanEvery)
	}

	if got := door.attempts(); got != 1 {
		t.Fatalf("попыток отправки за %d обходов: %d, ждали одну -- дальше до срока напоминания слать некому", scans, got)
	}
	if grew := w.Snapshot().OfflineErrors - errorsBefore; grew != 0 {
		t.Fatalf("offline_errors_total вырос на %d: недоступный получатель -- не сбой отправки", grew)
	}
	if _, text, _ := w.LastOfflineError(); text != "" {
		t.Fatalf("last_offline_error=%q: недоступный получатель -- не сбой отправки", text)
	}
	un, err := d.Unreachable().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 1 || un[0].TelegramUserID != 1001 || !strings.Contains(un[0].LastError, "chat not found") {
		t.Fatalf("недоступные=%v, ждали 1001 с причиной chat not found -- иначе оператор не увидит, почему тихо", un)
	}

	// Человек написал боту. Отдельного снятия отметки по /start нет: её снимает
	// первая удачная доставка -- в срок повторного напоминания.
	door.open()
	clock = clock.Add(time.Hour)
	w.ScanForTest(context.Background())

	if got := door.attempts(); got != 2 {
		t.Fatalf("попыток после срока напоминания: %d, ждали вторую", got)
	}
	un, err = d.Unreachable().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 0 {
		t.Fatalf("недоступные=%v, удачная доставка обязана снять отметку", un)
	}
}
