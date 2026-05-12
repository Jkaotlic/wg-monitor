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
	TG TGClient
	UI UIConfigSnapshot
}

func NewNotifier(c TGClient) *Notifier { return &Notifier{TG: c} }

// NewNotifierWithUI mirrors NewNotifier but lets cmd/backend/main.go inject
// the UI snapshot so the Notifier picks the right keyboard variant
// (ReplyKeyboardMarkup vs CompatInlineKeyboard) for command-result chunks.
func NewNotifierWithUI(c TGClient, ui UIConfigSnapshot) *Notifier {
	return &Notifier{TG: c, UI: ui}
}

func (n *Notifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, action string, result wire.CommandResult, maxChars int) error {
	chunks := alerts.FormatCommandResult(action, result, maxChars)
	if len(chunks) == 0 {
		return nil
	}
	prev := ref.MessageID
	for _, c := range chunks {
		replyTo := prev
		mid, err := n.TG.SendMessageWithReplyKeyboard(ctx, ref.ChatID, ref.ThreadID, c, "", &replyTo, n.UI.KeyboardForTopic("per_router"))
		if err != nil {
			return err
		}
		prev = mid
	}
	return nil
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
	markup := n.buildOpkgMarkup(res, userID)
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

// buildOpkgMarkup decodes FailedFeeds from the payload and returns an
// InlineKeyboardMarkup with one 🔧 button per URL, or nil when empty.
func (n *OpkgResultNotifier) buildOpkgMarkup(res wire.CommandResult, userID int64) *tg.InlineKeyboardMarkup {
	if len(res.Payload) == 0 {
		return nil
	}
	var payload wire.OpkgUpgradeResult
	if err := json.Unmarshal(res.Payload, &payload); err != nil {
		return nil
	}
	if len(payload.FailedFeeds) == 0 {
		return nil
	}
	var rows [][]tg.InlineKeyboardButton
	for _, rawURL := range payload.FailedFeeds {
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
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// notifierNormalizeFeedURL strips /Packages.gz suffix and trailing slashes.
// Mirrors normalizeFeedURLBackend in backend/opkg_render.go and the agent's
// normalizeFeedURL — kept here so callbacks does not import backend.
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
