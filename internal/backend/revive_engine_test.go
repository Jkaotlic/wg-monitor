package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

const (
	reviveFixtureRoot   = "Fixture-Root-9d1e"
	reviveFixtureLogin  = "fixture-admin-4c2b"
	reviveFixturePanel  = "Fixture-Panel-77aa"
	reviveFixtureAPIKey = "fixture-apikey-51f0"
)

func assertNoReviveFixture(t *testing.T, where string, got []byte) {
	t.Helper()
	for _, s := range []string{reviveFixtureRoot, reviveFixtureLogin, reviveFixturePanel, reviveFixtureAPIKey} {
		if bytes.Contains(got, []byte(s)) {
			t.Fatalf("%s содержит секрет фикстуры %q", where, s)
		}
	}
}

var reviveLatin = regexp.MustCompile(`[A-Za-z]`)

func TestReviveEngine_OutcomeMapping(t *testing.T) {
	store := provision.NewStore()
	e := NewReviveEngine(ReviveEngineDeps{Provision: provision.Deps{Store: store}})

	mk := func(state provision.JobState, hint, failedStep, version string) string {
		job := store.Create(provision.KindRepairReinstall, "bronya", provision.Template(provision.KindRepairReinstall))
		store.Update(job.ID, func(j *provision.Job) {
			j.State, j.Hint, j.Version = state, hint, version
			for i := range j.Steps {
				if j.Steps[i].Name == failedStep {
					j.Steps[i].Status = provision.StepFailed
				}
			}
		})
		return job.ID
	}

	if _, ok := e.Outcome("нет-такого"); ok {
		t.Fatal("неизвестное задание -- ok=false")
	}
	if out, ok := e.Outcome(mk(provision.StateRunning, "", "", "")); !ok || out.Finished {
		t.Fatalf("идущее: %+v %v", out, ok)
	}
	if out, _ := e.Outcome(mk(provision.StateSuccess, "", "", "v0.34.0")); !out.Finished || !out.Success || out.Version != "v0.34.0" {
		t.Fatalf("успех: %+v", out)
	}
	if out, _ := e.Outcome(mk(provision.StateFailed, provision.HintAuthFailed, provision.StepTerminalConnected, "")); !out.AuthFailed {
		t.Fatalf("ошибка входа: %+v", out)
	}
	cases := map[string]string{
		provision.StepTerminalConnected: "не удалось подключиться к панели роутера",
		provision.StepDownloading:       "роутер не смог скачать агент",
		provision.StepChecksumOK:        "скачанный агент не прошёл проверку подписи",
		provision.StepServiceStarted:    "агент поставлен, но не запустился",
		provision.StepVerifyOnline:      "агент поставлен, но не вышел на связь",
	}
	for step, want := range cases {
		out, _ := e.Outcome(mk(provision.StateFailed, "что-то пошло не так", step, ""))
		if !out.Finished || out.Success || out.AuthFailed || out.Text != want {
			t.Fatalf("шаг %s: %+v, want %q", step, out, want)
		}
		if reviveLatin.MatchString(out.Text) {
			t.Fatalf("латиница в причине: %q", out.Text)
		}
	}
}

// Carry #2 (мандатное ревью): ошибка входа узнаётся ТОЛЬКО по job.Hint ==
// provision.HintAuthFailed -- не по вхождению "401"/"403" в любой другой
// текст задания (runner.go делает такое сравнение подстрокой для СВОЕГО
// hintFor, но revive_engine на это не имеет права опираться: посторонняя
// ошибка со случайными цифрами 401 внутри не должна выглядеть как "пароль не
// подошёл" и стирать секрет раньше времени).
func TestReviveEngine_OutcomeMapping_401InTextIsNotAuthFailure(t *testing.T) {
	store := provision.NewStore()
	e := NewReviveEngine(ReviveEngineDeps{Provision: provision.Deps{Store: store}})

	job := store.Create(provision.KindRepairReinstall, "bronya", provision.Template(provision.KindRepairReinstall))
	store.Update(job.ID, func(j *provision.Job) {
		j.State = provision.StateFailed
		// Hint нарочно НЕ HintAuthFailed, но текст содержит "401" -- если бы
		// код когда-нибудь начал match'ить подстроку, тест бы это поймал.
		j.Hint = "relay exited with HTTP 401 while fetching an unrelated diagnostics endpoint"
		for i := range j.Steps {
			if j.Steps[i].Name == provision.StepDownloading {
				j.Steps[i].Status = provision.StepFailed
			}
		}
	})

	out, ok := e.Outcome(job.ID)
	if !ok {
		t.Fatal("задание должно быть известно")
	}
	if out.AuthFailed {
		t.Fatalf("вхождение \"401\" в постороннем тексте не должно значить AuthFailed: %+v", out)
	}
	if out.Text != "роутер не смог скачать агент" {
		t.Fatalf("причина должна идти по имени упавшего шага, а не по тексту hint: %q", out.Text)
	}
}

