package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type TGSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup *tg.InlineKeyboardMarkup) (int64, error)
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
}

// notifySink -- та часть notify.Fanout, которой пользуется диспетчер.
// Интерфейсом, а не конкретным типом: тесты подставляют счётчик отправок и не
// поднимают ради этого настоящую рассылку.
type notifySink interface {
	Send(ctx context.Context, routerUserID int64, text, parseMode string) (int, error)
	SendTracked(ctx context.Context, routerUserID int64, checkName, text, parseMode string, kb *tg.InlineKeyboardMarkup, silent bool) (int, error)
	ReplyToEach(ctx context.Context, routerUserID int64, checkName, text, parseMode string, silent bool) error
}

type Config struct {
	FailThreshold     int
	RecoveryThreshold int
	// MiniAppBaseURL, when non-empty, adds an "Open in app" web_app button to
	// HARD alert cards deep-linking into the Telegram Mini App for this
	// router. Empty disables the button entirely (unchanged behaviour).
	MiniAppBaseURL string
	// AdminUserID -- Telegram-номер админа, 0 -- не настроен. Админ получает
	// все уведомления по всем роутерам (решение оператора 15.09) и под каждым
	// -- кнопку выключения по роутеру (notify.Fanout).
	AdminUserID int64
}

const NeighborFreshWindow = 5 * time.Minute

type Dispatcher struct {
	d   *db.DB
	tg  TGSender
	cfg Config
	// now is the injectable wall-clock seam (mirrors heartbeat.Watcher /
	// realert.Poller / digest.Poller): production leaves it as time.Now,
	// tests override via SetNow for deterministic HARD/offline timestamps.
	now func() time.Time
	// notify -- рассылка по личкам получателей. Заменила адресацию в тему
	// группы: у роутера несколько получателей, и у каждого свой чат.
	notify notifySink

	// autoHint -- включена ли у проверки автопочинка (строка в тревоге);
	// nil -- не спрашивать. covered -- одноразовый вопрос при
	// «восстановилось»: итог скажет (или сказала) починка, ответ не нужен;
	// nil -- никогда. onHard -- новая HARD-тревога: прежнее закрытие
	// починкой к ней не относится.
	autoHint func(userID int64, checkName string) bool
	covered  func(userID int64, checkName string) bool
	onHard   func(userID int64, checkName string)

	// lastHard -- текст и кнопки последней HARD-тревоги по проверке. Только
	// в памяти: правка Telegram заменяет текст целиком, и починке, чтобы
	// дописать свой блок, нужен исходный текст (notify.AlertTexts). После
	// перезапуска памяти нет -- починка шлёт итог новым сообщением.
	mu       sync.Mutex
	lastHard map[hardKey]hardAlert
}

type hardKey struct {
	userID    int64
	checkName string
}

type hardAlert struct {
	text string
	kb   tg.InlineKeyboardMarkup
}

// SetAutoRepairHint -- откуда диспетчер знает, что у проверки включена
// автопочинка. Вызывается до первой тревоги.
func (di *Dispatcher) SetAutoRepairHint(fn func(userID int64, checkName string) bool) {
	di.autoHint = fn
}

// SetCovered -- откуда диспетчер знает, что «восстановилось» скажет (или
// сказала) починка правкой тревоги (notify.Repairs.TakeCovered). Вопрос
// одноразовый: ответ true гасит метку. Вызывается до первой тревоги.
func (di *Dispatcher) SetCovered(fn func(userID int64, checkName string) bool) {
	di.covered = fn
}

// SetHardHook -- что сделать при каждой новой HARD-тревоге до её отправки
// (notify.Repairs.Uncover: снять устаревшее закрытие). Вызывается до первой
// тревоги.
func (di *Dispatcher) SetHardHook(fn func(userID int64, checkName string)) {
	di.onHard = fn
}

// Last -- текст и кнопки последней HARD-тревоги по проверке
// (notify.AlertTexts). Кнопки отдаются копией: правки не трогают память.
func (di *Dispatcher) Last(userID int64, checkName string) (string, *tg.InlineKeyboardMarkup, bool) {
	di.mu.Lock()
	defer di.mu.Unlock()
	a, ok := di.lastHard[hardKey{userID, checkName}]
	if !ok {
		return "", nil, false
	}
	rows := make([][]tg.InlineKeyboardButton, len(a.kb.InlineKeyboard))
	for i, r := range a.kb.InlineKeyboard {
		rows[i] = append([]tg.InlineKeyboardButton(nil), r...)
	}
	return a.text, &tg.InlineKeyboardMarkup{InlineKeyboard: rows}, true
}

