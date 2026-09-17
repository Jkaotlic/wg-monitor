package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Тем группы больше нет (цикл 5): команды, которые их заводили и правили,
// удалены вместе с группой. Справка -- единственный список команд, который
// человек читает, и упоминание удалённой команды посылает его в никуда.
func TestUsageDropsTopicCommands(t *testing.T) {
	got := usage()
	for _, gone := range []string{"ensure-topics", "bind-topic", "set-topic", "announce-dm-migration", "init-menu", "--ensure-topic"} {
		if strings.Contains(got, gone) {
			t.Errorf("в справке осталась удалённая команда %q:\n%s", gone, got)
		}
	}
	for _, want := range []string{"add-user", "list-users", "bind-tg-user", "show-discovered-dns", "version"} {
		if !strings.Contains(got, want) {
			t.Errorf("в справке нет живой команды %q:\n%s", want, got)
		}
	}
}

// add-user больше не обещает тему: владельца привязывают по номеру, который
// человек получает от бота в ответ на /start.
func TestAddUserHintPointsToStartAndBindTGUser(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	var out bytes.Buffer
	if err := runAddUser(addUserOpts{
		DBPath: dbPath, Nickname: "vasya", AWGIface: "awg0",
		ExpectedExitIP: "198.51.100.21", BackendURL: "https://wgmonitor.example.com", Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "topic") || strings.Contains(got, "тем") {
		t.Errorf("add-user всё ещё говорит про темы:\n%s", got)
	}
	for _, want := range []string{"/start", "bind-tg-user"} {
		if !strings.Contains(got, want) {
			t.Errorf("подсказка без %q:\n%s", want, got)
		}
	}
}
