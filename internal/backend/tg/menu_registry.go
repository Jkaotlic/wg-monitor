package tg

import "strings"

type BotMenuItem struct {
	Code        string
	Label       string
	Command     string
	Description string
}

var routerMenuItems = []BotMenuItem{
	{Code: "smart_reply", Label: "📊 Что происходит?", Command: "status", Description: "Статус этого роутера"},
	{Code: "router_doctor", Label: "🩺 Проверка", Command: "check", Description: "Проверить роутер изнутри"},
	{Code: "via_tunnel", Label: "🌍 Через туннель?", Command: "via", Description: "Проверить связь через туннель"},
	{Code: "direct", Label: "🇷🇺 Напрямую?", Command: "direct", Description: "Проверить прямую связь"},
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
	{Command: "ensure_topics", Description: "Создать темы для всех роутеров"},
	{Command: "recreate_topic", Description: "Пересоздать тему текущего роутера"},
	{Command: "this_is", Description: "Привязать этот топик к роутеру"},
	{Command: "topic_help", Description: "Шпаргалка по управлению темами"},
}

var operatorCommandOrder = []string{
	"status",
	"check",
	"via",
	"direct",
	"menu",
	"keyboard",
	"help",
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
	return commandsFromItemsInOrder(items, append(operatorCommandOrder, "ensure_topics", "recreate_topic", "this_is", "topic_help"))
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
