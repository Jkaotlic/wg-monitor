package callbacks

import (
	"context"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func flattenKbCallbacks(kb *tg.InlineKeyboardMarkup) []string {
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Кнопки «ℹ Помощь» живут на панелях роутера до цикла 4. Тест берёт их из
// настоящих построителей клавиатур, а не из списка строк: переименование или
// удаление обработчика ловится здесь, а не у человека в чате.
func TestHelpCallback_EveryRealHelpButtonStillAnswers(t *testing.T) {
	var datas []string
	collect := func(kb tg.InlineKeyboardMarkup) {
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if isHelpCallback(b.CallbackData) {
					datas = append(datas, b.CallbackData)
				}
			}
		}
	}
	collect(tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		tg.HelpRowFor("tunnels"), // tg/tunnels_panel.go:203
		tg.HelpRowFor("routes"),  // tg/routes_panel.go:281
	}})
	collect(tg.DiagResultKeyboard("err", 42, "")) // tg/diag_keyboard.go:26
	collect(tg.PingCheckPanelKeyboard(42, nil))   // tg/pingcheck_panel.go:144
	if len(datas) != 4 {
		t.Fatalf("кнопок справки %d (%v), ждали 4", len(datas), datas)
	}

	for _, data := range datas {
		t.Run(data, func(t *testing.T) {
			d, _ := newTestDB(t)
			f := &fakeRouterTGFull{}
			r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
			// Жмёт не админ, в общей группе: справку может открыть любой.
			q := &tg.CallbackQuery{ID: "cb-help", From: tg.User{ID: 200}, Data: data,
				Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 77}}

			r.HandleCallback(context.Background(), q)

			screen := strings.TrimPrefix(data, helpCallbackPrefix)
			if len(f.edits) != 1 || f.edits[0] != tg.HelpForScreen(screen) {
				t.Fatalf("справка %q не показана: edits=%q answers=%q", screen, f.edits, f.answers)
			}
			if strings.Contains(f.edits[0], "ещё не написана") {
				t.Fatalf("для %q показана заглушка вместо справки", screen)
			}
			if len(f.answers) != 1 || f.answers[0] != "" {
				t.Fatalf("ответ на нажатие %q, ждали один пустой", f.answers)
			}
		})
	}
}

// Клавиатура справки одна на всех -- только «Закрыть», и она работает.
// Админ получает ту же: хаба, куда вело «« Назад», больше нет.
func TestHelpCallback_KeyboardIsSafeCloseForEveryone(t *testing.T) {
	for _, from := range []int64{42, 200} {
		d, _ := newTestDB(t)
		f := &fakeRouterTGFull{}
		r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
		q := &tg.CallbackQuery{ID: "cb-help", From: tg.User{ID: from}, Data: "panel:0:help:pingcheck",
			Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 77, Text: "help"}}

		r.HandleCallback(context.Background(), q)

		if len(f.editMarkups) != 1 || f.editMarkups[0] == nil {
			t.Fatalf("from=%d: справка без клавиатуры, markups=%+v", from, f.editMarkups)
		}
		got := flattenKbCallbacks(f.editMarkups[0])
		if len(got) != 1 || got[0] != "close_panel:0:_panel_" {
			t.Fatalf("from=%d: клавиатура справки %v, ждали только close_panel:0:_panel_", from, got)
		}

		f.answers, f.edits, f.editMarkups = nil, nil, nil
		q.ID, q.Data = "cb-help-close", "close_panel:0:_panel_"
		r.HandleCallback(context.Background(), q)
		if len(f.answers) != 1 || f.answers[0] == "доступ только у админа" {
			t.Fatalf("from=%d: «Закрыть» не прошло: answers=%q", from, f.answers)
		}
		if len(f.edits) != 1 {
			t.Fatalf("from=%d: «Закрыть» должно убрать клавиатуру одной правкой, edits=%q", from, f.edits)
		}
	}
}

// Справка из личной переписки с ботом: уведомления переехали в личку, и
// кнопка там -- законный источник нажатия для любого.
func TestHelpCallback_WorksInPrivateChat(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	q := &tg.CallbackQuery{ID: "cb-help-dm", From: tg.User{ID: 200}, Data: "panel:0:help:premium",
		Message: tg.Message{Chat: tg.Chat{ID: 200}, MessageID: 7}}

	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "в приложение") {
		t.Fatalf("справка в личке не показана: edits=%q answers=%q", f.edits, f.answers)
	}
}
