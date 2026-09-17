package tg

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplyKeyboardMarkupJSONShape(t *testing.T) {
	kb := ReplyKeyboardMarkup{
		Keyboard:       [][]ReplyKeyboardButton{{{Text: "X"}}},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
	raw, err := json.Marshal(kb)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	want := `{"keyboard":[[{"text":"X"}]],"is_persistent":true,"resize_keyboard":true}`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

func TestReplyKeyboardRemoveJSONShape(t *testing.T) {
	rm := ReplyKeyboardRemove{RemoveKeyboard: true}
	raw, _ := json.Marshal(rm)
	if string(raw) != `{"remove_keyboard":true}` {
		t.Errorf("got %s want {\"remove_keyboard\":true}", raw)
	}
}

func TestReplyKeyboardForTopic(t *testing.T) {
	cases := []struct {
		kind   string
		isMM   bool // is ReplyKeyboardMarkup expected?
		texts  []string
		wantR1 int // row 1 button count (0 means "don't care")
		wantR2 int
	}{
		{"per_router", true, []string{"📊 Что происходит?", "🎛 Туннели", "🌍 Через туннель?", "🇷🇺 Напрямую?", "🛣 Маршруты", "🩺 Проверка"}, 2, 2},
		{"summary", true, []string{"📋 Список юзеров", "📊 Здоровье флота"}, 2, 0},
		{"systemic", true, []string{"📋 Список юзеров", "📊 Здоровье флота"}, 2, 0},
		{"unknown", false, nil, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			got := ReplyKeyboardForTopic(c.kind)
			if c.isMM {
				kb, ok := got.(*ReplyKeyboardMarkup)
				if !ok {
					t.Fatalf("kind=%s: expected *ReplyKeyboardMarkup, got %T", c.kind, got)
				}
				if !kb.IsPersistent || !kb.ResizeKeyboard {
					t.Errorf("kind=%s: persistence flags off: %+v", c.kind, kb)
				}
				if c.wantR1 > 0 && len(kb.Keyboard[0]) != c.wantR1 {
					t.Errorf("kind=%s: row 0 has %d buttons want %d", c.kind, len(kb.Keyboard[0]), c.wantR1)
				}
				if c.wantR2 > 0 && (len(kb.Keyboard) < 2 || len(kb.Keyboard[1]) != c.wantR2) {
					t.Errorf("kind=%s: row 1 mismatch", c.kind)
				}
				// Texts must all appear
				flat := ""
				for _, row := range kb.Keyboard {
					for _, b := range row {
						flat += b.Text + "|"
					}
				}
				for _, want := range c.texts {
					if !contains(flat, want) {
						t.Errorf("kind=%s: missing text %q in %q", c.kind, want, flat)
					}
				}
			} else {
				rm, ok := got.(*ReplyKeyboardRemove)
				if !ok {
					t.Fatalf("kind=%s: expected *ReplyKeyboardRemove, got %T", c.kind, got)
				}
				if !rm.RemoveKeyboard {
					t.Errorf("kind=%s: RemoveKeyboard should be true", c.kind)
				}
			}
		})
	}
}

func TestReplyKeyboard_PerRouter_HasNoMaintenanceButtons(t *testing.T) {
	kb := ReplyKeyboardForTopic("per_router").(*ReplyKeyboardMarkup)
	for _, gone := range []string{"🛠 Обслуживание", "⬆ Обновить пакеты"} {
		if replyKeyboardHasText(kb, gone) {
			t.Errorf("кнопка %q переехала в приложение, а в меню осталась: %+v", gone, kb.Keyboard)
		}
	}
}

func TestOperatorMenuInlineKeyboardForTopic_PerRouter(t *testing.T) {
	kb := OperatorMenuInlineKeyboardForTopic("per_router")
	if kb == nil {
		t.Fatal("operator menu keyboard is nil")
	}
	for _, want := range []struct {
		text string
		cb   string
	}{
		{"📊 Что происходит?", "compat_btn:0:smart_reply"},
		{"🩺 Проверка", "compat_btn:0:router_doctor"},
		{"🎛 Туннели", "compat_btn:0:tunnels"},
		{"🛣 Маршруты", "compat_btn:0:routes"},
		{"🌍 Через туннель?", "compat_btn:0:via_tunnel"},
		{"🇷🇺 Напрямую?", "compat_btn:0:direct"},
	} {
		if !inlineKeyboardHasButton(kb, want.text, want.cb) {
			t.Fatalf("operator menu missing %q/%q: %+v", want.text, want.cb, kb.InlineKeyboard)
		}
	}
}

func TestOperatorMenuInlineKeyboardForTopic_UnknownNil(t *testing.T) {
	if kb := OperatorMenuInlineKeyboardForTopic("unknown"); kb != nil {
		t.Fatalf("unknown topic should not get operator menu: %+v", kb)
	}
}

func replyKeyboardHasText(kb *ReplyKeyboardMarkup, text string) bool {
	for _, row := range kb.Keyboard {
		for _, b := range row {
			if b.Text == text {
				return true
			}
		}
	}
	return false
}

func inlineKeyboardHasButton(kb *InlineKeyboardMarkup, text, callback string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.Text == text && b.CallbackData == callback {
				return true
			}
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestBotMenusHaveNoCabinets(t *testing.T) {
	kb := ReplyKeyboardForTopic("per_router").(*ReplyKeyboardMarkup)
	for _, gone := range []string{"🔐 Amnezia Premium", "🔑 HideMy.name"} {
		if replyKeyboardHasText(kb, gone) {
			t.Errorf("кнопка %q переехала в приложение, а в меню осталась", gone)
		}
	}
	for _, code := range []string{"amnezia_premium", "hidemyname"} {
		if got := CompatBtnTextByCode(code); got != "" {
			t.Errorf("compat-код %q всё ещё знает кнопку %q", code, got)
		}
	}
	for _, cmds := range [][]BotCommand{OperatorBotCommands(), AdminBotCommands()} {
		for _, c := range cmds {
			switch c.Command {
			case "amnezia", "hidemy", "selfhosted", "cancel":
				t.Errorf("/%s переехала в приложение, а осталась в меню", c.Command)
			}
		}
	}
	if strings.Contains(OperatorMenuHelpText(), "Amnezia") {
		t.Errorf("справка меню упоминает кабинет: %s", OperatorMenuHelpText())
	}
}

func TestMiniAppURL(t *testing.T) {
	if got := MiniAppURL(" https://wgmon.example.com/ "); got != "https://wgmon.example.com/miniapp/" {
		t.Fatalf("MiniAppURL = %q", got)
	}
	if MiniAppURL("http://wgmon.example.com") != "" || MiniAppURL("") != "" {
		t.Fatal("не https -- пусто")
	}
}
