package tg

import (
	"fmt"
	"strings"
)

// MiniAppRouterURL builds the address of one router's screen inside the
// Telegram Mini App. Empty base or a non-https base yields "" — Telegram
// opens a web_app button only over HTTPS, and a button pointed at a bare
// http:// URL would sit under the alert doing nothing, which is worse than
// not offering it at all. Every alert surface (HARD, ROUTER OFFLINE,
// STILL-DOWN, the mobile wake report) shares this single builder so the
// "Открыть в приложении" button always lands in the same place.
func MiniAppRouterURL(base string, routerUserID int64) string {
	base = strings.TrimSpace(base)
	if base == "" || !strings.HasPrefix(base, "https://") {
		return ""
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/miniapp/?router=%d", base, routerUserID)
}
