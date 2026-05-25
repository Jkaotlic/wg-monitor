package callbacks

import (
	"context"
	cryptoRand "crypto/rand"
	"crypto/sha256"
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

	"github.com/anex/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/internal/backend/upstream"
	"github.com/anex/wg-monitor/pkg/wire"
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
	AdminUserID        int64
	MuteCutoffHour     int
	BackendVersion     string
	UI                 UIConfigSnapshot
	AmneziaBaseURL     string
	AmneziaSecretsPath string
	SelfHostedAmnezia  selfhostedamnezia.Config
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
	routeWizard         *RouteWizardStore
	rebindConfirmAction Action
	cmdSink             CommandEnqueuer // saved for routes_open/refresh enqueue paths
	pendingRebindsMu    sync.Mutex
	pendingRebinds      map[string]*pendingRebind // keyed by 8-hex token

	// Maintenance panel plumbing (M6/M10/M11). All in-memory; lost on restart.
	pendingMaint    *pendingMaintStore
	cooldown        *cooldownStore
	maintConfirmAct Action
	auditCache      *simpleAuditCache
	upstream        *upstream.Cache // used by dispatchSmartReply for Updates section (M12)

	// OPKG-feed repair plumbing. All in-memory; lost on restart (tokens are
	// short-lived, 5 min TTL). SetOpkgRepair wires both at startup.
	pendingOpkgRepair *pendingOpkgRepairStore
	opkgRepairAction  Action

	// Access-control panel plumbing. All in-memory; lost on restart.
	pendingAddOperator *pendingAddOperatorStore

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

	fleetBatches *fleetBatchStore
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

func (r *Router) RouteWizardStore() *RouteWizardStore {
	return r.routeWizard
}

// SetOpkgRepair attaches the pendingOpkgRepair store and the OpkgRepairAction
// handler. Called from cmd/backend at startup; both must be wired together
// because the handler relay (in backend/handler.go) creates pending entries
// and the action consumes them.
func (r *Router) SetOpkgRepair(store *pendingOpkgRepairStore, action Action) {
	r.pendingOpkgRepair = store
	r.opkgRepairAction = action
}

// OpkgRepairStore exposes the store for the backend handler relay path,
// which needs to register pending entries when rendering 🔧 buttons.
func (r *Router) OpkgRepairStore() *pendingOpkgRepairStore {
	return r.pendingOpkgRepair
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
	r.routeWizard = NewRouteWizardStore(5 * time.Minute)
	r.rebindConfirmAction = NewRebindConfirmAction(sink, r.consumePendingRebind, defaultCmdID)
	r.pendingMaint = newPendingMaintStore()
	r.cooldown = newCooldownStore()
	r.auditCache = newSimpleAuditCache()
	r.maintConfirmAct = NewMaintConfirmAction(sink, r.pendingMaint, r.cooldown, defaultCmdID)
	r.pendingAddOperator = newPendingAddOperatorStore()
	r.diagCache = newDiagCache()
	r.fleetBatches = newFleetBatchStore()
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
	if r.cfg.ChatID != 0 && q.Message.Chat.ID != r.cfg.ChatID && !(r.cfg.AdminUserID != 0 && q.From.ID == r.cfg.AdminUserID && q.Message.Chat.ID == q.From.ID && (strings.HasPrefix(q.Data, "panel:") || strings.HasPrefix(q.Data, "access:"))) {
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
	if args.Action == "access" {
		if r.cfg.AdminUserID == 0 || q.From.ID != r.cfg.AdminUserID {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "доступ только у админа")
			return
		}
		r.handleAccessCallback(ctx, q, args)
		return
	}
	if args.Action == "panel" && args.PanelScreen != "help" && r.cfg.AdminUserID != 0 && q.From.ID != r.cfg.AdminUserID {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "доступ только у админа")
		return
	}
	if isSelfHostedAmneziaAction(args.Action) && r.cfg.AdminUserID != 0 && q.From.ID != r.cfg.AdminUserID {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "доступ только у админа")
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
	case "restart_tunnel", "diag_now", "pingcheck_now", "force_recheck", "opkg_upgrade", "router_doctor",
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
	case "amz_refresh":
		r.handleAmneziaRefresh(ctx, q, args)
		return
	case "amz_open":
		r.handleAmneziaOpen(ctx, q, args)
		return
	case "amz_countries":
		r.handleAmneziaCountries(ctx, q, args)
		return
	case "amz_delete":
		r.handleAmneziaDeleteAsk(ctx, q, args)
		return
	case "amz_delete_confirm":
		r.handleAmneziaDeleteConfirm(ctx, q, args)
		return
	case "amz_dl":
		r.handleAmneziaDownloadAsk(ctx, q, args)
		return
	case "amz_dl_confirm":
		r.handleAmneziaDownloadConfirm(ctx, q, args)
		return
	case "amz_selfhosted_issue":
		r.handleSelfHostedAmneziaIssue(ctx, q, args)
		return
	case "amz_selfhosted_confirm":
		r.handleSelfHostedAmneziaConfirm(ctx, q, args)
		return
	case "hmn_refresh":
		r.handleHideMyRefresh(ctx, q, args)
		return
	case "hmn_open":
		r.handleHideMyOpen(ctx, q, args)
		return
	case "hmn_page":
		r.handleHideMyPage(ctx, q, args)
		return
	case "hmn_delete":
		r.handleHideMyDeleteAsk(ctx, q, args)
		return
	case "hmn_delete_confirm":
		r.handleHideMyDeleteConfirm(ctx, q, args)
		return
	case "hmn_dl":
		r.handleHideMyDownloadAsk(ctx, q, args)
		return
	case "hmn_dl_confirm":
		r.handleHideMyDownloadConfirm(ctx, q, args)
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
	case "routes_add":
		r.handleRoutesAddStart(ctx, q, args)
		return
	case "routes_add_type":
		r.handleRoutesAddType(ctx, q, args)
		return
	case "routes_add_tunnel":
		r.handleRoutesAddTunnel(ctx, q, args)
		return
	case "routes_add_confirm":
		r.handleRoutesAddConfirm(ctx, q, args)
		return
	case "routes_add_cancel":
		r.handleRoutesAddCancel(ctx, q, args)
		return
	case "routes_del":
		r.handleRoutesDelete(ctx, q, args)
		return
	case "routes_del_confirm":
		r.handleRoutesDeleteConfirm(ctx, q, args)
		return
	case "routes_del_cancel":
		r.handleRoutesDeleteCancel(ctx, q, args)
		return
	case "routes_hrneo":
		r.handleRoutesHRNeo(ctx, q, args)
		return
	case "routes_hrneo_doctor":
		r.handleRoutesHRNeoDoctor(ctx, q, args)
		return
	case "routes_snapshot":
		r.handleRoutesSnapshot(ctx, q, args)
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
	case "pingcheck_open":
		if r.pingcheckOpenAct != nil {
			action = r.pingcheckOpenAct
		}
	case "pingcheck_toggle":
		if r.pingcheckToggleAct != nil {
			action = r.pingcheckToggleAct
		}
	case "maint_open":
		r.handleMaintOpen(ctx, q, args)
		return
	case "maint_close":
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "закрыто")
		empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
		_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, q.Message.Text, "", &empty)
		return
	case "maint_restart":
		r.handleMaintRestart(ctx, q, args)
		return
	case "maint_fw_open":
		r.handleMaintFwOpen(ctx, q, args)
		return
	case "maint_fw_check":
		r.handleMaintFwCheck(ctx, q, args)
		return
	case "maint_fw_install":
		r.handleMaintFwInstall(ctx, q, args)
		return
	case "maint_confirm", "maint_fw_confirm":
		if r.maintConfirmAct != nil {
			action = r.maintConfirmAct
		}
	case "diag_raw":
		body, ok := r.diagCache.Get(args.DiagRawToken)
		if !ok {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "отчёт уже не доступен (5 мин TTL)")
			return
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
		full := "📄 Полный отчёт диагностики:\n\n```\n" + body + "\n```"
		if _, err := r.tg.SendMessage(ctx, q.Message.Chat.ID, q.Message.MessageThreadID, full, "", nil); err != nil {
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
	case "opkg_disable":
		if r.opkgRepairAction == nil {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ремонт фидов не настроен")
			return
		}
		status, err := r.opkgRepairAction.Apply(ctx, q, args)
		if err != nil {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, err.Error())
			return
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, status)
		return
	case "panel":
		r.handlePanelCallback(ctx, q, args)
		return
	case "compat_btn":
		r.handleCompatBtn(ctx, q, args)
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

