package revive

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
)

// 503 -> 200 -> 200: запуск только после второго ответа подряд, без учётных
// данных в опросе, с расшифрованными секретами в движке.
func TestTick_OfflineThenReachableTwiceLaunches(t *testing.T) {
	env := newEnv(t)
	panel := env.panel(t, http.StatusServiceUnavailable, http.StatusOK, http.StatusOK)
	env.seedWaiting(t)

	env.tick(t) // 503
	if in := env.intent(t); in.Status != StatusWaiting || in.LastProbeState != "offline" || in.ReachableProbes != 0 {
		t.Fatalf("после 503: %+v", in)
	}
	if v, _ := env.svc.StatusFor(env.router); v.LastProbeText != "роутер не отвечает" {
		t.Fatalf("текст опроса: %+v", v)
	}
	env.tick(t) // 200, серия 1
	if len(env.engine.calls()) != 0 {
		t.Fatal("одного ответа мало для запуска")
	}
	env.tick(t) // 200, серия 2 -> запуск
	calls := env.engine.calls()
	// Secrets несёт открытый текст за неэкспортируемым указателем: != здесь
	// сравнил бы адреса, а не значения (у fixtureSecrets() и у расшифрованного
	// в launch() секрета они разные при любом исходе) -- нужен Equal.
	if len(calls) != 1 || !calls[0].Secrets.Equal(fixtureSecrets()) || calls[0].RouterID != env.router {
		t.Fatalf("запуски: %d", len(calls))
	}
	in := env.intent(t)
	if in.Status != StatusRunning || in.Attempts != 1 {
		t.Fatalf("после запуска: %+v", in)
	}
	for _, a := range panel.authHeaders() {
		if a != "" {
			t.Fatalf("опрос панели ушёл с авторизацией: %q", a)
		}
	}
	assertNoFixtureSecret(t, "журнал", env.logs.Bytes())
}

func TestTick_SuccessClosesDoneWipesSecretAndNotifies(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.tick(t)
	env.tick(t) // запуск
	env.tick(t) // задание ещё идёт
	if in := env.intent(t); in.Status != StatusRunning {
		t.Fatalf("%+v", in)
	}
	env.engine.set(func(f *fakeEngine) { f.outcome = Outcome{Finished: true, Success: true, Version: "v0.34.0"} })
	env.tick(t)
	if in := env.intent(t); in.Status != StatusDone {
		t.Fatalf("%+v", in)
	}
	if env.hasSecret(t) {
		t.Fatal("после успеха секрет обязан быть стёрт")
	}
	n := env.notifier.all()
	if len(n) != 1 || !strings.Contains(n[0].Text, "ожил") || !strings.Contains(n[0].Text, "0.34.0") || n[0].RouterID != env.router {
		t.Fatalf("уведомления: %+v", n)
	}
	env.tick(t)
	if len(env.notifier.all()) != 1 || len(env.engine.calls()) != 1 {
		t.Fatal("закрытое намерение больше ничего не делает")
	}
}

// Панель на опрос отвечает 401 (роутер жив), движок падает на входе ->
// failed сразу, без повторов.
func TestTick_AuthFailureFailsImmediately(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusUnauthorized)
	env.seedWaiting(t)
	env.engine.set(func(f *fakeEngine) {
		f.outcome = Outcome{Finished: true, AuthFailed: true, Text: "пароль не подошёл"}
	})
	env.tick(t)
	env.tick(t) // запуск
	env.tick(t) // итог
	in := env.intent(t)
	if in.Status != StatusFailed || in.Attempts != 1 || in.LastError != reasonAuthFailed {
		t.Fatalf("%+v", in)
	}
	if env.hasSecret(t) {
		t.Fatal("неверный пароль стирается")
	}
	n := env.notifier.all()
	if len(n) != 1 || !strings.Contains(n[0].Text, "пароль не подошёл — поставьте оживление заново") {
		t.Fatalf("уведомления: %+v", n)
	}
	for i := 0; i < 5; i++ {
		env.tick(t)
	}
	if len(env.engine.calls()) != 1 || len(env.notifier.all()) != 1 {
		t.Fatal("после ошибки входа повторов нет")
	}
}

func TestTick_AliveAgentClosesDoneWithoutProbeOrLaunch(t *testing.T) {
	env := newEnv(t)
	panel := env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.setLastSeen(t, env.clock.Now().Add(-2*time.Minute))
	env.tick(t)
	in := env.intent(t)
	if in.Status != StatusDone || in.LastError != reasonAliveItself {
		t.Fatalf("%+v", in)
	}
	if panel.hits() != 0 || len(env.engine.calls()) != 0 {
		t.Fatalf("живой агент: опросов %d, запусков %d", panel.hits(), len(env.engine.calls()))
	}
	if env.hasSecret(t) {
		t.Fatal("секрет стирается")
	}
	if n := env.notifier.all(); len(n) != 1 || !strings.Contains(n[0].Text, "снова на связи сам") {
		t.Fatalf("уведомления: %+v", n)
	}
}

