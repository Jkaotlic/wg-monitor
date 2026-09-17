package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

type scopeCall struct {
	cmds  []tg.BotCommand
	scope tg.BotCommandScope
}

type fakeCommandSetter struct {
	calls []scopeCall
	err   error
}

func (f *fakeCommandSetter) SetMyCommands(_ context.Context, cmds []tg.BotCommand) error {
	f.calls = append(f.calls, scopeCall{cmds: cmds, scope: tg.BotCommandScope{Type: "default"}})
	return f.err
}

func (f *fakeCommandSetter) SetMyCommandsWithScope(_ context.Context, cmds []tg.BotCommand, scope tg.BotCommandScope) error {
	f.calls = append(f.calls, scopeCall{cmds: cmds, scope: scope})
	return f.err
}

// Слеш-команд у бота больше нет: меню стирается во всех трёх областях, в
// которые бот его когда-либо ставил. Пропустить область -- значит оставить в
// ней навсегда меню из команд, которых нет.
func TestClearBotCommandMenus_AllThreeScopes(t *testing.T) {
	f := &fakeCommandSetter{}
	if errs := clearBotCommandMenus(context.Background(), f, 42, -100500); len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	want := []tg.BotCommandScope{
		{Type: "default"},
		{Type: "chat", ChatID: 42},
		{Type: "chat_member", ChatID: -100500, UserID: 42},
	}
	if len(f.calls) != len(want) {
		t.Fatalf("вызовов %d, ждали %d: %+v", len(f.calls), len(want), f.calls)
	}
	for i, w := range want {
		if f.calls[i].scope != w {
			t.Errorf("область %d = %+v, ждали %+v", i, f.calls[i].scope, w)
		}
		if len(f.calls[i].cmds) != 0 {
			t.Errorf("область %d: список команд не пуст: %+v", i, f.calls[i].cmds)
		}
	}
}

// Группы в конфиге нет -- областей две, и это не ошибка.
func TestClearBotCommandMenus_WithoutGroup(t *testing.T) {
	f := &fakeCommandSetter{}
	if errs := clearBotCommandMenus(context.Background(), f, 42, 0); len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(f.calls) != 2 {
		t.Fatalf("вызовов %d, ждали 2: %+v", len(f.calls), f.calls)
	}
	for _, c := range f.calls {
		if c.scope.Type == "chat_member" {
			t.Fatalf("без группы область chat_member не трогаем: %+v", c)
		}
	}
}

// Ошибки Telegram не фатальны: бот работает и без меню, а вызывающий их
// логирует -- и обязан узнать про каждую.
func TestClearBotCommandMenus_ErrorsAreReturned(t *testing.T) {
	f := &fakeCommandSetter{err: errors.New("429 Too Many Requests")}
	if errs := clearBotCommandMenus(context.Background(), f, 42, -100500); len(errs) != 3 {
		t.Fatalf("ошибок %d, ждали 3: %v", len(errs), errs)
	}
}