func isSelfHostedAmneziaAction(action string) bool {
	return action == "amz_selfhosted_issue" || action == "amz_selfhosted_confirm"
}

// aclAllow gates a callback by owner identity. Returns true to proceed,
// false to short-circuit (the function itself sends the toast and logs).
//
// Decision table (q.From.ID = F, args.UserID = R, user.TelegramUserID = O,
// user.TelegramThreadID = T, q.Message.MessageThreadID = M):
//
//	F == AdminUserID                        → allow (admin override)
//	R == 0                                  → allow (no router target encoded)
//	users.GetByID(R) fails or returns nil   → allow + warn (don't break on DB hiccup)
//	O != nil && *O == F                     → allow (rightful owner)
//	O != nil && *O != F                     → reject ("это не твой роутер")
//	O == nil && M != nil && T != nil && *M == *T  → TOFU bind F → SetTelegramUserID, allow
//	O == nil (otherwise)                    → allow (unbound, backwards-compat)
//
// The unbound-allow branch lets existing deployments keep working until
// the first owner action in the owner's topic auto-binds them. After that
// non-owner taps are rejected.
func (r *Router) aclAllow(ctx context.Context, q *tg.CallbackQuery, args Args) bool {
	if r.cfg.AdminUserID != 0 && q.From.ID == r.cfg.AdminUserID {
		return true
	}
	if args.UserID == 0 {
		return true
	}
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		slog.Warn("acl: user lookup failed, allowing", "router_user_id", args.UserID, "err", err)
		return true
	}
	if user.TelegramUserID != nil {
		if *user.TelegramUserID == q.From.ID {
			return true
		}
		// Owner mismatch — try the operator whitelist before rejecting.
		if r.d.RouterOperators().HasAccess(user.ID, q.From.ID) {
			return true
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
			slog.Warn("acl: TOFU bind failed, allowing", "router_user_id", user.ID, "from", q.From.ID, "err", err)
		} else {
			slog.Info("acl: TOFU bound router owner", "router_user_id", user.ID, "tg_user_id", q.From.ID)
		}
	}
	return true
}

