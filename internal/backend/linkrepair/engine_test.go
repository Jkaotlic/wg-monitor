package linkrepair

import (
	"context"
	"errors"
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
	mu      sync.Mutex
	sent    int
	replies map[string]wire.CommandResult
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
