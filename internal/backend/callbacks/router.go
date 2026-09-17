package callbacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// TGClient is the subset of tg.Client used by the router.
type TGClient interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error
	GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error)
	GetFile(ctx context.Context, fileID string) (string, error)
	DownloadFile(ctx context.Context, filePath string) ([]byte, error)
	// CreateForumTopic — used by admin slash-commands (/ensure_topics,
	// /recreate_topic) to provision topics on demand from inside Telegram
	// rather than via the wg-monitor-cli on the VPS.
	CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (int64, error)
}

type Config struct {
	ChatID             int64
	ExtraChatIDs       []int64
	AdminUserID        int64
	MuteCutoffHour     int
	BackendVersion     string
	PublicBaseURL      string
	UI                 UIConfigSnapshot
	AmneziaBaseURL     string
	AmneziaSecretsPath string
	HideMyBaseURL      string
	HideMySecretsPath  string
}

// UIConfigSnapshot mirrors backend.UIConfig (avoid an import cycle).
// Bool fields here are values, not pointers — caller (cmd/backend/main.go)
// dereferences backend.UIConfig.*bool fields when building the snapshot.
type UIConfigSnapshot struct {
	DeleteUserCommandMessages bool
	SmartReplyWithKeyboard    bool
	DiagMaxChars              int
	// CompatInlineKeyboard mirrors backend.UIConfig.CompatInlineKeyboard.
	// When true, the router substitutes inline-keyboard buttons for the
	// persistent ReplyKeyboardMarkup on every bot reply (TG Desktop forum
	// topic workaround).
	CompatInlineKeyboard bool
}

// KeyboardForTopic picks between the native ReplyKeyboardMarkup and the
// CompatInlineKeyboardForTopic equivalent based on the operator's UI
// preference. Returned type matches what SendMessageWithReplyKeyboard
// expects (any of *ReplyKeyboardMarkup, *ReplyKeyboardRemove,
// *InlineKeyboardMarkup, or nil). Returns nil — meaning "no reply_markup
// at all" — when compat mode is on and the kind has no matching button
// set, so the bot doesn't accidentally clobber the persistent keyboard
// with an empty one.
func (ui UIConfigSnapshot) KeyboardForTopic(kind string) any {
	if ui.CompatInlineKeyboard {
		if kb := tg.CompatInlineKeyboardForTopic(kind); kb != nil {
			return kb
		}
		return nil
	}
	return tg.ReplyKeyboardForTopic(kind)
}

type Router struct {
	d        *db.DB
	tg       TGClient
	cfg      Config
	silence  *SilenceAction
	ack      *AckAction
	mute     *MuteAction
	history  *HistoryAction
	command  *CommandAction
	upstream *upstream.Cache // used by dispatchSmartReply for Updates section (M12)

	// diagCache stores raw diag_now result bodies so "📄 Полный отчёт"
	// inline-button taps can fetch the body without re-running the diagnostic.
	// Shared with the Notifier via DiagCache(). All in-memory; lost on restart.
	diagCache *diagCache

	// PingCheck panel plumbing.
	pingcheckOpenAct   Action
	pingcheckToggleAct Action
	pingcheckInflight  *pingcheckInflightStore

	// diag drill-down (C-drilldown).
	diagDrillAct Action
	diagBackAct  Action
}

// NewRouter builds a Router without a command-channel sink. Command-action
// callbacks (diag_now/router_doctor/...) will toast an error.
func NewRouter(d *db.DB, tgClient TGClient, cfg Config) *Router {
	return NewRouterWithSink(d, tgClient, nil, cfg)
}

func (r *Router) chatAllowed(chatID int64) bool {
	if r.cfg.ChatID == 0 {
		return true
	}
	if chatID == r.cfg.ChatID {
		return true
	}
	for _, extra := range r.cfg.ExtraChatIDs {
		if chatID == extra {
			return true
		}
	}
	return false
}

// NewRouterWithSink builds a Router whose command-action callbacks enqueue
// wire.Command into the provided sink for the agent to long-poll.
func NewRouterWithSink(d *db.DB, tgClient TGClient, sink CommandEnqueuer, cfg Config) *Router {
	r := &Router{
		d:       d,
		tg:      tgClient,
		cfg:     cfg,
		silence: NewSilenceAction(d),
		ack:     NewAckAction(d),
		mute:    NewMuteAction(d, cfg.MuteCutoffHour),
		history: NewHistoryAction(d, tgClient, cfg.ChatID),
		command: NewCommandAction(sink, nil),
	}
	r.diagCache = newDiagCache()
	return r
}

// DiagCache returns the Router's cache for raw diag bodies, shared with
// the command-result notifier so "📄 Полный отчёт" taps can fetch the
// body the notifier stored.
func (r *Router) DiagCache() *diagCache {
	return r.diagCache
}

