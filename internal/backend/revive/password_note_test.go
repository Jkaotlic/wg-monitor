package revive

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// REV-01: «пароль стёрт» -- только когда пароля на сервере правда нет. С v0.45
// пароль root хранится в router_credentials, и закрытие намерения его не
// удаляет: сказать «стёрт» при сохранённом -- соврать владельцу.
func TestNotice_SavedPasswordIsNotClaimedWiped(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)
	env.seedWaiting(t)
	env.setLastSeen(t, env.clock.Now().Add(-2*time.Minute))
	env.tick(t)
	n := env.notifier.all()
	if len(n) != 1 {
		t.Fatalf("уведомления: %+v", n)
	}
	if strings.Contains(n[0].Text, "стёрт") {
		t.Fatalf("пароль сохранён, а текст говорит «стёрт»: %q", n[0].Text)
	}
	if !strings.Contains(n[0].Text, "хранится") {
		t.Fatalf("текст не говорит, что пароль хранится: %q", n[0].Text)
	}
	assertOwnerText(t, n[0].Text)
}

// Без сохранённого пароля закрытие стирает единственную копию -- «стёрт»
// тогда правда и остаётся.
func TestNotice_UnsavedPasswordIsWiped(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.setLastSeen(t, env.clock.Now().Add(-2*time.Minute))
	env.tick(t)
	n := env.notifier.all()
	if len(n) != 1 || !strings.Contains(n[0].Text, "стёрт") {
		t.Fatalf("уведомления: %+v", n)
	}
}