// HandleMessage dispatches an incoming text Message: chat/admin gate, topic
// resolution, then the appropriate smart-reply / operations action.
//
// Allowlist: chat must equal cfg.ChatID; from must equal cfg.AdminUserID
// (text-message router is admin-only — group members can still tap inline
// callbacks per the 2026-04-30 policy reversal, but typing into the chat
// is a one-operator surface).
func (r *Router) HandleMessage(ctx context.Context, m *tg.Message) {
	// Add-operator FSM intercept: admin sends a qualifying message in DM
	// with the bot while a pending FSM exists. Falls through to normal
	// handlers otherwise.
	if r.cfg.AdminUserID != 0 && m.From.ID == r.cfg.AdminUserID && m.Chat.ID == m.From.ID {
		if p, ok := r.pendingAddOperator.get(m.From.ID); ok {
			r.processAddOperatorMessage(ctx, m, p)
			return
		}
	}
	adminDM := r.cfg.AdminUserID != 0 && m.From.ID == r.cfg.AdminUserID && m.Chat.ID == m.From.ID
	if r.cfg.ChatID != 0 && m.Chat.ID != r.cfg.ChatID && !adminDM {
		return
	}
	isAdmin := r.cfg.AdminUserID == 0 || m.From.ID == r.cfg.AdminUserID
	if !isAdmin {
		// Non-admin path: operators get owner-parity but ONLY in their own
		// router's per_router topic. resolveTopicKind classifies by thread
		// id; if the topic is unknown / summary / systemic, drop. If the
		// operator isn't whitelisted on that router, drop. The downstream
		// switch re-runs resolveTopicKind, which is cheap — keeping the
		// existing flow untouched simplifies the diff.
		kind, user := r.resolveTopicKind(m.MessageThreadID)
		if kind != "per_router" || user == nil {
			return
		}
		if !r.d.RouterOperators().HasAccess(user.ID, m.From.ID) {
			return
		}
		// Operator passes the gate. Skip handleAdminCommand entirely — slash
		// commands (/ensure_topics, /this_is, /panel, ...) stay admin-only.
		// Operators get /help and /keyboard — both are personal-recovery
		// actions scoped to their own topic, not fleet-wide management.
		switch strings.TrimSpace(m.Text) {
		case "/help":
			r.handleHelpCommand(ctx, m)
			return
		case "/keyboard":
			r.handleKeyboardCommand(ctx, m)
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
	kind, user := r.resolveTopicKind(m.MessageThreadID)
	// Document handler — before text switch.
	if m.Document != nil {
		r.handleDocumentUpload(ctx, m, kind, user)
		return
	}
	if r.handleAmneziaKeyMessage(ctx, m, kind, user) {
		return
	}
	if r.handleHideMyCodeMessage(ctx, m, kind, user) {
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
	case "🎛 Туннели":
		if kind == "per_router" && user != nil {
			text, kb := r.buildTunnelsPanel(user)
			_, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &kb)
			if err != nil {
				slog.Warn("tunnels-panel send failed", "err", err, "user", user.Nickname)
			}
		} else {
			_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		}
	case "🛣 Маршруты":
		if kind == "per_router" && user != nil {
			r.openRoutesPanelMessage(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		}
	case "Amnezia Premium":
		if kind == "per_router" && user != nil {
			r.sendAmneziaPremiumPanel(ctx, m.Chat.ID, m.MessageThreadID, nil, user)
		} else {
			_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
				"Amnezia Premium работает только в топике роутера.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		}
	case "HideMy.name":
		if kind == "per_router" && user != nil {
			r.sendHideMyPremiumPanel(ctx, m.Chat.ID, m.MessageThreadID, nil, user)
		} else {
			_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
				"HideMy.name работает только в топике роутера.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		}
	case "🛠 Обслуживание":
		if kind == "per_router" && user != nil {
			r.openMaintPanelMessage(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
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
	case "🩺 Проверка", "🩺 Домашний роутер":
		r.dispatchConnectivityCheck(ctx, m, kind, user, "router_doctor",
			"⏳ Проверяю роутер изнутри: awg-manager, туннели, pingcheck и процессы…")
	case "📋 Список юзеров":
		r.dispatchListUsers(ctx, m, kind)
	case "📊 Здоровье флота":
		r.dispatchFleetHealth(ctx, m, kind)
	default:
		if r.handlePendingRouteReply(ctx, m, user) {
			return
		}
		if r.handleRouteExplainReply(ctx, m, kind, user) {
			return
		}
		if r.handlePendingNameReply(ctx, m, user) {
			return
		}
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

func (r *Router) handleRouteExplainReply(ctx context.Context, m *tg.Message, kind string, user *db.User) bool {
	if kind != "per_router" || user == nil || r.routesCache == nil {
		return false
	}
	target, ok := parseRouteExplainText(m.Text)
	if !ok {
		return false
	}
	snap, found := r.routesCache.Get(user.ID)
	if !found {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"Сначала обнови маршруты, потом отправь: explain example.com", "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
		return true
	}
	text := tg.RouteExplainText(user.Nickname, target, snap)
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
	return true
}

func parseRouteExplainText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	prefixes := []string{"explain ", "route ", "куда "}
	low := strings.ToLower(text)
	for _, p := range prefixes {
		if strings.HasPrefix(low, p) {
			target := strings.TrimSpace(text[len(p):])
			return target, target != ""
		}
	}
	return "", false
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

// openRoutesPanelMessage sends the initial Routes panel as a fresh message
// (so subsequent edits target this MessageID) and enqueues route_status.
// The cmd-result handler edits when the agent answers.
func (r *Router) openRoutesPanelMessage(ctx context.Context, m *tg.Message, user *db.User) {
	loadingText := alerts.Card{
		Badge:   "⏳",
		Label:   "🛣 Маршруты",
		Summary: "читаю правила с роутера",
		Meta:    []string{alerts.KV("роутер", user.Nickname)},
		Hint:    "Если экран не обновится, нажми «Обновить».",
	}.Render(alerts.CardOpts{})
	// IMPORTANT: send WITHOUT a reply_markup. TG refuses editMessageText on
	// messages whose reply_markup is a ReplyKeyboardMarkup (only inline-kb
	// markups are editable). RoutesNotifier needs to edit this message in
	// place, so we forgo the per-message keyboard re-attach here — the
	// bottom panel persists from the user's previous message anyway.
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

// openMaintPanelMessage sends the initial Maintenance panel and enqueues a
// fresh version_audit. When a recent (≤ maintCacheFreshFor) audit is cached,
// the panel is rendered from cache instantly with a "🔄 обновляется в фоне…"
// header — the MaintNotifier replaces it with fresh data when the agent
// answers (typically within a second). With no usable cache, falls back to
// the bare "обновляется…" placeholder.
func (r *Router) openMaintPanelMessage(ctx context.Context, m *tg.Message, user *db.User) {
	const maintCacheFreshFor = 5 * time.Minute
	var (
		mid int64
		err error
	)
	if va, age, ok := r.auditCache.GetVersionAuditWithAge(user.ID); ok && age < maintCacheFreshFor {
		args := buildMaintPanelArgs(ctx, user, va, r.upstream, r.cooldown)
		text := "🔄 обновляется в фоне…\n\n" + tg.MaintPanelText(args)
		kb := tg.MaintPanelKeyboard(user.ID, args)
		// SendMessageWithReplyKeyboard accepts an *InlineKeyboardMarkup —
		// editMessageText works against inline-kb markups (unlike reply-kb).
		mid, err = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &kb)
	} else {
		loadingText := alerts.Card{
			Badge:   "⏳",
			Label:   "🛠 Обслуживание",
			Summary: "читаю версии и состояние сервисов",
			Meta:    []string{alerts.KV("роутер", user.Nickname)},
			Hint:    "Если экран не обновится, нажми «Проверить апдейты».",
		}.Render(alerts.CardOpts{})
		// IMPORTANT: no reply_markup — see openRoutesPanelMessage for the
		// editMessageText/ReplyKeyboardMarkup incompatibility.
		mid, err = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, loadingText, "", nil)
	}
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

func (r *Router) BuildTunnelsPanelByUserID(userID int64) (string, tg.InlineKeyboardMarkup, bool) {
	u, err := r.d.Users().GetByID(userID)
	if err != nil || u == nil {
		if err != nil {
			slog.Warn("BuildTunnelsPanelByUserID: user lookup failed", "err", err, "user", userID)
		}
		return "", tg.InlineKeyboardMarkup{}, false
	}
	text, kb := r.buildTunnelsPanel(u)
	return text, kb, true
}

// dispatchHelp sends the static help text for the topic kind.
func (r *Router) dispatchHelp(ctx context.Context, m *tg.Message, kind string) {
	body := "Кнопки внизу:\n" +
		"📊 Что происходит? — состояние роутера прямо сейчас.\n" +
		"🆘 Помощь — этот текст.\n\n" +
		"В топиках Сводки/Системного:\n" +
		"📋 Список юзеров, 📊 Здоровье флота — операторские команды."
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, body, "", nil, r.cfg.UI.KeyboardForTopic(kind))
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
	if r.auditCache != nil {
		if va, ok := r.auditCache.GetVersionAudit(user.ID); ok {
			args.Updates = computeUpdates(ctx, r.upstream, va)
		}
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

// collectTunnelViews builds []alerts.TunnelView from the latest per-tunnel
// event for this user.
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
// current_status='hard' for this user. Routed via StateRepo so the SQL stays
// in one place (LOGIC-07).
func (r *Router) collectActiveIncidents(userID int64) []alerts.IncidentView {
	rows, err := r.d.State().ActiveHardForUser(userID, time.Now())
	if err != nil {
		slog.Warn("collectActiveIncidents: query failed", "err", err, "user", userID)
		return nil
	}
	out := make([]alerts.IncidentView, 0, len(rows))
	for _, row := range rows {
		out = append(out, alerts.IncidentView{
			CheckName: row.CheckName,
			HardSince: row.HardSince,
			FailCount: row.FailCount,
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

func (r *Router) handleDocumentUpload(ctx context.Context, m *tg.Message, kind string, user *db.User) {
	slog.Info("document-upload", "file", m.Document.FileName, "size", m.Document.FileSize, "kind", kind, "has_user", user != nil)
	if kind != "per_router" || user == nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"конфиги принимаются только в топике роутера.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return
	}
	if m.Document.FileSize > 50*1024 {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"файл слишком большой (максимум 50 КБ для .conf).", "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
		return
	}
	filePath, err := r.tg.GetFile(ctx, m.Document.FileID)
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"не удалось получить файл: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
		return
	}
	data, err := r.tg.DownloadFile(ctx, filePath)
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"не удалось скачать файл: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
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
			"", nil, r.cfg.UI.KeyboardForTopic("per_router"))
	}
}

func (r *Router) sendImportConfirmation(ctx context.Context, chatID int64, threadID *int64, userID int64, name, token string) {
	kb := tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: fmt.Sprintf("🔄 Заменить %s", name),
				CallbackData: fmt.Sprintf("tunnel_import_replace:%d:%s:%s", userID, panelSentinel, token)}},
			{{Text: "➕ Добавить как новый",
				CallbackData: fmt.Sprintf("tunnel_import_add:%d:%s:%s", userID, panelSentinel, token)}},
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
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			fmt.Sprintf("Имя %q не подходит (нужно a-z0-9_-, начинается с буквы). Попробуй снова.", m.Text),
			"", nil, r.cfg.UI.KeyboardForTopic("per_router"))
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
	if !ok {
		return nil, false
	}
	// Don't delete on UserID mismatch — другой member чата не должен
	// иметь возможности DoS'нуть owner'у его подтверждение (BUG-04).
	if pr.UserID != userID {
		return nil, false
	}
	if time.Now().After(pr.ExpiresAt) {
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

// ----- maint handlers -----

// handleMaintOpen re-renders the maint panel: re-enqueues version_audit so
// MaintNotifier (M11) edits the panel with fresh data when the agent answers.
func (r *Router) handleMaintOpen(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "user not found")
		return
	}
	if r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command sink не подключён")
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "version_audit", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	loadingText := fmt.Sprintf("🛠 Обслуживание — %s\n   обновляется…", user.Nickname)
	loadingKB := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, loadingText, "", &loadingKB)
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не получилось запросить статус")
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// handleMaintRestart renders the per-service confirm screen and stores a
// pending token. For "router" name, also checks the existing cooldown
// window and short-circuits with a toast if active.
func (r *Router) handleMaintRestart(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil {
		return
	}
	if args.MaintName == "router" {
		if rem := r.cooldown.remaining(user.ID, "router_reboot"); rem > 0 {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, fmt.Sprintf("🕒 кулдаун ещё %s", rem.Round(time.Second)))
			return
		}
	}
	tok := makeMaintToken()
	r.pendingMaint.put(&pendingMaint{
		UserID: user.ID, Name: args.MaintName, Token: tok,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	text := tg.RestartConfirmText(args.MaintName, tok)
	kb := tg.RestartConfirmKeyboard(user.ID, args.MaintName, tok)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// handleMaintFwOpen renders the firmware screen using cached FirmwareStatus
// when available, or triggers a fresh fetch via handleMaintFwCheck if not.
func (r *Router) handleMaintFwOpen(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil {
		return
	}
	fs, ok := r.auditCache.GetFirmwareStatus(user.ID)
	if !ok {
		// Fall back to a fresh fetch — same code path as Перепроверить.
		r.handleMaintFwCheck(ctx, q, args)
		return
	}
	cdRem := r.cooldown.remaining(user.ID, "firmware_install")
	text := tg.FirmwareScreenText(user.Nickname, fs)
	kb := tg.FirmwareScreenKeyboard(user.ID, fs, cdRem)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// handleMaintFwCheck enqueues a firmware_status command and shows a loading
// placeholder while we wait for the agent.
func (r *Router) handleMaintFwCheck(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.cmdSink == nil {
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "firmware_status", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	loading := fmt.Sprintf("📦 Прошивка — %s\n   обновляется…", user.Nickname)
	empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, loading, "", &empty)
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не получилось")
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// handleMaintFwInstall renders the firmware install confirm screen.
// Cooldown check short-circuits with a toast.
func (r *Router) handleMaintFwInstall(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil {
		return
	}
	if rem := r.cooldown.remaining(user.ID, "firmware_install"); rem > 0 {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, fmt.Sprintf("🕒 кулдаун ещё %s", rem.Round(time.Second)))
		return
	}
	tok := makeMaintToken()
	r.pendingMaint.put(&pendingMaint{
		UserID: user.ID, Name: "firmware", Token: tok,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	text := tg.FirmwareConfirmText(tok)
	kb := tg.FirmwareConfirmKeyboard(user.ID, tok)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// ----- routes rebind handlers -----

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

func (r *Router) handleRoutesAddStart(ctx context.Context, q *tg.CallbackQuery, args Args) {
	text := "🛣 Добавить маршрут\n\nЧто направляем:\n  • DNS / HR-Neo — домены через выбранный туннель\n  • Static CIDR — IP/подсети через выбранный туннель"
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{{Text: "DNS (NDMS)", CallbackData: fmt.Sprintf("routes_add_type:%d:_panel_:dns", args.UserID)}},
		{{Text: "DNS / HR-Neo", CallbackData: fmt.Sprintf("routes_add_type:%d:_panel_:dns_hr", args.UserID)}},
		{{Text: "Static CIDR", CallbackData: fmt.Sprintf("routes_add_type:%d:_panel_:static", args.UserID)}},
		{{Text: "↩ Отмена", CallbackData: fmt.Sprintf("routes_back:%d:_panel_", args.UserID)}},
	}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesAddType(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.routesCache == nil || r.routeWizard == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "маршруты ещё не загружены")
		return
	}
	snap, ok := r.routesCache.Get(user.ID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обнови маршруты и попробуй снова")
		return
	}
	draft := r.routeWizard.PutAddDraft(RouteAddDraft{
		UserID: user.ID, ThreadID: q.Message.MessageThreadID, RouterID: user.ID,
		Kind: args.RouteKind, UseHRNeo: args.RouteUseHRNeo && snap.HRNeo.Installed && snap.HRNeo.Running,
	})
	rows := make([][]tg.InlineKeyboardButton, 0, len(snap.Tunnels)+1)
	for _, t := range snap.Tunnels {
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         t.Name,
			CallbackData: fmt.Sprintf("routes_add_tunnel:%d:_panel_:%s:%s", user.ID, draft.Token, t.ID),
		}})
	}
	rows = append(rows, []tg.InlineKeyboardButton{{Text: "↩ Отмена", CallbackData: fmt.Sprintf("routes_add_cancel:%d:_panel_:%s", user.ID, draft.Token)}})
	text := "🛣 Добавить маршрут\n\nКуда вести трафик:\n  • выбери туннель, через который должны идти эти домены или IP"
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: rows}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesAddTunnel(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.routeWizard == nil {
		return
	}
	draft, ok := r.routeWizard.GetAddDraft(user.ID, q.Message.MessageThreadID, user.ID, args.RouteDraftToken)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "draft expired")
		return
	}
	draft.TunnelID = args.RebindDstID
	r.routeWizard.PutAddDraft(draft)
	text := "🛣 Добавить маршрут\n\nОтправь одним сообщением:\n  • первая строка — название правила\n  • следующие строки — домены или IP\n\nПример:\nmedia\nexample.com\napi.example.com"
	if draft.Kind == "static" {
		text = "🛣 Добавить static route\n\nОтправь одним сообщением:\n  • первая строка — название правила\n  • следующие строки — CIDR/IP цели\n\nПример:\ncorp\n10.10.0.0/16\n192.0.2.7"
	}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", nil)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handlePendingRouteReply(ctx context.Context, m *tg.Message, user *db.User) bool {
	if user == nil || r.routeWizard == nil {
		return false
	}
	draft, ok := r.routeWizard.GetOpenAddDraft(user.ID, m.MessageThreadID, user.ID)
	if !ok || draft.TunnelID == "" {
		return false
	}
	name, targets := parseRouteAddReply(m.Text)
	if name == "" || len(targets) == 0 {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"Не понял маршрут.\n\nФормат:\n  • первая строка — название\n  • ниже хотя бы одна цель: домен, IP или CIDR", "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
		return true
	}
	draft.Name = name
	draft.Targets = targets
	draft = r.routeWizard.PutAddDraft(draft)
	if r.cmdSink == nil {
		return true
	}
	ack := "⏳ Проверяю, не конфликтует ли маршрут с уже существующими правилами…"
	mid, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, ack, "", nil)
	if err != nil {
		mid = m.MessageID
	}
	cmd := wire.Command{
		ID:       fmt.Sprintf("route_add_plan:%s:%s", draft.Token, defaultCmdID()),
		Action:   "route_add_plan",
		IssuedAt: time.Now().UTC(),
		Args: map[string]any{
			"kind": draft.Kind, "name": draft.Name, "tunnel_id": draft.TunnelID,
			"targets": draft.Targets, "use_hr_neo": draft.UseHRNeo,
		},
	}
	ref := cmdpkg.MessageRef{ChatID: m.Chat.ID, MessageID: mid, ThreadID: m.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "Не удалось поставить проверку маршрута в очередь: "+err.Error(), "", nil)
	}
	return true
}

func (r *Router) handleRoutesAddConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.routeWizard == nil || r.cmdSink == nil {
		return
	}
	draft, ok := r.routeWizard.ConsumeAddConfirm(user.ID, q.Message.MessageThreadID, user.ID, args.RouteDraftToken, args.RouteConfirmToken)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "превью устарело")
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "route_add", IssuedAt: time.Now().UTC(), Args: map[string]any{
		"kind": draft.Kind, "name": draft.Name, "tunnel_id": draft.TunnelID,
		"targets": draft.Targets, "use_hr_neo": draft.UseHRNeo, "draft_hash": draft.PreviewHash,
	}}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось поставить задачу")
		return
	}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "⏳ Применяю изменение маршрута…", "", nil)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesAddCancel(ctx context.Context, q *tg.CallbackQuery, args Args) {
	if r.routeWizard != nil {
		r.routeWizard.CancelAddDraft(args.UserID, q.Message.MessageThreadID, args.UserID, args.RouteDraftToken)
	}
	r.handleRoutesOpen(ctx, q, args, false)
}

