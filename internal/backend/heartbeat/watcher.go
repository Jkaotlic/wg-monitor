// internal/backend/heartbeat/watcher.go
package heartbeat

import (
	"context"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type OfflineSender interface {
	SendOffline(ctx context.Context, userID int64, nickname string, since time.Duration) error
	// SendBackOnline -- «Роутер снова на связи» ответом на тревогу о
	// молчании. Зовётся, только если тревога была (строка agent_heartbeat
	// была hard): тревога обещает «Когда роутер вернётся, бот напишет сам»
	// (BOT-03). hardSince -- с какого момента роутер молчал.
	SendBackOnline(ctx context.Context, userID int64, nickname string, hardSince time.Time) error
}

// SleepNotifier is called once when a mobile user crosses MobileSleepAfter
// without heartbeats and MobileLifecycle is enabled. Unlike OfflineSender,
// it must NOT renotify — the watcher self-dedupes via sleepNotified.
type SleepNotifier interface {
	SendSleeping(ctx context.Context, userID int64, nickname string, lastSeen time.Time) error
}

// Config controls per-user heartbeat staleness detection.
//
// StaleAfterStatic / StaleAfterMobile pick the threshold based on user.Kind.
// Mobile routers (4G in-vehicle) are expected to drop into tunnels/garages
// for tens of minutes at a time without it meaning anything is wrong.
//
// ResumeGrace suppresses OFFLINE alerts for a brief window after the agent
// has explicitly told us it just resumed (Report.Resumed=true). The agent
// gathers a fresh diagnostic round before sending that report, so we want
// the FSM to ingest its checks and emit accurate signals — not race a
// premature OFFLINE.
//
// Повтора у сторожа нет (BOT-05): он шлёт только первую тревогу и пишет
// строку agent_heartbeat = hard с отметкой отправки. Дальше напоминает
// realert -- коротко и с затуханием (раз в 6 ч, после недели раз в сутки).
// «Уже предупредил» читается из базы, поэтому рестарт бэкенда не даёт
// новой тревоги по роутеру, который молчал и до рестарта.
//
// StaleAfter is the legacy single-threshold knob. If non-zero and the new
// kind-aware fields are zero, it's used as both static and mobile threshold
// (for backward compatibility with the Stage 1 config layout).
type Config struct {
	StaleAfter       time.Duration // deprecated, see StaleAfter{Static,Mobile}
	StaleAfterStatic time.Duration
	StaleAfterMobile time.Duration // applied only when MobileLifecycle == false
	MobileSleepAfter time.Duration // NEW: threshold for mobile sleep-info when MobileLifecycle == true
	MobileLifecycle  bool          // NEW: true = wake/sleep flow, false = legacy HARD-OFFLINE
	ResumeGrace      time.Duration
	ScanEvery        time.Duration
	// SendTimeout ограничивает одну отправку уведомления. Верхний ctx живёт
	// столько же, сколько backend, поэтому без собственного дедлайна один
	// подвисший запрос к Telegram останавливал бы обход всего парка -- и
	// мониторинг замолкал бы целиком из-за одного роутера.
	SendTimeout time.Duration
}

const (
	defaultStaleAfterStatic = 5 * time.Minute
	defaultStaleAfterMobile = 60 * time.Minute
	defaultMobileSleepAfter = 5 * time.Minute // NEW
	defaultResumeGrace      = 90 * time.Second
	defaultScanEvery        = 30 * time.Second
	defaultSendTimeout      = 60 * time.Second
)

func (c Config) staleFor(u db.User) time.Duration {
	if u.IsMobile() {
		if c.MobileLifecycle {
			switch {
			case c.MobileSleepAfter > 0:
				return c.MobileSleepAfter
			default:
				return defaultMobileSleepAfter
			}
		}
		switch {
		case c.StaleAfterMobile > 0:
			return c.StaleAfterMobile
		case c.StaleAfter > 0:
			return c.StaleAfter
		default:
			return defaultStaleAfterMobile
		}
	}
	switch {
	case c.StaleAfterStatic > 0:
		return c.StaleAfterStatic
	case c.StaleAfter > 0:
		return c.StaleAfter
	default:
		return defaultStaleAfterStatic
	}
}

// sendTimeout -- дедлайн одной отправки. Отдельный от ScanEvery: обход может
// быть частым, а сообщение в Telegram при плохой сети законно идёт дольше.
func (w *Watcher) sendTimeout() time.Duration {
	if w.cfg.SendTimeout > 0 {
		return w.cfg.SendTimeout
	}
	return defaultSendTimeout
}

// offlineRetryState -- сколько раз подряд тревога не ушла и когда пробовать
// снова.
type offlineRetryState struct {
	fails int
	next  time.Time
}

// offlineRetryPause -- пауза после fails-й неудачи подряд: 1 мин, 5 мин,
// дальше раз в 30 мин. Таймаут ответа Telegram бывает и тогда, когда
// сообщение уже принято, -- повтор на каждом обходе (30 с) дал бы по дублю
// карточки каждые 30 с.
func offlineRetryPause(fails int) time.Duration {
	switch fails {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	default:
		return 30 * time.Minute
	}
}

type Watcher struct {
	d             *db.DB
	off           OfflineSender
	sleep         SleepNotifier
	cfg           Config
	sleepNotified map[int64]time.Time
	resumed       map[int64]time.Time
	// offlineRetry -- пауза перед повтором тревоги «не на связи», упавшей
	// целиком (под mu). Только память: после рестарта первая попытка сразу.
	offlineRetry map[int64]offlineRetryState
	// Последняя неудачная отправка «роутер не на связи» -- под mu.
	lastOfflineErrRouter string
	lastOfflineErrText   string
	lastOfflineErrAt     time.Time
	mu                   sync.Mutex
	wg                   sync.WaitGroup
	// now is set once at construction (or by SetNow before Run starts) and
	// read concurrently from scan/MarkResumed. Tests inject a deterministic
	// clock; production keeps time.Now. The pointer write is single-shot;
	// no atomic needed.
	now func() time.Time
}

func NewWatcher(d *db.DB, off OfflineSender, cfg Config) *Watcher {
	if cfg.ResumeGrace <= 0 {
		cfg.ResumeGrace = defaultResumeGrace
	}
	if cfg.ScanEvery <= 0 {
		cfg.ScanEvery = defaultScanEvery
	}
	return &Watcher{
		d: d, off: off, cfg: cfg,
		sleepNotified: map[int64]time.Time{},
		resumed:       map[int64]time.Time{},
		offlineRetry:  map[int64]offlineRetryState{},
		now:           time.Now,
	}
}

// SetSleepNotifier wires the mobile-lifecycle one-shot sleep notifier. Must
// be called BEFORE Run. Nil disables the sleep-info path even if MobileLifecycle
// is true; the watcher then silently drops mobile staleness in that mode
// (acceptable: static users still get full coverage).
func (w *Watcher) SetSleepNotifier(s SleepNotifier) {
	w.sleep = s
}

// ScanForTest exposes scan() to integration tests in sibling packages
// (cmd/backend/...). Not used by production code.
func (w *Watcher) ScanForTest(ctx context.Context) { w.scan(ctx) }

// SetNow overrides the wall-clock seam for deterministic tests. Production
// callers must not use this; it must be called BEFORE Run starts so the
// production scan() loop sees a stable clock pointer.
func (w *Watcher) SetNow(fn func() time.Time) {
	if fn == nil {
		w.now = time.Now
		return
	}
	w.now = fn
}

// MarkResumed records that the user's agent reported a Resumed=true tick.
// While the resume mark is younger than cfg.ResumeGrace, the watcher will
// skip OFFLINE notices for that user.
func (w *Watcher) MarkResumed(userID int64) {
	t := w.now()
	w.mu.Lock()
	w.resumed[userID] = t
	w.mu.Unlock()
}

func (w *Watcher) Run(ctx context.Context) {
	w.wg.Add(1)
	defer w.wg.Done()
	slog.Info("heartbeat: watcher started",
		"scan_every", w.cfg.ScanEvery,
		"stale_static", w.cfg.staleFor(db.User{Kind: db.KindStatic}),
		"send_timeout", w.sendTimeout())
	// Выход из цикла возможен только по отмене корневого контекста, то есть
	// при остановке backend'а. Если строка появится в логе в другой момент --
	// это и есть объяснение молчащего мониторинга, а не «в парке всё хорошо».
	defer slog.Warn("heartbeat: watcher loop exited", "err", ctx.Err())
	w.scan(ctx)
	t := time.NewTicker(w.cfg.ScanEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.scan(ctx)
		}
	}
}

