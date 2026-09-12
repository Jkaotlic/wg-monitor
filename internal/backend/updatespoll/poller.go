// Package updatespoll доспрашивает роутеры о версиях, которых нет в обычном
// отчёте.
//
// Отчёт агента приносит версию панели, прошивку, KeeneticOS и модуль ядра --
// это бесплатно и приезжает каждые полторы минуты. Двух вещей там нет вовсе:
// версии HydraRoute Neo и ДОСТУПНОЙ прошивки, а их знает только version_audit.
// Ради них и существует этот поллер.
//
// Три правила, и каждое закрыто тестом:
//
//  1. Не чаще раза в сутки на роутер. Опрос нужен ради двух полей, и делать из
//     него постоянный фон на парке незачем.
//  2. Не ставить команду в очередь тому, кто молчит. У команды есть TTL, и на
//     этом уже наступали с деплоем: мобильный роутер просыпается позже, команда
//     протухает, очередь копит мусор, версии всё равно не приезжают.
//  3. Ни одного похода в GitHub. Лимит анонимного API -- 60 запросов в час,
//     поллер по парку сжёг бы его за минуты, а ошибка кэшируется на 12 часов --
//     новости молча исчезли бы со всех экранов. Сравнение живёт только в общем
//     upstream.Cache, и этот пакет про него не знает вовсе.
package updatespoll

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Sink -- очередь команд агенту. Интерфейсом, а не *cmd.Queue: поллеру нужен
// ровно один глагол, и тест не обязан поднимать настоящую очередь.
type Sink interface {
	Enqueue(userID int64, cmd wire.Command) error
}

type Config struct {
	// Every -- как часто можно спрашивать ОДИН роутер. 0 -- сутки.
	Every time.Duration
	// SilentAfter -- после какого молчания роутер считается спящим и команду
	// ему не ставят. 0 -- 15 минут.
	SilentAfter time.Duration
	// TickEvery -- как часто поллер сверяется с часами. 0 -- 30 минут.
	TickEvery time.Duration
}

const (
	defaultEvery       = 24 * time.Hour
	defaultSilentAfter = 15 * time.Minute
	defaultTickEvery   = 30 * time.Minute
)

type Poller struct {
	d    *db.DB
	sink Sink
	cfg  Config
	now  func() time.Time
	wg   sync.WaitGroup

	mu sync.Mutex
	// last -- когда этот роутер спрашивали в прошлый раз. Живёт в памяти
	// намеренно: после рестарта бэкенда один лишний опрос безвреден, а
	// колонка в базе ради него была бы третьим местом, где хранится «когда
	// мы в последний раз трогали роутер».
	last map[int64]time.Time
}

func NewPoller(d *db.DB, sink Sink, cfg Config) *Poller {
	if cfg.Every <= 0 {
		cfg.Every = defaultEvery
	}
	if cfg.SilentAfter <= 0 {
		cfg.SilentAfter = defaultSilentAfter
	}
	if cfg.TickEvery <= 0 {
		cfg.TickEvery = defaultTickEvery
	}
	return &Poller{d: d, sink: sink, cfg: cfg, now: time.Now, last: make(map[int64]time.Time)}
}

// SetNow подменяет часы для детерминированных тестов. Звать до Run.
func (p *Poller) SetNow(fn func() time.Time) {
	if fn == nil {
		p.now = time.Now
		return
	}
	p.now = fn
}

func (p *Poller) Run(ctx context.Context) {
	p.wg.Add(1)
	defer p.wg.Done()
	t := time.NewTicker(p.cfg.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) WaitForExit() { p.wg.Wait() }

// TickForTest открывает tick() тестам.
func (p *Poller) TickForTest(ctx context.Context) { p.tick(ctx) }

func (p *Poller) tick(_ context.Context) {
	now := p.now()
	users, err := p.d.Users().GetAll()
	if err != nil {
		slog.Warn("updatespoll: users.GetAll failed", "err", err)
		return
	}
	// Свежесть роутера считается по его последнему отчёту. Не получилось
	// прочитать -- пропускаем заход целиком: спросить всех подряд значило бы
	// поставить команду и спящим тоже.
	latest, err := p.d.Events().LatestPerUserAll()
	if err != nil {
		slog.Warn("updatespoll: LatestPerUserAll failed; заход пропущен", "err", err)
		return
	}

	for _, u := range users {
		ts := latest[u.ID]
		if ts.IsZero() || now.Sub(ts) > p.cfg.SilentAfter {
			continue
		}
		p.mu.Lock()
		last, seen := p.last[u.ID]
		p.mu.Unlock()
		if seen && now.Sub(last) < p.cfg.Every {
			continue
		}
		cmd := wire.Command{
			ID:       fmt.Sprintf("updpoll-%d-%d", u.ID, now.UnixNano()),
			Action:   "version_audit",
			IssuedAt: now.UTC(),
		}
		if err := p.sink.Enqueue(u.ID, cmd); err != nil {
			slog.Warn("updatespoll: enqueue failed", "user_id", u.ID, "err", err)
			continue
		}
		p.mu.Lock()
		p.last[u.ID] = now
		p.mu.Unlock()
	}
}
