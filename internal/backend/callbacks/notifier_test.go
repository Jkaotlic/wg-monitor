package callbacks

import (
	"context"
	"strings"
	"testing"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestNotifier_PingCheckResultOffersNextActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "pingcheck_now"},
		"pingcheck_now",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "pingcheck ok"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("pingcheck result should carry inline next-action keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"pingcheck_open:42:_panel_",
		"diag_now:42:_menu",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("pingcheck result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

// testAppBase -- публичный адрес бэкенда в тестах; кнопка ведёт роутер 42.
const testAppBase = "https://wgmon.example.com"

const testAppTunnelsURL = "https://wgmon.example.com/miniapp/?router=42&tab=tunnels"

// notifyForTest -- итог одной команды: что легло в первую разметку.
func notifyForTest(t *testing.T, chatID int64, action, status string) any {
	t.Helper()
	f := &fakeRouterTG{}
	n := NewNotifier(f)
	n.AppBaseURL = testAppBase
	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: chatID, MessageID: 200, Action: action},
		action,
		wire.CommandResult{ID: "cmd1", Status: status, Output: action + " " + status},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentMarkups) == 0 {
		t.Fatalf("%s/%s: ничего не отправлено", action, status)
	}
	return f.sentMarkups[0]
}

// Цикл 4: панели туннелей и маршрутов ушли из бота. Под итогами команд нет
// ни tunnels_refresh, ни routes_open; в личке вместо них -- кнопка
// приложения, в группе -- ничего (web_app там Telegram не принимает).
func TestNotifier_ResultKeyboardsLeadToAppNotRemovedPanels(t *testing.T) {
	cases := []struct {
		action, status string
		want           []string
	}{
		{"router_doctor", "ok", nil},
		{"router_doctor", "err", []string{"router_doctor:42:_menu"}},
		{"check_via_tunnel", "ok", []string{"pingcheck_open:42:_panel_", "router_doctor:42:_menu"}},
		{"check_direct", "err", []string{"pingcheck_open:42:_panel_", "router_doctor:42:_menu"}},
		{"force_recheck", "ok", []string{"router_doctor:42:_menu"}},
		{"pingcheck_now", "ok", []string{"pingcheck_open:42:_panel_", "diag_now:42:_menu"}},
	}
	for _, tc := range cases {
		t.Run(tc.action+"/"+tc.status, func(t *testing.T) {
			kb, ok := notifyForTest(t, 100, tc.action, tc.status).(*tg.InlineKeyboardMarkup)
			if !ok || kb == nil {
				t.Fatalf("в личке ждали инлайн-клавиатуру")
			}
			got := flattenKbCallbacks(kb)
			for _, want := range tc.want {
				if !containsStr(got, want) {
					t.Errorf("нет %q: %v", want, got)
				}
			}
			for _, cb := range got {
				if strings.HasPrefix(cb, "tunnels_refresh:") || strings.HasPrefix(cb, "routes_open:") {
					t.Errorf("кнопка удалённой панели: %q", cb)
				}
			}
			last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
			if len(last) != 1 || last[0].WebApp == nil || last[0].WebApp.URL != testAppTunnelsURL {
				t.Errorf("последний ряд -- кнопка приложения: %+v", last)
			}

			group := notifyForTest(t, -100, tc.action, tc.status)
			if gkb, ok := group.(*tg.InlineKeyboardMarkup); ok {
				for _, row := range gkb.InlineKeyboard {
					for _, b := range row {
						if b.WebApp != nil || strings.HasPrefix(b.CallbackData, "tunnels_refresh:") || strings.HasPrefix(b.CallbackData, "routes_open:") {
							t.Errorf("в группе: %+v", b)
						}
					}
				}
			}
		})
	}
}

// Без публичного адреса кнопки приложения нет и в личке, а итог проверки
// роутера уходит с обычной клавиатурой темы.
func TestNotifier_NoAppURLNoAppButton(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)
	if err := n.NotifyCommandResult(context.Background(), cmdpkg.MessageRef{ChatID: 100, MessageID: 200}, "router_doctor",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "doctor ok"}, 42, 3500); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup); ok {
		t.Fatalf("без адреса и без кнопок инлайн-клавиатуры быть не должно: %+v", f.sentMarkups[0])
	}
}