func (w *Watcher) WaitForExit() { w.wg.Wait() }

// LastOfflineError -- последняя неудачная отправка «роутер не на связи»: имя
// роутера, текст ошибки без секретов и время. Пустые значения -- ни одна
// отправка ещё не падала. Запись не сбрасывается удачной отправкой: время
// рядом с last_scan_at само говорит, свежая это беда или вчерашняя.
func (w *Watcher) LastOfflineError() (nickname, errText string, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastOfflineErrRouter, w.lastOfflineErrText, w.lastOfflineErrAt
}

// maxOfflineErrorRunes -- потолок текста ошибки в сводке. Ответ Telegram бывает
// длинным, а панели нужна причина, не весь ответ.
const maxOfflineErrorRunes = 300

var (
	// Токен бота в URL Telegram API: bot<id>:<secret>.
	reBotToken = regexp.MustCompile(`bot\d+:[A-Za-z0-9_-]+`)
	// Любой token=... -- в query-строке, в тексте ошибки, где угодно.
	reTokenParam = regexp.MustCompile(`(?i)(token=)[^\s&"';,)]+`)
)

// redactSecrets готовит текст ошибки к показу в панели: ошибка отправки может
// нести URL Telegram API вместе с токеном бота, а сводка уходит за пределы
// машины. Маскирует токены и обрезает до maxOfflineErrorRunes символов -- по
// рунам, чтобы не разрезать русскую букву пополам.
func redactSecrets(s string) string {
	s = reBotToken.ReplaceAllString(s, "bot<redacted>")
	s = reTokenParam.ReplaceAllString(s, "${1}<redacted>")
	if r := []rune(s); len(r) > maxOfflineErrorRunes {
		s = string(r[:maxOfflineErrorRunes])
	}
	return s
}