func (di *Dispatcher) rememberHard(userID int64, checkName, text string, kb tg.InlineKeyboardMarkup) {
	di.mu.Lock()
	defer di.mu.Unlock()
	if di.lastHard == nil {
		di.lastHard = map[hardKey]hardAlert{}
	}
	di.lastHard[hardKey{userID, checkName}] = hardAlert{text: text, kb: kb}
}

// NoteAlertAction -- notify.AlertMemory: человек заглушил тревогу в боте
// или ответил на неё в приложении. Память тревоги дополняется строкой
// statusLine (пустая -- текст прежний) и теряет кнопки, как сама тревога в
// Telegram: нить починки правит уже от этого (BOT-06). Тревоги в памяти нет
// (перезапуск, «восстановилось») -- делать нечего.
func (di *Dispatcher) NoteAlertAction(userID int64, checkName, statusLine string) {
	di.mu.Lock()
	defer di.mu.Unlock()
	key := hardKey{userID, checkName}
	a, ok := di.lastHard[key]
	if !ok {
		return
	}
	if statusLine != "" {
		a.text += "\n\n" + statusLine
	}
	a.kb = tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	di.lastHard[key] = a
}

func (di *Dispatcher) forgetHard(userID int64, checkName string) {
	di.mu.Lock()
	defer di.mu.Unlock()
	delete(di.lastHard, hardKey{userID, checkName})
}

func NewDispatcher(d *db.DB, tgc TGSender, cfg Config) *Dispatcher {
	return &Dispatcher{
		d: d, tg: tgc, cfg: cfg, now: time.Now,
		notify: notify.NewFanout(d, tgc, slog.Default(), cfg.AdminUserID),
	}
}

// SetNotifySink подменяет рассылку. Только для тестов -- как SetNow.
func (di *Dispatcher) SetNotifySink(s notifySink) { di.notify = s }

// SetNow overrides the wall-clock seam for deterministic tests.
func (di *Dispatcher) SetNow(fn func() time.Time) {
	if fn == nil {
		di.now = time.Now
		return
	}
	di.now = fn
}

// Handle reacts to one FSM transition for one (user, check). The full
// wire.Check is passed (not just an extracted detail string) so the
// formatter can render rich per-category text from Details.
//
// Handle = Apply + доставка на месте. Отчёт роутера зовёт Apply и шлёт в
// фоне (FRESH-04); остальные -- Handle.
func (di *Dispatcher) Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, check wire.Check) error {
	deliver, err := di.Apply(userID, nickname, checkName, tr, check)
	if err != nil || deliver == nil {
		return err
	}
	return deliver(ctx)
}

