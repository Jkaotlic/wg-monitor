package callbacks

import (
	"context"
	cryptoRand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
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
}

type Config struct {
	ChatID         int64
	AdminUserID    int64
	MuteCutoffHour int
	UI             UIConfigSnapshot
}

// UIConfigSnapshot mirrors backend.UIConfig (avoid an import cycle).
// Bool fields here are values, not pointers — caller (cmd/backend/main.go)
// dereferences backend.UIConfig.*bool fields when building the snapshot.
type UIConfigSnapshot struct {
	DeleteUserCommandMessages bool
	SmartReplyWithKeyboard    bool
	DiagMaxChars              int
}

type Router struct {
	d            *db.DB
	tg           TGClient
	cfg          Config
	silence      *SilenceAction
	ack          *AckAction
	mute         *MuteAction
	history      *HistoryAction
	command      *CommandAction
	importAction *ImportAction
	pendingMu    sync.Mutex
	pending      map[int64]*pendingUpload

	routesCache         *RoutesCache
	rebindConfirmAction Action
	cmdSink             CommandEnqueuer // saved for routes_open/refresh enqueue paths
	pendingRebindsMu    sync.Mutex
	pendingRebinds      map[string]*pendingRebind // keyed by 8-hex token

	// Maintenance panel plumbing (M6/M10/M11). All in-memory; lost on restart.
	pendingMaint    *pendingMaintStore
	cooldown        *cooldownStore
	maintConfirmAct Action
	auditCache      *simpleAuditCache
}

// NewRouter builds a Router without a command-channel sink. Command-action
// callbacks (restart_tunnel/diag_now/...) will toast an error.
func NewRouter(d *db.DB, tgClient TGClient, cfg Config) *Router {
	return NewRouterWithSink(d, tgClient, nil, cfg)
}

// SetRoutesCache attaches the per-user RoutesCache. Called from cmd/backend
// during startup so the router can serve cached snapshots for routes_open
// callbacks without re-querying the agent each time.
func (r *Router) SetRoutesCache(c *RoutesCache) {
	r.routesCache = c
}

// NewRouterWithSink builds a Router whose command-action callbacks enqueue
// wire.Command into the provided sink for the agent to long-poll.
func NewRouterWithSink(d *db.DB, tgClient TGClient, sink CommandEnqueuer, cfg Config) *Router {
	r := &Router{
		d:       d,
		tg:      tgClient,
		cfg:     cfg,
		pending: make(map[int64]*pendingUpload),
		silence: NewSilenceAction(d),
		ack:     NewAckAction(d),
		mute:    NewMuteAction(d, cfg.MuteCutoffHour),
		history: NewHistoryAction(d, tgClient, cfg.ChatID),
		command: NewCommandAction(sink, nil),
	}
	r.importAction = &ImportAction{
		sink: sink,
		consumeFn: func(userID int64, token string) (*pendingUpload, bool) {
			r.pendingMu.Lock()
			defer r.pendingMu.Unlock()
			up, ok := r.pending[userID]
			if !ok || up.Token != token || time.Now().After(up.ExpiresAt) || up.Name == "" {
				return nil, false
			}
			delete(r.pending, userID)
			return up, true
		},
		idGen: defaultCmdID,
	}
	r.cmdSink = sink
	r.pendingRebinds = make(map[string]*pendingRebind)
	r.rebindConfirmAction = NewRebindConfirmAction(sink, r.consumePendingRebind, defaultCmdID)
	r.pendingMaint = newPendingMaintStore()
	r.cooldown = newCooldownStore()
	r.auditCache = newSimpleAuditCache()
	r.maintConfirmAct = NewMaintConfirmAction(sink, r.pendingMaint, r.cooldown, defaultCmdID)
	return r
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

func (r *Router) storePending(userID int64, up *pendingUpload) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	r.pending[userID] = up
}