func (w *Watcher) scan(ctx context.Context) {
	started := time.Now()
	staleSeen := 0
	suppressed := 0
	// Счётчики пишутся и на аварийном выходе тоже: обход, упавший на чтении
	// БД, -- это обход, который состоялся и ничего не увидел, и наблюдателю
	// важно отличать его от обхода, которого не было вовсе.
	defer func() {
		metricScans.Add(1)
		metricLastScanUnix.Set(w.now().Unix())
		metricScanMillis.Set(time.Since(started).Milliseconds())
		metricStaleUsers.Set(int64(staleSeen))
		metricSuppressed.Set(int64(suppressed))
		if d := time.Since(started); w.cfg.ScanEvery > 0 && d > w.cfg.ScanEvery {
			slog.Warn("heartbeat: scan slower than its own interval",
				"took", d, "scan_every", w.cfg.ScanEvery)
		}
	}()
	users, err := w.d.Users().GetAll()
	if err != nil {
		slog.Warn("heartbeat scan: list users", "err", err)
		return
	}
	// One GROUP BY query instead of N per-user MAX(ts) queries (PERF-03/DB-09).
	// On lookup failure, fall back to per-user query so the scan still produces
	// signal — degraded mode beats silent skip.
	latestByUID, err := w.d.Events().LatestPerUserAll()
	if err != nil {
		slog.Warn("heartbeat scan: bulk latest lookup failed; falling back to per-user", "err", err)
		latestByUID = nil
	}
	for _, u := range users {
		var latest time.Time
		if latestByUID != nil {
			latest = latestByUID[u.ID]
		} else {
			lp, perr := w.d.Events().LatestPerUser(u.ID)
			if perr != nil {
				slog.Warn("heartbeat: latest event lookup failed", "user_id", u.ID, "nickname", u.Nickname, "err", perr)
				continue
			}
			latest = lp
		}
		if latest.IsZero() {
			// User never reported (agent stuck during install, never deployed,
			// or was just added). Treat CreatedAt as the "earliest possible
			// heartbeat" so the downstream stale check still gives a grace
			// window equal to staleFor(u) — without this, a freshly-added
			// router that fails to start would silently stay invisible.
			if u.CreatedAt.IsZero() {
				continue
			}
			latest = u.CreatedAt
		}
		// Refresh `now` per-user so a long scan loop doesn't render stale durations (BUG-19).
		now := w.now()
		stale := now.Sub(latest)
		threshold := w.cfg.staleFor(u)
		if stale < threshold {
			w.mu.Lock()
			delete(w.sleepNotified, u.ID)
			delete(w.offlineRetry, u.ID)
			// Eager-expire resumed entries for fresh users too. Previously
			// only the stale branch could evict, so a router that boots
			// (MarkResumed), reports normally, then stays online forever
			// kept its resumed entry forever. For long-running fleets the
			// map grew unbounded.
			if rt, ok := w.resumed[u.ID]; ok && now.Sub(rt) >= w.cfg.ResumeGrace {
				delete(w.resumed, u.ID)
			}
			w.mu.Unlock()
			w.clearOfflineStateIfHard(ctx, u)
			if u.IsMobile() && w.cfg.MobileLifecycle {
				if err := w.d.KV().ClearMobileSleepNotified(u.ID); err != nil {
					slog.Warn("heartbeat: clear mobile sleep state failed", "user_id", u.ID, "nickname", u.Nickname, "err", err)
				}
			}
			continue
		}
		staleSeen++
		w.mu.Lock()
		// Resumed-suppression: skip if we just received a resumed report.
		if rt, ok := w.resumed[u.ID]; ok {
			if now.Sub(rt) < w.cfg.ResumeGrace {
				w.mu.Unlock()
				continue
			}
			// Mark expired — drop it so the map doesn't grow unbounded.
			delete(w.resumed, u.ID)
		}
		w.mu.Unlock()

		// Mobile lifecycle: one-shot sleep info, no HARD, no renotify.
		if u.IsMobile() && w.cfg.MobileLifecycle {
			w.mu.Lock()
			_, already := w.sleepNotified[u.ID]
			if !already {
				w.sleepNotified[u.ID] = now
			}
			w.mu.Unlock()
			if !already {
				if notifiedAt, ok, err := w.d.KV().GetMobileSleepNotifiedAt(u.ID); err != nil {
					slog.Warn("heartbeat: read mobile sleep state failed", "user_id", u.ID, "nickname", u.Nickname, "err", err)
				} else if ok && !notifiedAt.Before(latest) {
					already = true
				}
			}
			if already || w.sleep == nil {
				continue
			}
			sleepCtx, cancelSleep := context.WithTimeout(ctx, w.sendTimeout())
			err := w.sleep.SendSleeping(sleepCtx, u.ID, u.Nickname, latest)
			cancelSleep()
			if err != nil {
				slog.Warn("heartbeat: send sleeping failed", "user_id", u.ID, "nickname", u.Nickname, "err", err)
				// Reset so a future scan retries the one-shot.
				w.mu.Lock()
				delete(w.sleepNotified, u.ID)
				w.mu.Unlock()
			} else {
				metricSleepSent.Add(1)
				if err := w.d.KV().SetMobileSleepNotifiedAt(u.ID, latest); err != nil {
					slog.Warn("heartbeat: persist mobile sleep state failed", "user_id", u.ID, "nickname", u.Nickname, "err", err)
				}
			}
			continue
		}

		// Legacy HARD-OFFLINE path (static + mobile with MobileLifecycle=false).
		// «Уже предупредил» -- строка agent_heartbeat в базе, не память
		// сторожа: после рестарта карта пуста, а тревога давно ушла (BOT-05).
		st, err := w.d.State().Get(u.ID, "agent_heartbeat")
		if err != nil {
			// Не знаем, предупреждали ли: повторная полная тревога хуже
			// паузы в 30 с до следующего обхода.
			slog.Warn("heartbeat: read offline state failed", "user_id", u.ID, "nickname", u.Nickname, "err", err)
			continue
		}
		if st.CurrentStatus == "hard" {
			// Тревога уже ушла. Напоминания -- дело realert, с затуханием.
			continue
		}
		if st.Acked {
			suppressed++
			slog.Info("heartbeat: offline notice suppressed by operator",
				"user_id", u.ID, "nickname", u.Nickname, "stale", stale)
			continue
		}
		w.mu.Lock()
		retry, failedBefore := w.offlineRetry[u.ID]
		w.mu.Unlock()
		if failedBefore && now.Before(retry.next) {
			continue // прошлая попытка упала -- ждём паузу
		}
		sendCtx, cancelSend := context.WithTimeout(ctx, w.sendTimeout())
		err = w.off.SendOffline(sendCtx, u.ID, u.Nickname, stale)
		cancelSend()
		if err != nil {
			metricOfflineErrors.Add(1)
			slog.Warn("heartbeat: send offline failed", "user_id", u.ID, "nickname", u.Nickname, "stale", stale, "err", err)
			// Отметку не пишем -- тревогу повторит обход после паузы.
			//
			// Причину запоминаем для сводки панели: счётчик ошибок говорит «что-то
			// падает», но не что именно, а docker-лог Pi удалённо недоступен.
			w.mu.Lock()
			retry.fails++
			retry.next = now.Add(offlineRetryPause(retry.fails))
			w.offlineRetry[u.ID] = retry
			w.lastOfflineErrRouter = u.Nickname
			w.lastOfflineErrText = redactSecrets(err.Error())
			w.lastOfflineErrAt = now
			w.mu.Unlock()
			continue
		}
		w.mu.Lock()
		delete(w.offlineRetry, u.ID)
		w.mu.Unlock()
		// Точечная запись: тишину и «понятно», нажатые за время отправки, не
		// трогает.
		if err := w.d.State().MarkHard(u.ID, "agent_heartbeat", latest, now); err != nil {
			slog.Error("heartbeat: offline notice sent but not recorded", "user_id", u.ID, "nickname", u.Nickname, "err", err)
		}
		metricOfflineSent.Add(1)
		slog.Info("heartbeat: router offline notice sent", "user_id", u.ID, "nickname", u.Nickname, "stale", stale)
	}
}

