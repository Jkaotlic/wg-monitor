package callbacks

import (
	"context"
	"fmt"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Notifier implements backend.TGNotifier by sending one or more chunks via
// the existing tg.Client. The first chunk replies to the original alert
// (ref.MessageID); subsequent chunks chain to the previous chunk so a paginated
// diag stays threaded together rather than scattered across the topic.
type Notifier struct {
	TG        TGClient
	UI        UIConfigSnapshot
	DiagCache *diagCache // staged by NotifyCommandResult when action=="diag_now" + Status=="ok"
	// AppBaseURL -- публичный адрес бэкенда (cfg.PublicBaseURL). VPN-туннели
	// и маршруты переехали в приложение (цикл 4): под итогом команды в личке
	// -- кнопка «Открыть в приложении» на вкладке VPN-туннелей. В группе
	// web_app Telegram не принимает, и кнопки там нет. Пусто -- нет и в личке.
	AppBaseURL string
}

func NewNotifier(c TGClient) *Notifier { return &Notifier{TG: c} }

// NewNotifierWithUI mirrors NewNotifier but lets cmd/backend/main.go inject
// the UI snapshot so the Notifier picks the right keyboard variant
// (ReplyKeyboardMarkup vs CompatInlineKeyboard) for command-result chunks.
func NewNotifierWithUI(c TGClient, ui UIConfigSnapshot) *Notifier {
	return &Notifier{TG: c, UI: ui}
}

func (n *Notifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, action string, result wire.CommandResult, userID int64, maxChars int) error {
	chunks := alerts.FormatCommandResult(action, result, maxChars)
	if len(chunks) == 0 {
		return nil
	}

	// Diag results get an inline keyboard with "Полный отчёт" / retry / close.
	// Cache the raw body so the first button can fetch it without a re-run.
	var diagMarkup *tg.InlineKeyboardMarkup
	if action == "diag_now" {
		token := ""
		if result.Status == "ok" && n.DiagCache != nil {
			token = n.DiagCache.Put(result.Output, 5*time.Minute)
		}
		var failing []tg.DiagFailingTest
		if result.Status == "ok" {
			tests := alerts.ParseDiagTests(result.Output)
			for _, t := range tests {
				if t.Status == "fail" {
					failing = append(failing, tg.DiagFailingTest{ID: t.ID, Label: t.Label})
				}
			}
		}
		kb := tg.DiagResultKeyboardWithTests(result.Status, userID, token, failing)
		diagMarkup = &kb
	}
	appURL := ""
	if tg.IsPrivateChat(ref.ChatID) {
		appURL = tg.MiniAppRouterTabURL(n.AppBaseURL, userID, "tunnels", "")
	}
	resultMarkup := commandResultNextActionKeyboard(action, result.Status, userID, appURL)

	prev := ref.MessageID
	for i, c := range chunks {
		replyTo := prev
		var markup any
		if i == 0 && diagMarkup != nil {
			markup = diagMarkup
		} else if i == 0 && resultMarkup != nil {
			markup = resultMarkup
		} else {
			markup = n.UI.KeyboardForTopic("per_router")
		}
		mid, err := n.TG.SendMessageWithReplyKeyboard(ctx, ref.ChatID, ref.ThreadID, c, "", &replyTo, markup)
		if err != nil {
			return err
		}
		prev = mid
	}
	return nil
}

// commandResultNextActionKeyboard -- что нажать под итогом команды. Кнопки
// панелей туннелей и маршрутов (tunnels_refresh, routes_open) ушли вместе с
// панелями (цикл 4): вместо них -- одна кнопка приложения, если appURL не
// пуст (личка и https-адрес).
func commandResultNextActionKeyboard(action, status string, userID int64, appURL string) *tg.InlineKeyboardMarkup {
	cd := func(a, suffix string) string { return fmt.Sprintf("%s:%d:%s", a, userID, suffix) }
	pingcheck := tg.InlineKeyboardButton{Text: "🛡 PingCheck", CallbackData: cd("pingcheck_open", "_panel_")}
	doctor := tg.InlineKeyboardButton{Text: "🩺 Проверка", CallbackData: cd("router_doctor", "_menu")}
	var rows [][]tg.InlineKeyboardButton
	switch {
	case status != "ok" && action == "router_doctor":
		rows = [][]tg.InlineKeyboardButton{{{Text: "🩺 Повторить проверку", CallbackData: cd("router_doctor", "_menu")}}}
	case action == "check_via_tunnel" || action == "check_direct":
		rows = [][]tg.InlineKeyboardButton{{pingcheck}, {doctor}}
	case status == "ok" && action == "pingcheck_now":
		rows = [][]tg.InlineKeyboardButton{{pingcheck}, {{Text: "📊 Диагностика", CallbackData: cd("diag_now", "_menu")}}}
	case status == "ok" && action == "force_recheck":
		rows = [][]tg.InlineKeyboardButton{{doctor}}
	case status == "ok" && action == "router_doctor":
		// Кнопок, кроме приложения, у итога проверки не осталось.
	default:
		return nil
	}
	if appURL != "" {
		rows = append(rows, []tg.InlineKeyboardButton{tg.OpenInAppButton(appURL)})
	}
	if len(rows) == 0 {
		return nil
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}