func newImportToken() string {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// HandleCallback applies allowlist, parses, dispatches to action, edits message.
// Exposed for tests.
//
// Allowlist policy (changed 2026-04-30 per user request): any user IN the
// configured group chat may tap buttons. The chat-id check still rejects
// callbacks coming from arbitrary chats where the bot may be lurking. We
// log every callback's from.id for audit so post-hoc you can see who pushed
// what — important since opkg_upgrade is enabled in the menu.
func (r *Router) HandleCallback(ctx context.Context, q *tg.CallbackQuery) {
	if r.cfg.ChatID != 0 && q.Message.Chat.ID != r.cfg.ChatID {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "wrong chat")
		slog.Warn("rejected callback (chat-id)", "from", q.From.ID, "chat", q.Message.Chat.ID, "data", q.Data)
		return
	}
	slog.Info("callback", "from", q.From.ID, "data", q.Data)
	args, err := Parse(q.Data)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "unknown action")
		slog.Warn("malformed callback_data", "data", q.Data, "err", err)
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
	case "restart_tunnel", "diag_now", "pingcheck_now", "force_recheck", "opkg_upgrade",
		"tunnel_enable", "tunnel_disable":
		action = r.command
	case "tunnel_import_replace", "tunnel_import_add":
		if r.importAction != nil {
			action = r.importAction
		}
	case "tunnels_refresh":
		// Local-only callback: re-render the Tunnels Panel inline.
		// Look up the user owning this thread (panel was sent from per_router topic).
		if u, err := r.d.Users().GetByID(args.UserID); err == nil && u != nil {
			text, kb := r.buildTunnelsPanel(u)
			if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
				slog.Warn("tunnels_refresh: edit failed", "err", err)
			}
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обновлено")
		return
	case "routes_open", "routes_refresh":
		r.handleRoutesOpen(ctx, q, args, args.Action == "routes_refresh")
		return
	case "routes_rebind":
		r.handleRoutesRebindStart(ctx, q, args)
		return
	case "routes_pick":
		r.handleRoutesRebindPick(ctx, q, args)
		return
	case "routes_back":
		r.handleRoutesOpen(ctx, q, args, false)
		return
	case "routes_close":
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "закрыто")
		empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
		_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, q.Message.Text, "", &empty)
		return
	case "routes_confirm":
		if r.rebindConfirmAction != nil {
			action = r.rebindConfirmAction
		}
	}
	statusLine, err := action.Apply(ctx, q, args)
	if err != nil {
		msg := "error: " + err.Error()
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
		// Tunnels-Panel callbacks: surface confirmation via toast, then
		// refresh the panel inline so the toggle state updates without
		// the operator re-tapping "🔄 Обновить". (We can't read the new
		// awg-manager state instantly — the toggle ran async via cmd-queue
		// — but refreshing pulls the freshest event row from DB; the next
		// agent tick (~60s) will reflect the actual change.)
		toast := statusLine
		if len(toast) > 190 {
			toast = toast[:190]
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, toast)
		if u, err := r.d.Users().GetByID(args.UserID); err == nil && u != nil {
			text, kb := r.buildTunnelsPanel(u)
			if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
				slog.Warn("panel refresh after action failed", "err", err)
			}
		}
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

