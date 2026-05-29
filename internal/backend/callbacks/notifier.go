package callbacks

import (
	"context"
	"encoding/json"
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
		return nil
	}
	switch action {
	case "restart_tunnel":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🎛 Проверить тоннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		}, {
			{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
		}}}
	case "pingcheck_now":
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
		}, {
			{Text: "📊 Диагностика", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
		}}}
	default:
		return nil
	}
}

func tunnelImportResultKeyboard(userID int64) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🎛 Проверить тоннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
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

// OpkgResultNotifier implements backend.OpkgNotifier. It sends the
// opkg_upgrade / opkg_feed_disable result as a TG message, attaching one
// inline 🔧 button per FailedFeed URL when the payload carries any.
type OpkgResultNotifier struct {
	TG       TGClient
	UI       UIConfigSnapshot
	Store    *pendingOpkgRepairStore
	TokenGen func() string // injectable; defaults to makeOpkgRepairToken
}

// NewOpkgResultNotifier constructs an OpkgResultNotifier. tokenGen may be nil
// (defaults to crypto/rand token generator).
func NewOpkgResultNotifier(tgClient TGClient, ui UIConfigSnapshot, store *pendingOpkgRepairStore, tokenGen func() string) *OpkgResultNotifier {
	if tokenGen == nil {
		tokenGen = makeOpkgRepairToken
	}
	return &OpkgResultNotifier{TG: tgClient, UI: ui, Store: store, TokenGen: tokenGen}
}

// NotifyOpkgResult sends the opkg result text and, when the payload carries
// FailedFeeds, attaches one inline 🔧 repair button per URL. The first
// message chunk carries the buttons; subsequent pagination chunks (if any)
// send plain (the fix-buttons are only needed once).
func (n *OpkgResultNotifier) NotifyOpkgResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64, maxChars int) error {
	chunks := alerts.FormatCommandResult(ref.Action, res, maxChars)
	if len(chunks) == 0 {
		return nil
	}
	markup := n.buildOpkgMarkup(ref.Action, res, userID)
	prev := ref.MessageID
	for i, chunk := range chunks {
		replyTo := prev
		var mid int64
		var err error
		if i == 0 && markup != nil {
			// First chunk: attach the repair buttons.
			mid, err = n.TG.SendMessageWithReplyKeyboard(ctx, ref.ChatID, ref.ThreadID, chunk, "", &replyTo, markup)
		} else {
			mid, err = n.TG.SendMessageWithReplyKeyboard(ctx, ref.ChatID, ref.ThreadID, chunk, "", &replyTo, n.UI.KeyboardForTopic("per_router"))
		}
		if err != nil {
			return err
		}
		prev = mid
	}
	return nil
}

// buildOpkgMarkup decodes FailedFeeds from the payload and returns inline
// follow-up controls for the opkg result.
func (n *OpkgResultNotifier) buildOpkgMarkup(action string, res wire.CommandResult, userID int64) *tg.InlineKeyboardMarkup {
	var rows [][]tg.InlineKeyboardButton

	if len(res.Payload) > 0 {
		var payload wire.OpkgUpgradeResult
		if err := json.Unmarshal(res.Payload, &payload); err == nil {
			for _, rawURL := range payload.FailedFeeds {
				if n.Store == nil {
					continue
				}
				token := n.TokenGen()
				normalized := notifierNormalizeFeedURL(rawURL)
				n.Store.PutForRender(userID, normalized, token, 5*time.Minute)
				host := notifierHostFromURL(rawURL)
				btn := tg.InlineKeyboardButton{
					Text:         fmt.Sprintf("🔧 Отключить мёртвый фид (%s)", host),
					CallbackData: fmt.Sprintf("opkg_disable:%d:_menu:%s", userID, token),
				}
				rows = append(rows, []tg.InlineKeyboardButton{btn})
			}
		}
	}

	if action == "opkg_upgrade" && res.Status == "ok" {
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         "🛠 Обслуживание",
			CallbackData: fmt.Sprintf("maint_open:%d:_panel_", userID),
		}, {
			Text:         "🩺 Проверка",
			CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID),
		}})
	}

	if len(rows) == 0 {
		return nil
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// notifierNormalizeFeedURL strips /Packages.gz suffix and trailing slashes.
// Mirrors the agent's normalizeFeedURL — kept here so callbacks does not
// import backend.
func notifierNormalizeFeedURL(u string) string {
	const suffix = "/Packages.gz"
	if len(u) >= len(suffix) && u[len(u)-len(suffix):] == suffix {
		u = u[:len(u)-len(suffix)]
	}
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}

// notifierHostFromURL returns the bare hostname for the button label.
func notifierHostFromURL(u string) string {
	const schemeSep = "://"
	idx := -1
	for i := 0; i+len(schemeSep) <= len(u); i++ {
		if u[i:i+len(schemeSep)] == schemeSep {
			idx = i
			break
		}
	}
	if idx < 0 {
		if len(u) > 40 {
			return u[:40] + "…"
		}
		return u
	}
	rest := u[idx+len(schemeSep):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == ':' {
			return rest[:i]
		}
	}
	return rest
}
