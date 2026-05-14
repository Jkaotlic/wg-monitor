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
	"fmt"
	"sort"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
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
		Label:   fmt.Sprintf("📡 PingCheck — %s — агент не ответил", user.Nickname),
		Summary: summary,
		Hint:    hint,
	}
	body := card.Render(alerts.CardOpts{MaxBytes: 3500})
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔄 Повторить", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", user.ID)},
	}}}
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, body, "", &kb)
}

// decodePingCheckStatus converts the awg-mgr passthrough JSON into the
// renderer entry shape. Sorted by tunnel name for stable order.
//
// TODO(NDMS-resolve): tunnels[].ndms_name is not in /api/pingcheck/status.
// Toggle keyboard needs it; piggybacks on a second roundtrip or cached
// /api/tunnels/all snapshot in Task 9. For now NDMSName is empty.
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