// Carry #3 (мандатное ревью): config_written сам по себе -- не успех. Успех
// -- это provision.StateSuccess, который сам движок выставляет только ПОСЛЕ
// того, как VerifyOnline подтвердил свежий отчёт агента (runner.go). Пока
// задание идёт (State всё ещё running, даже если шаг config_written уже
// done, а verify_online ещё активен), Outcome обязан отвечать
// Finished=false -- иначе оживление сочло бы роутер ожившим до того, как
// агент реально вышел на связь.
func TestReviveEngine_OutcomeMapping_ConfigWrittenAloneIsNotSuccess(t *testing.T) {
	store := provision.NewStore()
	e := NewReviveEngine(ReviveEngineDeps{Provision: provision.Deps{Store: store}})

	job := store.Create(provision.KindRepairReinstall, "bronya", provision.Template(provision.KindRepairReinstall))
	store.Update(job.ID, func(j *provision.Job) {
		j.State = provision.StateRunning // движок ещё не отчитался об успехе
		for i := range j.Steps {
			switch j.Steps[i].Name {
			case provision.StepConfigWritten, provision.StepInitInstalled, provision.StepServiceStarted:
				j.Steps[i].Status = provision.StepDone
			case provision.StepVerifyOnline:
				j.Steps[i].Status = provision.StepActive // ждём свежего отчёта
			}
		}
	})

	out, ok := e.Outcome(job.ID)
	if !ok {
		t.Fatal("задание должно быть известно")
	}
	if out.Finished || out.Success {
		t.Fatalf("config_written без подтверждённого отчёта -- ещё не итог: %+v", out)
	}
}

func TestReviveLaunchError_Mapping(t *testing.T) {
	cases := []struct {
		code      string
		permanent bool
	}{
		{"no_awgm_url", true}, {"downgrade_rejected", true}, {"no_public_base_url", true},
		{"provision_not_configured", true}, {"db_not_configured", true}, {"invalid_nickname", true}, {"invalid_kind", true},
		{"already_running", false}, {"provision_already_running", false},
		{"latest_version_failed", false}, {"checksums_failed", false}, {"internal_error", false},
	}
	for _, c := range cases {
		le := reviveLaunchError(&repairStartError{Code: c.code, Message: "English detail " + reviveFixtureRoot})
		if le.Permanent != c.permanent || le.Text == "" {
			t.Fatalf("%s: %+v", c.code, le)
		}
		if reviveLatin.MatchString(le.Text) {
			t.Fatalf("%s: латиница или сырой текст в причине: %q", c.code, le.Text)
		}
		assertNoReviveFixture(t, "причина запуска", []byte(le.Text))
	}
}

type reviveSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *reviveSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *reviveSyncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

type reviveNotices struct {
	mu    sync.Mutex
	texts []string
}

func (n *reviveNotices) Send(_ context.Context, _ int64, text, _ string) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.texts = append(n.texts, text)
	return 1, nil
}

func (n *reviveNotices) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.texts...)
}

