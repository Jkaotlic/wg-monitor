package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
	CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (int64, error)
}

// notifySink -- та часть notify.Fanout, которой пользуется диспетчер.
// Интерфейсом, а не конкретным типом: тесты подставляют счётчик отправок и не
// поднимают ради этого настоящую рассылку.
type notifySink interface {
	Send(ctx context.Context, routerUserID int64, text, parseMode string) (int, error)
	SendTracked(ctx context.Context, routerUserID int64, checkName, text, parseMode string, kb *tg.InlineKeyboardMarkup) (int, error)
	ReplyToEach(ctx context.Context, routerUserID int64, checkName, text, parseMode string) error
}

type Config struct {
	ChatID            int64
	FailThreshold     int
	RecoveryThreshold int
	// MiniAppBaseURL, when non-empty, adds an "Open in app" web_app button to
	// HARD alert cards deep-linking into the Telegram Mini App for this
	// router. Empty disables the button entirely (unchanged behaviour).
	MiniAppBaseURL string
}

// miniAppRouterURL -- адрес экрана роутера в приложении. Один на тревоги и
// отчёт о пробуждении: кнопка «Открыть в приложении» обязана вести в одно
// и то же место, откуда бы её ни нажали.
func miniAppRouterURL(base string, userID int64) string {
	return fmt.Sprintf("%s/miniapp/?router=%d", strings.TrimRight(base, "/"), userID)
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
}

func NewDispatcher(d *db.DB, tgc TGSender, cfg Config) *Dispatcher {
	return &Dispatcher{
		d: d, tg: tgc, cfg: cfg, now: time.Now,
		notify: notify.NewFanout(d, tgc, slog.Default()),
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
func (di *Dispatcher) Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, check wire.Check) error {
	switch tr.Kind {
	case state.Noop, state.Soft:
		return di.d.State().Save(userID, checkName, tr.Next)
	case state.SoftFlap:
		today := di.now().UTC().Format("2006-01-02")
		if err := di.d.State().IncSoftFlap(userID, checkName, today); err != nil {
			return err
		}
		return di.d.State().Save(userID, checkName, tr.Next)
	case state.Hard:
		// Save FSM transition FIRST, отправка в TG — после. Иначе при
		// 5xx/429 от TG возвращалась ошибка ДО Save, FSM оставался в
		// pre-Hard состоянии, следующий fail-репорт снова пересекал
		// порог → дубль алерта на каждый TG-глюк (BUG-02).
		// LastAlertMsgID/LastAlertAt пишем вторым save'ом только при
		// успехе TG-send. Если TG упал — FSM корректно в HARD, но
		// LastAlertAt=NULL значит realert poller его не подхватит до
		// следующего ручного refresh / OK-репорта.
		next := tr.Next
		if err := di.d.State().Save(userID, checkName, next); err != nil {
			return fmt.Errorf("save HARD state %s/%s: %w", nickname, checkName, err)
		}
		args := HardArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			ConsecFails: tr.Next.ConsecutiveFails,
			HardSince:   *tr.Next.HardSince,
			Check:       check,
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
		text := FormatHard(args)
		// Per-category command-channel buttons:
		// - tunnel_* checks → restart/diag/pingcheck (awg-manager actions on a tunnel)
		// - mobile-router heartbeat → force_recheck (poke a 4G router into a fresh report)
		var opts []tg.KeyboardOption
		if strings.HasPrefix(checkName, "tunnel_") {
			opts = append(opts, tg.WithTunnelActions())
		}
		if checkName == "hydraroute" {
			opts = append(opts, tg.WithHydraRouteActions())
		}
		if args.IsMobile && checkName == "agent_heartbeat" {
			opts = append(opts, tg.WithMobileActions())
		}
		if di.cfg.MiniAppBaseURL != "" {
			opts = append(opts, tg.WithWebAppButton(miniAppRouterURL(di.cfg.MiniAppBaseURL, userID)))
		}
		kb := tg.HardAlertKeyboard(userID, checkName, opts...)
		delivered, err := di.notify.SendTracked(ctx, userID, checkName, text, "", &kb)
		if err != nil {
			return fmt.Errorf("HARD tg send %s/%s: %w", nickname, checkName, err)
		}
		if delivered == 0 {
			// Слать некому: владелец не привязан, операторы не заведены либо
			// всем недоставимо. Тревога не теряется -- её видно в сводке
			// дашборда; здесь только след в логе.
			slog.Warn("тревогу некому доставить",
				"user_id", userID, "nickname", nickname, "check", checkName)
		}
		// LastAlertMsgID больше не источник правды: получателей несколько, и
		// кому какое сообщение ушло, помнит таблица alert_messages. Здесь
		// остаётся только отметка времени -- по ней realert решает, пора ли
		// напомнить.
		const noSingleMsgID = 0
		now := di.now()
		next.LastAlertAt = &now
		if err := di.d.State().Save(userID, checkName, next); err != nil {
			// Full Save failed (DB momentarily locked / disk error). The HARD
			// row was persisted earlier (line 75), so the FSM is correct, but
			// LastAlertAt is still NULL — the realert poller would now fire
			// on every tick instead of every RealertEvery. Targeted UPDATE
			// just for last_alert_at + last_alert_msg_id is a smaller
			// transaction more likely to succeed in this window.
			slog.Error("HARD save failed; falling back to SetLastAlert",
				"nickname", nickname, "check", checkName, "err", err)
			if fbErr := di.d.State().SetLastAlert(userID, checkName, noSingleMsgID, now); fbErr != nil {
				return fmt.Errorf("HARD save fallback %s/%s: %w (orig: %v)", nickname, checkName, fbErr, err)
			}
		}
		return nil
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
			return fmt.Errorf("save Recovery state %s/%s: %w", nickname, checkName, err)
		}
		text := FormatRecovery(RecoveryArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			HardSince:   hardSince,
			RecoveredAt: di.now(),
			Check:       check,
		})
		if err := di.notify.ReplyToEach(ctx, userID, checkName, text, ""); err != nil {
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
	return nil
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

// SendOffline sends a ROUTER OFFLINE notice (used by the heartbeat watcher).
func (di *Dispatcher) SendOffline(ctx context.Context, userID int64, nickname string, since time.Duration) error {
	now := di.now()
	hardSince := now.Add(-since)
	text := FormatRouterOffline(nickname, since)
	opts := []tg.KeyboardOption{}
	if u, err := di.d.Users().GetByID(userID); err == nil && u != nil && u.IsMobile() {
		opts = append(opts, tg.WithMobileActions())
	}
	kb := tg.HardAlertKeyboard(userID, "agent_heartbeat", opts...)
	if _, err := di.notify.SendTracked(ctx, userID, "agent_heartbeat", text, "", &kb); err != nil {
		return err
	}
	return di.d.State().Save(userID, "agent_heartbeat", db.IncidentState{
		UserID:           userID,
		CheckName:        "agent_heartbeat",
		ConsecutiveFails: 1,
		CurrentStatus:    "hard",
		HardSince:        &hardSince,
		// LastAlertMsgID не заполняем: получателей несколько, и кому какое
		// сообщение ушло, помнит таблица alert_messages.
		LastAlertAt: &now,
	})
}
