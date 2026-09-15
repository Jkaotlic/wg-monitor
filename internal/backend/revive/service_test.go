package revive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
)

func TestNilService_IsDisabled(t *testing.T) {
	var s *Service
	if s.Enabled() {
		t.Fatal("nil -- выключено")
	}
	if _, err := s.Schedule(context.Background(), 1, fixtureRequest()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("schedule: %v", err)
	}
	if _, err := s.Cancel(context.Background(), 1); !errors.Is(err, ErrDisabled) {
		t.Fatalf("cancel: %v", err)
	}
	if v, err := s.StatusFor(1); v != nil || err != nil {
		t.Fatalf("status: %+v %v", v, err)
	}
	var re *Error
	if !errors.As(error(ErrDisabled), &re) || re.Code != "revive_disabled" {
		t.Fatalf("код: %+v", re)
	}
}

func TestNew_RejectsBadKey(t *testing.T) {
	env := newEnv(t)
	if _, err := New(Config{DB: env.db, Engine: env.engine, Key: make([]byte, 16)}); err == nil {
		t.Fatal("ключ неверной длины -- функция выключена")
	}
}

func TestErrorCodes(t *testing.T) {
	want := map[*Error]string{
		ErrDisabled: "revive_disabled", ErrNoAWGMURL: "no_awgm_url", ErrURLAlreadySet: "awgm_url_already_set",
		ErrNoCredentials: "no_credentials", ErrAgentAlive: "agent_alive", ErrInvalidURL: "invalid_awgm_url",
		ErrRunning: "revive_running", ErrRouterNotFound: "router_not_found",
	}
	for e, code := range want {
		if e.Code != code || e.Status == 0 || e.Message == "" {
			t.Fatalf("%+v, want code %s", e, code)
		}
	}
}

func TestSchedule_StoresEncryptedSecretAndWaits(t *testing.T) {
	env := newEnv(t)
	req := fixtureRequest()
	req.RootPassword = "  " + fixtureRoot + "  " // пробелы по краям root-пароля срезаются, как в дашборде
	got, err := env.svc.Schedule(context.Background(), env.router, req)
	if err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	if got.Status != StatusWaiting || got.RequestedBy != 42 || !got.ExpiresAt.Equal(testT0.Add(30*24*time.Hour)) {
		t.Fatalf("намерение: %+v", got)
	}
	nonce, ct, ok, err := env.db.Revive().Secret(env.router)
	if err != nil || !ok {
		t.Fatalf("секрет: %v %v", ok, err)
	}
	box, _ := NewBox(env.key)
	creds, err := box.Open(env.router, nonce, ct)
	if err != nil || !creds.Equal(fixtureSecrets()) {
		t.Fatalf("расшифровка: %v", err)
	}
	assertNoFixtureSecret(t, "revive_intents", intentRowsDump(t, env.db))
	assertNoFixtureSecret(t, "файлы базы", rawDBFiles(t, env.db))
	assertNoFixtureSecret(t, "журнал", env.logs.Bytes())
	js, _ := json.Marshal(got)
	assertNoFixtureSecret(t, "ответ Schedule", js)
}

// Базы без ключа недостаточно: сервис с другим ключом на той же базе не
// открывает секрет.
func TestSchedule_DatabaseAloneCannotDecrypt(t *testing.T) {
	env := newEnv(t)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	nonce, ct, _, _ := env.db.Revive().Secret(env.router)
	other, _ := NewBox(randomKey(t))
	if _, err := other.Open(env.router, nonce, ct); !errors.Is(err, ErrSecretUnreadable) {
		t.Fatalf("чужой ключ открыл секрет: %v", err)
	}
}

func TestSchedule_ExpiresDays(t *testing.T) {
	for _, c := range []struct {
		days int
		want time.Duration
	}{
		{7, 7 * 24 * time.Hour}, {0, 30 * 24 * time.Hour}, {-3, 30 * 24 * time.Hour}, {90, 30 * 24 * time.Hour},
		// Границы диапазона 1..30 (fix round 1, #3): 1 -- минимум как есть, 30 --
		// максимум как есть, не путать с умолчанием.
		{1, 24 * time.Hour}, {30, 30 * 24 * time.Hour},
	} {
		env := newEnv(t)
		req := fixtureRequest()
		req.ExpiresDays = c.days
		got, err := env.svc.Schedule(context.Background(), env.router, req)
		if err != nil {
			t.Fatal(err)
		}
		if !got.ExpiresAt.Equal(testT0.Add(c.want)) {
			t.Fatalf("days=%d: expires %v", c.days, got.ExpiresAt)
		}
	}
}

