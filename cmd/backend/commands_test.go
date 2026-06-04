package main

import "testing"

func TestTelegramCommandMenuIncludesOperatorSlashCommands(t *testing.T) {
	cmds := telegramOperatorCommandMenu()
	want := map[string]bool{
		"status":   false,
		"check":    false,
		"tunnels":  false,
		"routes":   false,
		"amnezia":  false,
		"hidemy":   false,
		"via":      false,
		"direct":   false,
		"maint":    false,
		"upgrade":  false,
		"menu":     false,
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

func TestTelegramOperatorCommandMenuExcludesAdminCommands(t *testing.T) {
	cmds := telegramOperatorCommandMenu()
	for _, c := range cmds {
		switch c.Command {
		case "panel", "ensure_topics", "recreate_topic", "this_is", "topic_help", "selfhosted":
			t.Fatalf("operator command menu must not expose admin command /%s: %+v", c.Command, cmds)
		}
	}
}

func TestTelegramAdminCommandMenuIncludesAdminCommands(t *testing.T) {
	cmds := telegramAdminCommandMenu()
	want := map[string]bool{
		"panel":          false,
		"ensure_topics":  false,
		"recreate_topic": false,
		"this_is":        false,
		"topic_help":     false,
		"selfhosted":     false,
	}
	for _, c := range cmds {
		if _, ok := want[c.Command]; ok {
			want[c.Command] = true
		}
	}
	for cmd, found := range want {
		if !found {
			t.Fatalf("admin command menu missing /%s; got %+v", cmd, cmds)
		}
	}
}
