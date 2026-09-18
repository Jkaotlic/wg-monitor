package revive

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func fixtureStored() StoredCredentials {
	return StoredCredentials{RootPassword: fixtureRoot, AWGMLogin: fixtureLogin, AWGMPassword: fixturePanel, AWGMAPIKey: fixtureAPIKey}
}

func (e *testEnv) saveFixture(t *testing.T) {
	t.Helper()
	saved, err := e.svc.SaveCredentials(e.router, fixtureStored())
	if err != nil || !saved {
		t.Fatalf("SaveCredentials: %v %v", saved, err)
	}
}

// Пароль root лежит в базе только шифртекстом: ни в строке таблицы, ни в
// сырых файлах базы открытого текста нет, а ключ сервиса его открывает.
func TestSaveCredentials_EncryptedAtRest(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)

	nonce, ct, at, ok, err := env.db.RouterCredentials().Get(env.router)
	if err != nil || !ok || !at.Equal(testT0) {
		t.Fatalf("строка: ok=%v at=%v err=%v", ok, at, err)
	}
	box, _ := NewBox(env.key)
	creds, err := box.Open(env.router, nonce, ct)
	if err != nil || !creds.Equal(fixtureSecrets()) {
		t.Fatalf("расшифровка: %v", err)
	}
	raw := rawDBFiles(t, env.db)
	for _, s := range []string{fixtureRoot, fixtureLogin, fixturePanel, fixtureAPIKey} {
		if bytes.Contains(raw, []byte(s)) || bytes.Contains(ct, []byte(s)) {
			t.Fatalf("открытый текст %q в базе", s)
		}
	}
	// Журнал -- без значений.
	for _, s := range []string{fixtureRoot, fixturePanel, fixtureAPIKey} {
		if bytes.Contains(env.logs.Bytes(), []byte(s)) {
			t.Fatalf("журнал несёт %q", s)
		}
	}
}

// Без пароля root сохранять нечего: авто-оживление без него не запустится
// (Secrets.Usable), а вход панели в одиночку -- не повод хранить пароль.
func TestSaveCredentials_WithoutRootPasswordSavesNothing(t *testing.T) {
	env := newEnv(t)
	for _, root := range []string{"", "   "} {
		saved, err := env.svc.SaveCredentials(env.router, StoredCredentials{RootPassword: root, AWGMPassword: fixturePanel})
		if err != nil || saved {
			t.Fatalf("root %q: saved=%v err=%v", root, saved, err)
		}
	}
	if _, _, _, ok, _ := env.db.RouterCredentials().Get(env.router); ok {
		t.Fatal("строка появилась без пароля root")
	}
	var nilSvc *Service
	if saved, err := nilSvc.SaveCredentials(env.router, fixtureStored()); saved || !errors.Is(err, ErrDisabled) {
		t.Fatalf("выключенный сервис: %v %v", saved, err)
	}
}

func TestStoredCredentials_Masked(t *testing.T) {
	c := fixtureStored()
	for _, s := range []string{c.String(), c.GoString(), c.LogValue().String()} {
		if bytes.Contains([]byte(s), []byte(fixtureRoot)) {
			t.Fatalf("печать несёт пароль: %q", s)
		}
	}
}

func TestAutoSchedule_NeedsPasswordAndPanelAddress(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	if got, err := env.svc.AutoSchedule(ctx, env.router); err != nil || got != AutoNoPassword {
		t.Fatalf("без пароля: %v %v", got, err)
	}
	env.saveFixture(t)
	env.clearAWGMURL(t)
	if got, err := env.svc.AutoSchedule(ctx, env.router); err != nil || got != AutoNoPanelAddress {
		t.Fatalf("без адреса: %v %v", got, err)
	}
	// Записанный дашбордом локальный адрес -- то же «нет годного адреса»:
	// пароль root в чужой роутер той же сети не уходит.
	if _, err := env.db.SQL().Exec(`UPDATE users SET awgm_url = 'http://198.51.100.1' WHERE id = ?`, env.router); err != nil {
		t.Fatal(err)
	}
	if got, err := env.svc.AutoSchedule(ctx, env.router); err != nil || got != AutoNoPanelAddress {
		t.Fatalf("негодный адрес: %v %v", got, err)
	}
	if in := env.intent(t); in != nil {
		t.Fatalf("намерение появилось: %+v", in)
	}
}

