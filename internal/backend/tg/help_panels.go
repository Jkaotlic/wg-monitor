package tg

// HelpForScreen returns the inline-help body for a given panel screen.
// Used by the "ℹ Помощь" button: tap → EditMessageText replaces the
// panel body with this text; "« Назад" returns to the panel.
func HelpForScreen(screen string) string {
	switch screen {
	case "maint":
		return `🛠 Обслуживание — справка

🔁 Restart hrneo — перезапустить HydraRoute-Neo (DNS/routing маршрутизатор). Безопасно; пакеты задерживаются на ~5с.
🔁 Restart awg-mgr — перезапустить менеджер AmneziaWG. Туннели не падают (системные), но awg-mgr API недоступен ~10с.
🔁 Reboot router — полная перезагрузка. ~2–3 мин downtime. Используется кулдаун 10 мин.
📦 Прошивка — установить новую версию KeeneticOS. Включает reboot. Кулдаун 60 мин.
🔄 Проверить апдейты — заново снять отчёт о доступных версиях.`
	case "routes":
		return `🛣 Маршруты — справка

Снимок DNS- и Static-маршрутов HydraRoute-Neo, сгруппированный по туннелям.
🔄 Rebind — массово переключить все правила одного туннеля на другой. WAN/system правила НЕ трогаются.
🔁 Обновить — заново снять снимок.`
	case "tunnels":
		return `🎛 Туннели — справка

Снимок состояния всех туннелей: ✅ up, 🔴 down, ⏸ disabled.
▶/⏸ — включить/выключить конкретный туннель (awg-mgr enable/disable).
🔁 Перезагрузить awg-mgr — рестарт менеджера.
🔄 Обновить — заново снять снимок.`
	case "access":
		return `👥 Доступ — справка

Управление операторами роутеров. Каждый роутер имеет одного owner'а и опциональный whitelist дополнительных TG user'ов.
➕ Добавить оператора — FSM: forward сообщение от человека в личку боту, ИЛИ числовой TG ID.
✖ — удалить запись (owner'а или оператора).`
	case "diag":
		return `📊 Диагностика — справка

Короткий отчёт о системе: версия awg-manager, состояние WAN, туннели, DNS.
Запуск ~30–60с. Не меняет состояние, только читает.
📄 Полный отчёт — JSON-дамп (для отладки).
🔁 Перезапустить — снять отчёт заново.`
	case "status":
		return `📊 Status — справка

Публикует smart-reply (📊 Что происходит?) в топик каждого роутера. Useful когда нужно одним тапом получить срез по всем роутерам.`
	}
	return "ℹ Помощь по этому экрану ещё не написана."
}

// HelpRowFor returns a single-row inline-keyboard suffix containing the
// "ℹ Помощь" button for a given panel screen. Designed to be appended
// to existing panel keyboards just before the "✖ Закрыть" row.
func HelpRowFor(screen string) []InlineKeyboardButton {
	return []InlineKeyboardButton{
		{Text: "ℹ Помощь", CallbackData: "panel:0:help:" + screen},
	}
}