// HandleMessage dispatches an incoming text Message: chat/admin gate, topic
// resolution, then the appropriate smart-reply / operations action.
//
// Allowlist: chat must equal cfg.ChatID; from must equal cfg.AdminUserID
// (text-message router is admin-only — group members can still tap inline
// callbacks per the 2026-04-30 policy reversal, but typing into the chat
// is a one-operator surface).
func (r *Router) HandleMessage(ctx context.Context, m *tg.Message) {
	if r.cfg.ChatID != 0 && m.Chat.ID != r.cfg.ChatID {
		return
	}
	if r.cfg.AdminUserID != 0 && m.From.ID != r.cfg.AdminUserID {
		return
	}
	kind, user := r.resolveTopicKind(m.MessageThreadID)
	// Document handler — before text switch.
	if m.Document != nil {
		r.handleDocumentUpload(ctx, m, kind, user)
		return
	}
	switch m.Text {
	case "📊 Что происходит?":
		if kind == "per_router" && user != nil {
			r.dispatchSmartReply(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя или в Сводке.", "", nil)
		}
	case "🎛 Туннели":
		if kind == "per_router" && user != nil {
			text, kb := r.buildTunnelsPanel(user)
			_, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &kb)
			if err != nil {
				slog.Warn("tunnels-panel send failed", "err", err, "user", user.Nickname)
			}
		} else {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя.", "", nil)
		}
	case "🛣 Маршруты":
		if kind == "per_router" && user != nil {
			r.openRoutesPanelMessage(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя.", "", nil)
		}
	case "🛠 Обслуживание":
		if kind == "per_router" && user != nil {
			r.openMaintPanelMessage(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя.", "", nil)
		}
	case "🌍 Через тоннель?":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "check_via_tunnel",
			"⏳ Проверяю YouTube/Telegram/Instagram через тоннель…")
	case "🇷🇺 Напрямую?":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "check_direct",
			"⏳ Проверяю Яндекс/VK/Mail.ru через прямой маршрут…")
	case "⬆ Обновить пакеты":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "opkg_upgrade",
			"⏳ Обновляю пакеты Entware (update + space check + upgrade)… это может занять минуту-две.")
	case "📋 Список юзеров":
		r.dispatchListUsers(ctx, m)
	case "📊 Здоровье флота":
		r.dispatchFleetHealth(ctx, m)
	default:
		if r.handlePendingNameReply(ctx, m, user) {
			return
		}
		// Ignore — could be operator chatting; don't delete.
		return
	}
	if r.cfg.UI.DeleteUserCommandMessages {
		if err := r.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
			slog.Warn("deleteMessage failed (non-fatal)", "err", err, "chat", m.Chat.ID, "msg", m.MessageID)
		}
	}
}

// resolveTopicKind classifies a thread id into "per_router" / "summary" /
// "systemic" / "unknown" using db.Users + db.KV operations-topic IDs.
func (r *Router) resolveTopicKind(threadID *int64) (string, *db.User) {
	if threadID == nil || *threadID == 0 {
		return "unknown", nil
	}
	if u, err := r.d.Users().GetByThreadID(*threadID); err == nil {
		return "per_router", u
	} else if !errors.Is(err, db.ErrUserNotFound) {
		slog.Warn("resolveTopicKind: users lookup failed", "thread", *threadID, "err", err)
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
// agent's CommandResult will reply to the user's text message via Notifier.
func (r *Router) dispatchConnectivityCheck(ctx context.Context, m *tg.Message, kind string, user *db.User, action, ackText string) {
	if kind != "per_router" || user == nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"эта команда работает только в топике пользователя.", "", nil)
		return
	}
	if err := r.command.DispatchFromMessage(ctx, action, user.ID, m.Chat.ID, m.MessageID, m.MessageThreadID); err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"не удалось поставить задачу: "+err.Error(), "", nil)
		slog.Warn("dispatchConnectivityCheck enqueue failed", "err", err, "action", action)
		return
	}
	_, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, ackText, "", nil, tg.ReplyKeyboardForTopic("per_router"))
	if err != nil {
		slog.Warn("dispatchConnectivityCheck ack failed", "err", err)
	}
}

// openRoutesPanelMessage sends the initial Routes panel as a fresh message
// (so subsequent edits target this MessageID) and enqueues route_status.
// The cmd-result handler edits when the agent answers.
func (r *Router) openRoutesPanelMessage(ctx context.Context, m *tg.Message, user *db.User) {
	loadingText := fmt.Sprintf("🛣 Маршруты — %s\n   обновляется…", user.Nickname)
	mid, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, loadingText, "", nil)
	if err != nil {
		slog.Warn("routes panel send failed", "err", err)
		return
	}
	if r.cmdSink == nil {
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "route_status", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: m.Chat.ID, MessageID: mid, ThreadID: m.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		slog.Warn("route_status enqueue failed", "err", err)
	}
}

// openMaintPanelMessage sends the initial Maintenance panel placeholder and
// enqueues a version_audit command. The cmd-result handler (MaintNotifier
// in M11) edits the panel in place when the agent answers.
func (r *Router) openMaintPanelMessage(ctx context.Context, m *tg.Message, user *db.User) {
	loadingText := fmt.Sprintf("🛠 Обслуживание — %s\n   обновляется…", user.Nickname)
	mid, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, loadingText, "", nil)
	if err != nil {
		slog.Warn("maint panel send failed", "err", err)
		return
	}
	if r.cmdSink == nil {
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "version_audit", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: m.Chat.ID, MessageID: mid, ThreadID: m.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		slog.Warn("version_audit enqueue failed", "err", err)
	}
}