// Run loops on GetUpdates, persisting the last-processed update_id in tg_state KV.
// Backoff on errors. Exits when ctx is cancelled.
func (r *Router) Run(ctx context.Context) error {
	var attempt int
	offset, _ := r.loadOffset()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := r.tg.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			attempt++
			wait := time.Duration(math.Min(math.Pow(2, float64(attempt)), 60)) * time.Second
			slog.Warn("getUpdates failed; backoff", "err", err, "wait", wait)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
			continue
		}
		attempt = 0
		for _, u := range updates {
			r.handleUpdate(ctx, u)
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
		}
		if len(updates) > 0 {
			_ = r.saveOffset(offset)
		}
	}
}

func (r *Router) handleUpdate(ctx context.Context, u tg.Update) {
	switch {
	case u.CallbackQuery != nil:
		r.HandleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		r.HandleMessage(ctx, u.Message)
	}
}

func (r *Router) loadOffset() (int64, error) {
	s, err := r.d.KV().Get("last_update_id")
	if err != nil || s == "" {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

func (r *Router) saveOffset(offset int64) error {
	return r.d.KV().Set("last_update_id", strconv.FormatInt(offset, 10))
}

// HandleCallback applies allowlist, parses, dispatches to action, edits message.
// Exposed for tests.
//
// Allowlist policy (changed 2026-04-30 per user request): any user IN the
// configured group chat may tap buttons. The chat-id check still rejects
// callbacks coming from arbitrary chats where the bot may be lurking. We
// log every callback's from.id for audit so post-hoc you can see who pushed
// what.
func (r *Router) HandleCallback(ctx context.Context, q *tg.CallbackQuery) {
	// Кнопка выключения уведомлений админа: из лички, двумя полями -- до
	// проверки чата и до Parse (notify_mute_callback.go).
	if isAdminMuteCallback(q.Data) {
		r.handleAdminMuteCallback(ctx, q)
		return
	}
	// Кнопки панелей туннелей, маршрутов и перезапуска служб ушли в
	// приложение (цикл 4), а сообщения с ними остались в чатах: ответ
	// словами, до проверки чата -- тост ничего не раскрывает и не меняет.
	if isMovedToAppCallback(q.Data) {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, movedToAppToast)
		return
	}
	// Кнопка в собственной личке -- законный источник нажатия: уведомления
	// переехали из тем группы туда. Право на действие с роутером всё равно
	// проверяет aclAllow по человеку. Админских панелей в боте больше нет
	// (цикл 2), и отдельного пропуска для них не нужно.
	routerButtonInDM := q.Message.Chat.ID == q.From.ID
	if !r.chatAllowed(q.Message.Chat.ID) && !routerButtonInDM {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "wrong chat")
		slog.Warn("rejected callback (chat-id)", "from", q.From.ID, "chat", q.Message.Chat.ID, "data", q.Data)
		return
	}
	slog.Info("callback", "from", q.From.ID, "data", q.Data)
	args, err := Parse(q.Data)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "неизвестная кнопка")
		slog.Warn("malformed callback_data", "data", q.Data, "err", err)
		return
	}
	// От хаба /panel осталась одна справка (help_callback.go).
	if args.Action == "panel" {
		r.handleHelpCallback(ctx, q, args.PanelKind)
		return
	}
	if !r.aclAllow(ctx, q, args) {
		return
	}
	// Parse() validates args.Action against the action whitelist (parse.go validActions).
	var action Action
	switch args.Action {
	case "silence":
		action = r.silence
	case "ack":
		action = r.ack
	case "mute":
		action = r.mute
	case "history":
		action = r.history
	case "diag_now", "pingcheck_now", "force_recheck", "router_doctor", "check_via_tunnel", "check_direct":
		action = r.command
	case "close_panel":
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "закрыто")
		empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
		_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, q.Message.Text, "", &empty)
		return
	case "pingcheck_open":
		if r.pingcheckOpenAct != nil {
			action = r.pingcheckOpenAct
		}
	case "pingcheck_toggle":
		if r.pingcheckToggleAct != nil {
			action = r.pingcheckToggleAct
		}
	case "diag_raw":
		body, ok := r.diagCache.Get(args.DiagRawToken)
		if !ok {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "отчёт уже не доступен (5 мин TTL)")
			return
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
		full := "📄 Полный отчёт диагностики:\n\n```\n" + body + "\n```"
		kb := diagRawKeyboard(args.UserID, args.DiagRawToken)
		if _, err := r.tg.SendMessageWithReplyKeyboard(ctx, q.Message.Chat.ID, q.Message.MessageThreadID, full, "", nil, &kb); err != nil {
			slog.Warn("diag_raw send failed", "err", err)
		}
		return
	case "diag_test":
		if r.diagDrillAct != nil {
			action = r.diagDrillAct
		}
	case "diag_back":
		if r.diagBackAct != nil {
			action = r.diagBackAct
		}
	case "compat_btn":
		r.handleCompatBtn(ctx, q, args)
		return
	}
	if action == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "action is not configured")
		slog.Warn("callback action not configured", "action", args.Action)
		return
	}
	statusLine, err := action.Apply(ctx, q, args)
	if err != nil {
		msg := "Ошибка: " + err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, msg)
		slog.Error("action failed", "action", args.Action, "err", err)
		return
	}
	if args.IsMenu {
		// Control-panel callbacks: keep the keyboard intact (pinned message
		// must survive taps) and surface confirmation via toast. statusLine
		// is truncated because TG caps callback toasts at 200 chars.
		toast := statusLine
		if len(toast) > 190 {
			toast = toast[:190]
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, toast)
		return
	}
	if args.IsPanel {
		// Tunnels-Panel callbacks enqueue async agent work. Keep the current
		// panel in place here; the command-result path edits it after the agent
		// has forced a fresh report, avoiding a stale pre-command snapshot.
		toast := statusLine
		if len(toast) > 190 {
			toast = toast[:190]
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, toast)
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
	if statusLine == "" {
		// History returns "" — do not edit original.
		return
	}
	newText := q.Message.Text + "\n\n" + statusLine
	empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, newText, "", &empty); err != nil {
		slog.Warn("editMessageText failed (state already updated)", "err", err)
	}
}

