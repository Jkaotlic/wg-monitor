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
	case "doctor":
		return `🩺 Проверка — что это

Read-only doctor запускается агентом прямо на выбранном роутере. Это важно, когда ноутбук не в домашней LAN или есть конфликт подсетей: проверка не ломится с твоего ПК, а смотрит состояние изнутри роутера.

Что проверяет:
  ✅ awg-manager API и версию
  ✅ активный backend: NativeWG / Kernel / HydraRoute-Neo
  ✅ managed-туннели и defaultRoute
  ✅ PingCheck watchdog и статусы alive/dead
  ✅ процессы wg-monitor и awg-manager
  ✅ default route роутера

Команда ничего не меняет: не рестартит сервисы, не трогает маршруты и не включает/выключает туннели.

В админской панели кнопка «🩺 Все роутеры» ставит такую же read-only проверку в очередь по всем роутерным топикам.`
	case "pingcheck":
		return `📡 PingCheck — что это
PingCheck — это watchdog awg-manager. Раз в N секунд он пингует целевой
хост через туннель; при N подряд провалах awg-mgr автоматически
рестартит туннель.

Что показывает панель:
  🟢 — туннель жив, последний ping успешен
  🔴 — пинги провалены, туннель помечен как dead
  ⏸ — watchdog для туннеля выключен оператором
  ❓ — состояние неизвестно

Колонки:
  82ms  — задержка последнего ping
  ✓417  — счётчик успешных проверок (с момента старта watchdog)
  ✗0/3  — счётчик провалов / порог рестарта
  Restart×0 — сколько раз watchdog рестартил туннель

Кнопки:
  [⏸/▶ awgN] — выключить/включить watchdog для конкретного туннеля
  [▶ Проверить сейчас] — пнуть watchdog глобально
  [🔄 Обновить] — перечитать состояние

Параметры (host / interval / threshold) задаются NDMS-профилем
ping-check на роутере; меняются через ndmc, не из бота.`
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
