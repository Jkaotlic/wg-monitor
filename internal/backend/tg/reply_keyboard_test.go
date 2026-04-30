package tg

import (
	"encoding/json"
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