// aclAllow gates a callback by owner identity. Returns true to proceed,
// false to short-circuit (the function itself sends the toast and logs).
//
// Decision table (q.From.ID = F, args.UserID = R, user.TelegramUserID = O,
// user.TelegramThreadID = T, q.Message.MessageThreadID = M):
//
//	R == 0                                  → allow (no router target encoded)
//	users.GetByID(R) fails or returns nil   → allow + warn (don't break on DB hiccup)
//	T != nil && (M == nil || *M != *T)      → reject (known router topic mismatch)
//	F == AdminUserID                        → allow (admin override inside correct topic)
//	O != nil && T == nil && F != admin      → reject (router topic not created/recorded yet)
//	O != nil && *O == F                     → allow (rightful owner in topic)
//	O != nil && *O != F                     → reject ("это не твой роутер")
//	O == nil && M != nil && T != nil && *M == *T  → TOFU bind F → SetTelegramUserID, allow
//
// Non-admin router-scoped callbacks require a recorded per-router topic before
// owner/operator checks are honored. Admin remains the bootstrap escape hatch:
// a new router can still be diagnosed or wired up before the topic is recorded,
// but bound owners/operators cannot run controls from the summary/main group.
func (r *Router) aclAllow(ctx context.Context, q *tg.CallbackQuery, args Args) bool {
	if args.UserID == 0 {
		return true
	}
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "Ошибка: роутер не найден")
		slog.Warn("acl: user lookup failed, rejecting", "router_user_id", args.UserID, "err", err)
		return false
	}
	// Личка человека -- законный источник нажатия: уведомления переехали
	// туда, и у такого сообщения нет ни темы роутера, ни его чата. Сам по
	// себе приватный чат ничего не разрешает: доступ проверяется ниже по
	// человеку (RouterAccessRole), как и для нажатий из группы.
	isOwnDM := q.Message.Chat.ID == q.From.ID && q.Message.MessageThreadID == nil
	if !isOwnDM && user.TelegramThreadID != nil &&
		(q.Message.MessageThreadID == nil || *q.Message.MessageThreadID != *user.TelegramThreadID) {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "это не топик этого роутера")
		slog.Warn("acl: rejected (foreign topic)",
			"from", q.From.ID, "router_user_id", args.UserID, "thread", q.Message.MessageThreadID, "owner_thread", *user.TelegramThreadID, "data", q.Data)
		return false
	}
	if !isOwnDM && user.TelegramThreadID != nil && q.Message.Chat.ID != user.EffectiveTelegramChatID(r.cfg.ChatID) {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "это не чат этого роутера")
		slog.Warn("acl: rejected (foreign chat)",
			"from", q.From.ID, "router_user_id", args.UserID, "chat", q.Message.Chat.ID, "owner_chat", user.EffectiveTelegramChatID(r.cfg.ChatID), "data", q.Data)
		return false
	}
	if r.cfg.AdminUserID != 0 && q.From.ID == r.cfg.AdminUserID {
		return true
	}
	if user.TelegramUserID != nil {
		role, err := r.d.RouterAccessRole(user.ID, q.From.ID)
		if err != nil {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "Ошибка проверки доступа")
			slog.Warn("acl: access role lookup failed, rejecting", "router_user_id", user.ID, "from", q.From.ID, "err", err)
			return false
		}
		if role != "" {
			return !r.rejectBeforeRouterTopicExists(ctx, q, args, user)
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "это не твой роутер")
		slog.Warn("acl: rejected (owner mismatch)",
			"from", q.From.ID, "router_user_id", args.UserID, "owner", *user.TelegramUserID, "data", q.Data)
		return false
	}
	// Unbound. TOFU only if callback came from this router's own topic.
	if q.Message.MessageThreadID != nil && user.TelegramThreadID != nil &&
		*q.Message.MessageThreadID == *user.TelegramThreadID {
		if err := r.d.Users().SetTelegramUserID(user.ID, q.From.ID); err != nil {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось привязать владельца роутера")
			slog.Warn("acl: TOFU bind failed, rejecting", "router_user_id", user.ID, "from", q.From.ID, "err", err)
			return false
		} else {
			slog.Info("acl: TOFU bound router owner", "router_user_id", user.ID, "tg_user_id", q.From.ID)
		}
		return true
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не привязан к этому топику")
	slog.Warn("acl: rejected unbound router outside bound topic",
		"from", q.From.ID, "router_user_id", args.UserID, "thread", q.Message.MessageThreadID, "data", q.Data)
	return false
}