// Apply -- синхронная часть Handle: автомат и отметки в базе. Возвращает
// доставку в Telegram (nil -- слать нечего), которую вызывающий запускает
// когда и где хочет. Запись автомата обязана случиться до ответа на отчёт:
// следующий отчёт читает её как prev, и отложенная запись дала бы дубль
// тревоги (BUG-02). Доставка с повторами длится до полуминуты, а агент ждёт
// ответа на отчёт 10 с (FRESH-04).
func (di *Dispatcher) Apply(userID int64, nickname, checkName string, tr state.Transition, check wire.Check) (func(ctx context.Context) error, error) {
	switch tr.Kind {
	case state.Noop, state.Soft:
		return nil, di.d.State().Save(userID, checkName, tr.Next)
	case state.SoftFlap:
		today := di.now().UTC().Format("2006-01-02")
		if err := di.d.State().IncSoftFlap(userID, checkName, today); err != nil {
			return nil, err
		}
		return nil, di.d.State().Save(userID, checkName, tr.Next)
	case state.Hard:
		// Save FSM transition FIRST, отправка в TG — после. Иначе при
		// 5xx/429 от TG возвращалась ошибка ДО Save, FSM оставался в
		// pre-Hard состоянии, следующий fail-репорт снова пересекал
		// порог → дубль алерта на каждый TG-глюк (BUG-02).
		// Первый Save несёт LastAlertAt=now от автомата: пока идёт отправка
		// (с повторами -- до полуминуты), напоминания эту тревогу не трогают.
		// Не дошла никому -- LastAlertAt снимается в NULL, и realert
		// доставляет её на ближайшем обходе (BOT-01), а не через 6 часов.
		next := tr.Next
		if di.onHard != nil {
			di.onHard(userID, checkName)
		}
		if err := di.d.State().Save(userID, checkName, next); err != nil {
			return nil, fmt.Errorf("save HARD state %s/%s: %w", nickname, checkName, err)
		}
		// Новая поломка: недоставленное «восстановилось» прошлой уже не к
		// месту.
		if err := di.d.OwedReplies().Delete(userID, checkName); err != nil {
			slog.Warn("не удалось снять долг «восстановилось»", "user_id", userID, "check", checkName, "err", err)
		}
		return func(ctx context.Context) error {
			if _, err := di.DeliverHard(ctx, userID, nickname, checkName, next, check); err != nil {
				return fmt.Errorf("HARD tg send %s/%s: %w", nickname, checkName, err)
			}
			return nil
		}, nil
	case state.Recovery:
		// Same ordering invariant как Hard: state-Save до TG-send. Если
		// TG отвалится, recovery фактически уже сохранён в FSM; следующий
		// OK-репорт превратится в Noop (state==ok), не в дубль Recovery.
		prev, prevErr := di.d.State().Get(userID, checkName)
		if prevErr != nil {
			slog.Warn("recovery: state.Get failed; rendering without HardSince", "user_id", userID, "check", checkName, "err", prevErr)
		}
		var hardSince time.Time
		if prev.HardSince != nil {
			hardSince = *prev.HardSince
		}
		next := tr.Next
		next.LastAlertMsgID = nil
		next.LastAlertAt = nil
		next.Acked = false // defensive (FSM also sets this in Recovery transition)
		if err := di.d.State().Save(userID, checkName, next); err != nil {
			return nil, fmt.Errorf("save Recovery state %s/%s: %w", nickname, checkName, err)
		}
		text := FormatRecovery(RecoveryArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			HardSince:   hardSince,
			RecoveredAt: di.now(),
			Check:       check,
		})
		return func(ctx context.Context) error {
			// Память тревоги забывается в доставке, а не в Apply: карточка
			// той же проверки, что ещё стоит в очереди перед этим ответом,
			// запомнит себя позже и иначе пережила бы «восстановилось».
			di.forgetHard(userID, checkName)
			return di.deliverRecovery(ctx, userID, nickname, checkName, text, next, prev, prevErr)
		}, nil
	}
	return nil, nil
}

// deliverRecovery -- доставка «восстановилось» после записи автомата в Apply.
func (di *Dispatcher) deliverRecovery(ctx context.Context, userID int64, nickname, checkName, text string, next, prev db.IncidentState, prevErr error) error {
	// Починка идёт или уже дописала «Починил» в саму тревогу -- второе
	// «восстановилось» ответом было бы шумом. Вопрос одноразовый.
	if di.covered != nil && di.covered(userID, checkName) {
		slog.Info("«восстановилось» не шлём: итог починки уже в тревоге",
			"user_id", userID, "check", checkName)
	} else if err := di.notify.ReplyToEach(ctx, userID, checkName, text, "", Silenced(next, di.now())); err != nil {
		// Не дошло никому: автомат уже в ok, напоминаний не будет.
		// Долг повторит обход напоминаний (realert) ответом на ту же
		// тревогу -- поэтому переписку не забываем. Саму тревогу
		// человек не получал -- долга нет (M-7).
		if !di.hardNeverDelivered(userID, checkName, prev, prevErr) {
			if putErr := di.d.OwedReplies().Put(userID, checkName, text, di.now()); putErr != nil {
				slog.Error("не удалось запомнить недоставленное «восстановилось»",
					"user_id", userID, "check", checkName, "err", putErr)
			}
		} else {
			slog.Info("«восстановилось» не дошло, тревога тоже не доходила: долга нет",
				"user_id", userID, "check", checkName)
		}
		return fmt.Errorf("recovery tg send %s/%s: %w", nickname, checkName, err)
	}
	// Переписка по этой проверке закончилась: следующая поломка начнёт
	// свою ветку с чистого листа.
	if err := di.d.AlertMessages().Clear(userID, checkName); err != nil {
		slog.Warn("не удалось забыть переписку по проверке",
			"user_id", userID, "check", checkName, "err", err)
	}
	return nil
}

