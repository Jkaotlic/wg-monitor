package tg

import "strings"

type BotMenuItem struct {
	Code        string
	Label       string
	Command     string
	Description string
}

type PanelKindItem struct {
	Kind  string
	Label string
}

type PanelHomeButton struct {
	Text         string
	CallbackData string
}

var routerMenuItems = []BotMenuItem{
	{Code: "smart_reply", Label: "📊 Что происходит?", Command: "status", Description: "Статус этого роутера"},
	{Code: "router_doctor", Label: "🩺 Проверка", Command: "check", Description: "Проверить роутер изнутри"},
	{Code: "tunnels", Label: "🎛 Туннели", Command: "tunnels", Description: "Открыть туннели"},
	{Code: "routes", Label: "🛣 Маршруты", Command: "routes", Description: "Открыть маршруты"},
	{Code: "amnezia_premium", Label: "🔐 Amnezia Premium", Command: "amnezia", Description: "Кабинет Amnezia Premium"},
	{Code: "hidemyname", Label: "🔑 HideMy.name", Command: "hidemy", Description: "Кабинет HideMy.name"},
	{Code: "via_tunnel", Label: "🌍 Через туннель?", Command: "via", Description: "Проверить связь через туннель"},
	{Code: "direct", Label: "🇷🇺 Напрямую?", Command: "direct", Description: "Проверить прямую связь"},
	{Code: "maint", Label: "🛠 Обслуживание", Command: "maint", Description: "Открыть обслуживание"},
	{Code: "opkg_upgrade", Label: "⬆ Обновить пакеты", Command: "upgrade", Description: "Обновить пакеты Entware"},
}

var fleetMenuItems = []BotMenuItem{
	{Code: "fleet_health", Label: "📊 Здоровье флота", Command: "fleet", Description: "Здоровье флота"},
	{Code: "list_users", Label: "📋 Список юзеров", Command: "users", Description: "Список роутеров"},
}

var utilityCommandItems = []BotMenuItem{
	{Command: "menu", Description: "Показать кнопки в этом топике"},
	{Command: "keyboard", Description: "Восстановить кнопки в этом топике"},
	{Command: "help", Description: "Справка по командам и кнопкам"},
}

var adminCommandItems = []BotMenuItem{
	{Command: "panel", Description: "Открыть панель управления"},
	{Command: "ensure_topics", Description: "Создать темы для всех роутеров"},
	{Command: "recreate_topic", Description: "Пересоздать тему текущего роутера"},
	{Command: "this_is", Description: "Привязать этот топик к роутеру"},
	{Command: "topic_help", Description: "Шпаргалка по управлению темами"},
	{Command: "selfhosted", Description: "Self-hosted Amnezia VPS"},
}

var operatorCommandOrder = []string{
	"status",
	"check",
	"tunnels",
	"routes",
	"via",
	"direct",
	"amnezia",
	"hidemy",
	"maint",
	"upgrade",
	"menu",
	"keyboard",
	"help",
}

var adminPanelRouterKindItems = []PanelKindItem{
	{Kind: "status", Label: "📊 Статус"},
	{Kind: "doctor", Label: "🩺 Проверка"},
	{Kind: "tunnels", Label: "🎛 Туннели"},
	{Kind: "routes", Label: "🛣 Маршруты"},
	{Kind: "pingcheck", Label: "📡 PingCheck"},
	{Kind: "maint", Label: "🛠 Обслуживание"},
}

