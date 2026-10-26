// Package realert sends a STILL-DOWN reminder for HARD incidents older than
// `RealertEvery` (per spec §5.3). Tick cadence is decoupled (typically 5 min);
// the actual realert interval is enforced via StaleHards SQL filter.
package realert

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type TGSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup *tg.InlineKeyboardMarkup) (int64, error)
}

type Config struct {
	RealertEvery       time.Duration // default 6h
	MobileRealertEvery time.Duration // default 6h
	TickEvery          time.Duration // default 5min
	// MiniAppBaseURL, when non-empty, adds an "Open in app" web_app button to
	// STILL-DOWN reminders -- the same button and the same base URL as
	// alerts.Config.MiniAppBaseURL. Empty omits the button entirely.
	MiniAppBaseURL string
	// AdminUserID -- Telegram-номер админа, 0 -- не настроен. Напоминания
	// тоже уходят админу (решение оператора 15.09: «всё подряд»).
	AdminUserID int64
	// BypassLeakEnabled -- alerts.bypass_leak.enabled. Выключен (тихий
	// режим) -- по bypass_leak нет ни первой тревоги (backend/handler.go),
	// ни напоминаний: открытая HARD-строка могла остаться с тех пор, как
	// флаг был включён (ревью v0.56, M4).
	BypassLeakEnabled bool
}

const (
	defaultRealertEvery = 6 * time.Hour
	defaultTickEvery    = 5 * time.Minute
	// Пороги затухания. Роутер, лежащий третью неделю, присылал напоминание
	// каждый час -- четыре сотни сообщений в один топик. Такой поток
	// перестают читать, и когда рядом ляжет второй роутер, его сообщение
	// утонет в этом фоне. Первые сутки частота прежняя: пока инцидент
	// свежий, напоминание -- это шанс на быструю починку.
	backoffAfterDay  = 6 * time.Hour
	backoffAfterWeek = 24 * time.Hour
)

// effectiveCadence удлиняет паузу между напоминаниями по возрасту инцидента.
// Никогда не укорачивает: если оператор настроил редкие напоминания, пороги
// затухания не имеют права его ускорять.
func effectiveCadence(base, age time.Duration) time.Duration {
	floor := time.Duration(0)
	switch {
	case age >= 7*24*time.Hour:
		floor = backoffAfterWeek
	case age >= 24*time.Hour:
		floor = backoffAfterDay
	}
	if floor > base {
		return floor
	}
	return base
}

type Poller struct {
	d              *db.DB
	tg             TGSender
	cfg            Config
	wg             sync.WaitGroup
	now            func() time.Time // injectable for tests; defaults to time.Now
	rateLimitMu    sync.Mutex
	rateLimitedTil time.Time
	// sendFailCount tracks consecutive TG-send failures per (user,check) so
	// we don't spam ERROR every tick when TG is sustained-down (OBS-11).
	// Reset on success.
	sendFailMu    sync.Mutex
	sendFailCount map[string]int
	// notify -- рассылка напоминаний по личкам получателей. Заменила
	// адресацию в тему группы.
	notify realertSink
	// hard -- доставка самой карточки HARD-тревоги (alerts.Dispatcher), когда
	// первая не дошла никому (last_alert_at NULL); nil -- напоминание.
	hard HardDeliverer
	// beforeHardQueue -- шов для тестов: зовётся между чтением состояния и
	// постановкой карточки в очередь проверки. В проде nil.
	beforeHardQueue func()
}

// HardDeliverer -- отправка карточки HARD-тревоги так же, как при первом
// пересечении порога: текст тревоги, запись в alert_messages, память для
// нити починки, отметка last_alert_at. Реализует alerts.Dispatcher.
type HardDeliverer interface {
	DeliverHard(ctx context.Context, userID int64, nickname, checkName string, st db.IncidentState, check wire.Check) (int, error)
}

// SetHardDeliverer -- кто шлёт карточку недоставленной тревоги (BOT-01).
// Вызывается до Run.
func (p *Poller) SetHardDeliverer(h HardDeliverer) { p.hard = h }

// realertSink -- та часть веера, которой пользуются напоминания. Кнопки
// прикладываются, но сообщения НЕ запоминаются: «починилось» обязано
// отвечать на корневую тревогу, а не на последнее напоминание.
type realertSink interface {
	SendKeyboard(ctx context.Context, routerUserID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) (int, error)
	ReplyToEach(ctx context.Context, routerUserID int64, checkName, text, parseMode string, silent bool) error
}