func (r *Router) handleRoutesDelete(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.routesCache == nil || r.routeWizard == nil {
		return
	}
	snap, ok := r.routesCache.Get(user.ID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обнови маршруты и попробуй снова")
		return
	}
	if args.RouteToken == "_list_" {
		rows := make([][]tg.InlineKeyboardButton, 0, len(snap.Rules)+1)
		for _, rule := range snap.Rules {
			token := r.routeWizard.PutRouteToken(RouteToken{UserID: user.ID, ThreadID: q.Message.MessageThreadID, RouterID: user.ID, Kind: rule.Kind, RouteID: rule.ID, PreviewHash: routeHash(rule)})
			label := rule.Name
			if label == "" {
				label = rule.ID
			}
			rows = append(rows, []tg.InlineKeyboardButton{{Text: label, CallbackData: fmt.Sprintf("routes_del:%d:_panel_:%s", user.ID, token)}})
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: "↩ Отмена", CallbackData: fmt.Sprintf("routes_back:%d:_panel_", user.ID)}})
		_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "🛣 Удалить маршрут\n\nВыбери одно правило для удаления.", "", &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
		return
	}
	rt, ok := r.routeWizard.GetRouteToken(user.ID, q.Message.MessageThreadID, user.ID, args.RouteToken)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "выбор маршрута устарел")
		return
	}
	draft := r.routeWizard.PutDeleteDraft(RouteDeleteDraft{UserID: user.ID, ThreadID: q.Message.MessageThreadID, RouterID: user.ID, Kind: rt.Kind, RouteID: rt.RouteID, PreviewHash: rt.PreviewHash})
	cmd := wire.Command{ID: fmt.Sprintf("route_delete_plan:%s:%s", draft.Token, defaultCmdID()), Action: "route_delete_plan", IssuedAt: time.Now().UTC(), Args: map[string]any{
		"kind": rt.Kind, "route_id": rt.RouteID,
	}}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось поставить задачу")
		return
	}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "⏳ Готовлю превью удаления…", "", nil)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesDeleteConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.routeWizard == nil || r.cmdSink == nil {
		return
	}
	draft, ok := r.routeWizard.ConsumeDeleteConfirm(user.ID, q.Message.MessageThreadID, user.ID, args.RouteDraftToken, args.RouteConfirmToken)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "превью устарело")
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "route_delete", IssuedAt: time.Now().UTC(), Args: map[string]any{
		"kind": draft.Kind, "route_id": draft.RouteID, "preview_hash": draft.PreviewHash,
	}}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось поставить задачу")
		return
	}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "⏳ Удаляю маршрут…", "", nil)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesDeleteCancel(ctx context.Context, q *tg.CallbackQuery, args Args) {
	if r.routeWizard != nil {
		r.routeWizard.CancelDeleteDraft(args.UserID, q.Message.MessageThreadID, args.UserID, args.RouteDraftToken)
	}
	r.handleRoutesOpen(ctx, q, args, false)
}