func (r *Router) rejectBeforeRouterTopicExists(ctx context.Context, q *tg.CallbackQuery, args Args, user *db.User) bool {
	if user == nil || user.TelegramThreadID != nil || !routerTopicRequiredBeforeNonAdminCallback(args.Action) {
		return false
	}
	// Из лички тема не нужна и не будет: уведомления переехали туда, а
	// сообщение и так пришло лично тому, чей доступ уже проверен. Проверка
	// стерегла нажатия в группе до того, как у роутера появилась своя тема.
	if q.Message.Chat.ID == q.From.ID && q.Message.MessageThreadID == nil {
		return false
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "сначала создай топик роутера")
	slog.Warn("acl: rejected before router topic exists",
		"from", q.From.ID, "router_user_id", args.UserID, "data", q.Data)
	return true
}

func routerTopicRequiredBeforeNonAdminCallback(action string) bool {
	return action != "" && action != "history"
}

// HandleMessage dispatches an incoming text Message: chat/admin gate, topic
// resolution, then the appropriate smart-reply / operations action.
//
// Allowlist: chat must equal cfg.ChatID; from must equal cfg.AdminUserID
// (text-message router is admin-only — group members can still tap inline
// callbacks per the 2026-04-30 policy reversal, but typing into the chat
// is a one-operator surface).
func (r *Router) HandleMessage(ctx context.Context, m *tg.Message) {
	// Секрет кабинета, присланный в чат по старой привычке, удаляется до
	// любых проверок доступа и даже до /myid (иначе «/myid vpn://…» оставит
	// ключ в чате): он не должен висеть в переписке ни у кого.
	if r.handleCabinetSecretMessage(ctx, m) {
		return
	}
	// .conf с приватным ключом бот больше не импортирует (цикл 4): файл
	// удаляется из переписки, человеку -- куда идти.
	if r.handleConfDocument(ctx, m) {
		return
	}
	// Старая нижняя клавиатура «🎛 Туннели» / «🛣 Маршруты» -- подсказка, куда
	// это переехало (ревью цикла 4).
	if r.handleMovedToAppText(ctx, m) {
		return
	}
	// /myid отвечает кому угодно и откуда угодно -- до всех проверок доступа.
	//
	// Чтобы дать человеку доступ к роутеру, нужен его числовой номер в
	// Telegram, а сам он его нигде не видит. Личные сообщения от посторонних
	// бот отбрасывает молча, поэтому узнать номер было негде: добавить
	// оператора мог только тот, кто умеет доставать id окольными путями.
	//
	// Ничего, кроме собственного номера отправителя, команда не сообщает,
	// поэтому и пропуск ей нужен ровно один -- этот.
	if cmd, _, ok := parseSlashCommand(m.Text); ok && cmd == "/myid" {
		r.handleMyIDCommand(ctx, m)
		return
	}
	adminDM := r.cfg.AdminUserID != 0 && m.From.ID == r.cfg.AdminUserID && m.Chat.ID == m.From.ID
	if !r.chatAllowed(m.Chat.ID) && !adminDM {
		return
	}
	isAdmin := r.cfg.AdminUserID == 0 || m.From.ID == r.cfg.AdminUserID
	if !isAdmin {
		// Non-admin path: owners and operators get owner-parity but ONLY in
		// their own router's per_router topic. resolveTopicKind classifies by
		// thread id; if the topic is unknown / summary / systemic, drop. If the
		// sender is neither the bound owner nor an operator, drop. The downstream
		// switch re-runs resolveTopicKind, which is cheap — keeping the
		// existing flow untouched simplifies the diff.
		kind, user := r.resolveTopicKind(m.Chat.ID, m.MessageThreadID)
		if kind != "per_router" || user == nil {
			return
		}
		isOwner := user.TelegramUserID != nil && *user.TelegramUserID == m.From.ID
		isOperator := r.d.RouterOperators().HasAccess(user.ID, m.From.ID)
		if !isOwner && !isOperator {
			return
		}
		// The router actor passes the gate. Skip handleAdminCommand entirely —
		// slash commands (/ensure_topics, /this_is, ...) stay admin-only.
		// Owners/operators get safe router-scoped slash commands, plus /help
		// and /keyboard as personal-recovery actions scoped to their own topic.
		if cmd, _, ok := parseSlashCommand(m.Text); ok {
			switch cmd {
			case "/help":
				r.handleHelpCommand(ctx, m)
			case "/menu", "/keyboard":
				r.handleKeyboardCommand(ctx, m)
			case "/status", "/check", "/via", "/direct":
				r.handleRouterSlashCommand(ctx, m, kind, user)
			}
			return
		}
	} else if r.handleAdminCommand(ctx, m) {
		// Slash-style admin commands run BEFORE topic resolution: /this_is
		// is useful precisely when the topic is unbound (resolveTopicKind
		// would return "unknown" + nil), and /ensure_topics works from any
		// topic.
		if r.cfg.UI.DeleteUserCommandMessages && m.MessageID != 0 {
			if err := r.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
				slog.Warn("deleteMessage failed (non-fatal)", "err", err, "chat", m.Chat.ID, "msg", m.MessageID)
			}
		}
		return
	}
	kind, user := r.resolveTopicKind(m.Chat.ID, m.MessageThreadID)
	if r.handleRouterSlashCommand(ctx, m, kind, user) {
		return
	}
	switch m.Text {
	case "📊 Что происходит?":
		if kind == "per_router" && user != nil {
			r.dispatchSmartReply(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя или в Сводке.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		}
	case "🌍 Через туннель?":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "check_via_tunnel",
			"⏳ Проверяю YouTube/Telegram/Instagram через туннель…")
	case "🇷🇺 Напрямую?":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "check_direct",
			"⏳ Проверяю Яндекс/VK/Mail.ru через прямой маршрут…")
	case "🩺 Проверка", "🩺 Домашний роутер":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "router_doctor",
			"⏳ Проверяю роутер изнутри: awg-manager, туннели, pingcheck и процессы…")
	case "📋 Список юзеров":
		if isAdmin && isFleetTopic(kind) {
			r.dispatchListUsers(ctx, m, kind)
		}
	case "📊 Здоровье флота":
		if isAdmin && isFleetTopic(kind) {
			r.dispatchFleetHealth(ctx, m, kind)
		}
	default:
		// Ignore — could be operator chatting; don't delete.
		return
	}
	// MessageID==0 marks a synthetic message (e.g. compat-inline-keyboard tap
	// dispatched through HandleMessage) — there is no real user message to
	// delete, and TG would reject deleteMessage(0) with "message not found".
	if r.cfg.UI.DeleteUserCommandMessages && m.MessageID != 0 {
		if err := r.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
			slog.Warn("deleteMessage failed (non-fatal)", "err", err, "chat", m.Chat.ID, "msg", m.MessageID)
		}
	}
}

