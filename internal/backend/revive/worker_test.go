package revive

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
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

// Fix round 2, Minor #3: прежняя версия теста подменяла шифртекст ВНУТРИ
// опроса -- то есть строго ДО MarkRunning в любом случае, поэтому реализация,
// расшифровывающая секрет сразу после опроса (но до MarkRunning), тоже бы её
// прошла. Здесь граница закреплена явным хуком testAfterMarkRunning: подмена
// происходит ПОСЛЕ того, как MarkRunning уже отработал, и ДО чтения секрета.
func TestTick_LaunchUsesSecretCurrentAtMarkRunningNotStale(t *testing.T) {
	t.Run("подмена после MarkRunning -- запуск получает новый секрет", func(t *testing.T) {
		env := newEnv(t)
		env.panel(t, http.StatusOK)
		env.seedWaiting(t)
		newSecrets := NewSecrets("New-Root-Pw-After-Reschedule", "", "", "")

		env.svc.testAfterMarkRunning = func(routerID int64) {
			box, err := NewBox(env.key)
			if err != nil {
				t.Fatal(err)
			}
			nonce, ct, err := box.Seal(routerID, newSecrets)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.db.SQL().Exec(
				`UPDATE revive_secrets SET nonce = ?, ciphertext = ? WHERE user_id = ?`,
				nonce, ct, routerID,
			); err != nil {
				t.Fatal(err)
			}
		}

		env.tick(t) // серия 1
		env.tick(t) // серия 2 -> запуск: хук подменяет шифртекст между MarkRunning и Secret()

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
	})

	// Подмена шифртекста ДО MarkRunning (во время опроса) не имеет права
	// портить сам переход строки намерения: launch читает секрет заново уже
	// после MarkRunning, так что для перехода waiting->running и счётчика
	// попыток то, что было в revive_secrets на момент опроса, не имеет
	// значения вовсе.
	t.Run("подмена до MarkRunning -- переход строки намерения не портится", func(t *testing.T) {
		env := newEnv(t)
		env.seedWaiting(t)
		swapped := false
		env.probe.mu.Lock()
		env.probe.delegate = func(context.Context, string) string {
			if !swapped {
				swapped = true
				box, err := NewBox(env.key)
				if err != nil {
					t.Fatal(err)
				}
				nonce, ct, err := box.Seal(env.router, NewSecrets("Swapped-Before-MarkRunning", "", "", ""))
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

		env.tick(t) // серия 1, подмена происходит здесь
		env.tick(t) // серия 2 -> запуск

		in := env.intent(t)
		if in.Status != StatusRunning || in.Attempts != 1 {
			t.Fatalf("подмена секрета до MarkRunning не должна портить переход строки намерения: %+v", in)
		}
	})
}

// Fix round 2, Important B (мандатное ревью): раньше (fix round 2, Minor #1)
// forgetJob в finish срабатывал ТОЛЬКО после успешной записи -- защита от
// повторного "job lost" на каждом обходе при стабильно падающей записи. У
// этой защиты обнаружилась своя, худшая цена: если запись падала (SQLite
// busy, диск), job оставался отмеченным НАВСЕГДА -- pollOne видел бы его и
// на каждом обходе заново пытался Outcome/запись, а строка так и осталась
// бы running до перезапуска бэкенда. Теперь forgetJob безусловен: job
// забывается сразу, ДО попытки записи. Цена обратная и меньшая -- если
// запись действительно не прошла, следующий обход не сможет переспросить
// итог у уже забытого job и увидит "потеряно", вернув намерение в waiting
// (даже если запуск на самом деле уже завершился успехом) -- вместо того
// чтобы зависнуть навсегда. Хук testFinishErr подменяет собой запись в базу
// ровно один раз, воспроизводя эту ошибку детерминированно.
func TestTick_FinishDBErrorForgetsJobImmediatelyAndRecoversNextTick(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.tick(t)
	env.tick(t) // запуск
	env.engine.set(func(f *fakeEngine) { f.outcome = Outcome{Finished: true, Success: true, Version: "v0.34.0"} })

	failed := false
	env.svc.testFinishErr = func() error {
		if failed {
			return nil
		}
		failed = true
		return errors.New("тестовая ошибка записи")
	}

	env.tick(t) // Finish "падает" -- запись не проходит
	if !failed {
		t.Fatal("хук testFinishErr не сработал")
	}
	if in := env.intent(t); in.Status != StatusRunning {
		t.Fatalf("после неудачной записи статус должен остаться running (запись не прошла): %+v", in)
	}
	if !env.hasSecret(t) {
		t.Fatal("после неудачной записи секрет не должен стираться")
	}
	if len(env.notifier.all()) != 0 {
		t.Fatal("после неудачной записи уведомления быть не должно")
	}
	if _, ok := env.svc.job(env.router); ok {
		t.Fatal("job обязан быть забыт СРАЗУ, даже при неудачной записи (Fix round 2, Important B)")
	}

	env.tick(t) // job уже забыт -> pollOne видит "потеряно" -> обратно в waiting
	in := env.intent(t)
	if in.Status != StatusWaiting {
		t.Fatalf("после потери job намерение обязано восстановиться в waiting, а не зависнуть: %+v", in)
	}
}

// Fix round 2, Minor #2: при остановке бэкенда Run передаёт уже отменённый
// ctx. Строка к этому моменту уже терминальная и секрет уже стёрт --
// уведомление не имеет права потеряться навсегда только из-за отменённого
// ctx (Finish идемпотентен только один раз, повтора не будет).
func TestTick_NotificationSentDespiteCancelledCallerCtx(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.tick(t)
	env.tick(t) // запуск
	env.engine.set(func(f *fakeEngine) { f.outcome = Outcome{Finished: true, Success: true, Version: "v0.34.0"} })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // как при остановке бэкенда: ctx уже отменён до вызова Tick

	env.svc.Tick(ctx)

	n := env.notifier.all()
	if len(n) != 1 || !strings.Contains(n[0].Text, "ожил") {
		t.Fatalf("уведомление обязано уйти даже при отменённом ctx вызывающего: %+v", n)
	}
	if in := env.intent(t); in.Status != StatusDone {
		t.Fatalf("%+v", in)
	}
}

// Fix round 2, Minor #5: серии из двух "reachable" самой по себе мало --
// между ПЕРВЫМ и ПОСЛЕДНИМ опросом серии должно пройти не меньше ConfirmGap
// (иначе Tick, случайно попавший на тот же роутер почти сразу после
// confirmSoon, засчитал бы вторую метку и запустил переустановку с разбросом
// в секунду вместо заявленных спекой тридцати).
func TestTick_ReachableStreakFasterThanConfirmGapDoesNotLaunchYet(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)

	env.svc.Tick(context.Background()) // серия 1
	if in := env.intent(t); in.ReachableProbes != 1 {
		t.Fatalf("после первого опроса: %+v", in)
	}

	env.clock.Advance(time.Second) // куда меньше ConfirmGap (30 с)
	env.svc.Tick(context.Background())
	if in := env.intent(t); in.Status != StatusWaiting || in.ReachableProbes != 2 {
		t.Fatalf("серия должна набраться и записаться: %+v", in)
	}
	if len(env.engine.calls()) != 0 {
		t.Fatal("серия быстрее ConfirmGap не должна была запускать переустановку")
	}

	env.clock.Advance(DefaultConfirmGap) // теперь между первым и этим опросом прошло больше ConfirmGap
	env.svc.Tick(context.Background())
	calls := env.engine.calls()
	if len(calls) != 1 {
		t.Fatalf("запусков %d, ждали один после набора полного ConfirmGap", len(calls))
	}
	if in := env.intent(t); in.Status != StatusRunning {
		t.Fatalf("%+v", in)
	}
}

// Fix round 2, Important #1: Schedule не брала s.work, поэтому конкурентный
// Put() мог прийти посреди checkOne (между его Get и записью итога) и
// подменить строку под уже прочитанным, устаревшим `in`: серия набежала бы
// на новую строку с чужой отметкой старта, счётчик попыток пересчитался бы
// от чужого числа, а TargetVersion в движок ушла бы старая. Здесь checkOne
// намеренно застревает внутри опроса (канал releaseProbe), пока идёт
// Schedule -- и мы проверяем, что Schedule ДОЖИДАЕТСЯ своей очереди (не
// пролезает мимо s.work): checkOne держит замок непрерывно от своего Get и
// до MarkRunning включительно (launch, Fix round 1, Important #1, отпускает
// его только ПОСЛЕ MarkRunning, а не раньше).
//
// Fix round 1, Important #1 (мандатное ревью) поменял, ЧТО именно видит
// Schedule дальше: раньше s.work держался на весь launch (включая поход
// Engine.Launch в сеть и запись итога), и Schedule был вынужден ждать, пока
// вся попытка разрешится -- отсюда и старое имя теста. Теперь замок
// отпускается сразу после MarkRunning, и Schedule, заставший саму попытку
// (Launch ещё не вернулся -- он намеренно заблокирован ниже), обязана
// честно увидеть "running" и получить ErrRunning -- это не порча данных, а
// точный ответ "дождитесь итога". Как только попытка разрешится (здесь --
// неудачей, 5-я из 5 -- переходит в failed, терминальный статус), повторный
// Schedule обязан пройти по уже свободной строке, полностью свежей: без
// унаследованных серии/попыток/версии и с новым, не стёртым секретом.
func TestSchedule_DuringConcurrentCheckOne_WaitsAndDoesNotInheritStaleState(t *testing.T) {
	env := newEnv(t)
	env.seedWaiting(t)
	// Симулируем "предыдущий, ещё не завершённый цикл" намерения: попытка 4
	// (одна до исчерпания при MaxAttempts=5), серия уже была 1, версия
	// закреплена прошлой постановкой. reachable_since -- достаточно давно,
	// чтобы ОДИН опрос в этом Tick сразу набрал и счёт, и ConfirmGap.
	since := env.clock.Now().Add(-DefaultConfirmGap - time.Second)
	if _, err := env.db.SQL().Exec(
		`UPDATE revive_intents SET attempts = 4, reachable_probes = 1, reachable_since = ?, target_version = 'v-old' WHERE user_id = ?`,
		since.UTC().Format(time.RFC3339Nano), env.router,
	); err != nil {
		t.Fatal(err)
	}
	launchBlock := make(chan struct{})
	launchEntered := make(chan struct{})
	env.engine.set(func(f *fakeEngine) {
		f.launchErr = &LaunchError{Text: "временная неудача для теста гонки"}
		f.launchBlock = launchBlock
		f.launchEntered = launchEntered
	})

	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	var closeProbeEntered sync.Once
	probeCalls := 0
	env.probe.mu.Lock()
	env.probe.delegate = func(context.Context, string) string {
		// closeProbeEntered.Do: после гонки Schedule сама успешно отработает
		// и запустит confirmSoon, который снова зовёт этот же делегат --
		// probeEntered закрывать нужно только один раз, а <-releaseProbe на
		// уже закрытом канале и так не блокирует. Вызовы Probe уже
		// сериализованы через s.work (его держит либо checkOne, либо
		// confirmSoon), поэтому счётчик без мьютекса безопасен. Только
		// ПЕРВЫЙ (застрявший) опрос -- содержательный, "reachable" для
		// СТАРОЙ строки; всё, что confirmSoon спросит уже про СВЕЖУЮ строку
		// после Schedule, получает "offline" и не должно её трогать.
		closeProbeEntered.Do(func() { close(probeEntered) })
		<-releaseProbe
		probeCalls++
		if probeCalls == 1 {
			return awgmstate.Reachable
		}
		return awgmstate.Offline
	}
	env.probe.mu.Unlock()

	tickDone := make(chan struct{})
	go func() {
		env.svc.Tick(context.Background())
		close(tickDone)
	}()
	<-probeEntered // checkOne держит s.work, застряв внутри опроса

	scheduleDone := make(chan struct{})
	var scheduleErr error
	go func() {
		_, scheduleErr = env.svc.Schedule(context.Background(), env.router, ScheduleRequest{
			RootPassword: "New-Root-After-Reschedule", RequestedBy: 77, ExpiresDays: 10,
		})
		close(scheduleDone)
	}()

	select {
	case <-scheduleDone:
		t.Fatal("Schedule прошёл, не дождавшись, пока checkOne освободит s.work")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseProbe) // checkOne дописывает итог по СВОЕЙ, ещё старой строке, MarkRunning фиксирует "running"

	select {
	case <-launchEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("launch не дошёл до Engine.Launch")
	}

	<-scheduleDone // put() уже мог взять освободившийся s.work и прочитать статус
	if !errors.Is(scheduleErr, ErrRunning) {
		t.Fatalf("Schedule, заставший саму попытку (Launch ещё не вернулся) -- ErrRunning, получили: %v", scheduleErr)
	}

	close(launchBlock) // отпускаем застрявший Launch: 5-я попытка из 5 -> finish(failed)
	<-tickDone

	// failed -- терминальный статус: как и waiting/cancelled/expired, ставить
	// поверх него можно. Второй Schedule идёт по уже разрешившейся строке.
	if _, err := env.svc.Schedule(context.Background(), env.router, ScheduleRequest{
		RootPassword: "New-Root-After-Reschedule", RequestedBy: 77, ExpiresDays: 10,
	}); err != nil {
		t.Fatalf("Schedule после разрешения попытки: %v", err)
	}
	env.svc.Wait() // дожидаемся confirmSoon, запущенного этим Schedule

	in := env.intent(t)
	if in.Status != StatusWaiting || in.Attempts != 0 || in.ReachableProbes != 0 || in.TargetVersion != "" {
		t.Fatalf("постановка после гонки обязана быть чистой, без унаследованных серии/попыток/версии: %+v", in)
	}
	nonce, ct, ok, err := env.db.Revive().Secret(env.router)
	if err != nil || !ok {
		t.Fatalf("секрет после гонки: %v %v", ok, err)
	}
	box, err := NewBox(env.key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Open(env.router, nonce, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(NewSecrets("New-Root-After-Reschedule", "", "", "")) {
		t.Fatal("после гонки секрет обязан быть новым, а не стёрт неудачным закрытием старой попытки")
	}
}

// Fix round 1, Important #1 (мандатное ревью): s.work обязан отпускаться
// сразу после MarkRunning, ДО похода Engine.Launch в сеть (в проде -- до 30 с
// на версию/чексуммы GitHub) -- иначе Schedule для ДРУГОГО роутера ждал бы
// чужого запуска, а KeenDNS обрывает HTTP на 15 с. Здесь Launch застревает
// нарочно (fakeEngine.launchBlock), и тест проверяет, что Schedule и на
// ТОТ ЖЕ роутер (обязан отказать ErrRunning -- статус уже "running", замок
// тут ни при чём), и на ДРУГОЙ роутер проходят быстро, не дожидаясь, пока
// застрявший Launch вернётся.
func TestLaunch_ReleasesWorkLockDuringEngineLaunch(t *testing.T) {
	env := newEnv(t)
	env.probe.script(awgmstate.Reachable)
	env.seedWaiting(t)

	block := make(chan struct{})
	entered := make(chan struct{})
	env.engine.set(func(f *fakeEngine) { f.launchBlock = block; f.launchEntered = entered })

	env.svc.Tick(context.Background()) // серия 1, без запуска
	env.clock.Advance(DefaultConfirmGap)

	tickDone := make(chan struct{})
	go func() {
		env.svc.Tick(context.Background()) // серия 2 -> launch, застревает в движке
		close(tickDone)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Launch не вызвался за разумное время")
	}
	// Строка уже "running" (MarkRunning прошёл до закрытия entered) -- дальше
	// опрашивать "reachable" незачем: без этого второй роутер, случайно
	// подхваченный собственным confirmSoon, попытался бы запуститься тоже и
	// столкнулся бы с уже закрытым launchEntered.
	env.probe.script(awgmstate.Offline)

	done2 := make(chan struct{})
	var err2 error
	go func() {
		_, err2 = env.svc.Schedule(context.Background(), env.router, fixtureRequest())
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("Schedule на тот же роутер ждёт замка, который держит чужой Launch")
	}
	if !errors.Is(err2, ErrRunning) {
		t.Fatalf("Schedule на тот же роутер: err=%v, want ErrRunning", err2)
	}

	id2, err := env.db.Users().Insert("gachi", "tok-gachi", "198.51.100.21", "awg1")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := env.db.Users().SetAWGMURLIfEmpty(id2, "https://awg2.example.com"); err != nil || !ok {
		t.Fatalf("awgm_url: %v %v", ok, err)
	}
	done3 := make(chan struct{})
	var err3 error
	go func() {
		_, err3 = env.svc.Schedule(context.Background(), id2, fixtureRequest())
		close(done3)
	}()
	select {
	case <-done3:
	case <-time.After(2 * time.Second):
		t.Fatal("Schedule на другой роутер ждёт замка, который держит чужой Launch")
	}
	if err3 != nil {
		t.Fatalf("Schedule на другой роутер: %v", err3)
	}

	close(block) // отпускаем застрявший Launch, чтобы горутина не висела
	<-tickDone
}

// Fix round 1, Minor #3 (мандатное ревью): движок переустановки занят чужим
// заданием на этом же роутере (LaunchError.NoAttempt -- дашборд уже чинит
// или ставит) -- не вина оживления, тратить на это одну из пяти попыток
// нечестно. Запуск возвращается в waiting БЕЗ инкремента attempts.
func TestLaunch_NoAttemptLaunchErrorDoesNotConsumeAttempt(t *testing.T) {
	env := newEnv(t)
	env.panel(t, http.StatusOK)
	env.seedWaiting(t)
	env.engine.set(func(f *fakeEngine) {
		f.launchErr = &LaunchError{NoAttempt: true, Text: "на роутере уже идёт другая установка или ремонт"}
	})

	env.tick(t)
	env.tick(t) // запуск -> движок занят чужим заданием

	in := env.intent(t)
	if in.Status != StatusWaiting {
		t.Fatalf("возврат в ожидание: %+v", in)
	}
	if in.Attempts != 0 {
		t.Fatalf("попытка не должна была потратиться: %+v", in)
	}
	if in.LastError != "на роутере уже идёт другая установка или ремонт" {
		t.Fatalf("причина: %+v", in)
	}
	if !env.hasSecret(t) {
		t.Fatal("секрет остаётся -- попытка не окончательная")
	}

	// Повторные обходы могут пытаться сколько угодно раз -- ни один не
	// исчерпывает лимит, пока движок занят чужим.
	for i := 0; i < 10; i++ {
		env.tick(t)
	}
	in = env.intent(t)
	if in.Status != StatusWaiting || in.Attempts != 0 {
		t.Fatalf("после десяти обходов всё ещё не потрачено ни одной попытки: %+v", in)
	}
}