func TestAutoSchedule_SchedulesFromStoredCredentials(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)
	got, err := env.svc.AutoSchedule(context.Background(), env.router)
	if err != nil || got != AutoScheduled {
		t.Fatalf("AutoSchedule: %v %v", got, err)
	}
	env.svc.Wait()
	in := env.intent(t)
	if in == nil || in.Status != StatusWaiting || in.RequestedBy != RequestedBySystem ||
		!in.ExpiresAt.Equal(testT0.Add(DefaultExpiryDays*24*time.Hour)) || in.TargetVersion != "" {
		t.Fatalf("намерение: %+v", in)
	}
	nonce, ct, ok, err := env.db.Revive().Secret(env.router)
	if err != nil || !ok {
		t.Fatalf("секрет намерения: %v %v", ok, err)
	}
	box, _ := NewBox(env.key)
	creds, err := box.Open(env.router, nonce, ct)
	if err != nil || !creds.Equal(fixtureSecrets()) {
		t.Fatalf("секрет намерения не тот: %v", err)
	}
	view, err := env.svc.StatusFor(env.router)
	if err != nil || view == nil || !view.Auto {
		t.Fatalf("экран не видит, что поставлено автоматически: %+v %v", view, err)
	}
	// Повтор -- ничего не переставляет: поколение то же.
	again, err := env.svc.AutoSchedule(context.Background(), env.router)
	if err != nil || again != AutoActive || env.intent(t).Generation != in.Generation {
		t.Fatalf("повтор: %v %v, поколение %d -> %d", again, err, in.Generation, env.intent(t).Generation)
	}
}

// Оживление, поставленное админом руками, авто-проход не трогает.
func TestAutoSchedule_LeavesManualIntentAlone(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)
	env.seedWaiting(t)
	before := env.intent(t)
	got, err := env.svc.AutoSchedule(context.Background(), env.router)
	if err != nil || got != AutoActive {
		t.Fatalf("AutoSchedule: %v %v", got, err)
	}
	after := env.intent(t)
	if after.Generation != before.Generation || after.RequestedBy != 42 {
		t.Fatalf("ручное намерение переставлено: %+v", after)
	}
	if view, _ := env.svc.StatusFor(env.router); view.Auto {
		t.Fatal("ручное намерение помечено как автоматическое")
	}
}

// После неудачи или истечения срока -- не чаще раза в AutoRetryAfter.
func TestAutoSchedule_RateLimitedAfterFinish(t *testing.T) {
	for _, status := range []string{StatusFailed, StatusExpired, StatusDone} {
		t.Run(status, func(t *testing.T) {
			env := newEnv(t)
			env.saveFixture(t)
			env.seedWaiting(t)
			in := env.intent(t)
			if ok, err := env.db.Revive().Finish(env.router, []string{StatusWaiting}, status, "x", env.clock.Now(), in.Generation); err != nil || !ok {
				t.Fatalf("finish: %v %v", ok, err)
			}
			env.clock.Advance(AutoRetryAfter - time.Minute)
			if got, err := env.svc.AutoSchedule(context.Background(), env.router); err != nil || got != AutoCooldown {
				t.Fatalf("раньше суток: %v %v", got, err)
			}
			env.clock.Advance(2 * time.Minute)
			if got, err := env.svc.AutoSchedule(context.Background(), env.router); err != nil || got != AutoScheduled {
				t.Fatalf("после суток: %v %v", got, err)
			}
		})
	}
}

// Отмену админа авто-проход уважает: заново -- только когда админ сохранил
// пароль после отмены (ввёл его снова).
func TestAutoSchedule_RespectsAdminCancel(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)
	if got, _ := env.svc.AutoSchedule(context.Background(), env.router); got != AutoScheduled {
		t.Fatalf("постановка: %v", got)
	}
	env.svc.Wait()
	env.clock.Advance(time.Minute)
	if ok, err := env.svc.Cancel(context.Background(), env.router); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	env.clock.Advance(10 * AutoRetryAfter)
	if got, err := env.svc.AutoSchedule(context.Background(), env.router); err != nil || got != AutoCancelledByAdmin {
		t.Fatalf("после отмены: %v %v", got, err)
	}
	env.saveFixture(t)
	if got, err := env.svc.AutoSchedule(context.Background(), env.router); err != nil || got != AutoScheduled {
		t.Fatalf("после нового пароля: %v %v", got, err)
	}
}