func (r *Router) handleRouterSlashCommand(ctx context.Context, m *tg.Message, kind string, user *db.User) bool {
	cmd, _, ok := parseSlashCommand(m.Text)
	if !ok {
		return false
	}
	switch cmd {
	case "/status":
		if kind == "per_router" && user != nil {
			r.dispatchSmartReply(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике конкретного роутера.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		}
		return true
	case "/check":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "router_doctor",
			"⏳ Проверяю роутер изнутри: awg-manager, туннели, pingcheck и процессы…")
		return true
	case "/via":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "check_via_tunnel",
			"⏳ Проверяю YouTube/Telegram/Instagram через туннель…")
		return true
	case "/direct":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "check_direct",
			"⏳ Проверяю Яндекс/VK/Mail.ru через прямой маршрут…")
		return true
	}
	return false
}

func isFleetTopic(kind string) bool {
	return kind == "summary" || kind == "systemic"
}

// resolveTopicKind classifies a thread id into "per_router" / "summary" /
// "systemic" / "unknown" using db.Users + db.KV operations-topic IDs.
func (r *Router) resolveTopicKind(chatID int64, threadID *int64) (string, *db.User) {
	if threadID == nil || *threadID == 0 {
		return "unknown", nil
	}
	if u, err := r.d.Users().GetByChatThreadID(chatID, *threadID, r.cfg.ChatID); err == nil {
		return "per_router", u
	} else if !errors.Is(err, db.ErrUserNotFound) {
		slog.Warn("resolveTopicKind: users lookup failed", "chat", chatID, "thread", *threadID, "err", err)
	}
	if id, ok, err := r.d.KV().GetTopicID("summary"); err != nil {
		slog.Warn("resolveTopicKind: kv summary lookup failed", "err", err)
	} else if ok && id == *threadID {
		return "summary", nil
	}
	if id, ok, err := r.d.KV().GetTopicID("systemic"); err != nil {
		slog.Warn("resolveTopicKind: kv systemic lookup failed", "err", err)
	} else if ok && id == *threadID {
		return "systemic", nil
	}
	return "unknown", nil
}

