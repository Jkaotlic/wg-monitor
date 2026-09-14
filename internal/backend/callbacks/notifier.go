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
	TG                  TGClient
	UI                  UIConfigSnapshot
	DiagCache           *diagCache // staged by NotifyCommandResult when action=="diag_now" + Status=="ok"
	TunnelsPanelBuilder func(userID int64) (string, tg.InlineKeyboardMarkup, bool)
	TunnelsRefreshSink  CommandEnqueuer
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
	var tunnelImportMarkup *tg.InlineKeyboardMarkup
	if action == "tunnel_import" && result.Status == "ok" {
		tunnelImportMarkup = tunnelImportResultKeyboard(userID)
	}
	resultMarkup := commandResultNextActionKeyboard(action, result.Status, userID)

	if action == "tunnel_import" && ref.MessageID != 0 {
		var markup *tg.InlineKeyboardMarkup
		if tunnelImportMarkup != nil {
			markup = tunnelImportMarkup
		} else {
			markup = resultMarkup
		}
		if err := n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, chunks[0], "", markup); err != nil {
			return err
		}
		prev := ref.MessageID
		for _, c := range chunks[1:] {
			mid, err := n.TG.SendMessageWithReplyKeyboard(ctx, ref.ChatID, ref.ThreadID, c, "", &prev, n.UI.KeyboardForTopic("per_router"))
			if err != nil {
				return err
			}
			prev = mid
		}
		return nil
	}

	prev := ref.MessageID
	for i, c := range chunks {
		replyTo := prev
		var markup any
		if i == 0 && diagMarkup != nil {
			markup = diagMarkup
		} else if i == 0 && tunnelImportMarkup != nil {
			markup = tunnelImportMarkup
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
	if isTunnelPanelMutatingAction(action) && result.Status == "ok" && ref.MessageID != 0 {
		if n.TunnelsRefreshSink != nil {
			cmd := wire.Command{ID: defaultCmdID(), Action: "tunnels_status", IssuedAt: time.Now().UTC()}
			_ = n.TunnelsRefreshSink.EnqueueWithRef(userID, cmd, ref)
		} else if n.TunnelsPanelBuilder != nil {
			if text, kb, ok := n.TunnelsPanelBuilder(userID); ok {
				_ = n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
			}
		}
	}
	return nil
}

func commandResultNextActionKeyboard(action, status string, userID int64) *tg.InlineKeyboardMarkup {
	if status != "ok" {
		switch action {
		case "tunnels_status":
			return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
				{Text: "🎛 Повторить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
			}, {
				{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
			}}}
		case "tunnel_import":
			return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
				{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
			}, {
				{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
			}}}
		case "router_doctor":
			return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
				{Text: "🩺 Повторить проверку", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
			}, {
				{Text: "🎛 Туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
			}, {
				{Text: "🛣 Маршруты", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
			}}}
		case "check_via_tunnel", "check_direct":
			return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
				{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
			}, {
				{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
			}, {
				{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
			}, {
				{Text: "🛣 Маршруты", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
			}}}
		}
		return nil
	}
	switch action {
	case "tunnels_status":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🛣 Маршруты / перенос", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		}, {
			{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
		}, {
			{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
		}}}
	case "restart_tunnel":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		}, {
			{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
		}}}
	case "tunnel_enable", "tunnel_disable", "tunnel_delete":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		}, {
			{Text: "🛣 Маршруты / перенос", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		}, {
			{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
		}}}
	case "pingcheck_now":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
		}, {
			{Text: "📊 Диагностика", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
		}}}
	case "router_doctor":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		}, {
			{Text: "🛣 Маршруты", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		}}}
	case "check_via_tunnel", "check_direct":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
		}, {
			{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		}, {
			{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
		}, {
			{Text: "🛣 Маршруты", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		}}}
	case "force_recheck":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
		}, {
			{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		}, {
			{Text: "🛣 Маршруты", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		}}}
	default:
		return nil
	}
}

func tunnelImportResultKeyboard(userID int64) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
	}, {
		{Text: "🌍 Проверить выход", CallbackData: fmt.Sprintf("check_via_tunnel:%d:_panel_", userID)},
		{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
	}, {
		{Text: "🛣 Маршруты / перенос", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
	}}}
}

func isTunnelPanelMutatingAction(action string) bool {
	switch action {
	case "tunnel_enable", "tunnel_disable", "tunnel_delete", "tunnel_import":
		return true
	default:
		return false
	}
}