func TestAutoSchedule_UnreadableStoredCredentials(t *testing.T) {
	env := newEnv(t)
	other, _ := NewBox(randomKey(t))
	nonce, ct, _ := other.Seal(env.router, fixtureSecrets())
	if err := env.db.RouterCredentials().Put(env.router, nonce, ct, testT0); err != nil {
		t.Fatal(err)
	}
	if got, err := env.svc.AutoSchedule(context.Background(), env.router); err != nil || got != AutoUnreadable {
		t.Fatalf("чужой ключ: %v %v", got, err)
	}
	if env.intent(t) != nil {
		t.Fatal("намерение поставлено по нечитаемому секрету")
	}
}

// Агент на связи -- оживлять нечего, если только он не слишком старый:
// тогда NeedsReinstall говорит «переустанавливать всё равно», и воркер не
// закрывает намерение как «ожил сам».
func TestAutoSchedule_AliveAgentOnlyWhenNeedsReinstall(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)
	env.setLastSeen(t, testT0.Add(-time.Minute))
	if got, err := env.svc.AutoSchedule(context.Background(), env.router); err != nil || got != AutoAgentAlive {
		t.Fatalf("живой агент без NeedsReinstall: %v %v", got, err)
	}

	env.svc.cfg.NeedsReinstall = func(u *db.User) bool { return u.ID == env.router }
	env.probe.script(awgmstate.Reachable)
	if got, err := env.svc.AutoSchedule(context.Background(), env.router); err != nil || got != AutoScheduled {
		t.Fatalf("старый агент на связи: %v %v", got, err)
	}
	env.svc.Wait()
	env.clock.Advance(DefaultConfirmGap)
	env.tick(t)
	if in := env.intent(t); in.Status == StatusDone {
		t.Fatalf("старый агент на связи закрыт как «ожил сам»: %+v", in)
	}
	if calls := env.engine.calls(); len(calls) != 1 {
		t.Fatalf("переустановка не запущена: %d", len(calls))
	}
}

// Отказ входа с тем же паролем, что сохранён, стирает сохранённый: иначе
// авто-проход раз в сутки стучался бы в панель заведомо неверным паролем.
func TestAuthFailure_ForgetsSameStoredPassword(t *testing.T) {
	env := newEnv(t)
	env.saveFixture(t)
	env.seedWaiting(t) // тот же пароль фикстуры
	env.probe.script(awgmstate.Reachable)
	env.engine.set(func(f *fakeEngine) { f.outcome = Outcome{Finished: true, AuthFailed: true} })
	for i := 0; i < 4 && len(env.engine.calls()) == 0; i++ {
		env.clock.Advance(DefaultConfirmGap)
		env.tick(t)
	}
	env.tick(t)
	if in := env.intent(t); in.Status != StatusFailed {
		t.Fatalf("намерение: %+v", in)
	}
	if _, _, _, ok, _ := env.db.RouterCredentials().Get(env.router); ok {
		t.Fatal("неподошедший пароль остался сохранённым")
	}
}

func TestAuthFailure_KeepsDifferentStoredPassword(t *testing.T) {
	env := newEnv(t)
	if saved, err := env.svc.SaveCredentials(env.router, StoredCredentials{RootPassword: "другой-пароль"}); err != nil || !saved {
		t.Fatal(err)
	}
	env.seedWaiting(t)
	env.probe.script(awgmstate.Reachable)
	env.engine.set(func(f *fakeEngine) { f.outcome = Outcome{Finished: true, AuthFailed: true} })
	for i := 0; i < 4 && len(env.engine.calls()) == 0; i++ {
		env.clock.Advance(DefaultConfirmGap)
		env.tick(t)
	}
	env.tick(t)
	if in := env.intent(t); in.Status != StatusFailed {
		t.Fatalf("намерение: %+v", in)
	}
	if _, _, _, ok, _ := env.db.RouterCredentials().Get(env.router); !ok {
		t.Fatal("стёрт другой, не проверенный пароль")
	}
}