func TestTick_OtherFailuresRetryThenGiveUpAfterFive(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.engine.set(func(f *fakeEngine) {
		f.outcome = Outcome{Finished: true, Text: "роутер не смог скачать агент"}
	})
	for i := 0; i < 40; i++ {
		env.tick(t)
		if in := env.intent(t); in.Status == StatusFailed {
			break
		}
		if in := env.intent(t); in.Status == StatusWaiting && in.Attempts > 0 && in.LastError != "роутер не смог скачать агент" {
			t.Fatalf("причина неудачной попытки не записана: %+v", in)
		}
	}
	in := env.intent(t)
	if in.Status != StatusFailed || in.Attempts != DefaultMaxAttempts {
		t.Fatalf("%+v", in)
	}
	if got := len(env.engine.calls()); got != DefaultMaxAttempts {
		t.Fatalf("запусков %d, ждали %d", got, DefaultMaxAttempts)
	}
	if env.hasSecret(t) {
		t.Fatal("после пятой попытки секрет стирается")
	}
	n := env.notifier.all()
	if len(n) != 1 || !strings.Contains(n[0].Text, "попыток: 5") || !strings.Contains(n[0].Text, "роутер не смог скачать агент") {
		t.Fatalf("уведомления: %+v", n)
	}
}

func TestTick_ExpiredClosesAndNotifiesOnce(t *testing.T) {
	env := newEnv(t)
	panel := env.panel(t, http.StatusServiceUnavailable)
	env.seedWaiting(t)
	env.clock.Advance(30*24*time.Hour + time.Minute)
	env.tick(t)
	env.tick(t)
	in := env.intent(t)
	if in.Status != StatusExpired || in.LastError != reasonExpired {
		t.Fatalf("%+v", in)
	}
	if env.hasSecret(t) {
		t.Fatal("по сроку секрет стирается")
	}
	if n := env.notifier.all(); len(n) != 1 || !strings.Contains(n[0].Text, "так и не появился") {
		t.Fatalf("уведомления: %+v", n)
	}
	if panel.hits() != 0 {
		t.Fatal("просроченное не опрашивается")
	}
}

func TestTick_LaunchErrors(t *testing.T) {
	t.Run("окончательная", func(t *testing.T) {
		env := newEnv(t)
		env.panel(t, http.StatusOK)
		env.seedWaiting(t)
		env.engine.set(func(f *fakeEngine) {
			f.launchErr = &LaunchError{Permanent: true, Text: "версия сервера старее установленного агента"}
		})
		env.tick(t)
		env.tick(t)
		in := env.intent(t)
		if in.Status != StatusFailed || in.LastError != "версия сервера старее установленного агента" || env.hasSecret(t) {
			t.Fatalf("%+v", in)
		}
		if n := env.notifier.all(); len(n) != 1 || !strings.Contains(n[0].Text, "версия сервера старее") {
			t.Fatalf("%+v", n)
		}
	})
	t.Run("временная", func(t *testing.T) {
		env := newEnv(t)
		env.panel(t, http.StatusOK)
		env.seedWaiting(t)
		env.engine.set(func(f *fakeEngine) {
			f.launchErr = &LaunchError{Text: "на роутере уже идёт другая установка или ремонт"}
		})
		env.tick(t)
		env.tick(t)
		in := env.intent(t)
		if in.Status != StatusWaiting || in.Attempts != 1 || in.ReachableProbes != 0 || !env.hasSecret(t) {
			t.Fatalf("%+v", in)
		}
		if len(env.notifier.all()) != 0 {
			t.Fatal("временная неудача -- без уведомления")
		}
	})
}

// Базы без ключа недостаточно: сервис с другим ключом не запускает движок.
func TestTick_WrongKeyFailsWithoutLaunch(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.svc = env.newService(t, randomKey(t))
	env.tick(t)
	env.tick(t)
	in := env.intent(t)
	if in.Status != StatusFailed || in.LastError != reasonSecretUnreadable {
		t.Fatalf("%+v", in)
	}
	if len(env.engine.calls()) != 0 {
		t.Fatal("с чужим ключом движок не зовётся")
	}
}

func TestTick_OneRunPerRouter(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	for i := 0; i < 10; i++ {
		env.tick(t) // итог не наступает: Outcome{} -- «идёт»
	}
	if got := len(env.engine.calls()); got != 1 {
		t.Fatalf("запусков %d, ждали один", got)
	}
}