// buildTunnelsPanel queries the user's latest per-tunnel events and renders
// (text, inline-keyboard) for the Tunnels Panel. Used both by initial dispatch
// (tap on "🎛 Туннели" reply-keyboard button) and by the tunnels_refresh
// callback after a toggle action.
func (r *Router) buildTunnelsPanel(u *db.User) (string, tg.InlineKeyboardMarkup) {
	// Stale-entity elision: only show tunnels whose latest event is at most
	// 3 agent cycles old (~3 min). When awg-manager removes a tunnel, the
	// agent stops emitting events for it; without this filter the dead
	// tunnel sticks in the panel forever as "last known state".
	freshSince := time.Now().Add(-3 * time.Minute)
	rows, err := r.d.Events().LatestEventsByPrefixSince(u.ID, "tunnel_", freshSince)
	if err != nil {
		slog.Warn("buildTunnelsPanel: events lookup failed", "err", err, "user", u.ID)
	}
	entries := make([]tg.TunnelPanelEntry, 0, len(rows))
	for _, row := range rows {
		var det map[string]any
		if row.DetailsJSON != "" && row.DetailsJSON != "null" {
			_ = json.Unmarshal([]byte(row.DetailsJSON), &det)
		}
		// `enabled` may not be present in older events — default to true so we
		// don't accidentally render a stale entry as disabled (the rest of the
		// row will still surface real state via Status / handshake).
		enabled := true
		if v, ok := det["enabled"].(bool); ok {
			enabled = v
		}
		entries = append(entries, tg.TunnelPanelEntry{
			Name:         strOrEmpty(det, "tunnel_name"),
			CheckName:    row.CheckName,
			Interface:    strOrEmpty(det, "interface"),
			NDMSName:     strOrEmpty(det, "ndms_name"),
			Enabled:      enabled,
			Status:       strOrEmpty(det, "status"),
			HandshakeAge: intOrZero(det, "handshake_age_sec"),
		})
	}
	return tg.TunnelsPanelText(u.Nickname, entries), tg.TunnelsPanelKeyboard(u.ID, entries)
}

// dispatchHelp sends the static help text for the topic kind.
func (r *Router) dispatchHelp(ctx context.Context, m *tg.Message, kind string) {
	body := "Кнопки внизу:\n" +
		"📊 Что происходит? — состояние роутера прямо сейчас.\n" +
		"🆘 Помощь — этот текст.\n\n" +
		"В топиках Сводки/Системного:\n" +
		"📋 Список юзеров, 📊 Здоровье флота — операторские команды."
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, body, "", nil, tg.ReplyKeyboardForTopic(kind))
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
		_, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, body, "", nil, tg.ReplyKeyboardForTopic("per_router"))
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
	text, inline := alerts.FormatSmartReply(args)
	// ReplyKeyboard cannot coexist with InlineKeyboard on a single message
	// — TG accepts only one reply_markup per send. Per spec §6.1, inline
	// keyboard wins for the smart-reply itself; the ReplyKeyboard re-installs
	// on the next bot message (alert / RECOVERY). However we still pass the
	// inline as the reply_markup so smart-reply buttons appear.
	_, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &inline)
	if err != nil {
		slog.Warn("smart reply send failed", "err", err, "user", user.Nickname)
	}
}

