package backend

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/anex/wg-monitor/internal/backend/callbacks"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// renderOpkgUpgradeReply returns the message text and optional inline keyboard
// for an opkg_upgrade / opkg_feed_disable CommandResult. When the payload
// carries FailedFeeds, one inline button is appended per URL — tapping
// enqueues an opkg_feed_disable command for the agent (via OpkgRepairAction).
//
// store is the per-router pendingOpkgRepairStore; the renderer registers a
// new pending entry per URL with a 5-minute TTL. tokenGen is injectable so
// tests can pin a deterministic token.
func renderOpkgUpgradeReply(res wire.CommandResult, userID int64, store *callbacks.PendingOpkgRepairStore, tokenGen func() string) (string, *tg.InlineKeyboardMarkup) {
	text := res.Output
	if len(res.Payload) == 0 {
		return text, nil
	}
	var payload wire.OpkgUpgradeResult
	if err := json.Unmarshal(res.Payload, &payload); err != nil {
		return text, nil
	}
	if len(payload.FailedFeeds) == 0 {
		return text, nil
	}
	var rows [][]tg.InlineKeyboardButton
	for _, rawURL := range payload.FailedFeeds {
		token := tokenGen()
		store.PutForRender(userID, normalizeFeedURLBackend(rawURL), token, 5*time.Minute)
		host := hostFromURL(rawURL)
		btn := tg.InlineKeyboardButton{
			Text:         fmt.Sprintf("🔧 Отключить мёртвый фид (%s)", host),
			CallbackData: fmt.Sprintf("opkg_disable:%d:_menu:%s", userID, token),
		}
		rows = append(rows, []tg.InlineKeyboardButton{btn})
	}
	return text, &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// normalizeFeedURLBackend mirrors the agent's normalizeFeedURL: strips
// trailing /Packages.gz and any trailing /.
func normalizeFeedURLBackend(u string) string {
	const suffix = "/Packages.gz"
	if len(u) >= len(suffix) && u[len(u)-len(suffix):] == suffix {
		u = u[:len(u)-len(suffix)]
	}
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}

// hostFromURL returns the bare host for the button label. Falls back to the
// raw URL truncated if parsing fails.
func hostFromURL(u string) string {
	const schemeSep = "://"
	idx := indexOfSubstr(u, schemeSep)
	if idx < 0 {
		return truncateFeedURL(u, 40)
	}
	rest := u[idx+len(schemeSep):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == ':' {
			return rest[:i]
		}
	}
	return rest
}

func indexOfSubstr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func truncateFeedURL(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
