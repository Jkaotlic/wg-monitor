package callbacks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
)

// TGClient is the subset of tg.Client used by the router.
type TGClient interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error
	GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error)
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
	d       *db.DB
	tg      TGClient
	cfg     Config
	silence *SilenceAction
	ack     *AckAction
	mute    *MuteAction
	history *HistoryAction
	command *CommandAction
}

// NewRouter builds a Router without a command-channel sink. Command-action
// callbacks (restart_tunnel/diag_now/...) will toast an error.
func NewRouter(d *db.DB, tgClient TGClient, cfg Config) *Router {
	return NewRouterWithSink(d, tgClient, nil, cfg)
}

// NewRouterWithSink builds a Router whose command-action callbacks enqueue
// wire.Command into the provided sink for the agent to long-poll.
func NewRouterWithSink(d *db.DB, tgClient TGClient, sink CommandEnqueuer, cfg Config) *Router {
	return &Router{
		d:       d,
		tg:      tgClient,
		cfg:     cfg,
		silence: NewSilenceAction(d),
		ack:     NewAckAction(d),
		mute:    NewMuteAction(d, cfg.MuteCutoffHour),
		history: NewHistoryAction(d, tgClient, cfg.ChatID),
		command: NewCommandAction(sink, nil),
	}
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
	case "🆘 Помощь":
		r.dispatchHelp(ctx, m, kind)
	case "📋 Список юзеров":
		r.dispatchListUsers(ctx, m)
	case "📊 Здоровье флота":
		r.dispatchFleetHealth(ctx, m)
	default:
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

// buildTunnelsPanel queries the user's latest per-tunnel events and renders
// (text, inline-keyboard) for the Tunnels Panel. Used both by initial dispatch
// (tap on "🎛 Туннели" reply-keyboard button) and by the tunnels_refresh
// callback after a toggle action.
func (r *Router) buildTunnelsPanel(u *db.User) (string, tg.InlineKeyboardMarkup) {
	rows, err := r.d.Events().LatestEventsByPrefix(u.ID, "tunnel_")
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