// collectTunnelViews builds []alerts.TunnelView from the latest per-tunnel
// event for this user.
func (r *Router) collectTunnelViews(userID int64) []alerts.TunnelView {
	rows, err := r.d.Events().LatestEventsByPrefix(userID, "tunnel_")
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
		out = append(out, alerts.TunnelView{
			Name:         strOrEmpty(det, "tunnel_name"),
			CheckName:    row.CheckName,
			Interface:    strOrEmpty(det, "interface"),
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
// current_status='hard' for this user. Direct SQL — no new repo method needed.
func (r *Router) collectActiveIncidents(userID int64) []alerts.IncidentView {
	q, err := r.d.SQL().Query(`SELECT check_name, hard_since, consecutive_fails
                            FROM incident_state
                           WHERE user_id = ? AND current_status = 'hard'
                             AND (silenced_until IS NULL OR silenced_until < CURRENT_TIMESTAMP)
                             AND acked = 0`, userID)
	if err != nil {
		slog.Warn("collectActiveIncidents: query failed", "err", err, "user", userID)
		return nil
	}
	defer q.Close()
	var out []alerts.IncidentView
	for q.Next() {
		var iv alerts.IncidentView
		var hs sql.NullTime
		if err := q.Scan(&iv.CheckName, &hs, &iv.FailCount); err != nil {
			continue
		}
		if hs.Valid {
			iv.HardSince = hs.Time
		}
		out = append(out, iv)
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
func (r *Router) dispatchListUsers(ctx context.Context, m *tg.Message) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "ошибка чтения пользователей: "+err.Error(), "", nil)
		return
	}
	if len(users) == 0 {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, "Пользователей нет.", "", nil, tg.ReplyKeyboardForTopic("summary"))
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
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, b.String(), "", nil, tg.ReplyKeyboardForTopic("summary"))
}

// dispatchFleetHealth renders the operator-only "📊 Здоровье флота" reply:
// counts of currently-HARD incidents, breakdown by check, and a per-row list
// keyed by nickname. Includes silenced/acked rows so the operator sees them.
func (r *Router) dispatchFleetHealth(ctx context.Context, m *tg.Message) {
	rows, err := r.d.State().AllActiveHard()
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "ошибка чтения incident_state: "+err.Error(), "", nil)
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
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, b.String(), "", nil, tg.ReplyKeyboardForTopic("summary"))
}

func (r *Router) handleDocumentUpload(ctx context.Context, m *tg.Message, kind string, user *db.User) {
	slog.Info("document-upload", "file", m.Document.FileName, "size", m.Document.FileSize, "kind", kind, "has_user", user != nil)
	if kind != "per_router" || user == nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"конфиги принимаются только в топике роутера.", "", nil)
		return
	}
	if m.Document.FileSize > 50*1024 {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"файл слишком большой (максимум 50 КБ для .conf).", "", nil)
		return
	}
	filePath, err := r.tg.GetFile(ctx, m.Document.FileID)
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"не удалось получить файл: "+err.Error(), "", nil)
		return
	}
	data, err := r.tg.DownloadFile(ctx, filePath)
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"не удалось скачать файл: "+err.Error(), "", nil)
		return
	}
	confB64 := base64.StdEncoding.EncodeToString(data)
	suggested := sanitizeTunnelName(strings.TrimSuffix(m.Document.FileName, ".conf"))
	token := newImportToken()
	up := &pendingUpload{
		ConfB64:       confB64,
		SuggestedName: suggested,
		ThreadID:      m.MessageThreadID,
		Token:         token,
		ExpiresAt:     time.Now().Add(5 * time.Minute),
	}
	if isValidTunnelName(suggested) {
		up.Name = suggested
		r.storePending(user.ID, up)
		r.sendImportConfirmation(ctx, m.Chat.ID, m.MessageThreadID, user.ID, suggested, token)
	} else {
		r.storePending(user.ID, up)
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			fmt.Sprintf("📁 Получен файл «%s». Как назвать туннель? (a-z0-9_-, начинается с буквы, предложение: %q)",
				m.Document.FileName, suggested),
			"", nil, tg.ReplyKeyboardForTopic("per_router"))
	}
}

func (r *Router) sendImportConfirmation(ctx context.Context, chatID int64, threadID *int64, userID int64, name, token string) {
	kb := tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: fmt.Sprintf("🔄 Заменить %s", name),
				CallbackData: fmt.Sprintf("tunnel_import_replace:%d:%s:%s", userID, name, token)}},
			{{Text: "➕ Добавить как новый",
				CallbackData: fmt.Sprintf("tunnel_import_add:%d:%s:%s", userID, name, token)}},
		},
	}
	if _, err := r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID,
		fmt.Sprintf("📁 Конфиг для туннеля «%s». Что делать?", name),
		"", nil, &kb); err != nil {
		slog.Warn("sendImportConfirmation failed", "err", err, "name", name)
	}
}