var adminPanelHomeRows = [][]PanelHomeButton{
	{
		{Text: "🔎 Аудит всех", CallbackData: "panel:0:audit_all"},
		{Text: "⬆ Обновить все", CallbackData: "panel:0:update_all_confirm"},
	},
	panelKindButtonRow(adminPanelRouterKindItems[0], adminPanelRouterKindItems[1]),
	panelKindButtonRow(adminPanelRouterKindItems[2], adminPanelRouterKindItems[3]),
	panelKindButtonRow(adminPanelRouterKindItems[4], adminPanelRouterKindItems[5]),
	{
		{Text: "🩺 Все роутеры", CallbackData: "panel:0:doctor_all"},
		{Text: "🪄 Оживить топики", CallbackData: "panel:0:awaken_confirm"},
	},
	{
		{Text: "🚗 Мобильные", CallbackData: "panel:0:mobile"},
		{Text: "👥 Доступ", CallbackData: "access:0:home"},
	},
	{
		{Text: "ℹ Помощь оператору", CallbackData: "panel:0:help:operator"},
	},
	{
		{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
	},
}

func RouterMenuItems() []BotMenuItem {
	return cloneMenuItems(routerMenuItems)
}

func FleetMenuItems() []BotMenuItem {
	return cloneMenuItems(fleetMenuItems)
}

func OperatorBotCommands() []BotCommand {
	items := append(cloneMenuItems(routerMenuItems), utilityCommandItems...)
	return commandsFromItemsInOrder(items, operatorCommandOrder)
}

func AdminBotCommands() []BotCommand {
	items := append(cloneMenuItems(routerMenuItems), utilityCommandItems...)
	items = append(items, adminCommandItems...)
	return commandsFromItemsInOrder(items, append(operatorCommandOrder, "panel", "ensure_topics", "recreate_topic", "this_is", "topic_help", "selfhosted"))
}

func AdminPanelRouterKindItems() []PanelKindItem {
	out := make([]PanelKindItem, len(adminPanelRouterKindItems))
	copy(out, adminPanelRouterKindItems)
	return out
}

func AdminPanelKindLabel(kind string) string {
	for _, item := range adminPanelRouterKindItems {
		if item.Kind == kind {
			return item.Label
		}
	}
	return ""
}

func AdminPanelHomeKeyboard() InlineKeyboardMarkup {
	rows := make([][]InlineKeyboardButton, 0, len(adminPanelHomeRows))
	for _, row := range adminPanelHomeRows {
		buttons := make([]InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			buttons = append(buttons, InlineKeyboardButton{
				Text:         b.Text,
				CallbackData: b.CallbackData,
			})
		}
		rows = append(rows, buttons)
	}
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

func OperatorMenuHelpText() string {
	var lines []string
	for _, item := range routerMenuItems {
		if item.Label == "" || item.Description == "" {
			continue
		}
		lines = append(lines, "  "+item.Label+" - "+item.Description)
	}
	return strings.Join(lines, "\n")
}

func commandsFromItemsInOrder(items []BotMenuItem, order []string) []BotCommand {
	byCommand := make(map[string]BotMenuItem, len(items))
	for _, item := range items {
		if item.Command == "" {
			continue
		}
		if _, exists := byCommand[item.Command]; exists {
			continue
		}
		byCommand[item.Command] = item
	}
	out := make([]BotCommand, 0, len(byCommand))
	seen := map[string]bool{}
	for _, command := range order {
		item, ok := byCommand[command]
		if !ok || seen[command] {
			continue
		}
		seen[command] = true
		out = append(out, BotCommand{Command: item.Command, Description: item.Description})
	}
	for _, item := range items {
		if item.Command == "" || seen[item.Command] {
			continue
		}
		seen[item.Command] = true
		out = append(out, BotCommand{Command: item.Command, Description: item.Description})
	}
	return out
}

func cloneMenuItems(items []BotMenuItem) []BotMenuItem {
	out := make([]BotMenuItem, len(items))
	copy(out, items)
	return out
}

func panelKindButtonRow(left, right PanelKindItem) []PanelHomeButton {
	return []PanelHomeButton{
		{Text: left.Label, CallbackData: "panel:0:kind:" + left.Kind},
		{Text: right.Label, CallbackData: "panel:0:kind:" + right.Kind},
	}
}
