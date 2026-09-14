package backend

import (
	"net"
	"net/url"
	"strings"
)

// panelAddress -- адрес панели awg-manager роутера, пригодный для перехода:
// непустой и прошедший ТОТ ЖЕ валидатор, что у дашборда. Второго валидатора не
// заводим: он разошёлся бы с первым.
func panelAddress(stored *string) (string, bool) {
	if stored == nil {
		return "", false
	}
	raw := strings.TrimSpace(*stored)
	if raw == "" || validateDashboardAWGMURL(raw) != nil {
		return "", false
	}
	return raw, true
}

// PanelKnown -- сохранён ли у роутера годный адрес панели. Для бота: кнопку
// панели он рисует только тогда, когда мини-апп сможет её открыть.
func PanelKnown(stored *string) bool {
	_, ok := panelAddress(stored)
	return ok
}

// panelScope отвечает на вопрос «откроется ли эта кнопка из кафе». Частный
// адрес -- диапазоны домашних сетей, петля и имя без точки или в .local; всё
// остальное публичное. Наружу уходит только это слово, никогда не хост: без
// него владельцу нечем объяснить, почему кнопка не сработала вне дома.
func panelScope(raw string) string {
	addr, ok := panelAddress(&raw)
	if !ok {
		return ""
	}
	u, err := url.Parse(addr)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return "private"
		}
		return "public"
	}
	if !strings.Contains(host, ".") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return "private"
	}
	return "public"
}
