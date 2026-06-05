// pingcheck_panel.go — backend rendering of the PingCheck monitor panel
// (design spec section 2). Two responsibilities:
//   - PingCheckPanelNotifier: handles pingcheck_status / pingcheck_toggle
//     CommandResults from the agent, edits the panel message in place
//   - PingCheckOpenAction / PingCheckToggleAction: callback handlers
//     for the inline-keyboard taps (added in Task 9)
package callbacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// PingCheckEditTG is the subset of tg.Client used by PingCheckPanelNotifier.
type PingCheckEditTG interface {
	EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error
}

// PingCheckPanelNotifier renders pingcheck_status and pingcheck_toggle
// CommandResults into the original panel message. Stateless aside from
// the tg/db handles.
type PingCheckPanelNotifier struct {
	TG PingCheckEditTG
	DB *db.DB
}

// NotifyCommandResult dispatches on ref.Action. Returns nil for actions
// not owned by this notifier (caller falls through to TGNotifier).
func (n *PingCheckPanelNotifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error {
	user, err := n.DB.Users().GetByID(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user lookup: %w", err)
	}
	switch ref.Action {
	case "pingcheck_status":
		return n.renderStatus(ctx, ref, res, user)
	case "pingcheck_toggle":
		return n.renderToggle(ctx, ref, res, user)
	default:
		return fmt.Errorf("PingCheckPanelNotifier: unsupported action %q", ref.Action)
	}
}

func (n *PingCheckPanelNotifier) renderStatus(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		return n.renderErr(ctx, ref, user, res.Output, "Не удалось прочитать PingCheck")
	}
	entries, globalEnabled, err := decodePingCheckStatus(res.Output)
	if err != nil {
		return n.renderErr(ctx, ref, user, err.Error(), "Не удалось распарсить ответ awg-mgr")
	}
	text := tg.PingCheckPanelText(user.Nickname, globalEnabled, entries)
	kb := tg.PingCheckPanelKeyboard(user.ID, entries)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *PingCheckPanelNotifier) renderToggle(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	// Banner-only render. The follow-up pingcheck_status (auto-enqueued
	// by the toggle handler — see Task 9) will replace the panel body on
	// its own CommandResult. So here we just drop a one-line banner.
	prefix := "✅ Переключение применено"
	if res.Status != "ok" {
		card := alerts.Card{
			Badge: "❌",
			Label: "Не удалось переключить PingCheck",
		}
		summary, hint := alerts.HintFor("pingcheck_toggle", res.Output)
		card.Summary = summary
		card.Hint = hint
		prefix = card.Render(alerts.CardOpts{MaxBytes: 800})
	}
	// Append banner; keyboard stays the same as last render (we don't
	// have it in hand here — passing nil would clear it). Realistic
	// path: the auto-refresh that follows will rebuild the keyboard
	// from fresh status. So we just edit the text and keep going.
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, prefix, "", nil)
}

func (n *PingCheckPanelNotifier) renderErr(ctx context.Context, ref cmdpkg.MessageRef, user *db.User, errOut, label string) error {
	summary, hint := alerts.HintFor("pingcheck_status", errOut)
	card := alerts.Card{
		Badge:   "❌",
		Label:   "📡 PingCheck",
		Summary: summary,
		Meta:    []string{alerts.KV("роутер", user.Nickname), "агент не ответил"},
		Hint:    hint,
	}
	body := card.Render(alerts.CardOpts{MaxBytes: 3500})
	kb := pingcheckErrorRecoveryKeyboard(user.ID)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, body, "", &kb)
}

func pingcheckErrorRecoveryKeyboard(userID int64) tg.InlineKeyboardMarkup {
	return tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔄 Повторить", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
	}, {
		{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
		{Text: "🎛 Туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
	}, {
		{Text: "🛣 Маршруты", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		{Text: "🛠 Обслуживание", CallbackData: fmt.Sprintf("maint_open:%d:_panel_", userID)},
	}}}
}