func (r *Router) handlePendingNameReply(ctx context.Context, m *tg.Message, user *db.User) bool {
	if user == nil {
		return false
	}
	r.pendingMu.Lock()
	up, ok := r.pending[user.ID]
	r.pendingMu.Unlock()
	if !ok || time.Now().After(up.ExpiresAt) || up.Name != "" {
		return false
	}
	name := sanitizeTunnelName(m.Text)
	if !isValidTunnelName(name) {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			fmt.Sprintf("Имя %q не подходит (нужно a-z0-9_-, начинается с буквы). Попробуй снова.", m.Text),
			"", nil)
		return true
	}
	up.Name = name
	r.storePending(user.ID, up)
	r.sendImportConfirmation(ctx, m.Chat.ID, m.MessageThreadID, user.ID, name, up.Token)
	return true
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

// ----- pendingRebind helpers -----

func (r *Router) putPendingRebind(pr *pendingRebind) {
	r.pendingRebindsMu.Lock()
	defer r.pendingRebindsMu.Unlock()
	if r.pendingRebinds == nil {
		r.pendingRebinds = make(map[string]*pendingRebind)
	}
	r.pendingRebinds[pr.Token] = pr
}

func (r *Router) consumePendingRebind(userID int64, token string) (*pendingRebind, bool) {
	r.pendingRebindsMu.Lock()
	defer r.pendingRebindsMu.Unlock()
	pr, ok := r.pendingRebinds[token]
	if !ok || pr.UserID != userID || time.Now().After(pr.ExpiresAt) {
		delete(r.pendingRebinds, token)
		return nil, false
	}
	delete(r.pendingRebinds, token)
	return pr, true
}

// ----- routes handlers -----

// handleRoutesOpen renders Screen 2. Cache lookup unless force=true.
// On miss, enqueues route_status; the result handler (RoutesPanelNotifier
// in M7.4) edits the panel when the agent answers.
func (r *Router) handleRoutesOpen(ctx context.Context, q *tg.CallbackQuery, args Args, force bool) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "user not found")
		return
	}
	if !force && r.routesCache != nil {
		if snap, ok := r.routesCache.Get(user.ID); ok {
			text := tg.RoutesPanelText(user.Nickname, snap)
			kb := tg.RoutesPanelKeyboard(user.ID, snap)
			_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
			return
		}
	}
	if r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command sink не подключён")
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "route_status", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	loadingText := fmt.Sprintf("🛣 Маршруты — %s\n   обновляется…", user.Nickname)
	loadingKB := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, loadingText, "", &loadingKB)
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		slog.Warn("routes_open: enqueue failed", "err", err)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не получилось запросить статус")
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesRebindStart(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil {
		return
	}
	if r.routesCache == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обнови маршруты и попробуй ещё раз")
		return
	}
	snap, ok := r.routesCache.Get(user.ID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обнови маршруты и попробуй ещё раз")
		return
	}
	text, kb := tg.RebindPickKeyboard(user.ID, args.RebindSrcID, snap)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesRebindPick(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil {
		return
	}
	if args.RebindSrcID == args.RebindDstID {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "src == dst — нечего переносить")
		return
	}
	if r.routesCache == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обнови маршруты и попробуй ещё раз")
		return
	}
	snap, ok := r.routesCache.Get(user.ID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обнови маршруты и попробуй ещё раз")
		return
	}
	token := makeRebindToken()
	r.putPendingRebind(&pendingRebind{
		UserID: user.ID, SrcID: args.RebindSrcID, DstID: args.RebindDstID,
		Token: token, ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	text := tg.RebindPreviewText(snap, args.RebindSrcID, args.RebindDstID, token)
	kb := tg.RebindPreviewKeyboard(user.ID, args.RebindSrcID, args.RebindDstID, token)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}