// owedReplyMaxAge -- сколько повторять недоставленное «восстановилось». Сутки
// спустя оно уже шум: человек давно увидел роутер живым в приложении.
const owedReplyMaxAge = 24 * time.Hour

// retryOwedReplies повторяет ответы на тревоги, которые не дошли ни до кого
// (db.OwedReplies, BOT-01). Доставлено -- долг снят и переписка по проверке
// забыта, как после обычного «восстановилось». Проверка снова не в порядке
// (fail или hard) -- долг снят без отправки: «восстановилось» поверх новой
// поломки соврало бы, а о ней скажет своя тревога (FRESH-03). Лимит
// частоты -- обход останавливается.
// Возвращает false, если обход надо прервать.
func (p *Poller) retryOwedReplies(ctx context.Context, now time.Time) bool {
	owed, err := p.d.OwedReplies().List()
	if err != nil {
		slog.Error("realert: owed replies query failed", "err", err)
		return true
	}
	for _, o := range owed {
		drop := now.Sub(o.CreatedAt) > owedReplyMaxAge
		silent := false
		if !drop {
			st, err := p.d.State().Get(o.UserID, o.CheckName)
			if err != nil {
				slog.Warn("realert: owed reply state get failed", "user_id", o.UserID, "check", o.CheckName, "err", err)
				continue
			}
			drop = st.CurrentStatus != "ok"
			silent = alerts.Silenced(st, now)
		}
		if drop {
			if err := p.d.OwedReplies().Delete(o.UserID, o.CheckName); err != nil {
				slog.Warn("realert: owed reply delete failed", "user_id", o.UserID, "check", o.CheckName, "err", err)
			}
			continue
		}
		// Очередь доставок проверки общая с отчётом: впереди доставка (новая
		// карточка, «восстановилось») -- повтор не шлём, долг остаётся до
		// следующего обхода. Иначе повтор мог ответить мимо нити или стереть
		// alert_messages карточки, ушедшей следом.
		release, ok := alerts.TryEnqueueDelivery(o.UserID, o.CheckName)
		if !ok {
			slog.Info("realert: доставка по проверке уже идёт, «восстановилось» -- на следующем обходе",
				"user_id", o.UserID, "check", o.CheckName)
			continue
		}
		if !p.sendOwedReply(ctx, now, o, silent) {
			release()
			return false
		}
		release()
	}
	return true
}

// sendOwedReply -- повтор одного долга «восстановилось» с местом в очереди
// проверки, уже занятым вызывающим. false -- лимит частоты, обход прервать.
func (p *Poller) sendOwedReply(ctx context.Context, now time.Time, o db.OwedReply, silent bool) bool {
	if err := p.notify.ReplyToEach(ctx, o.UserID, o.CheckName, o.Text, "", silent); err != nil {
		slog.Warn("realert: повтор «восстановилось» не дошёл", "user_id", o.UserID, "check", o.CheckName, "err", err)
		if delay, ok := tg.RateLimitDelay(err); ok {
			p.markRateLimited(now, delay)
			return false
		}
		return true
	}
	if err := p.d.OwedReplies().Delete(o.UserID, o.CheckName); err != nil {
		slog.Warn("realert: owed reply delete failed", "user_id", o.UserID, "check", o.CheckName, "err", err)
	}
	if err := p.d.AlertMessages().ClearSentBefore(o.UserID, o.CheckName, o.CreatedAt); err != nil {
		slog.Warn("realert: alert messages clear failed", "user_id", o.UserID, "check", o.CheckName, "err", err)
	}
	return true
}

// SetNotifySink подменяет рассылку. Только для тестов.
func (p *Poller) SetNotifySink(s realertSink) { p.notify = s }

func NewPoller(d *db.DB, tg TGSender, cfg Config) *Poller {
	if cfg.RealertEvery <= 0 {
		cfg.RealertEvery = defaultRealertEvery
	}
	if cfg.MobileRealertEvery <= 0 {
		cfg.MobileRealertEvery = defaultRealertEvery
	}
	if cfg.TickEvery <= 0 {
		cfg.TickEvery = defaultTickEvery
	}
	return &Poller{
		d: d, tg: tg, cfg: cfg, now: time.Now,
		sendFailCount: make(map[string]int),
		notify:        notify.NewFanout(d, tg, slog.Default(), cfg.AdminUserID),
	}
}

