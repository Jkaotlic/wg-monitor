package callbacks

import (
	"context"
	"fmt"
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

func diagRawKeyboard(userID int64, rawToken string) tg.InlineKeyboardMarkup {
	return tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "« К сводке", CallbackData: fmt.Sprintf("diag_back:%d:_panel_:%s", userID, rawToken)},
		{Text: "🔁 Диагностика", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
	}, {
		{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
	}}}
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
		{Text: "« К сводке", CallbackData: fmt.Sprintf("diag_back:%d:_panel_:%s", args.UserID, args.DiagRawToken)},
		{Text: "📄 Полный отчёт", CallbackData: fmt.Sprintf("diag_raw:%d:_panel_:%s", args.UserID, args.DiagRawToken)},
	}}}
	return "", a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func (a *DiagTestExpandAction) editStale(ctx context.Context, q *tg.CallbackQuery, userID int64) error {
	text := "⏱ Сводка устарела. Запусти свежую диагностику."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔁 Диагностика", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
	}}}
	return a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func (a *DiagTestExpandAction) editNotFound(ctx context.Context, q *tg.CallbackQuery, userID int64) error {
	text := "❓ Не нашёл этот тест в результатах. Возможно awg-manager обновился — запусти свежую диагностику."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔁 Диагностика", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
	}}}
	return a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

// renderTestDetail — страница одной проверки, на шаг глубже сводки. Здесь уже
// можно показать, что увидел роутер: человек сюда пришёл сам, по кнопке. Но
// проверка названа словами владельца, а VPN-туннель — его именем, не id.
func renderTestDetail(d alerts.TestDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Диагностика / %s\n\n", d.Label)
	if len(d.PerTunnel) == 0 {
		fmt.Fprintf(&b, "%s %s\n", iconForStatus(d.Status), humanDiagStatus(d.Status))
		if d.Detail != "" {
			fmt.Fprintf(&b, "   что увидел роутер: %s\n", d.Detail)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	for _, p := range d.PerTunnel {
		fmt.Fprintf(&b, "%s VPN-туннель «%s» — %s\n", iconForStatus(p.Status), p.TunnelLabel, humanDiagStatus(p.Status))
		if p.Reason != "" {
			fmt.Fprintf(&b, "   что увидел роутер: %s\n", p.Reason)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanDiagStatus(s string) string {
	switch s {
	case "ok":
		return "в норме"
	case "fail":
		return "сбой"
	case "warn":
		return "есть замечание"
	case "skip":
		return "пропущено — на этом роутере не нужно"
	default:
		return s
	}
}

func iconForStatus(s string) string {
	switch s {
	case "ok":
		return "✅"
	case "fail":
		return "❌"
	case "warn":
		return "⚠"
	case "skip":
		return "⏭"
	}
	return "⚪"
}

var _ Action = (*DiagTestExpandAction)(nil)

// DiagBackAction renders the original parsed diag summary in place,
// from the same cached raw JSON used by drill-down. Used as the
// « К сводке button in test-detail screens; complements diag_raw
// (which sends raw JSON as a new message).
type DiagBackAction struct {
	cache *diagCache
	tg    diagDrillDownTG
}

// NewDiagBackAction constructs a DiagBackAction backed by the given
// diagCache and TG edit client.
func NewDiagBackAction(cache *diagCache, tgClient diagDrillDownTG) *DiagBackAction {
	return &DiagBackAction{cache: cache, tg: tgClient}
}

// Apply implements Action. Fetches cached raw JSON and re-renders the
// parsed summary by EditMessageText (in-place, no new message).
func (a *DiagBackAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	body, ok := a.cache.Get(args.DiagRawToken)
	if !ok {
		text := "⏱ Сводка устарела. Запусти свежую диагностику."
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🔁 Диагностика", CallbackData: fmt.Sprintf("diag_now:%d:_menu", args.UserID)},
		}}}
		return "", a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	}
	// Re-render the parsed summary via the existing diag-report parser.
	summary, bullets, rawFallback := alerts.ParseDiagReport(body)
	var text string
	if rawFallback {
		text = "📊 Диагностика\n\n(не удалось распарсить — нажми «📄 Полный отчёт»)"
	} else {
		// ✅ — только когда сводка говорит «всё в порядке»: зелёная галка над
		// «нашлись проблемы» противоречила бы самой себе.
		badge := "✅"
		if !strings.HasPrefix(summary, "всё в порядке") {
			badge = "⚠"
		}
		card := alerts.Card{
			Badge:   badge,
			Label:   "Диагностика",
			Summary: summary,
			Details: strings.Join(bullets, "\n"),
		}
		text = card.Render(alerts.CardOpts{MaxBytes: 3500})
	}
	// Build the same keyboard as the original (with failing-test buttons).
	tests := alerts.ParseDiagTests(body)
	var failing []tg.DiagFailingTest
	for _, t := range tests {
		if t.Status == "fail" {
			failing = append(failing, tg.DiagFailingTest{ID: t.ID, Label: t.Label})
		}
	}
	kb := tg.DiagResultKeyboardWithTests("ok", args.UserID, args.DiagRawToken, failing)
	return "", a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

var _ Action = (*DiagBackAction)(nil)
