package linkrepair

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// countingCommander считает команды, ушедшие роутеру, и отвечает заранее
// заготовленным. Нужен, чтобы доказать главное свойство выключенного
// полуавтомата: роутер не трогают ВООБЩЕ.
type countingCommander struct {
	mu   sync.Mutex
	sent int
}

func (c *countingCommander) Enqueue(_ int64, cmd wire.Command) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent++
	return nil
}

func (c *countingCommander) AwaitResult(_ context.Context, _ int64, id string, _ time.Duration) (*wire.CommandResult, bool) {
	res := wire.CommandResult{ID: id, Status: "ok"}
	return &res, true
}

func (c *countingCommander) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent
}

type fakeOriginReader struct{}

func (fakeOriginReader) Get(int64, string) (string, string, bool) {
	return "amnezia", "nl", true
}

func testDeps(t *testing.T, cmd *countingCommander) Deps {
	t.Helper()
	return Deps{
		Store:      provision.NewStore(),
		Origin:     fakeOriginReader{},
		Attempts:   Attempts{KV: fakeKV{}},
		AutoRepair: func(int64) bool { return true },
		Commands:   cmd,
		Notify:     func(context.Context, int64, string) {},
		BaseCtx:    context.Background(),
		AwaitStep:  time.Second,
	}
}

// Резерв -- первый доступный интерфейс политики, который не упавшая линия и
// за которым стоит наш туннель. Список уже отсортирован по приоритету, так
// что «первый подходящий» и есть тот, кого подхватит политика.
func TestPickBackup(t *testing.T) {
	pol := wire.RoutePolicySummary{
		Name: "HydraRoute",
		Interfaces: []wire.RoutePolicyInterface{
			{Bind: "nwg1", TunnelID: "awg12", Role: "active", Available: false, Order: 1},
			{Bind: "nwg3", TunnelID: "awg13", Role: "unavailable", Available: false, Order: 2},
			{Bind: "eth3", TunnelID: "", Role: "fallback", Available: true, Order: 3},
			{Bind: "nwg2", TunnelID: "awg10", Role: "fallback", Available: true, Order: 4},
		},
	}
	got, ok := pickBackup(pol, "awg12")
	if !ok {
		t.Fatal("резерв обязан найтись")
	}
	if got != "awg10" {
		t.Fatalf("резерв = %q, хотим awg10: awg13 недоступен, eth3 -- не туннель", got)
	}
}

func TestPickBackup_NoneWhenAlone(t *testing.T) {
	pol := wire.RoutePolicySummary{
		Interfaces: []wire.RoutePolicyInterface{
			{Bind: "nwg1", TunnelID: "awg12", Role: "active", Available: false},
		},
	}
	if _, ok := pickBackup(pol, "awg12"); ok {
		t.Fatal("одна линия -- резерва нет, и это нормальный случай")
	}
}

// Выключенный полуавтомат означает: роутер не трогаем вовсе.
func TestStart_AutoDisabledSendsNothing(t *testing.T) {
	cmd := &countingCommander{}
	d := testDeps(t, cmd)
	d.AutoRepair = func(int64) bool { return false }

	_, err := d.Start(StartReq{
		RouterID: 1, Nickname: "роутер", CheckName: "tunnel_awg12",
		AgentVersion: "v0.19.7", Auto: true,
	})
	if !errors.Is(err, ErrAutoDisabled) {
		t.Fatalf("хотим ErrAutoDisabled, получили %v", err)
	}
	if cmd.count() != 0 {
		t.Fatalf("роутеру ушло %d команд, должно быть 0", cmd.count())
	}
}

// Для нечинибельной поломки движок не заводит задание вообще.
func TestStart_NoScenario(t *testing.T) {
	cmd := &countingCommander{}
	d := testDeps(t, cmd)

	if _, err := d.Start(StartReq{
		RouterID: 1, Nickname: "роутер", CheckName: "external_reach",
		AgentVersion: "v0.19.7",
	}); !errors.Is(err, ErrNoScenario) {
		t.Fatalf("хотим ErrNoScenario, получили %v", err)
	}
	if cmd.count() != 0 {
		t.Fatalf("роутеру ушло %d команд, должно быть 0", cmd.count())
	}
}

// Пока идёт замена конфига, починку начинать нельзя -- замок общий.
func TestStart_RefusesWhileLocked(t *testing.T) {
	cmd := &countingCommander{}
	d := testDeps(t, cmd)
	if !d.Store.TryLock("роутер") {
		t.Fatal("замок должен браться")
	}

	_, err := d.Start(StartReq{
		RouterID: 1, Nickname: "роутер", CheckName: "tunnel_awg12",
		AgentVersion: "v0.19.7",
	})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("хотим ErrAlreadyRunning, получили %v", err)
	}
}

// Старый агент не тянет ни promote, ни tunnel_power -- начинать нельзя.
func TestStart_RefusesOldAgent(t *testing.T) {
	cmd := &countingCommander{}
	d := testDeps(t, cmd)

	_, err := d.Start(StartReq{
		RouterID: 1, Nickname: "роутер", CheckName: "tunnel_awg12",
		AgentVersion: "v0.14.4",
	})
	if !errors.Is(err, replace.ErrAgentTooOld) {
		t.Fatalf("хотим ErrAgentTooOld, получили %v", err)
	}
	if cmd.count() != 0 {
		t.Fatalf("роутеру ушло %d команд, должно быть 0", cmd.count())
	}
}

// scriptedCommander отвечает так, чтобы весь сценарий дошёл до конца:
// снимок с политикой и резервом, импорт с идентификатором, разные адреса
// выхода через туннель и напрямую.
type scriptedCommander struct {
	mu      sync.Mutex
	actions []string
	last    string
}

