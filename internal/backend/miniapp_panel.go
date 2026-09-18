package backend

import (
	"net"
	"net/url"
	"strings"
)

// panelAddress -- адрес панели awg-manager роутера, пригодный для перехода:
// непустой и прошедший ТОТ ЖЕ валидатор, что у дашборда. Второго валидатора не
// заводим: он разошёлся бы с первым.
//
// С 18.09 адрес уезжает владельцу и админу ссылкой panel_url, поэтому
// логин/пароль, если их вписали в адрес (https://user:pass@host), срезаются:
// ссылка -- для браузера, креды панели в приложение не попадают никогда.
func panelAddress(stored *string) (string, bool) {
	if stored == nil {
		return "", false
	}
	raw := strings.TrimSpace(*stored)
	if raw == "" || validateDashboardAWGMURL(raw) != nil {
		return "", false
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		raw = u.String()
	}
	return raw, true
}

// miniappPanelURLFor -- ссылка на панель для строки списка и настроек: только
// админу и владельцу роутера (role "admin"/"owner"). Оператор роутера адреса
// не получает (решение оператора № 9).
func miniappPanelURLFor(role string, stored *string) string {
	if role != "owner" && role != "admin" {
		return ""
	}
	addr, _ := panelAddress(stored)
	return addr
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