// dispatchConnectivityCheck enqueues an on-demand check_via_tunnel /
// check_direct command and acks the user with a "⏳ Проверяю…" line. The
// agent's CommandResult will reply to the ack message via Notifier.
//
// IMPORTANT: we send the ack FIRST and pass ITS message id to
// DispatchFromMessage as reply_to_message_id. The original user-tap message
// gets deleted by handleNonCommandMessage when DeleteUserCommandMessages=true,
// so replying to it from the result handler 30+ seconds later fails with
// "message to be replied not found (code=400)" and the operator never sees
// the result. The ack message stays in the chat, so anchoring on it is safe.
func (r *Router) dispatchConnectivityCheck(ctx context.Context, m *tg.Message, kind string, user *db.User, action, ackText string) {
	if kind != "per_router" || user == nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"эта команда работает только в топике пользователя.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return
	}
	ackMid, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, ackText, "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
	if err != nil {
		slog.Warn("dispatchConnectivityCheck ack failed", "err", err)
		// Fall through and still try to enqueue — at worst the result
		// reply target is missing, which is the bug we're already
		// fixing for the happy path.
	}
	if err := r.command.DispatchFromMessage(ctx, action, user.ID, m.Chat.ID, ackMid, m.MessageThreadID); err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"не удалось поставить задачу: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
		slog.Warn("dispatchConnectivityCheck enqueue failed", "err", err, "action", action)
		return
	}
}

func topicHelpBody(kind string) string {
	switch kind {
	case "per_router":
		return "Меню роутера под сообщениями бота:\n" +
			"📊 Что происходит? — короткая сводка и безопасный следующий шаг.\n" +
			"🩺 Проверка — doctor изнутри роутера, без изменений.\n" +
			"🌍 Через туннель? / 🇷🇺 Напрямую? — проверки связности.\n\n" +
			"VPN-туннели и маршруты — в приложении: роутер → «VPN-туннели».\n\n" +
			"Если кнопка меняет состояние, бот поставит команду в очередь. Жди результат в этом топике и используй кнопки под результатом."
	case "summary", "systemic":
		return "Меню под сообщениями бота:\n" +
			"📊 Здоровье флота — общая картина по роутерам.\n" +
			"📋 Список юзеров — список роутеров и привязанных топиков.\n\n" +
			"Управляющие действия по конкретному роутеру запускай в его топике."
	default:
		return "Меню зависит от топика. В топике роутера начни с кнопки «📊 Что происходит?» под сообщением бота."
	}
}

// dispatchSmartReply renders the [📊 Что происходит?] response (spec §5.2,
// §6.2 c) by collecting per-tunnel views + active hard incidents from DB,
// classifying state, and formatting via alerts.FormatSmartReply.
func (r *Router) dispatchSmartReply(ctx context.Context, m *tg.Message, user *db.User) {
	tunnels := r.collectTunnelViews(user.ID)
	incidents := r.collectActiveIncidents(user.ID)
	lastTS, _ := r.d.Events().LatestPerUser(user.ID)
	if lastTS.IsZero() {
		// Never reported — show a clear message instead of fabricating an
		// "offline 1440 минут назад" via the StateOffline template.
		body := "🆕 " + user.Nickname + " — ещё не отчитывался.\n\n" +
			"Подождите, пока агент пришлёт первый heartbeat. Проверить агент: " +
			"ssh root@router и `/opt/etc/init.d/S99wg-monitor status` " +
			"(на Keenetic нет systemd; агент логирует в stderr и логи не сохраняются)."
		_, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, body, "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
		if err != nil {
			slog.Warn("smart reply (never reported) send failed", "err", err, "user", user.Nickname)
		}
		return
	}
	lastAge := time.Since(lastTS)
	args := alerts.SmartReplyArgs{
		Nickname:        user.Nickname,
		UserID:          user.ID,
		Tunnels:         tunnels,
		ActiveIncidents: incidents,
		LastReportAge:   lastAge,
		IsMobile:        user.IsMobile(),
	}
	// Кэша версий в памяти бота больше нет (он жил ради панели обслуживания):
	// блок обновлений берётся из снимка в базе.
	args.Updates = updatesFromCacheOrSnapshot(ctx, r.d, r.upstream, wire.VersionAudit{}, false, user.ID)
	// Перезапуск и удаление VPN-туннелей и маршруты -- в приложении (цикл 4).
	// Кнопку web_app Telegram принимает только в личке.
	if tg.IsPrivateChat(m.Chat.ID) {
		args.AppURL = tg.MiniAppRouterTabURL(r.cfg.PublicBaseURL, user.ID, "tunnels", "")
	}
	text, inline := alerts.FormatSmartReply(args)
	// ReplyKeyboard cannot coexist with InlineKeyboard on a single message
	// — TG accepts only one reply_markup per send. When FormatSmartReply
	// returns an empty inline keyboard (StateOK / StateOffline have no
	// per-tunnel actions), we MUST swap to the topic ReplyKeyboard
	// instead — passing &inline with a nil 2D-slice marshals to
	// {"inline_keyboard": null} and TG rejects it with "field
	// inline_keyboard must be of type Array (code=400)". Bonus: keeps the
	// bottom panel re-attached when there are no inline buttons to show.
	var kbArg any = &inline
	if len(inline.InlineKeyboard) == 0 {
		kbArg = r.cfg.UI.KeyboardForTopic("per_router")
	}
	_, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, kbArg)
	if err != nil {
		slog.Warn("smart reply send failed", "err", err, "user", user.Nickname)
	}
}