// recordSendOutcome bumps/clears the per-(user,check) consecutive-failure
// counter and returns whether the current outcome should be logged. We log
// the 1st, 5th, and every 50th sustained failure so a chronic TG outage
// produces a small, useful trail instead of one ERROR every realert tick.
func (p *Poller) recordSendOutcome(userID int64, checkName string, ok bool) (logIt bool, count int) {
	key := strconv.FormatInt(userID, 10) + "/" + checkName
	p.sendFailMu.Lock()
	defer p.sendFailMu.Unlock()
	if ok {
		delete(p.sendFailCount, key)
		return false, 0
	}
	p.sendFailCount[key]++
	c := p.sendFailCount[key]
	switch {
	case c == 1, c == 5:
		return true, c
	case c%50 == 0:
		return true, c
	default:
		return false, c
	}
}

func (p *Poller) rateLimitActive(now time.Time) bool {
	p.rateLimitMu.Lock()
	defer p.rateLimitMu.Unlock()
	return !p.rateLimitedTil.IsZero() && now.Before(p.rateLimitedTil)
}

func (p *Poller) markRateLimited(now time.Time, delay time.Duration) {
	if delay <= 0 {
		return
	}
	p.rateLimitMu.Lock()
	defer p.rateLimitMu.Unlock()
	until := now.Add(delay)
	if until.After(p.rateLimitedTil) {
		p.rateLimitedTil = until
	}
}

// SetNow overrides the wall-clock seam for deterministic tests. Must be
// called before Run() starts; production callers must not use this.
func (p *Poller) SetNow(fn func() time.Time) {
	if fn == nil {
		p.now = time.Now
		return
	}
	p.now = fn
}

