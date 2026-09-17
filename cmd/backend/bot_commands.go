package main

import (
	"context"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Слеш-команд у бота больше нет (цикл 5): управление -- в приложении.
// Telegram помнит список команд на своей стороне, пока его не перезапишут, и
// после обновления люди видели бы меню из команд, которых нет. Пустой список
// ставится на каждом старте: это дёшево, идемпотентно и чинит бота, которому
// меню выставили руками через BotFather.
//
// Области -- те же три, в которые бот его когда-либо ставил: по умолчанию,
// личка админа и (если группа ещё задана в конфиге) её участник-админ.
// Пропустить любую из них -- значит оставить в ней старое меню навсегда.

// botCommandSetter -- то, чем чистка пользуется у tg.Client.
type botCommandSetter interface {
	SetMyCommands(ctx context.Context, cmds []tg.BotCommand) error
	SetMyCommandsWithScope(ctx context.Context, cmds []tg.BotCommand, scope tg.BotCommandScope) error
}

// clearBotCommandMenus стирает меню команд во всех трёх областях. Ошибки не
// фатальны -- бот работает и без меню; вызывающий их логирует.
func clearBotCommandMenus(ctx context.Context, c botCommandSetter, adminUserID, groupChatID int64) []error {
	var errs []error
	if err := c.SetMyCommands(ctx, []tg.BotCommand{}); err != nil {
		errs = append(errs, err)
	}
	if adminUserID != 0 {
		if err := c.SetMyCommandsWithScope(ctx, []tg.BotCommand{}, tg.BotCommandScope{
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