// collectTunnelViews builds []alerts.TunnelView for smart-reply from the
// latest tunnel_* events of the last three minutes: a deleted tunnel stops
// reporting and drops out. The live route snapshot cache went away with the
// bot's routes panel (cycle 4).
func (r *Router) collectTunnelViews(userID int64) []alerts.TunnelView {
	rows, err := r.d.Events().LatestEventsByPrefixSince(userID, "tunnel_", time.Now().Add(-3*time.Minute))
	if err != nil {
		slog.Warn("collectTunnelViews: query failed", "err", err, "user", userID)
		return nil
	}
	var out []alerts.TunnelView
	for _, row := range rows {
		var det map[string]any
		if row.DetailsJSON != "" {
			_ = json.Unmarshal([]byte(row.DetailsJSON), &det)
		}
		enabled := true
		hasEnabled := false
		if v, ok := det["enabled"].(bool); ok {
			enabled = v
			hasEnabled = true
		}
		_, hasAge := det["handshake_age_sec"]
		_, hasLastHandshake := det["last_handshake"]
		out = append(out, alerts.TunnelView{
			Name:         strOrEmpty(det, "tunnel_name"),
			CheckName:    row.CheckName,
			NDMSName:     strOrEmpty(det, "ndms_name"),
			Interface:    strOrEmpty(det, "interface"),
			Enabled:      enabled,
			HasEnabled:   hasEnabled,
			Status:       strOrEmpty(det, "status"),
			HasHandshake: hasAge || hasLastHandshake,
			HandshakeAge: intOrZero(det, "handshake_age_sec"),
			PingStatus:   strOrEmpty(det, "ping_check_status"),
			Latency:      intOrZero(det, "ping_check_last_latency_ms"),
			FailCount:    intOrZero(det, "ping_check_fail_count"),
			FailThresh:   intOrZero(det, "ping_check_fail_threshold"),
		})
	}
	return out
}

// collectActiveIncidents returns each `incident_state` row with
// current_status='hard' for this user. Routed via StateRepo so the SQL stays
// in one place (LOGIC-07).
func (r *Router) collectActiveIncidents(userID int64) []alerts.IncidentView {
	rows, err := r.d.State().ActiveHardForUserStatus(userID)
	if err != nil {
		slog.Warn("collectActiveIncidents: query failed", "err", err, "user", userID)
		return nil
	}
	out := make([]alerts.IncidentView, 0, len(rows))
	for _, row := range rows {
		var details map[string]any
		if ev, ok, err := r.d.Events().LatestEvent(userID, row.CheckName); err == nil && ok && ev.DetailsJSON != "" {
			_ = json.Unmarshal([]byte(ev.DetailsJSON), &details)
		}
		out = append(out, alerts.IncidentView{
			CheckName: row.CheckName,
			HardSince: row.HardSince,
			FailCount: row.FailCount,
			Details:   details,
		})
	}
	return out
}

// Local helpers for map-pulling (mirrors alerts/format.go's helpers but kept
// here so we don't widen the alerts package's exported surface).
func strOrEmpty(d map[string]any, k string) string {
	if d == nil {
		return ""
	}
	if v, ok := d[k].(string); ok {
		return v
	}
	return ""
}

func intOrZero(d map[string]any, k string) int {
	if d == nil {
		return 0
	}
	v, ok := d[k]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}

