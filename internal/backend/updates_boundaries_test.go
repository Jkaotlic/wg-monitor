package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// В личку об обновлениях не уходит НИЧЕГО -- прямое решение оператора
// 12.09.2026: личка остаётся каналом поломок.
//
// Тест читает исходники этой задачи и проверяет, что ни один её путь не зовёт
// рассылку. Проверка именно по исходникам, а не по поведению: «не отправилось»
// в одном прогоне доказывает только то, что в этом прогоне не совпали
// условия, а запрет здесь абсолютный -- ни разово, ни «тихо раз в неделю».
func TestUpdatesNeverReachDirectMessages(t *testing.T) {
	files := []string{
		"miniapp_versions.go",
		"version_snapshot.go",
		filepath.Join("updatespoll", "poller.go"),
		filepath.Join("db", "update_reminders.go"),
		filepath.Join("upstream", "compare.go"),
	}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := string(body)
		for _, forbidden := range []string{"notify.Fanout", "Fanout(", "SendMessage("} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s зовёт доставку в личку (%q), а новость об обновлении -- только экран", name, forbidden)
			}
		}
	}
}

// Новость об обновлении -- не тревога и не идёт через FSM.
//
// Псевдо-проверка в incident_state всплыла бы в списке тревог мини-аппа и в
// счётчике alerts дашборда, и обновление стало бы выглядеть поломкой. Счётчик
// активных инцидентов обязан остаться нетронутым.
func TestUpdateNewsIsNotAnIncident(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("router-a", "tok-inc-0000000000000000000000000000000000", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}

	before, err := d.State().AllActiveHard()
	if err != nil {
		t.Fatal(err)
	}

	if err := d.UpdateReminders().Ensure(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateReminders().Ensure(uid, "firmware", "5.02.A.9.0-0"); err != nil {
		t.Fatal(err)
	}

	after, err := d.State().AllActiveHard()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("новость об обновлении завела инцидент: было %d, стало %d", len(before), len(after))
	}
}
