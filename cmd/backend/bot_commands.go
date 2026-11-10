package main

import (
	"context"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Управление — в приложении; аварийный busybox_hold доступен только админу в личке.
// Telegram помнит список команд на своей стороне, пока его не перезапишут, и
// после обновления люди видели бы меню из команд, которых нет. На каждом
// старте старые списки заменяются; аварийная команда остаётся только в личке
// администратора. Это также исправляет меню, заданное через BotFather.
//
// Области -- те же три, в которые бот его когда-либо ставил: по умолчанию,
// личка админа и (если группа ещё задана в конфиге) её участник-админ.
// Пропустить любую из них -- значит оставить в ней старое меню навсегда.

// botCommandSetter -- то, чем чистка пользуется у tg.Client.
type botCommandSetter interface {
	SetMyCommands(ctx context.Context, cmds []tg.BotCommand) error
	SetMyCommandsWithScope(ctx context.Context, cmds []tg.BotCommand, scope tg.BotCommandScope) error
}

// clearBotCommandMenus убирает старые меню; в личке админа оставляет busybox_hold. Ошибки не
// фатальны -- бот работает и без меню; вызывающий их логирует.
func clearBotCommandMenus(ctx context.Context, c botCommandSetter, adminUserID, groupChatID int64) []error {
	var errs []error
	if err := c.SetMyCommands(ctx, []tg.BotCommand{}); err != nil {
		errs = append(errs, err)
	}
	if adminUserID != 0 {
		if err := c.SetMyCommandsWithScope(ctx, []tg.BotCommand{{Command: "busybox_hold", Description: "Защитить BusyBox от обновления"}, {Command: "entware_recover", Description: "Восстановить команды Entware"}}, tg.BotCommandScope{
			Type: "chat", ChatID: adminUserID,
		}); err != nil {
			errs = append(errs, err)
		}
		if groupChatID != 0 {
			if err := c.SetMyCommandsWithScope(ctx, []tg.BotCommand{}, tg.BotCommandScope{
				Type: "chat_member", ChatID: groupChatID, UserID: adminUserID,
			}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}
