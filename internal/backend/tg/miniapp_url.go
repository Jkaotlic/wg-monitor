package tg

import (
	"fmt"
	"net/url"
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

// MiniAppURL -- адрес мини-аппа целиком, без роутера: кнопка web_app в личке.
// Не https -- пусто, по той же причине, что у MiniAppRouterURL.
func MiniAppURL(base string) string {
	base = strings.TrimSpace(base)
	if base == "" || !strings.HasPrefix(base, "https://") {
		return ""
	}
	return strings.TrimRight(base, "/") + "/miniapp/"
}

// MiniAppRouterTabURL -- экран роутера сразу на вкладке и слое: кнопка
// «Открыть в приложении» под панелями бота, откуда ушли VPN-туннели и
// маршруты (цикл 4). tab -- ключ вкладки (router|tunnels|diag|events|manage),
// open -- слой (routes, agentcfg, cabinet, …) или "". Настройки -- вкладка
// manage, а не слой: старые open=settings/open=admin мини-апп переводит на
// неё сам. Ключи -- адреса мини-аппа (miniapp/src/nav.js, TABS и
// OPEN_OVERLAYS). Не https -- пусто.
func MiniAppRouterTabURL(base string, routerUserID int64, tab, open string) string {
	u := MiniAppRouterURL(base, routerUserID)
	if u == "" {
		return ""
	}
	if tab != "" {
		u += "&tab=" + url.QueryEscape(tab)
	}
	if open != "" {
		u += "&open=" + url.QueryEscape(open)
	}
	return u
}

// IsPrivateChat -- личка с человеком. У Telegram id лички положительный (он
// же id человека), у групп и каналов -- отрицательный. Кнопку web_app Telegram
// принимает только в личке.
func IsPrivateChat(chatID int64) bool {
	return chatID > 0
}

// OpenInAppButton -- кнопка web_app «Открыть в приложении» с готовым адресом.
func OpenInAppButton(appURL string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: "📱 Открыть в приложении", WebApp: &WebAppInfo{URL: appURL}}
}