// Silenced -- действует ли «Тише на час» по проверке: тревога и
// «восстановилось» тогда уходят без звука (BOT-02). Тишину ставит кнопка под
// тревогой; автомат тревог переносит её через «восстановилось», так что
// флап внутри часа тоже приходит тихо.
func Silenced(st db.IncidentState, now time.Time) bool {
	return st.SilencedUntil != nil && st.SilencedUntil.After(now)
}

// DeliverHard -- отправить карточку HARD-тревоги по строке st, которая уже
// сохранена как hard: рассылка с записью в alert_messages, память текста для
// нити починки, отметка last_alert_at. Зовут Handle при пересечении порога и
// realert, когда первая тревога не дошла никому (last_alert_at NULL): человек
// получает саму тревогу, а не «Всё ещё … #0», и «восстановилось» ответит на
// неё (BOT-01).
//
// Отметка пишется точечным UPDATE, а не Save всей строки: отправка с
// повторами длится до полуминуты, и за это время человек мог нажать «Тише
// на час» -- снимок st затёр бы тишину. Не дошло никому и это известно
// наверняка (Telegram отказал, запрос не ушёл) -- last_alert_at снимается в
// NULL (realert повторит на ближайшем обходе). Могло дойти (таймаут ответа,
// срок контекста) -- отметка ставится, дальше обычное напоминание: иначе
// при медленном Telegram карточка дублировалась бы каждые 5 минут (I-2).
// Ошибка возвращается в обоих случаях.
func (di *Dispatcher) DeliverHard(ctx context.Context, userID int64, nickname, checkName string, st db.IncidentState, check wire.Check) (int, error) {
	if st.HardSince == nil {
		return 0, fmt.Errorf("HARD %s/%s: нет hard_since", nickname, checkName)
	}
	args := HardArgs{
		Nickname:    nickname,
		CheckName:   checkName,
		ConsecFails: st.ConsecutiveFails,
		HardSince:   *st.HardSince,
		Check:       check,
	}
	if di.autoHint != nil {
		args.AutoRepair = di.autoHint(userID, checkName)
	}
	if u, err := di.d.Users().GetByID(userID); err == nil {
		args.IsMobile = u.IsMobile()
	} else {
		slog.Warn("dispatch HARD: user lookup failed; mobile-badge omitted", "user_id", userID, "err", err)
	}
	// Tunnel and DNS checks get tunnel context so the operator can see
	// whether this is one tunnel flapping or the whole router/WAN.
	if strings.HasPrefix(checkName, "tunnel_") || checkName == "dns" {
		args.Neighbors = di.collectNeighbors(userID, checkName)
	}
	if checkName == "dns_ru" {
		args.Neighbors = di.dnsRuNeighbors(userID)
	}
	if checkName == BypassLeakCheck {
		args.Neighbors = di.collectNeighbors(userID, NeighborExclude(checkName, check.Details))
	}
	text := FormatHard(args)
	// Под тревогой ровно две кнопки: открыть роутер в приложении (когда
	// база настроена) и отложить эту проверку на час. Командные кнопки
	// (restart/diag/pingcheck/force_recheck/maint_restart) переехали в
	// приложение; обработчики в callbacks/ остаются для старых сообщений.
	appURL := tg.MiniAppRouterURL(di.cfg.MiniAppBaseURL, userID)
	kb := tg.AlertKeyboard(userID, checkName, appURL)
	delivered, err := di.notify.SendTracked(ctx, userID, checkName, text, "", &kb, Silenced(st, di.now()))
	if err != nil {
		if notify.MaybeDelivered(err) {
			// Таймаут ответа или срок контекста: карточка могла дойти.
			// Отметка ставится, как при доставке -- дальше обычное
			// напоминание по расписанию, а не полная карточка на каждом
			// обходе realert (I-2).
			if bErr := di.d.State().BumpLastAlertAt(userID, checkName, di.now()); bErr != nil {
				slog.Error("HARD: не удалось поставить отметку отправки", "user_id", userID, "check", checkName, "err", bErr)
			}
			return 0, err
		}
		if clrErr := di.d.State().ClearLastAlertAt(userID, checkName); clrErr != nil {
			slog.Error("HARD: не удалось снять отметку отправки", "user_id", userID, "check", checkName, "err", clrErr)
		}
		return 0, err
	}
	di.rememberHard(userID, checkName, text, kb)
	if delivered == 0 {
		// Слать некому: владелец не привязан, операторы не заведены либо
		// всем недоставимо. Тревога не теряется -- её видно в сводке
		// дашборда; здесь только след в логе.
		slog.Warn("тревогу некому доставить",
			"user_id", userID, "nickname", nickname, "check", checkName)
	}
	// Кому какое сообщение ушло, помнит alert_messages; здесь только
	// отметка времени -- по ней realert решает, пора ли напомнить.
	if err := di.d.State().BumpLastAlertAt(userID, checkName, di.now()); err != nil {
		return delivered, fmt.Errorf("HARD отметка отправки %s/%s: %w", nickname, checkName, err)
	}
	return delivered, nil
}