// Сквозной сторож: постановка -> проверка -> запуск настоящего движка с
// поддельным relay -> итог. Пароли фикстуры доходят до задания для терминала
// и не появляются ни в журнале, ни в уведомлениях, ни в состоянии для экрана,
// ни в строках revive_intents, ни в сырых файлах базы.
func TestRevive_EndToEndNeverLeaksSecrets(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Users().UpsertEnrollment("bronya", "tok-bronya-000000000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo("bronya", db.DeployInfo{AWGMURL: "https://awg.example.com"}); err != nil {
		t.Fatal(err)
	}
	u, _ := database.Users().GetByNickname("bronya")

	stubLatestVersion(t, "v0.34.0")
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "cafebabe"})

	logs := &reviveSyncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	relay := &fakeProvisionRelay{rc: 0, lines: []string{"__WG_STEP__ config_written"}}
	store := provision.NewStore()
	provDeps := provision.Deps{Store: store, BaseCtx: context.Background(), Relay: relay.run, LastSeen: freshLastSeen, Logger: logger}
	notices := &reviveNotices{}
	key := bytes.Repeat([]byte{7}, revive.KeySize)

	// Отвязанные часы: confirmSoon ждёт между первым и вторым опросом не
	// меньше ConfirmGap (worker.go, "Fix round 2, Minor #5") -- настоящий
	// Sleep-стаб без хода часов эту гонку никогда бы не набрал. clock здесь
	// продвигается ровно на столько, на сколько Sleep попросили ждать --
	// как у fakeClock в internal/backend/revive/worker_test.go.
	var clockMu sync.Mutex
	clock := time.Now()
	nowFn := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	sleepFn := func(_ context.Context, d time.Duration) bool {
		clockMu.Lock()
		clock = clock.Add(d)
		clockMu.Unlock()
		return true
	}

	svc, err := revive.New(revive.Config{
		DB: database, Key: key,
		Engine: NewReviveEngine(ReviveEngineDeps{
			DB: database, Provision: provDeps, PublicBaseURL: "https://wgmon.example.com", PublicIP: "203.0.113.9", Logger: logger,
		}),
		Notifier: notices,
		Probe:    func(context.Context, string) string { return awgmstate.Reachable },
		Now:      nowFn,
		Sleep:    sleepFn,
		Logger:   logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	intent, err := svc.Schedule(context.Background(), u.ID, revive.ScheduleRequest{
		RootPassword: reviveFixtureRoot, AWGMLogin: reviveFixtureLogin, AWGMPassword: reviveFixturePanel,
		AWGMAPIKey: reviveFixtureAPIKey, RequestedBy: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()

	job, ok := store.LatestFor("bronya")
	if !ok {
		t.Fatal("задание переустановки не создано")
	}
	waitForProvisionTerminal(t, store, job.ID, 2*time.Second)
	// Секрет дошёл до задания для терминала -- значит, сторожу есть что ловить.
	// Проверяем после завершения задания: relay зовётся в горутине движка.
	if !bytes.Contains(relay.capturedJobJSON(), []byte(reviveFixtureRoot)) {
		t.Fatal("переустановка не получила root-пароль: сторож проверял бы пустоту")
	}

	view, _ := svc.StatusFor(u.ID)
	running, _ := json.Marshal(view)
	svc.Tick(context.Background())

	final, err := svc.StatusFor(u.ID)
	if err != nil || final == nil || final.Status != revive.StatusDone {
		t.Fatalf("итог: %+v %v", final, err)
	}
	if _, _, ok, _ := database.Revive().Secret(u.ID); ok {
		t.Fatal("после успеха секрет обязан быть стёрт")
	}
	if n := notices.all(); len(n) != 1 {
		t.Fatalf("уведомлений %d", len(n))
	}

	scheduled, _ := json.Marshal(intent)
	finalJSON, _ := json.Marshal(final)
	assertNoReviveFixture(t, "журнал", logs.Bytes())
	assertNoReviveFixture(t, "ответ Schedule", scheduled)
	assertNoReviveFixture(t, "состояние во время переустановки", running)
	assertNoReviveFixture(t, "итоговое состояние", finalJSON)
	for _, text := range notices.all() {
		assertNoReviveFixture(t, "уведомление", []byte(text))
	}
	assertNoReviveFixture(t, "строки revive_intents", reviveIntentRows(t, database))
	assertNoReviveFixture(t, "файлы базы", reviveRawDBFiles(t, database))
}

func reviveIntentRows(t *testing.T, d *db.DB) []byte {
	t.Helper()
	rows, err := d.SQL().Query(`SELECT * FROM revive_intents`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out bytes.Buffer
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			if b, ok := v.([]byte); ok {
				out.Write(b)
			} else {
				fmt.Fprintf(&out, "%v", v)
			}
			out.WriteByte('|')
		}
	}
	return out.Bytes()
}

func reviveRawDBFiles(t *testing.T, d *db.DB) []byte {
	t.Helper()
	var out []byte
	for _, p := range []string{d.Path(), d.Path() + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out = append(out, b...)
	}
	return out
}

func TestReviveEngine_LaunchUsesBackendVersionWhenTagged(t *testing.T) {
	orig := serverVersion
	t.Cleanup(func() { serverVersion = orig })

	relay := &fakeProvisionRelay{rc: 0}
	d, database := newReinstallCoreDeps(t, relay)
	u := seedReinstallRouter(t, database, "bronya", "https://awg.example.com", "")
	stubVerifiedChecksums(t, map[string]string{"a": "b"})
	e := NewReviveEngine(ReviveEngineDeps{DB: database, Provision: d.Provision, PublicBaseURL: d.PublicBaseURL, PublicIP: d.PublicIP})

	serverVersion = "v0.34.1"
	jobID, err := e.Launch(context.Background(), u.ID, revive.NewSecrets("x", "", "", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	job := waitForProvisionTerminal(t, d.Provision.Store, jobID, time.Second)
	if job.Version != "v0.34.1" {
		t.Fatalf("версия: %q", job.Version)
	}

	// Второй роутер: замок первого задания снимается отложенно, после смены
	// State, и повтор на том же имени мог бы поймать «уже идёт».
	serverVersion = "unknown"
	stubLatestVersion(t, "v0.34.2")
	u2 := seedReinstallRouter(t, database, "gachi", "https://awg2.example.com", "")
	jobID, err = e.Launch(context.Background(), u2.ID, revive.NewSecrets("x", "", "", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	if job := waitForProvisionTerminal(t, d.Provision.Store, jobID, time.Second); job.Version != "v0.34.2" {
		t.Fatalf("без тега у бэкенда -- последняя опубликованная: %q", job.Version)
	}

	if _, err := e.Launch(context.Background(), u.ID+100, revive.NewSecrets("x", "", "", ""), ""); err == nil {
		t.Fatal("нет роутера -- отказ")
	} else if le, ok := err.(*revive.LaunchError); !ok || !le.Permanent {
		t.Fatalf("нет роутера -- окончательный отказ: %v", err)
	}
}

// Carry #1 (мандатное ревью): Launch обязан вернуться быстро -- задание
// переустановки запускается фоново (startRepairReinstall форкает горутину
// внутри provision.Store.Start и возвращает jobID сразу же). Воркер держит
// s.work на всё время Launch (см. worker.go: checkOne -> launch ->
// Engine.Launch), и Schedule() ждёт того же замка -- если бы Launch блокировался
// на всю установку, постановка нового оживления зависала бы на те же минуты,
// а KeenDNS-реле обрубает HTTP на 15 секундах.
func TestReviveEngine_LaunchReturnsBeforeInstallFinishes(t *testing.T) {
	block := make(chan struct{})
	defer close(block) // не оставлять горутину relay висеть после теста
	relay := &fakeProvisionRelay{rc: 0, block: block}
	d, database := newReinstallCoreDeps(t, relay)
	u := seedReinstallRouter(t, database, "bronya", "https://awg.example.com", "")
	stubVerifiedChecksums(t, map[string]string{"a": "b"})
	e := NewReviveEngine(ReviveEngineDeps{DB: database, Provision: d.Provision, PublicBaseURL: d.PublicBaseURL, PublicIP: d.PublicIP})

	done := make(chan struct{})
	var jobID string
	var launchErr error
	go func() {
		jobID, launchErr = e.Launch(context.Background(), u.ID, revive.NewSecrets("x", "", "", ""), "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Launch не вернулось быстро -- держит замок на время установки, а relay ещё не отпущен")
	}
	if launchErr != nil {
		t.Fatalf("launchErr = %v", launchErr)
	}
	if jobID == "" {
		t.Fatal("нет jobID")
	}
}

// Carry #4 (мандатное ревью): Launch не имеет права умирать только потому,
// что вызывающий ctx (ctx воркера, который сам приходит из Run(ctx) --
// отменяется при остановке бэкенда) уже завершён. Сеть за версией и
// чексуммами -- на своём отдельном, отвязанном таймауте: гонка "процесс
// остановился ровно в момент запуска" не должна ронять уже начатую
// переустановку.
func TestReviveEngine_LaunchDetachesFromCallerCtx(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	d, database := newReinstallCoreDeps(t, relay)
	u := seedReinstallRouter(t, database, "bronya", "https://awg.example.com", "")
	stubVerifiedChecksums(t, map[string]string{"a": "b"})
	e := NewReviveEngine(ReviveEngineDeps{DB: database, Provision: d.Provision, PublicBaseURL: d.PublicBaseURL, PublicIP: d.PublicIP})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // имитация: вызывающий ctx уже отменён (остановка бэкенда)

	jobID, err := e.Launch(ctx, u.ID, revive.NewSecrets("x", "", "", ""), "")
	if err != nil {
		t.Fatalf("Launch не должен падать из-за отменённого вызывающего ctx: %v", err)
	}
	if jobID == "" {
		t.Fatal("нет jobID")
	}
}
