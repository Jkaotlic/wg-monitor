package revive

import (
	"strings"
	"testing"
)

// REV-04: панель ответила 401 (сменили ключ или логин) -- пароль root тут ни
// при чём, и сохранённый не стирается. Намерение закрывается сразу:
// повторять с теми же данными входа бессмысленно.
func TestPanelAuthFailure_KeepsStoredRootPassword(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)
	env.seedWaiting(t) // тот же пароль, что сохранён
	env.runToOutcome(t, Outcome{Finished: true, AuthFailed: true, Text: "вход в панель роутера не подошёл"})
	if in := env.intent(t); in.Status != StatusFailed {
		t.Fatalf("намерение: %+v", in)
	}
	if _, _, _, ok, _ := env.db.RouterCredentials().Get(env.router); !ok {
		t.Fatal("401 панели стёр верный пароль root")
	}
	n := env.notifier.all()
	if len(n) != 1 || strings.Contains(n[0].Text, "стёрт") {
		t.Fatalf("уведомления: %+v", n)
	}
	assertOwnerText(t, n[0].Text)
}

// Отказ входа root -- стирает тот же сохранённый пароль, как и раньше.
func TestRootAuthFailure_ForgetsSameStoredPassword(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)
	env.seedWaiting(t)
	env.runToOutcome(t, Outcome{Finished: true, AuthFailed: true, RootAuthFailed: true, Text: "пароль не подошёл"})
	if _, _, _, ok, _ := env.db.RouterCredentials().Get(env.router); ok {
		t.Fatal("неподошедший пароль root остался сохранённым")
	}
}
