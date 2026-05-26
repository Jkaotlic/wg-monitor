package main

import "testing"

func TestTelegramCommandMenuIncludesOperatorSlashCommands(t *testing.T) {
	cmds := telegramCommandMenu()
	want := map[string]bool{
		"status":   false,
		"check":    false,
		"tunnels":  false,
		"routes":   false,
		"via":      false,
		"direct":   false,
		"maint":    false,
		"upgrade":  false,
		"keyboard": false,
		"help":     false,
	}
	for _, c := range cmds {
		if _, ok := want[c.Command]; ok {
			want[c.Command] = true
		}
	}
	for cmd, found := range want {
		if !found {
			t.Fatalf("telegram command menu missing /%s; got %+v", cmd, cmds)
		}
	}
}