// hardNeverDelivered -- карточка тревоги заведомо не дошла никому: отметки
// отправки нет (DeliverHard снимает её только при «заведомо не отправлено»)
// и в alert_messages ни одного сообщения. Долг «восстановилось» по такой
// тревоге сообщил бы человеку о починке того, о чём он не знал (M-7).
// Ошибка чтения -- «не знаем», долг остаётся, как раньше.
func (di *Dispatcher) hardNeverDelivered(userID int64, checkName string, prev db.IncidentState, prevErr error) bool {
	if prevErr != nil || prev.LastAlertAt != nil {
		return false
	}
	msgs, err := di.d.AlertMessages().List(userID, checkName)
	return err == nil && len(msgs) == 0
}

// collectNeighbors returns short summaries of the user's other tunnel checks.
// On any DB error we silently return nil — neighbour context is decoration,
// not the load-bearing payload, and we'd rather send a slightly thinner
// alert than no alert at all.
func (di *Dispatcher) collectNeighbors(userID int64, excludeCheck string) []NeighborSummary {
	rows, err := di.d.Events().LatestEventsByPrefixSince(userID, "tunnel_", di.now().Add(-NeighborFreshWindow))
	if err != nil {
		slog.Warn("collectNeighbors: events lookup failed", "user_id", userID, "err", err)
		return nil
	}
	return BuildNeighborSummaries(rows, excludeCheck)
}

// BypassLeakCheck -- вердикт бэкенда «трафик мимо VPN-туннеля» (v0.56).
const BypassLeakCheck = "bypass_leak"

// NeighborExclude -- какую проверку tunnel_* не считать соседом тревоги: саму
// упавшую, а у bypass_leak -- несущий VPN-туннель (он жив, но адрес не
// меняет, и запасным его называть нельзя). Общая для тревоги и напоминания.
func NeighborExclude(checkName string, d map[string]any) string {
	if checkName == BypassLeakCheck {
		if id := strOrEmpty(d, "tunnel_id"); id != "" {
			return "tunnel_" + id
		}
	}
	return checkName
}

// dnsRuNeighbors -- сосед для тревоги dns_ru: общая проверка dns, если у
// роутера она сейчас в аварии. Ошибка чтения -- как нет аварии (украшение).
func (di *Dispatcher) dnsRuNeighbors(userID int64) []NeighborSummary {
	st, err := di.d.State().Get(userID, neighborDNSCheck)
	if err != nil {
		slog.Warn("dnsRuNeighbors: state lookup failed", "user_id", userID, "err", err)
		return nil
	}
	if st.CurrentStatus != "hard" {
		return nil
	}
	return []NeighborSummary{{CheckName: neighborDNSCheck, Status: "hard"}}
}