// dispatchListUsers prints every onboarded router with kind + last_seen age,
// scoped for the operator-only Сводка topic.
func (r *Router) dispatchListUsers(ctx context.Context, m *tg.Message, kind string) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, "ошибка чтения пользователей: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return
	}
	if len(users) == 0 {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, "Пользователей нет.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return
	}
	var b strings.Builder
	b.WriteString("📋 Список юзеров\n")
	now := time.Now()
	for _, u := range users {
		seen := "никогда"
		if u.LastSeenAt != nil {
			seen = humanAgeDur(now.Sub(*u.LastSeenAt)) + " назад"
		}
		fmt.Fprintf(&b, "• %s — %s — %s\n", u.Nickname, u.Kind, seen)
	}
	fmt.Fprintf(&b, "\nВсего: %d", len(users))
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, b.String(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
}

// dispatchFleetHealth renders the operator-only "📊 Здоровье флота" reply:
// counts of currently-HARD incidents, breakdown by check, and a per-row list
// keyed by nickname. Includes silenced/acked rows so the operator sees them.
func (r *Router) dispatchFleetHealth(ctx context.Context, m *tg.Message, kind string) {
	rows, err := r.d.State().AllActiveHard()
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, "ошибка чтения incident_state: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return
	}
	users, _ := r.d.Users().GetAll()
	nickByID := make(map[int64]string, len(users))
	for _, u := range users {
		nickByID[u.ID] = u.Nickname
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Здоровье флота\nАктивных HARD: %d\n", len(rows))
	if len(rows) > 0 {
		byCheck := make(map[string]int, len(rows))
		for _, row := range rows {
			byCheck[row.CheckName]++
		}
		b.WriteString("\nПо типам проблем:\n")
		for check, n := range byCheck {
			fmt.Fprintf(&b, "  • %s — %d\n", check, n)
		}
		b.WriteString("\nДетали:\n")
		for _, row := range rows {
			nick := nickByID[row.UserID]
			if nick == "" {
				nick = "user#" + strconv.FormatInt(row.UserID, 10)
			}
			age := "—"
			if !row.HardSince.IsZero() {
				age = humanAgeDur(time.Since(row.HardSince))
			}
			fmt.Fprintf(&b, "  • [%s] %s — %s\n", nick, row.CheckName, age)
		}
	}
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, b.String(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
}

// humanAgeDur is a local copy of alerts.humanAgeDur (private there). Keeping
// it local avoids exporting an alerts symbol just for this caller.
func humanAgeDur(d time.Duration) string {
	if d <= 0 {
		return "0с"
	}
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%dс", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dм", s/60)
	}
	return fmt.Sprintf("%dч", s/3600)
}

// SetUpstream attaches the upstream version cache used by smart-reply
// Updates section computation. Optional — nil-safe in dispatchSmartReply.
func (r *Router) SetUpstream(c *upstream.Cache) {
	r.upstream = c
}

// computeUpdates projects upstream.ComputeUpdates into alerts.UpdateAvailable
// for the smart-reply Updates section. Single source of truth via the upstream
// helper (LOGIC-09).
func computeUpdates(ctx context.Context, up *upstream.Cache, va wire.VersionAudit) []alerts.UpdateAvailable {
	// Причины «неизвестно» здесь не рисуются намеренно: умный ответ -- это
	// разговор о поломке, и отсутствие блока в нём не читается как «всё
	// актуально». Про незнание словами говорят экраны (мини-апп и дашборд) и
	// панель обслуживания -- те поверхности, которые человек открыл сам.
	infos, _ := upstream.ComputeUpdates(ctx, up, va)
	if len(infos) == 0 {
		return nil
	}
	out := make([]alerts.UpdateAvailable, 0, len(infos))
	for _, u := range infos {
		out = append(out, alerts.UpdateAvailable{Name: u.Name, Installed: u.Installed, Available: u.Available, Hint: u.Hint})
	}
	return out
}

// SetPingCheck wires the PingCheck panel actions. Called from cmd/backend/main.go
// at startup. inflight is shared by Open/Toggle so dup-protection works.
func (r *Router) SetPingCheck(sink CommandEnqueuer) {
	r.pingcheckInflight = newPingCheckInflightStore()
	r.pingcheckOpenAct = NewPingCheckOpenAction(sink, defaultCmdID)
	r.pingcheckToggleAct = NewPingCheckToggleAction(sink, r.pingcheckInflight, defaultCmdID)
}

// SetDiagDrillDown wires the diag drill-down action. Called from
// cmd/backend/main.go at startup. Reuses the existing diagCache.
func (r *Router) SetDiagDrillDown() {
	r.diagDrillAct = NewDiagTestExpandAction(r.diagCache, r.tg)
	r.diagBackAct = NewDiagBackAction(r.diagCache, r.tg)
}

// NewPingCheckNotifier returns a PingCheckPanelNotifier wired against this
// router's TG client and DB. Pass the returned value into handler.Deps.PingCheckNotifier.
func (r *Router) NewPingCheckNotifier() *PingCheckPanelNotifier {
	return &PingCheckPanelNotifier{TG: r.tg, DB: r.d, AppBaseURL: r.cfg.PublicBaseURL}
}