func (r *Router) handleRoutesHRNeo(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.cmdSink == nil {
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "hrneo_inventory", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось поставить задачу")
		return
	}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "⏳ Загружаю HR-Neo правила…", "", nil)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesHRNeoDoctor(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.cmdSink == nil {
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "hrneo_doctor", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{Action: "hrneo_doctor", ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось поставить задачу")
		return
	}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "⏳ Проверяю HR-Neo…", "", nil)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesSnapshot(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.routesCache == nil {
		return
	}
	snap, ok := r.routesCache.Get(user.ID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "сначала обнови маршруты")
		return
	}
	text := tg.RouteSnapshotText(user.Nickname, snap)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", user.ID)},
		{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", user.ID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func parseRouteAddReply(text string) (string, []string) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 1 {
		parts := strings.SplitN(lines[0], ":", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0]), splitRouteTargets(parts[1])
		}
		return "", nil
	}
	name := strings.TrimSpace(lines[0])
	targets := splitRouteTargets(strings.Join(lines[1:], "\n"))
	return name, targets
}

func splitRouteTargets(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool)
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func routeHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
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
	infos := upstream.ComputeUpdates(ctx, up, va)
	if len(infos) == 0 {
		return nil
	}
	out := make([]alerts.UpdateAvailable, 0, len(infos))
	for _, u := range infos {
		out = append(out, alerts.UpdateAvailable{Name: u.Name, Installed: u.Installed, Available: u.Available, Hint: u.Hint})
	}
	return out
}

// NewMaintNotifier returns a MaintPanelNotifier wired to this router's
// internal stores (cooldown, auditCache). Call once at startup with the
// upstream cache and DB; pass the returned value into handler.Deps.MaintNotifier.
func (r *Router) NewMaintNotifier(tgClient MaintEditTG, up *upstream.Cache) *MaintPanelNotifier {
	return &MaintPanelNotifier{
		TG:       tgClient,
		Up:       up,
		Cooldown: r.cooldown,
		Audit:    r.auditCache,
		DB:       r.d,
	}
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
	return &PingCheckPanelNotifier{TG: r.tg, DB: r.d}
}