func (c *scriptedCommander) Enqueue(_ int64, cmd wire.Command) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.actions = append(c.actions, cmd.Action)
	c.last = cmd.Action
	return nil
}

func (c *scriptedCommander) AwaitResult(_ context.Context, _ int64, id string, _ time.Duration) (*wire.CommandResult, bool) {
	c.mu.Lock()
	action := c.last
	c.mu.Unlock()
	out := ""
	switch action {
	case "route_status":
		out = `{"tunnels":[{"id":"awg21","name":"amnezia_nl","has_handshake":true}],` +
			`"policies":[{"name":"HydraRoute","interfaces":[` +
			`{"bind":"OpkgTun12","name":"Дача","tunnel_id":"awg12","role":"active","available":false,"order":1},` +
			`{"bind":"OpkgTun10","name":"Работа","tunnel_id":"awg10","role":"fallback","available":true,"order":2}]}]}`
	case "tunnel_import":
		out = `Туннель "amnezia_nl" создан (id=awg21)`
	case "check_via_tunnel":
		out = "Exit IP: 203.0.113.19"
	case "check_direct":
		out = "Exit IP: 203.0.113.7"
	}
	return &wire.CommandResult{ID: id, Status: "ok", Output: out}, true
}

type scriptedCabinet struct{}

func (scriptedCabinet) IssueConfig(context.Context, int64, string, string) (replace.Issued, error) {
	return replace.Issued{TunnelName: "amnezia_nl", Conf: []byte("[Interface]\n"), Backend: "nativewg"}, nil
}

// Человек получает ОДНО сообщение об одном событии. Мастер замены внутри
// починки обязан молчать: его текст говорит про «замену конфига» языком
// инженера, а починка уже рассказала ту же историю по-человечески.
func TestRun_SingleNotification(t *testing.T) {
	cmd := &scriptedCommander{}
	var mu sync.Mutex
	var notes []string
	collect := func(_ context.Context, _ int64, text string) {
		mu.Lock()
		defer mu.Unlock()
		notes = append(notes, text)
	}

	d := testDeps(t, nil)
	d.Commands = cmd
	d.Notify = collect
	d.Replace = replace.Deps{
		Store:          d.Store,
		Commands:       cmd,
		Cabinet:        scriptedCabinet{},
		Origin:         noopOrigin{},
		Notify:         collect,
		BaseCtx:        context.Background(),
		AwaitStep:      time.Second,
		HandshakeTries: 2,
		HandshakeWait:  time.Millisecond,
		Sleep:          func(context.Context, time.Duration) {},
	}

	id, err := d.Start(StartReq{
		RouterID: 1, Nickname: "роутер", CheckName: "tunnel_awg12",
		AgentVersion: "v0.19.7",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, d, id)

	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 {
		t.Fatalf("человеку ушло %d сообщений об одном событии: %v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "Увёл трафик") {
		t.Fatalf("сообщение не от починки: %q", notes[0])
	}
}

// Отчёт о починке уходит владельцу в личку, и линии в нём обязаны
// называться так, как он их назвал. Раньше в текст подставлялся
// идентификатор: «Линия «awg12» падала. Увёл трафик на «awg10»» -- при том,
// что комментарий над функцией прямо запрещал машинные имена. Тесты этого не
// видели: в фейковом снимке у звеньев не было поля name вовсе.
func TestRun_NotificationUsesLineNames(t *testing.T) {
	cmd := &scriptedCommander{}
	var mu sync.Mutex
	var notes []string
	collect := func(_ context.Context, _ int64, text string) {
		mu.Lock()
		defer mu.Unlock()
		notes = append(notes, text)
	}

	d := testDeps(t, nil)
	d.Commands = cmd
	d.Notify = collect
	d.Replace = replace.Deps{
		Store:          d.Store,
		Commands:       cmd,
		Cabinet:        scriptedCabinet{},
		Origin:         noopOrigin{},
		BaseCtx:        context.Background(),
		AwaitStep:      time.Second,
		HandshakeTries: 2,
		HandshakeWait:  time.Millisecond,
		Sleep:          func(context.Context, time.Duration) {},
	}

	id, err := d.Start(StartReq{
		RouterID: 1, Nickname: "роутер", CheckName: "tunnel_awg12",
		AgentVersion: "v0.19.7",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := waitDone(t, d, id)

	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 {
		t.Fatalf("сообщений: %d", len(notes))
	}
	text := notes[0]
	if !strings.Contains(text, "«Дача»") || !strings.Contains(text, "«Работа»") {
		t.Fatalf("в отчёте нет имён линий, какими их назвал владелец: %q", text)
	}
	if strings.Contains(text, "awg12") || strings.Contains(text, "awg10") {
		t.Fatalf("в личку ушёл идентификатор линии: %q", text)
	}
	// Шаг на экране починки -- то же правило: человек видит его в приложении.
	for _, st := range job.Steps {
		if strings.Contains(st.Detail, "awg10") {
			t.Fatalf("шаг %q показывает идентификатор: %q", st.Name, st.Detail)
		}
	}
}

type noopOrigin struct{}

func (noopOrigin) Record(int64, string, string, string, string, time.Time) error { return nil }

func waitDone(t *testing.T, d Deps, jobID string) provision.Job {
	t.Helper()
	// Пятнадцать секунд, а не три: при полном прогоне пакеты идут
	// параллельно, машина загружена, и трёх секунд иногда не хватало --
	// тест падал через раз, не имея отношения к тому, что проверяет.
	// Ожидание не удлиняет прогон: цикл выходит, как только задание
	// завершилось.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := d.Store.Get(jobID)
		if ok && job.State != provision.StateRunning {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("задание не завершилось за отведённое время")
	return provision.Job{}
}