// decodePingCheckStatus converts the awg-mgr passthrough JSON into the
// renderer entry shape. Sorted by tunnel name for stable order.
// NDMSName is resolved by the agent via /api/tunnels/all before the JSON
// is passed through; if absent the toggle button will have an empty segment
// (caught upstream by the agent enrichment step).
func decodePingCheckStatus(body string) ([]tg.PingCheckPanelEntry, bool, error) {
	var st awgmgr.PingCheckStatus
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		return nil, false, fmt.Errorf("decode pingcheck status: %w", err)
	}
	entries := make([]tg.PingCheckPanelEntry, 0, len(st.Tunnels))
	for _, t := range st.Tunnels {
		entries = append(entries, tg.PingCheckPanelEntry{
			TunnelID:         t.TunnelID,
			Name:             t.TunnelName,
			NDMSName:         t.NDMSName,
			Status:           t.Status,
			PerTunnelEnabled: t.Enabled,
			LastLatencyMs:    t.LastLatency,
			SuccessCount:     t.SuccessCount,
			FailCount:        t.FailCount,
			FailThreshold:    t.FailThreshold,
			RestartCount:     t.RestartCount,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, st.Enabled, nil
}

// pingcheckInflightStore guards against double-tap toggle. Per (userID,
// tunnelID) → claimed-until timestamp. 5-second window: long enough for
// the agent roundtrip + auto-refresh, short enough that no operator
// notices it during normal use.
type pingcheckInflightStore struct {
	mu sync.Mutex
	m  map[pingcheckInflightKey]time.Time
}

type pingcheckInflightKey struct {
	UserID   int64
	TunnelID string
}

func newPingCheckInflightStore() *pingcheckInflightStore {
	return &pingcheckInflightStore{m: make(map[pingcheckInflightKey]time.Time)}
}

// tryClaim returns true iff the slot was free; stores expiry on success.
// Lazy eviction on read.
func (s *pingcheckInflightStore) tryClaim(userID int64, tunnelID string, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := pingcheckInflightKey{userID, tunnelID}
	now := time.Now()
	if until, ok := s.m[k]; ok {
		if now.Before(until) {
			return false
		}
	}
	s.m[k] = now.Add(ttl)
	return true
}

const pingcheckInflightTTL = 5 * time.Second

// PingCheckOpenAction enqueues a pingcheck_status command on every
// pingcheck_open / refresh tap.
type PingCheckOpenAction struct {
	sink  CommandEnqueuer
	idGen func() string
}

func NewPingCheckOpenAction(sink CommandEnqueuer, idGen func() string) *PingCheckOpenAction {
	if idGen == nil {
		idGen = defaultCmdID
	}
	return &PingCheckOpenAction{sink: sink, idGen: idGen}
}

func (a *PingCheckOpenAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	if a.sink == nil {
		return "", errors.New("командная очередь не подключена; действие не отправлено агенту")
	}
	cmd := wire.Command{
		ID:       a.idGen(),
		Action:   "pingcheck_status",
		Args:     map[string]any{},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
		Action:    "pingcheck_status",
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue pingcheck_status: %w", err)
	}
	return "📡 обновляю…", nil
}

// PingCheckToggleAction enqueues pingcheck_toggle followed by an
// auto-refresh pingcheck_status. Dup-protected via inflight store.
type PingCheckToggleAction struct {
	sink     CommandEnqueuer
	inflight *pingcheckInflightStore
	idGen    func() string
}

func NewPingCheckToggleAction(sink CommandEnqueuer, inflight *pingcheckInflightStore, idGen func() string) *PingCheckToggleAction {
	if idGen == nil {
		idGen = defaultCmdID
	}
	return &PingCheckToggleAction{sink: sink, inflight: inflight, idGen: idGen}
}

func (a *PingCheckToggleAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	if a.sink == nil {
		return "", errors.New("командная очередь не подключена; действие не отправлено агенту")
	}
	if !a.inflight.tryClaim(args.UserID, args.PingCheckTunnelID, pingcheckInflightTTL) {
		return "", errors.New("⏳ команда уже выполняется")
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}

	toggleCmd := wire.Command{
		ID:     a.idGen(),
		Action: "pingcheck_toggle",
		Args: map[string]any{
			"tunnel_id": args.PingCheckTunnelID,
			"ndms_name": args.NDMSName,
			"enable":    args.PingCheckEnable,
		},
		IssuedAt: time.Now().UTC(),
	}
	toggleRef := ref
	toggleRef.Action = "pingcheck_toggle"
	if err := a.sink.EnqueueWithRef(args.UserID, toggleCmd, toggleRef); err != nil {
		return "", fmt.Errorf("enqueue pingcheck_toggle: %w", err)
	}

	// Auto-refresh: agent FIFO ensures order — toggle runs first, then status.
	statusCmd := wire.Command{
		ID:       a.idGen(),
		Action:   "pingcheck_status",
		Args:     map[string]any{},
		IssuedAt: time.Now().UTC(),
	}
	statusRef := ref
	statusRef.Action = "pingcheck_status"
	if err := a.sink.EnqueueWithRef(args.UserID, statusCmd, statusRef); err != nil {
		return "", fmt.Errorf("enqueue auto-refresh: %w", err)
	}
	return "📡 переключаю…", nil
}

// openPingCheckPanelMessage publishes an empty "loading…" PingCheck panel
// into the user's per-router topic and immediately enqueues the first
// pingcheck_status. The notifier replaces the placeholder when the
// agent answers.
func (r *Router) openPingCheckPanelMessage(ctx context.Context, m *tg.Message, u *db.User) {
	text := alerts.Card{
		Badge:   "⏳",
		Label:   "📡 PingCheck",
		Summary: "загружаю состояние",
		Meta:    []string{alerts.KV("роутер", u.Nickname)},
	}.Render(alerts.CardOpts{})
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔄 Обновить", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", u.ID)},
		{Text: "✖ Закрыть", CallbackData: fmt.Sprintf("close_panel:%d:_panel_", u.ID)},
	}}}
	msgID, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil)
	if err != nil {
		return
	}
	_ = r.tg.EditMessageText(ctx, m.Chat.ID, msgID, text, "", &kb)
	cmd := wire.Command{
		ID:       defaultCmdID(),
		Action:   "pingcheck_status",
		Args:     map[string]any{},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    m.Chat.ID,
		MessageID: msgID,
		ThreadID:  m.MessageThreadID,
		Action:    "pingcheck_status",
	}
	if r.cmdSink != nil {
		_ = r.cmdSink.EnqueueWithRef(u.ID, cmd, ref)
	}
}

// Compile-time interface guards.
var _ Action = (*PingCheckOpenAction)(nil)
var _ Action = (*PingCheckToggleAction)(nil)
