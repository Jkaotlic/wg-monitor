package callbacks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// diagDrillDownTG is the subset of tg.Client this action edits with.
type diagDrillDownTG interface {
	EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error
}

// DiagTestExpandAction handles diag_test:<token>:<test_id> taps.
// Pulls cached raw JSON, locates the test, renders the per-tunnel
// detail block. Cache miss / test not found → graceful error screens.
type DiagTestExpandAction struct {
	cache *diagCache
	tg    diagDrillDownTG
}

// NewDiagTestExpandAction constructs a DiagTestExpandAction backed by
// the given diagCache and TG edit client.
func NewDiagTestExpandAction(cache *diagCache, tgClient diagDrillDownTG) *DiagTestExpandAction {
	return &DiagTestExpandAction{cache: cache, tg: tgClient}
}

// Apply implements Action. It looks up the cached diag body, finds the
// matching test detail, and edits the message with a per-tunnel breakdown.
func (a *DiagTestExpandAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	body, ok := a.cache.Get(args.DiagRawToken)
	if !ok {
		return "", a.editStale(ctx, q, args.UserID)
	}
	tests := alerts.ParseDiagTests(body)
	var det *alerts.TestDetail
	for i := range tests {
		if tests[i].ID == args.DiagTestID {
			det = &tests[i]
			break
		}
	}
	if det == nil {
		return "", a.editNotFound(ctx, q, args.UserID)
	}
	text := renderTestDetail(*det)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "« К сводке", CallbackData: fmt.Sprintf("diag_raw:%d:_panel_:%s", args.UserID, args.DiagRawToken)},
		{Text: "📄 Полный отчёт", CallbackData: fmt.Sprintf("diag_raw:%d:_panel_:%s", args.UserID, args.DiagRawToken)},
	}}}
	return "", a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func (a *DiagTestExpandAction) editStale(ctx context.Context, q *tg.CallbackQuery, userID int64) error {
	text := "⏱ Сводка устарела (5 мин TTL). Запусти свежий diag."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔁 Diag", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
	}}}
	return a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func (a *DiagTestExpandAction) editNotFound(ctx context.Context, q *tg.CallbackQuery, userID int64) error {
	text := "❓ Не нашёл этот тест в результатах. Возможно awg-mgr обновился — попробуй свежий diag."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔁 Diag", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
	}}}
	return a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func renderTestDetail(d alerts.TestDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Диагностика / %s\n\n", d.Label)
	if len(d.PerTunnel) == 0 {
		// Global test (no per-tunnel breakdown). Render just the aggregate.
		fmt.Fprintf(&b, "%s статус: %s\n", iconForStatus(d.Status), d.Status)
		return b.String()
	}
	for _, p := range d.PerTunnel {
		fmt.Fprintf(&b, "%s %s\n", iconForStatus(p.Status), p.TunnelLabel)
		// Stable key order
		keys := make([]string, 0, len(p.KeyValues))
		for k := range p.KeyValues {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "   %s: %s\n", k, p.KeyValues[k])
		}
		if p.Reason != "" {
			fmt.Fprintf(&b, "   reason: %s\n", p.Reason)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func iconForStatus(s string) string {
	switch s {
	case "ok":
		return "✅"
	case "fail":
		return "❌"
	case "skip":
		return "⏭"
	}
	return "⚪"
}

var _ Action = (*DiagTestExpandAction)(nil)