func (p *Poller) Run(ctx context.Context) error {
	p.wg.Add(1)
	defer p.wg.Done()
	t := time.NewTicker(p.cfg.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) WaitForExit() { p.wg.Wait() }

// resolverGuardCheck -- проверка DNS-сторожа агента (как в backend/handler.go;
// пакет стоит ниже backend, поэтому строка продублирована).
const resolverGuardCheck = "resolver_guard"

// heartbeatCheck -- проверка молчания роутера; её первую тревогу шлёт
// heartbeat.Watcher.
const heartbeatCheck = "agent_heartbeat"

// notReadyLookback -- сколько последних строк просматривается в поисках
// строки сторожа с настоящим ответом.
const notReadyLookback = 50

// resolverGuardNotReadyJSON: details_json несёт ready:false. Нечитаемое и
// пустое -- обычная строка (агент старый), как в backend.resolverGuardNotReady.
func resolverGuardNotReadyJSON(raw string) bool {
	if raw == "" {
		return false
	}
	var d struct {
		Ready *bool `json:"ready"`
	}
	if json.Unmarshal([]byte(raw), &d) != nil {
		return false
	}
	return d.Ready != nil && !*d.Ready
}

// lastKnownCheck loads the most recent event row for (userID, checkName) and
// reconstructs a wire.Check from its details_json. Returns zero Check if no
// event exists or unmarshal fails — formatter degrades gracefully.
func (p *Poller) lastKnownCheck(userID int64, checkName string) wire.Check {
	row, ok, err := p.d.Events().LatestEvent(userID, checkName)
	if err != nil || !ok {
		return wire.Check{}
	}
	if checkName == resolverGuardCheck && resolverGuardNotReadyJSON(row.DetailsJSON) {
		// Строка «ещё не прочитал настройки» -- не ответ о DNS: автомат её
		// пропускает, и напоминание не должно строиться из неё. Берём
		// последнюю строку с настоящим ответом (окно мало: таких подряд
		// бывает несколько после одного перезапуска).
		if recent, err := p.d.Events().RecentEvents(userID, checkName, notReadyLookback); err == nil {
			for _, r := range recent {
				if !resolverGuardNotReadyJSON(r.DetailsJSON) {
					row = r
					break
				}
			}
		}
	}
	c := wire.Check{Name: row.CheckName, Status: row.Status}
	if row.DetailsJSON != "" {
		if err := json.Unmarshal([]byte(row.DetailsJSON), &c.Details); err != nil {
			slog.Warn("realert: details_json unmarshal failed", "user_id", userID, "check", checkName, "err", err)
		}
	}
	return c
}

// neighborSummaries returns short status of OTHER tunnel_* checks for the same
// user — context shown next to a failing tunnel ("are siblings dead too?").
// Returns nil for non-tunnel checks. Now shares projection logic with the
// dispatcher via alerts.BuildNeighborSummaries (LOGIC-08).
func (p *Poller) neighborSummaries(userID int64, checkName string) []alerts.NeighborSummary {
	if !strings.HasPrefix(checkName, "tunnel_") && checkName != "dns" {
		return nil
	}
	rows, err := p.d.Events().LatestEventsByPrefixSince(userID, "tunnel_", p.now().Add(-alerts.NeighborFreshWindow))
	if err != nil {
		slog.Warn("realert: neighborSummaries events lookup failed", "user_id", userID, "err", err)
		return nil
	}
	return alerts.BuildNeighborSummaries(rows, checkName)
}

// neighborsFor -- соседи для напоминания: у bypass_leak это VPN-туннели
// роутера, кроме несущего (как у первой тревоги), у прочих --
// neighborSummaries.
func (p *Poller) neighborsFor(userID int64, checkName string, d map[string]any) []alerts.NeighborSummary {
	if checkName != alerts.BypassLeakCheck {
		return p.neighborSummaries(userID, checkName)
	}
	rows, err := p.d.Events().LatestEventsByPrefixSince(userID, "tunnel_", p.now().Add(-alerts.NeighborFreshWindow))
	if err != nil {
		slog.Warn("realert: neighborsFor events lookup failed", "user_id", userID, "err", err)
		return nil
	}
	return alerts.BuildNeighborSummaries(rows, alerts.NeighborExclude(checkName, d))
}

func (p *Poller) tick(ctx context.Context) {
	now := p.now()
	if p.rateLimitActive(now) {
		return
	}
	if !p.retryOwedReplies(ctx, now) {
		return
	}
	cutoff := now.Add(-p.cfg.RealertEvery)
	stale, err := p.d.State().StaleHards(cutoff)
	if err != nil {
		slog.Error("realert: StaleHards query failed", "err", err)
		return
	}
	// Pre-fetch users once to avoid the per-incident GetByID lookup (DB-10).
	users, err := p.d.Users().GetAll()
	if err != nil {
		slog.Warn("realert: users.GetAll failed; falling back to per-incident lookup", "err", err)
	}
	usersByID := make(map[int64]db.User, len(users))
	for _, u := range users {
		usersByID[u.ID] = u
	}
	for _, sh := range stale {
		if sh.CheckName == alerts.BypassLeakCheck && !p.cfg.BypassLeakEnabled {
			continue // тихий режим: напоминаний, как и тревоги, нет (M4)
		}
		u, ok := usersByID[sh.UserID]
		if !ok {
			one, err := p.d.Users().GetByID(sh.UserID)
			if err != nil {
				slog.Warn("realert: user lookup failed (orphan?)", "user_id", sh.UserID, "err", err)
				continue
			}
			u = *one
		}
		st, err := p.d.State().Get(sh.UserID, sh.CheckName)
		if err != nil {
			slog.Error("realert: state get failed", "user_id", sh.UserID, "check", sh.CheckName, "err", err)
			continue
		}
		if st.HardSince == nil {
			slog.Warn("realert: HardSince nil despite hard status", "user_id", sh.UserID, "check", sh.CheckName)
			continue
		}
		cadence := p.cfg.RealertEvery
		if u.IsMobile() {
			cadence = p.cfg.MobileRealertEvery
		}
		// Счётчик считается по настроенной частоте, а не по затухшей: он
		// должен расти монотонно, иначе на переходе через сутки номер
		// напоминания поехал бы вниз.
		count := int(now.Sub(*st.HardSince) / cadence)
		cadence = effectiveCadence(cadence, now.Sub(*st.HardSince))
		if st.LastAlertAt != nil && now.Sub(*st.LastAlertAt) < cadence {
			continue
		}
		check := p.lastKnownCheck(sh.UserID, sh.CheckName)
		// agent_heartbeat -- исключение: первую тревогу «Роутер не на связи»
		// шлёт только сторож молчания (свой текст, свои повторы после
		// сбоя), realert по ней -- только напоминания (BOT-05). Строка без
		// отметки (старые данные) получает напоминание, а не карточку
		// FormatHard -- иначе у тревоги было бы два владельца.
		if st.LastAlertAt == nil && p.hard != nil && sh.CheckName != heartbeatCheck {
			// Первая тревога не дошла никому: шлём её саму, с записью в
			// alert_messages и памятью для нити починки (BOT-01).
			// Очередь доставок проверки общая с отчётом (карточка,
			// «восстановилось»): впереди доставка -- карточку не шлём, обход
			// не ждёт, повтор на следующем; состояние перечитается заново.
			if p.beforeHardQueue != nil {
				p.beforeHardQueue()
			}
			release, ok := alerts.TryEnqueueDelivery(sh.UserID, sh.CheckName)
			if !ok {
				slog.Info("realert: доставка по проверке уже идёт, карточка -- на следующем обходе",
					"user_id", sh.UserID, "check", sh.CheckName)
				continue
			}
			// Место в очереди занято -- снимок st мог устареть: доставка из
			// отчёта закончилась между чтением и постановкой (проверка ожила
			// или карточка уже ушла). Перечитываем и шлём, только если тревога
			// всё ещё hard и не отправлена -- иначе дубль карточки.
			fresh, ferr := p.d.State().Get(sh.UserID, sh.CheckName)
			if ferr != nil || fresh.CurrentStatus != "hard" || fresh.LastAlertAt != nil || fresh.HardSince == nil {
				release()
				if ferr != nil {
					slog.Warn("realert: state reread failed", "user_id", sh.UserID, "check", sh.CheckName, "err", ferr)
				}
				continue
			}
			_, err := p.hard.DeliverHard(ctx, sh.UserID, u.Nickname, sh.CheckName, fresh, check)
			release()
			if err != nil {
				if logIt, count := p.recordSendOutcome(sh.UserID, sh.CheckName, false); logIt {
					slog.Error("realert: повтор тревоги не дошёл", "user_id", sh.UserID, "check", sh.CheckName, "consecutive_fails", count, "err", err)
				}
				if delay, ok := tg.RateLimitDelay(err); ok {
					p.markRateLimited(now, delay)
					return
				}
				continue
			}
			p.recordSendOutcome(sh.UserID, sh.CheckName, true)
			continue
		}
		neighbors := p.neighborsFor(sh.UserID, sh.CheckName, check.Details)
		text := alerts.FormatRealert(alerts.RealertArgs{
			Nickname:     u.Nickname,
			CheckName:    sh.CheckName,
			HardSince:    *st.HardSince,
			RealertCount: count,
			IsMobile:     u.IsMobile(),
			Check:        check,
			Neighbors:    neighbors,
			RealertEvery: cadence,
		})
		// Тот же двухкнопочный набор, что и под исходной HARD-тревогой:
		// открыть роутер в приложении (если база настроена) и отложить
		// проверку на час. Keep in sync with alerts.Dispatcher.Handle.
		appURL := tg.MiniAppRouterURL(p.cfg.MiniAppBaseURL, sh.UserID)
		kb := tg.AlertKeyboard(sh.UserID, sh.CheckName, appURL)
		_, err = p.notify.SendKeyboard(ctx, sh.UserID, text, "", &kb)
		if err != nil {
			if logIt, count := p.recordSendOutcome(sh.UserID, sh.CheckName, false); logIt {
				slog.Error("realert: tg send failed", "user_id", sh.UserID, "check", sh.CheckName, "consecutive_fails", count, "err", err)
			}
			if delay, ok := tg.RateLimitDelay(err); ok {
				p.markRateLimited(now, delay)
				return // stop this batch; retry later without advancing LastAlertAt
			}
			continue // do not advance LastAlertAt → retry next tick
		}
		p.recordSendOutcome(sh.UserID, sh.CheckName, true)
		// Realerts are sent as standalone messages (replyTo nil above);
		// LastAlertMsgID intentionally not updated so RECOVERY (dispatcher.go)
		// replies to the original HARD root, not the most recent reminder.
		// Use BumpLastAlertAt instead of Save to avoid race-overwriting an FSM
		// Recovery that occurred between StaleHards and this update.
		if err := p.d.State().BumpLastAlertAt(sh.UserID, sh.CheckName, now); err != nil {
			slog.Error("realert: bump LastAlertAt failed", "user_id", sh.UserID, "check", sh.CheckName, "err", err)
		}
	}
}