func TestSchedule_Errors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, env *testEnv, req *ScheduleRequest)
		want  *Error
	}{
		{"нет учётных данных", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			*req = ScheduleRequest{RootPassword: "   "}
		}, ErrNoCredentials},
		{"логин без пароля", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			*req = ScheduleRequest{AWGMLogin: "admin"}
		}, ErrNoCredentials},
		// Пре-флайт 15.09: вход в панель без root-пароля движок не примет.
		{"панель без root", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			*req = ScheduleRequest{AWGMLogin: "admin", AWGMPassword: "p", AWGMAPIKey: "k"}
		}, ErrNoCredentials},
		{"нет адреса нигде", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			env.clearAWGMURL(t)
		}, ErrNoAWGMURL},
		{"адрес уже есть", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			req.AWGMURL = "https://other.example.com"
		}, ErrURLAlreadySet},
		{"кривой адрес", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			env.clearAWGMURL(t)
			req.AWGMURL = "ftp://awg.example.com"
		}, ErrInvalidURL},
		{"адрес с паролем внутри", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			env.clearAWGMURL(t)
			req.AWGMURL = "https://admin:pw@example.com"
		}, ErrInvalidURL},
		{"агент на связи", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			env.setLastSeen(t, testT0.Add(-9*time.Minute))
		}, ErrAgentAlive},
		{"уже идёт", func(t *testing.T, env *testEnv, req *ScheduleRequest) {
			if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
				t.Fatal(err)
			}
			env.svc.Wait()
			if ok, _ := env.db.Revive().MarkRunning(env.router, testT0); !ok {
				t.Fatal("mark running")
			}
		}, ErrRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newEnv(t)
			req := fixtureRequest()
			c.setup(t, env, &req)
			before := env.intent(t)
			_, err := env.svc.Schedule(context.Background(), env.router, req)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			after := env.intent(t)
			if (before == nil) != (after == nil) || (after != nil && after.Status != before.Status) {
				t.Fatalf("отказ не имеет права менять намерение: %+v -> %+v", before, after)
			}
		})
	}

	t.Run("нет роутера", func(t *testing.T) {
		env := newEnv(t)
		if _, err := env.svc.Schedule(context.Background(), env.router+100, fixtureRequest()); !errors.Is(err, ErrRouterNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSchedule_AgentSilentLongEnoughIsAllowed(t *testing.T) {
	env := newEnv(t)
	env.setLastSeen(t, testT0.Add(-11*time.Minute))
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatalf("агент молчит 11 минут -- ставить можно: %v", err)
	}
}

func TestSchedule_SetsURLForRouterWithout(t *testing.T) {
	env := newEnv(t)
	env.clearAWGMURL(t)
	req := fixtureRequest()
	req.AWGMURL = " https://awg.example.com/ "
	if _, err := env.svc.Schedule(context.Background(), env.router, req); err != nil {
		t.Fatal(err)
	}
	u, _ := env.db.Users().GetByID(env.router)
	if u.AWGMURL == nil || *u.AWGMURL != "https://awg.example.com" {
		t.Fatalf("адрес панели: %v", u.AWGMURL)
	}
}

// Fix round 1, #2: если Put не может записать секрет (DB-ошибка), адрес
// панели не должен всё равно осесть в users -- иначе повтор постановки с тем
// же адресом упирается в awgm_url_already_set без единого намерения в базе.
func TestSchedule_URLNotCommittedIfPutFails(t *testing.T) {
	env := newEnv(t)
	env.clearAWGMURL(t)
	if _, err := env.db.SQL().Exec(`DROP TABLE revive_secrets`); err != nil {
		t.Fatal(err)
	}
	req := fixtureRequest()
	req.AWGMURL = "https://awg.example.com"
	if _, err := env.svc.Schedule(context.Background(), env.router, req); err == nil {
		t.Fatal("ожидали ошибку постановки -- Put не может записать секрет без таблицы")
	}
	u, err := env.db.Users().GetByID(env.router)
	if err != nil {
		t.Fatal(err)
	}
	if u.AWGMURL != nil {
		t.Fatalf("адрес панели не должен сохраниться при неудачном Put: %v", *u.AWGMURL)
	}
}

// Fix round 1, #3: постановка поверх waiting-намерения (после неудачной
// попытки воркера) заменяет секрет и сбрасывает счётчик попыток -- старые
// учётные данные не должны продолжать жить в базе.
func TestSchedule_ReplacesWaitingIntent(t *testing.T) {
	env := newEnv(t)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	// Имитация неудачной попытки воркера: waiting -> running -> waiting,
	// attempts становится 1.
	if ok, err := env.db.Revive().MarkRunning(env.router, testT0); !ok || err != nil {
		t.Fatalf("mark running: %v %v", ok, err)
	}
	if ok, err := env.db.Revive().BackToWaiting(env.router, "первая попытка не удалась", testT0); !ok || err != nil {
		t.Fatalf("back to waiting: %v %v", ok, err)
	}
	if in := env.intent(t); in.Attempts != 1 || in.Status != StatusWaiting {
		t.Fatalf("предусловие сломано: %+v", in)
	}

	other := ScheduleRequest{RootPassword: "other-root-pw", AWGMLogin: "other-login", AWGMPassword: "other-panel-pw", AWGMAPIKey: "other-api-key", RequestedBy: 99}
	got, err := env.svc.Schedule(context.Background(), env.router, other)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 0 || got.RequestedBy != 99 || got.Status != StatusWaiting {
		t.Fatalf("переустановка намерения не сбросила состояние: %+v", got)
	}

	nonce, ct, ok, err := env.db.Revive().Secret(env.router)
	if err != nil || !ok {
		t.Fatalf("секрет: %v %v", ok, err)
	}
	box, _ := NewBox(env.key)
	creds, err := box.Open(env.router, nonce, ct)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Equal(fixtureSecrets()) {
		t.Fatal("старый секрет всё ещё расшифровывается -- не заменён новым")
	}
	if !creds.Equal(NewSecrets("other-root-pw", "other-login", "other-panel-pw", "other-api-key")) {
		t.Fatalf("новый секрет не совпадает с тем, что передали: %+v", creds)
	}
}

// Постановка поверх завершённого (failed) намерения -- то же самое: новый
// секрет, попытки с нуля. Finish уже стёр старый секрет, так что это
// одновременно проверка, что Put справляется с "секрета нет вовсе".
func TestSchedule_ReplacesFailedIntent(t *testing.T) {
	env := newEnv(t)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	if ok, err := env.db.Revive().MarkRunning(env.router, testT0); !ok || err != nil {
		t.Fatalf("mark running: %v %v", ok, err)
	}
	if ok, err := env.db.Revive().Finish(env.router, []string{StatusRunning}, StatusFailed, "не дозвонились", testT0); !ok || err != nil {
		t.Fatalf("finish -> failed: %v %v", ok, err)
	}
	if env.hasSecret(t) {
		t.Fatal("предусловие: Finish обязан стереть секрет")
	}

	got, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusWaiting || got.Attempts != 0 {
		t.Fatalf("постановка поверх failed: %+v", got)
	}
	if !env.hasSecret(t) {
		t.Fatal("новый секрет обязан появиться")
	}
}

func TestCancel_WipesSecret(t *testing.T) {
	env := newEnv(t)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	ok, err := env.svc.Cancel(context.Background(), env.router)
	if err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if in := env.intent(t); in.Status != StatusCancelled {
		t.Fatalf("%+v", in)
	}
	if env.hasSecret(t) {
		t.Fatal("после отмены секрет обязан быть стёрт")
	}
	if ok, err := env.svc.Cancel(context.Background(), env.router); ok || err != nil {
		t.Fatalf("повторная отмена: %v %v", ok, err)
	}
	if len(env.notifier.all()) != 0 {
		t.Fatal("отмена человеком -- без уведомления")
	}
}

// Cancel не отменяет идущую переустановку (пре-флайт 15.09): прервать её на
// полпути опаснее, чем дождаться итога.
func TestCancel_RunningRefused(t *testing.T) {
	env := newEnv(t)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	if ok, err := env.db.Revive().MarkRunning(env.router, testT0); !ok || err != nil {
		t.Fatalf("mark running: %v %v", ok, err)
	}
	ok, err := env.svc.Cancel(context.Background(), env.router)
	if ok || !errors.Is(err, ErrRunning) {
		t.Fatalf("cancel во время running: ok=%v err=%v, want ErrRunning", ok, err)
	}
	if in := env.intent(t); in.Status != StatusRunning {
		t.Fatalf("отказ отмены не имеет права трогать статус: %+v", in)
	}
}

// Fix round 1, #1: гонка между Cancel's Get (видит waiting) и Finish
// (воркер успевает перевести в running первым) не должна ни терять jobID из
// карты sync-состояния, ни возвращать (false, nil) вместо ErrRunning.
func TestCancel_RaceToRunningReturnsErrRunning(t *testing.T) {
	env := newEnv(t)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	env.svc.setJob(env.router, "job-in-flight")
	env.svc.testBeforeCancelFinish = func() {
		if ok, err := env.db.Revive().MarkRunning(env.router, testT0); !ok || err != nil {
			t.Fatalf("гонка: mark running: %v %v", ok, err)
		}
	}

	ok, err := env.svc.Cancel(context.Background(), env.router)
	if ok || !errors.Is(err, ErrRunning) {
		t.Fatalf("cancel в гонке waiting->running: ok=%v err=%v, want ErrRunning", ok, err)
	}
	if in := env.intent(t); in.Status != StatusRunning {
		t.Fatalf("гонка не имеет права терять running: %+v", in)
	}
	if id, ok := env.svc.job(env.router); !ok || id != "job-in-flight" {
		t.Fatalf("jobID потерян гонкой: id=%q ok=%v", id, ok)
	}
}

// Fix round 1, #1: ошибка Get не должна тихо проглатываться -- раньше при
// err != nil код проверки running просто пропускал её и шёл к Finish как
// если бы намерения не было вовсе.
func TestCancel_PropagatesGetError(t *testing.T) {
	env := newEnv(t)
	if _, err := env.db.SQL().Exec(`DROP TABLE revive_intents`); err != nil {
		t.Fatal(err)
	}
	if ok, err := env.svc.Cancel(context.Background(), env.router); ok || err == nil {
		t.Fatalf("cancel обязан вернуть ошибку Get, а не (false,nil): ok=%v err=%v", ok, err)
	}
}

func TestStatusFor_RussianTextsWithoutSecrets(t *testing.T) {
	env := newEnv(t)
	if v, err := env.svc.StatusFor(env.router); v != nil || err != nil {
		t.Fatalf("без намерения: %+v %v", v, err)
	}
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	probeAt := testT0.Add(time.Minute)
	if err := env.db.Revive().RecordProbe(env.router, probeAt, awgmstate.Offline, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	v, err := env.svc.StatusFor(env.router)
	if err != nil || v == nil {
		t.Fatalf("%+v %v", v, err)
	}
	if v.Status != StatusWaiting || v.LastProbeText != "роутер не отвечает" || !v.LastProbeAt.Equal(probeAt) || v.Attempts != 0 {
		t.Fatalf("view: %+v", v)
	}
	js, _ := json.Marshal(v)
	assertNoFixtureSecret(t, "IntentView", js)
	for _, key := range []string{`"status"`, `"expires_at"`, `"attempts"`, `"last_error_text"`, `"last_probe_text"`, `"last_probe_at"`} {
		if !json.Valid(js) || !bytes.Contains(js, []byte(key)) {
			t.Fatalf("в JSON нет %s: %s", key, js)
		}
	}
}

// StatusFor обязан отдавать и завершённые статусы (пре-флайт 15.09): экран
// «Парк» показывает done/failed/expired/cancelled, а не только waiting/running.
func TestStatusFor_ReturnsFinishedStatuses(t *testing.T) {
	env := newEnv(t)
	if _, err := env.svc.Schedule(context.Background(), env.router, fixtureRequest()); err != nil {
		t.Fatal(err)
	}
	env.svc.Wait()
	if ok, err := env.svc.Cancel(context.Background(), env.router); !ok || err != nil {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	v, err := env.svc.StatusFor(env.router)
	if err != nil || v == nil {
		t.Fatalf("%+v %v", v, err)
	}
	if v.Status != StatusCancelled {
		t.Fatalf("view: %+v, want cancelled", v)
	}
}

func TestProbeText(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		awgmstate.Reachable: "роутер отвечает",
		awgmstate.Offline:   "роутер не отвечает",
		awgmstate.TLSError:  "сертификат панели роутера не подошёл",
		awgmstate.DNSError:  "адрес панели роутера не находится",
		awgmstate.AuthError: "панель роутера отказала во входе",
		"что-то новое":      "роутер не отвечает",
	}
	for state, want := range cases {
		if got := probeText(state); got != want {
			t.Fatalf("probeText(%q) = %q, want %q", state, got, want)
		}
	}
}