func TestTick_LostJobReturnsToWaitingWithAttemptCounted(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.tick(t)
	env.tick(t) // запуск
	env.engine.set(func(f *fakeEngine) { f.lost = true })
	env.svc.Tick(context.Background())
	in := env.intent(t)
	// Тот же обход после возврата опросил панель заново: серия 1.
	if in.Status != StatusWaiting || in.Attempts != 1 || in.LastError != reasonJobLost {
		t.Fatalf("%+v", in)
	}
}

func TestTick_NilAndDisabledAreSafe(t *testing.T) {
	var s *Service
	s.Tick(context.Background())
}

// Опрос, прерванный отменой/дедлайном ВЫЗЫВАЮЩЕГО (не самой панелью), не
// имеет права ничего писать: ни менять серию "панель отвечает", ни портить
// last_probe_state настоящим состоянием сна роутера (fix round 1, Minor #2 в
// probe.go). Проверяем: серия и последний опрос не тронуты, запуска нет.
func TestTick_ProbeCancelledRecordsNothing(t *testing.T) {
	env := newEnv(t)
	env.seedWaiting(t)
	env.probe.mu.Lock()
	env.probe.delegate = func(context.Context, string) string { return ProbeCancelled }
	env.probe.mu.Unlock()

	before := env.intent(t)
	env.tick(t)
	after := env.intent(t)
	if after.LastProbeState != before.LastProbeState || !after.LastProbeAt.Equal(before.LastProbeAt) ||
		after.ReachableProbes != before.ReachableProbes || after.Status != StatusWaiting {
		t.Fatalf("отменённый опрос не должен ничего писать: до=%+v после=%+v", before, after)
	}
	if len(env.engine.calls()) != 0 {
		t.Fatal("отменённый опрос не должен запускать переустановку")
	}
}

// ProbeInvalidURL -- ошибка конфигурации роутера (адрес панели не
// разбирается), а не "роутер спит": серия не набирается никогда, но человек
// должен увидеть правильный текст, а не общее "роутер не отвечает".
func TestTick_ProbeInvalidURLShowsTextAndNeverLaunches(t *testing.T) {
	env := newEnv(t)
	env.seedWaiting(t)
	env.probe.script(ProbeInvalidURL)
	for i := 0; i < 5; i++ {
		env.tick(t)
	}
	in := env.intent(t)
	if in.Status != StatusWaiting || in.ReachableProbes != 0 {
		t.Fatalf("%+v", in)
	}
	if v, err := env.svc.StatusFor(env.router); err != nil || v.LastProbeText != "адрес панели неверный" {
		t.Fatalf("текст опроса: %+v, err=%v", v, err)
	}
	if len(env.engine.calls()) != 0 {
		t.Fatal("неверный адрес панели не должен запускать переустановку")
	}
}

// Reviewer round 1, mandatory: секрет расшифровывается ТОЛЬКО после
// MarkRunning. Между вторым (запускающим) опросом и launch()
// имитируем конкурентный Schedule(), подменивший шифртекст в базе -- launch
// обязан использовать актуальный на момент MarkRunning секрет, а не тот, что
// мог быть у него "в руках" раньше (раньше в руках вообще ничего не было:
// опрос панели секретов не касается).
func TestTick_LaunchUsesSecretCurrentAtMarkRunningNotStale(t *testing.T) {
	env := newEnv(t)
	env.seedWaiting(t)
	newSecrets := NewSecrets("New-Root-Pw-After-Reschedule", "", "", "")

	calls := 0
	env.probe.mu.Lock()
	env.probe.delegate = func(context.Context, string) string {
		calls++
		if calls == 2 {
			// Второй опрос -- тот самый, что запускает переустановку.
			// Конкурентный Schedule() успевает переставить шифртекст между
			// этим опросом и MarkRunning/Secret() внутри launch().
			box, err := NewBox(env.key)
			if err != nil {
				t.Fatal(err)
			}
			nonce, ct, err := box.Seal(env.router, newSecrets)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.db.SQL().Exec(
				`UPDATE revive_secrets SET nonce = ?, ciphertext = ? WHERE user_id = ?`,
				nonce, ct, env.router,
			); err != nil {
				t.Fatal(err)
			}
		}
		return awgmstate.Reachable
	}
	env.probe.mu.Unlock()

	env.tick(t) // серия 1
	env.tick(t) // серия 2 -> запуск, шифртекст подменён между опросом и MarkRunning

	got := env.engine.calls()
	if len(got) != 1 {
		t.Fatalf("запусков %d, ждали один", len(got))
	}
	if !got[0].Secrets.Equal(newSecrets) {
		t.Fatal("запуск обязан использовать секрет, актуальный на момент MarkRunning, а не расшифрованный заранее")
	}
	if got[0].Secrets.Equal(fixtureSecrets()) {
		t.Fatal("старый секрет не должен был уйти в движок после подмены")
	}
}
