// Package maintnotify -- мягкое напоминание «есть что обновить или
// перезагрузить роутер» (v0.57, спека C).
//
// Не тревога: в инциденты и счётчик тревог не попадает, приходит без звука,
// только днём и один раз на каждую новость (компонент + версия). Источник --
// тот же сборщик новостей, что у экрана «Обновления» мини-аппа
// (upstream.CollectNews), плюс отстающий агент; скрытая или отложенная в
// приложении новость не рассылается (router_update_reminders).
package maintnotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Sender -- тихая рассылка всем, кто отвечает за роутер (notify.Fanout).
// Семантика итога как у Fanout: (0, nil) -- слать некому, ошибка -- никому
// не дошло.
type Sender interface {
	SendSilentKeyboard(ctx context.Context, routerUserID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) (int, error)
}

// Config -- настройка пуллера. Нулевые Interval, Location и часы окна
// получают значения по умолчанию: раз в час, Москва, 10:00–20:00.
type Config struct {
	Enabled  bool
	Interval time.Duration
	Location *time.Location
	// Окно [FromHour, ToHour) по часам Location. Оба нуля -- 10–20.
	FromHour, ToHour int
	// MiniAppBaseURL -- адрес бэкенда для кнопки «Открыть в приложении».
	// Не https -- кнопки нет, текст тот же.
	MiniAppBaseURL string
	// Audit -- снимок версий в форме ответа агента
	// (backend.VersionAuditFromSnapshot). Функцией, чтобы не тянуть сюда
	// пакет backend.
	Audit func(db.RouterVersionRow) wire.VersionAudit
	// AgentNews -- отстаёт ли агент этой версии и на что обновляться
	// (backend.AgentUpdateNews).
	AgentNews func(agentVersion string) (available string, ok bool)
}

const (
	defaultInterval = time.Hour
	defaultFromHour = 10
	defaultToHour   = 20
)

// Poller раз в Interval обходит роутеры со снимком версий и рассылает
// неразосланные новости одним тихим сообщением на роутер.
type Poller struct {
	d      *db.DB
	up     *upstream.Cache
	s      Sender
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
	wg     sync.WaitGroup

	// delivered -- доставленные в этом процессе новости (роутер + компонент
	// + версия). Страховка на случай, когда MarkNotified упрямо не пишется:
	// без неё то же сообщение уходило бы каждый час. Живёт до рестарта --
	// дальше правду держит notified_at.
	mu        sync.Mutex
	delivered map[string]bool
}

// NewPoller проверяет швы: без Audit и AgentNews пуллер собирал бы не тот
// список, что экран, и это ошибка сборки, а не повод молчать.
func NewPoller(d *db.DB, up *upstream.Cache, s Sender, cfg Config, logger *slog.Logger) (*Poller, error) {
	if cfg.Audit == nil {
		return nil, errors.New("maintnotify: Config.Audit не задан")
	}
	if cfg.AgentNews == nil {
		return nil, errors.New("maintnotify: Config.AgentNews не задан")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Location == nil {
		cfg.Location = moscow()
	}
	if cfg.FromHour == 0 && cfg.ToHour == 0 {
		cfg.FromHour, cfg.ToHour = defaultFromHour, defaultToHour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{d: d, up: up, s: s, cfg: cfg, logger: logger, now: time.Now, delivered: map[string]bool{}}, nil
}

// SetNow подменяет часы для тестов. Звать до Run.
func (p *Poller) SetNow(fn func() time.Time) {
	if fn == nil {
		fn = time.Now
	}
	p.now = fn
}

// Run -- обход сразу и затем раз в Interval, до отмены ctx. Повтор после
// рестарта безвреден: разосланное помечено в базе.
func (p *Poller) Run(ctx context.Context) {
	p.wg.Add(1)
	defer p.wg.Done()
	p.Tick(ctx)
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Tick(ctx)
		}
	}
}

// WaitForExit ждёт выхода Run.
func (p *Poller) WaitForExit() { p.wg.Wait() }

// inWindow -- дневное окно по часам Location.
func (p *Poller) inWindow(now time.Time) bool {
	h := now.In(p.cfg.Location).Hour()
	return h >= p.cfg.FromHour && h < p.cfg.ToHour
}

// Tick -- один обход. Вне окна и выключенный -- ничего не делает и ничего не
// помечает.
func (p *Poller) Tick(ctx context.Context) {
	if !p.cfg.Enabled {
		return
	}
	now := p.now()
	if !p.inWindow(now) {
		return
	}
	snapshots, err := p.d.RouterVersions().All()
	if err != nil {
		p.logger.Warn("maintnotify: снимки версий не прочитаны", "err", err)
		return
	}
	users, err := p.d.Users().GetAll()
	if err != nil {
		p.logger.Warn("maintnotify: роутеры не прочитаны", "err", err)
		return
	}
	for i := range users {
		if ctx.Err() != nil {
			return
		}
		u := &users[i]
		row, ok := snapshots[u.ID]
		if !ok {
			continue
		}
		p.routerPass(ctx, u, row, now)
	}
}

// Item -- одна строка напоминания.
type Item struct {
	Component string
	Version   string // ключ новости в router_update_reminders
	Installed string
	Available string
}

func newsKey(component, version string) string { return component + "\x00" + version }

func deliveredKey(routerID int64, it Item) string {
	return fmt.Sprintf("%d\x00%s", routerID, newsKey(it.Component, it.Version))
}

