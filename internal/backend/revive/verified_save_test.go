package revive

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
)

func (e *testEnv) runToOutcome(t *testing.T, out Outcome) {
	t.Helper()
	e.probe.script(awgmstate.Reachable)
	e.engine.set(func(f *fakeEngine) { f.outcome = out })
	for i := 0; i < 4 && len(e.engine.calls()) == 0; i++ {
		e.clock.Advance(DefaultConfirmGap)
		e.tick(t)
	}
	e.tick(t)
}

// REV-03: пароль ручного оживления сохраняется, когда вход в терминал им
// прошёл (config_written), -- и не раньше.
func TestManualRevive_SavesPasswordAfterVerifiedLogin(t *testing.T) {
	env := newEnv(t)
	env.seedWaiting(t)
	env.runToOutcome(t, Outcome{Finished: true, Text: "роутер не смог скачать агент", CredentialsVerified: true})
	nonce, ct, _, ok, err := env.db.RouterCredentials().Get(env.router)
	if err != nil || !ok {
		t.Fatalf("проверенный пароль не сохранён: %v %v", ok, err)
	}
	box, _ := NewBox(env.key)
	got, err := box.Open(env.router, nonce, ct)
	if err != nil || !got.Equal(fixtureSecrets()) {
		t.Fatalf("сохранено не то: %v", err)
	}
}

func TestManualRevive_UnverifiedPasswordIsNotSaved(t *testing.T) {
	env := newEnv(t)
	env.seedWaiting(t)
	env.runToOutcome(t, Outcome{Finished: true, Text: "переустановка не удалась"})
	if _, _, _, ok, _ := env.db.RouterCredentials().Get(env.router); ok {
		t.Fatal("непроверенный пароль сохранён")
	}
}
