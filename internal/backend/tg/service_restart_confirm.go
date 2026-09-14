// internal/backend/tg/service_restart_confirm.go
//
// Подтверждение перезапуска службы в боте. После цикла 1 бот перезапускает
// только HydraRoute Neo (кнопки панели маршрутов) и awg-manager (кнопка
// «Перезагрузить awg-mgr» панели туннелей, callback restart_tunnel).
// Перезагрузка роутера, прошивка и пакеты Entware -- в мини-аппе.
package tg

import "fmt"

// RestartConfirmText is the warning shown after a restart tap.
func RestartConfirmText(name, token string) string {
	display := NameToDisplay(name)
	var what string
	switch name {
	// hrneo/hrneo_start/hrneo_stop открываются кнопками панели маршрутов
	// (routes_notifier.go: карточка HR-Neo inventory/doctor), а не из-под
	// тревоги -- поэтому и говорят словами того экрана, а не владельца в
	// личке.
	case "hrneo":
		what = "  • HydraRoute Neo — движок умной раздельной маршрутизации — перезапустится за несколько секунд.\n" +
			"  • На это время правила по именам сайтов перестанут работать, потом всё вернётся само.\n" +
			"  • Правила по адресам продолжат работать."
	case "hrneo_start":
		what = "  • HydraRoute Neo — движок умной раздельной маршрутизации — будет запущен.\n" +
			"  • Правила по именам сайтов снова начнут работать, как только он поднимется.\n" +
			"  • Если он уже работает, ничего не изменится."
	case "hrneo_stop":
		what = "  • HydraRoute-Neo будет остановлен.\n" +
			"  • DNS/HR-Neo правила временно перестанут маршрутизировать домены.\n" +
			"  • Static-routes awg-manager продолжат работать."
	case "awgmgr":
		what = "  • Веб-интерфейс awg-manager на ~3-5 сек перестанет отвечать.\n" +
			"  • Туннели НЕ разрываются — это перезапуск только демона awg-manager.\n" +
			"  • API-вызовы из бэкенда (recheck, restart_tunnel) дадут ошибку, если попадут в окно."
	}
	// Заголовок называет то действие, которое подтверждают: кнопка
	// «▶ Запустить HydraRoute Neo» не должна открывать «Перезапустить …?».
	verb := "Перезапустить"
	switch name {
	case "hrneo_start":
		verb = "Запустить"
	case "hrneo_stop":
		verb = "Остановить"
	}
	return fmt.Sprintf("🔁 Служба роутера\n\n⚠️ %s %s?\n\nЧто произойдёт:\n%s\n\nКод подтверждения: %s (живёт 5 мин)",
		verb, display, what, token)
}

// RestartConfirmKeyboard is two buttons: Confirm (maint_confirm with the bound
// name+token) and Cancel (closes the message: the maintenance panel is gone).
func RestartConfirmKeyboard(userID int64, name, token string) InlineKeyboardMarkup {
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "✅ Подтвердить", CallbackData: fmt.Sprintf("maint_confirm:%d:%s:%s", userID, name, token)},
		{Text: "↩ Отмена", CallbackData: fmt.Sprintf("close_panel:%d:_panel_", userID)},
	}}}
}

// NameToDisplay translates a service-name token into the human label
// (hrneo/hrneo_start/hrneo_stop → "HydraRoute Neo", awgmgr → "awg-manager").
// Exported so callers outside package tg (the maint_confirm toast in
// internal/backend/callbacks) can show the same human name instead of the
// raw internal token.
func NameToDisplay(name string) string {
	switch name {
	case "hrneo", "hrneo_start", "hrneo_stop":
		return "HydraRoute Neo"
	case "awgmgr":
		return "awg-manager"
	default:
		return name
	}
}