func (p *Poller) wasDelivered(routerID int64, it Item) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.delivered[deliveredKey(routerID, it)]
}

func (p *Poller) noteDelivered(routerID int64, items []Item) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, it := range items {
		p.delivered[deliveredKey(routerID, it)] = true
	}
}

// routerPass -- новости одного роутера: завести (Ensure), отобрать
// неразосланные и не спрятанные (PendingNotify) среди актуальных сейчас,
// разослать одним тихим сообщением и пометить -- только при доставке.
func (p *Poller) routerPass(ctx context.Context, u *db.User, row db.RouterVersionRow, now time.Time) {
	news, _ := upstream.CollectNews(ctx, p.up, p.cfg.Audit(row))
	var current []Item
	var reboot *Item
	for _, n := range news {
		it := Item{Component: n.Component, Version: n.Version}
		if n.Update != nil {
			it.Installed, it.Available = n.Update.Installed, n.Update.Available
			current = append(current, it)
		} else {
			reboot = &it
		}
	}
	agentVersion := ""
	if u.LastDeployedVersion != nil {
		agentVersion = *u.LastDeployedVersion
	}
	if available, ok := p.cfg.AgentNews(agentVersion); ok {
		current = append(current, Item{Component: "agent", Version: available, Installed: agentVersion, Available: available})
	}
	// Перезагрузка -- последней строкой: сначала что обновить, потом что сделать.
	if reboot != nil {
		current = append(current, *reboot)
	}
	if len(current) == 0 {
		return
	}

	reminders := p.d.UpdateReminders()
	for _, it := range current {
		if err := reminders.Ensure(u.ID, it.Component, it.Version); err != nil {
			p.logger.Warn("maintnotify: новость не заведена", "router_id", u.ID, "component", it.Component, "err", err)
		}
	}
	pending, err := reminders.PendingNotify(u.ID, now)
	if err != nil {
		p.logger.Warn("maintnotify: новости не прочитаны", "router_id", u.ID, "err", err)
		return
	}
	want := make(map[string]bool, len(pending))
	for _, rem := range pending {
		want[newsKey(rem.Component, rem.Version)] = true
	}
	var items []Item
	for _, it := range current {
		if want[newsKey(it.Component, it.Version)] && !p.wasDelivered(u.ID, it) {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return
	}

	var kb *tg.InlineKeyboardMarkup
	if appURL := tg.MiniAppRouterTabURL(p.cfg.MiniAppBaseURL, u.ID, "manage", ""); appURL != "" {
		kb = &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{tg.OpenInAppButton(appURL)}}}
	}
	delivered, err := p.s.SendSilentKeyboard(ctx, u.ID, Render(u.Nickname, items), "", kb)
	if err != nil {
		p.logger.Warn("maintnotify: напоминание не доставлено, повтор в следующий обход", "router_id", u.ID, "err", err)
		return
	}
	if delivered == 0 {
		// Слать некому -- не «разослано»: появится получатель, получит.
		return
	}
	p.noteDelivered(u.ID, items)
	for _, it := range items {
		if err := reminders.MarkNotified(u.ID, it.Component, it.Version); err != nil {
			p.logger.Warn("maintnotify: отметка не записана", "router_id", u.ID, "component", it.Component, "err", err)
		}
	}
}

// Render -- текст напоминания. Простой текст без разметки: имя роутера
// задаёт человек, и экранировать его незачем. Словарь владельца: имена
// продуктов в ёлочках, «движок умной раздельной маршрутизации» при упоминании
// HydraRoute, «VPN-туннель», без инженерных слов.
func Render(nickname string, items []Item) string {
	onlyReboot := true
	for _, it := range items {
		if it.Component != "kmod_reboot" {
			onlyReboot = false
		}
	}
	var b strings.Builder
	if onlyReboot {
		fmt.Fprintf(&b, "🔔 Роутер «%s»: нужна перезагрузка\n", nickname)
	} else {
		fmt.Fprintf(&b, "🔔 Роутер «%s»: есть что обновить\n", nickname)
	}
	for _, it := range items {
		b.WriteString("• ")
		b.WriteString(itemLine(it))
		b.WriteString("\n")
	}
	b.WriteString("Это не срочно — сделайте в удобное время. Подробности и кнопки — в приложении.")
	return b.String()
}

func itemLine(it Item) string {
	versions := it.Installed + " → " + it.Available
	switch it.Component {
	case "awgmgr":
		return "Панель «awg-manager»: " + versions
	case "hrneo":
		return "Движок умной раздельной маршрутизации «HydraRoute Neo»: " + versions
	case "firmware":
		return "Прошивка роутера «KeeneticOS»: " + versions
	case "agent":
		return "Агент «wg-monitor»: " + versions
	case "kmod_reboot":
		return "Нужна перезагрузка роутера: сменился модуль ядра «AmneziaWG» — VPN-туннели поднимутся после перезагрузки."
	}
	return "Обновление: " + versions
}

var (
	moscowOnce sync.Once
	moscowLoc  *time.Location
)

func moscow() *time.Location {
	moscowOnce.Do(func() {
		loc, err := time.LoadLocation("Europe/Moscow")
		if err != nil {
			loc = time.FixedZone("МСК", 3*3600)
		}
		moscowLoc = loc
	})
	return moscowLoc
}