// clearOfflineStateIfHard гасит след прошлого молчания, когда роутер снова на
// связи.
//
// Условие «только если статус hard» когда-то казалось достаточным, но отметка
// «вижу проблему» живёт дольше самого состояния: инцидент мог быть закрыт
// другой веткой, а ack остаться. Роутер возвращался с непогашенной отметкой --
// и тревог по нему больше не приходило НИКОГДА, потому что offline-ветка
// молча пропускала заглушённых. Именно так боевой роутер молчал сутки, а
// сторож считал это послушанием.
//
// «Понятно, вижу» обещает напомнить ПОСЛЕ восстановления, поэтому возвращение
// роутера снимает отметку независимо от того, что записано в статусе.
func (w *Watcher) clearOfflineStateIfHard(ctx context.Context, u db.User) {
	userID := u.ID
	st, err := w.d.State().Get(userID, "agent_heartbeat")
	if err != nil {
		slog.Warn("heartbeat: read offline recovery state failed", "user_id", userID, "err", err)
		return
	}
	// Только ack: он обещает «напомню после восстановления» и обязан гаснуть
	// с возвращением роутера. Тишина -- другое обещание, с явным сроком
	// («тихо до утра»), и обрывать её на первом же ответе роутера значило бы
	// нарушить ровно то, о чём просил оператор.
	suppressionLeftOver := st.Acked || st.AckedUntil != nil
	if st.CurrentStatus != "hard" && !suppressionLeftOver {
		return
	}
	wasHard := st.CurrentStatus == "hard"
	if !wasHard {
		slog.Info("heartbeat: снимаем заглушку с вернувшегося роутера",
			"user_id", userID, "status", st.CurrentStatus, "acked", st.Acked)
	}
	hardSince := w.now()
	if st.HardSince != nil {
		hardSince = *st.HardSince
	}
	st.CurrentStatus = "ok"
	st.ConsecutiveFails = 0
	st.ConsecutiveOKs = 1
	st.HardSince = nil
	st.LastAlertMsgID = nil
	st.LastAlertAt = nil
	st.Acked = false
	st.AckedUntil = nil
	// Сначала строка в ok, потом сообщение -- как у Recovery в диспетчере:
	// следующий обход уже не увидит hard и второго «снова на связи» не
	// пришлёт. Не дошло никому -- долг повторит обход напоминаний.
	if err := w.d.State().Save(userID, "agent_heartbeat", st); err != nil {
		slog.Warn("heartbeat: clear offline recovery state failed", "user_id", userID, "err", err)
		return
	}
	if !wasHard || w.off == nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, w.sendTimeout())
	err = w.off.SendBackOnline(sendCtx, userID, u.Nickname, hardSince)
	cancel()
	if err != nil {
		slog.Warn("heartbeat: «роутер снова на связи» не отправлено", "user_id", userID, "nickname", u.Nickname, "err", err)
		return
	}
	slog.Info("heartbeat: router back online notice sent", "user_id", userID, "nickname", u.Nickname)
}
