package alerts

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const testAdminTG int64 = 9000

// chatsTG запоминает, в какие чаты ушли сообщения и с какими кнопками.
type chatsTG struct {
	mu    sync.Mutex
	chats map[int64]int
	kbs   map[int64]*tg.InlineKeyboardMarkup
}

func newChatsTG() *chatsTG {
	return &chatsTG{chats: map[int64]int{}, kbs: map[int64]*tg.InlineKeyboardMarkup{}}
}

func (s *chatsTG) note(chatID int64, kb *tg.InlineKeyboardMarkup) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chats[chatID]++
	s.kbs[chatID] = kb
	return int64(len(s.chats)), nil
}

func (s *chatsTG) SendMessage(_ context.Context, chatID int64, _ *int64, _, _ string, _ *int64) (int64, error) {
	return s.note(chatID, nil)
}

func (s *chatsTG) SendMessageWithKeyboard(_ context.Context, chatID int64, _ *int64, _, _ string, _ *int64, kb *tg.InlineKeyboardMarkup) (int64, error) {
	return s.note(chatID, kb)
}

func (s *chatsTG) SendMessageWithReplyKeyboard(_ context.Context, chatID int64, _ *int64, _, _ string, _ *int64, m any) (int64, error) {
	switch v := m.(type) {
	case tg.InlineKeyboardMarkup:
		return s.note(chatID, &v)
	case *tg.InlineKeyboardMarkup:
		return s.note(chatID, v)
	}
	return s.note(chatID, nil)
}

func (s *chatsTG) got(chatID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chats[chatID]
}

func (s *chatsTG) hasMute(chatID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	kb := s.kbs[chatID]
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.Text == notify.AdminMuteButtonText {
				return true
			}
		}
	}
	return false
}

// Решение оператора 15.09: админу по всем роутерам -- всё подряд, что получает
// владелец. Роутер чужой (владелец 1001), и каждый вид уведомления из пакета
// alerts обязан дойти и до админа -- с кнопкой выключения.
func TestAlertsEveryKindReachesAdminOfForeignRouter(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, d *db.DB, s *chatsTG, uid int64)
	}{
		{"не на связи", func(t *testing.T, d *db.DB, s *chatsTG, uid int64) {
			disp := NewDispatcher(d, s, Config{FailThreshold: 3, RecoveryThreshold: 2, AdminUserID: testAdminTG})
			if err := disp.SendOffline(context.Background(), uid, "router-a", 10*time.Minute); err != nil {
				t.Fatal(err)
			}
		}},
		{"пробуждение", func(t *testing.T, d *db.DB, s *chatsTG, uid int64) {
			n := NewWakeNotifier(d, s, testAdminTG)
			if err := n.SendWake(context.Background(), uid, "router-a", []wire.Check{{Name: "tunnels", Status: "ok"}}); err != nil {
				t.Fatal(err)
			}
		}},
		{"сон", func(t *testing.T, d *db.DB, s *chatsTG, uid int64) {
			n := NewSleepNotifier(d, s, testAdminTG)
			if err := n.SendSleeping(context.Background(), uid, "router-a", time.Now().Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
		}},
		{"отложенный деплой", func(t *testing.T, d *db.DB, s *chatsTG, uid int64) {
			n := NewDeployNotifier(d, s, testAdminTG)
			if err := n.SendDeferredUpdate(context.Background(), uid, "router-a", "v0.33.0", "ok", ""); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDB(t) // dispatcher_test.go:115
			uid, err := d.Users().Insert("router-a", "8888888888888888888888888888888888888888888888888888888888888888", "198.51.100.13", "awg0")
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Users().SetTelegramUserID(uid, 1001); err != nil {
				t.Fatal(err)
			}
			s := newChatsTG()
			c.run(t, d, s, uid)
			if s.got(1001) != 1 {
				t.Fatalf("владельцу ушло %d, ждали 1", s.got(1001))
			}
			if s.got(testAdminTG) != 1 {
				t.Fatalf("админу ушло %d, ждали 1 -- этот вид уведомлений забыл админа", s.got(testAdminTG))
			}
			if !s.hasMute(testAdminTG) {
				t.Fatal("у админа нет кнопки «Не писать мне про этот роутер»")
			}
			if s.hasMute(1001) {
				t.Fatal("у владельца появилась кнопка админа")
			}
		})
	}
}