// BuildNeighborSummaries projects events.LatestEventsByPrefix rows into
// renderable []NeighborSummary, applying the ping_check_status override and
// JSON detail extraction that both dispatcher and realert.poller need
// identically (LOGIC-08). Single source of truth for "neighbour view".
func BuildNeighborSummaries(rows []db.EventRow, excludeCheck string) []NeighborSummary {
	var out []NeighborSummary
	for _, r := range rows {
		if r.CheckName == excludeCheck {
			continue
		}
		ns := NeighborSummary{CheckName: r.CheckName, Status: r.Status}
		if r.DetailsJSON != "" && r.DetailsJSON != "null" {
			var details map[string]any
			if err := json.Unmarshal([]byte(r.DetailsJSON), &details); err == nil {
				ns.TunnelName, _ = details["tunnel_name"].(string)
				ns.NDMSName, _ = details["ndms_name"].(string)
				ns.Interface, _ = details["interface"].(string)
				if pcStatus, ok := details["ping_check_status"].(string); ok && pcStatus != "" {
					ns.Status = pcStatus
				}
				if v, ok := details["handshake_age_sec"].(float64); ok {
					ns.HandshakeAge = int(v)
				}
			}
		}
		out = append(out, ns)
	}
	return out
}

// SendOffline -- первая тревога «Роутер не на связи» (зовёт сторож молчания).
// Состояние agent_heartbeat пишет сторож (MarkHard) после удачной отправки;
// повторяет realert коротким напоминанием (BOT-05). Под «Тише на час»,
// оставшейся от прошлого раза, -- без звука (как HARD, BOT-02).
func (di *Dispatcher) SendOffline(ctx context.Context, userID int64, nickname string, since time.Duration) error {
	silent := false
	if st, err := di.d.State().Get(userID, "agent_heartbeat"); err != nil {
		slog.Warn("«не на связи»: состояние не прочитано, шлём со звуком", "user_id", userID, "err", err)
	} else {
		silent = Silenced(st, di.now())
	}
	text := FormatRouterOffline(nickname, since)
	kb := tg.AlertKeyboard(userID, "agent_heartbeat", tg.MiniAppRouterURL(di.cfg.MiniAppBaseURL, userID))
	delivered, err := di.notify.SendTracked(ctx, userID, "agent_heartbeat", text, "", &kb, silent)
	if err != nil {
		return err
	}
	if delivered == 0 {
		// Слать некому: владелец не привязан, операторов нет либо все
		// недоступны. Как и у HARD-тревоги, это не сбой отправки -- сторож не
		// должен повторять её на каждом обходе; видно это в сводке дашборда.
		slog.Warn("«роутер не на связи» некому доставить",
			"user_id", userID, "nickname", nickname)
	}
	return nil
}

// SendBackOnline -- «Роутер снова на связи» ответом на тревогу «Роутер не на
// связи» (BOT-03): сторож уже перевёл строку agent_heartbeat в ok. Под «Тише
// на час» -- без звука. Не дошло никому -- текст ложится в owed_replies, и
// realert повторит его ответом на ту же тревогу; переписку тогда не
// забываем. Дошло -- переписка по молчанию закончена.
func (di *Dispatcher) SendBackOnline(ctx context.Context, userID int64, nickname string, hardSince time.Time) error {
	const check = "agent_heartbeat"
	now := di.now()
	silent := false
	if st, err := di.d.State().Get(userID, check); err != nil {
		slog.Warn("«снова на связи»: состояние не прочитано, шлём со звуком", "user_id", userID, "err", err)
	} else {
		silent = Silenced(st, now)
	}
	text := FormatRecovery(RecoveryArgs{
		Nickname:    nickname,
		CheckName:   check,
		HardSince:   hardSince,
		RecoveredAt: now,
	})
	if err := di.notify.ReplyToEach(ctx, userID, check, text, "", silent); err != nil {
		if putErr := di.d.OwedReplies().Put(userID, check, text, now); putErr != nil {
			slog.Error("не удалось запомнить недоставленное «снова на связи»",
				"user_id", userID, "err", putErr)
		}
		return fmt.Errorf("«снова на связи» %s: %w", nickname, err)
	}
	if err := di.d.AlertMessages().Clear(userID, check); err != nil {
		slog.Warn("не удалось забыть переписку о молчании роутера", "user_id", userID, "err", err)
	}
	return nil
}

// Диспетчер -- память тревог для нити починки.
var (
	_ notify.AlertTexts  = (*Dispatcher)(nil)
	_ notify.AlertMemory = (*Dispatcher)(nil)
)
