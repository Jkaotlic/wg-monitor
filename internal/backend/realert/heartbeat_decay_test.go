package realert

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// clockTG -- Telegram, который помнит, когда (по часам теста) ушло каждое
// сообщение. Умеет всё, что нужно диспетчеру тревог.
type clockTG struct {
	mu    sync.Mutex
	clock *time.Time
	msgs  []clockMsg
}

type clockMsg struct {
	at   time.Time
	text string
}

func (c *clockTG) rec(text string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, clockMsg{*c.clock, text})
	return int64(len(c.msgs)), nil
}

func (c *clockTG) SendMessage(_ context.Context, _ int64, _ *int64, text, _ string, _ *int64) (int64, error) {
	return c.rec(text)
}

func (c *clockTG) SendMessageWithKeyboard(_ context.Context, _ int64, _ *int64, text, _ string, _ *int64, _ *tg.InlineKeyboardMarkup) (int64, error) {
	return c.rec(text)
}

func (c *clockTG) SendMessageWithReplyKeyboard(_ context.Context, _ int64, _ *int64, text, _ string, _ *int64, _ any) (int64, error) {
	return c.rec(text)
}

// BOT-05: роутер выключен 8 суток. Полная тревога «Роутер не на связи» --
// одна; дальше только короткие напоминания realert с затуханием: первую
// неделю раз в 6 ч, после недели -- раз в сутки. Ни двух сообщений за один
// интервал (сторож + напоминание), ни новой тревоги после рестарта бэкенда.
//
// Шаг 5 мин -- частота обхода напоминаний; сторож обходит на каждом шаге.
// Тревога в 0:05 (порог молчания 5 мин). Напоминание -- когда с прошлого
// прошло строго больше 6 ч, на сетке 5 мин это 6:05: в 0:05 + k·6:05 при
// k = 1..27 (k = 27 -> 164:20, k = 28 пришлось бы на 170:25, а там возраст
// уже больше недели и пауза -- сутки). Следующее -- 164:20 + 24 ч = 188:20,
// в пределах 8 суток (192:00). Итого 27 + 1 = 28 напоминаний.
func TestSilentRouterEightDaysOneAlertAndDecayingReminders(t *testing.T) {
	d, uid := newTestDB(t)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	clock := start
	if err := d.Events().Insert(uid, "agent_heartbeat", "ok", "", start); err != nil {
		t.Fatal(err)
	}
	ctg := &clockTG{clock: &clock}
	now := func() time.Time { return clock }

	disp := alerts.NewDispatcher(d, ctg, alerts.Config{FailThreshold: 3, RecoveryThreshold: 2})
	disp.SetNow(now)
	p := NewPoller(d, ctg, Config{RealertEvery: 6 * time.Hour, TickEvery: 5 * time.Minute})
	p.SetNow(now)
	p.SetHardDeliverer(disp)
	newWatcher := func() *heartbeat.Watcher {
		w := heartbeat.NewWatcher(d, disp, heartbeat.Config{
			StaleAfter: 5 * time.Minute,
			ScanEvery:  30 * time.Second,
		})
		w.SetNow(now)
		return w
	}
	w := newWatcher()

	const step = 5 * time.Minute
	end := start.Add(8 * 24 * time.Hour)
	restartAt := start.Add(3*24*time.Hour + 17*time.Minute)
	restarted := false
	for clock = start.Add(step); !clock.After(end); clock = clock.Add(step) {
		if !restarted && !clock.Before(restartAt) {
			// Рестарт бэкенда: память сторожа пуста.
			w = newWatcher()
			restarted = true
		}
		p.tick(context.Background())
		w.ScanForTest(context.Background())
	}

	var full, reminders []clockMsg
	for _, m := range ctg.msgs {
		switch {
		case strings.Contains(m.text, "Всё ещё"):
			reminders = append(reminders, m)
		case strings.Contains(m.text, "Роутер не на связи"):
			full = append(full, m)
		default:
			t.Errorf("неожиданное сообщение в %s: %q", m.at.Sub(start), m.text)
		}
	}
	if len(full) != 1 {
		var at []time.Duration
		for _, m := range full {
			at = append(at, m.at.Sub(start))
		}
		t.Fatalf("полных тревог «Роутер не на связи»: %d (в %v), ждали одну -- повтор должен быть коротким напоминанием", len(full), at)
	}
	if got := full[0].at.Sub(start); got != 5*time.Minute {
		t.Fatalf("тревога ушла в %s, ждали 5m", got)
	}
	prev := full[0].at
	for _, m := range ctg.msgs[1:] {
		gap := m.at.Sub(prev)
		minGap := 6 * time.Hour
		if m.at.Sub(start) >= 7*24*time.Hour {
			minGap = 24 * time.Hour
		}
		if gap < minGap {
			t.Errorf("в %s сообщение через %s после предыдущего, ждали не чаще раза в %s", m.at.Sub(start), gap, minGap)
		}
		prev = m.at
	}
	if len(reminders) != 28 {
		t.Fatalf("напоминаний за 8 суток: %d, ждали 28 (раз в 6 ч неделю, потом раз в сутки)", len(reminders))
	}
}
