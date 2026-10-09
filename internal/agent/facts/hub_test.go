package facts

import (
	"context"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type fakePing struct {
	runs      []wire.PingRun
	log       *wire.PingLogFacts
	polled    int
	committed [][]wire.PingRun
}

func (f *fakePing) Poll(context.Context)    { f.polled++ }
func (f *fakePing) Pending() []wire.PingRun { return f.runs }
func (f *fakePing) Commit(s []wire.PingRun) { f.committed = append(f.committed, s) }
func (f *fakePing) Log() *wire.PingLogFacts { return f.log }

func TestHubSendsUnchangedBlockOnlyAfterRefresh(t *testing.T) {
	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	exit := &wire.ExitFacts{At: now, Tunnels: map[string]wire.ExitProbe{"awg11": {VPNIP: "203.0.113.7", Source: "awgm", At: now}}}
	h := &Hub{Exit: func() *wire.ExitFacts { return exit }, Now: func() time.Time { return now }}
	f := h.Collect(context.Background())
	if f == nil || f.Exit == nil {
		t.Fatalf("первый отчёт без блока: %+v", f)
	}
	h.Committed(f)
	if f := h.Collect(context.Background()); f != nil {
		t.Fatalf("неизменный блок ушёл снова: %+v", f)
	}
	now = now.Add(11 * time.Minute)
	if f := h.Collect(context.Background()); f == nil || f.Exit == nil {
		t.Fatal("раз в 10 минут блок подтверждается")
	}
}

func TestHubResendsUncommitted(t *testing.T) {
	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	hooks := &wire.HookFacts{At: now, State: "installed"}
	h := &Hub{Hooks: func() *wire.HookFacts { return hooks }, Now: func() time.Time { return now }}
	h.Collect(context.Background())
	if f := h.Collect(context.Background()); f == nil || f.Hooks == nil {
		t.Fatal("отчёт не дошёл -- блок обязан уйти снова")
	}
}

func TestHubResendsChangedBlock(t *testing.T) {
	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	hooks := &wire.HookFacts{At: now, State: "installed"}
	h := &Hub{Hooks: func() *wire.HookFacts { return hooks }, Now: func() time.Time { return now }}
	h.Committed(h.Collect(context.Background()))
	hooks = &wire.HookFacts{At: now, State: "installed", Wakes1h: 1}
	if f := h.Collect(context.Background()); f == nil || f.Hooks.Wakes1h != 1 {
		t.Fatal("изменённый блок обязан уйти сразу")
	}
}

func TestHubPassesPingRunsAndCommits(t *testing.T) {
	p := &fakePing{runs: []wire.PingRun{{TunnelID: "awg11", Fails: 2}}}
	h := &Hub{Ping: p}
	f := h.Collect(context.Background())
	if p.polled != 1 || f == nil || len(f.PingRuns) != 1 {
		t.Fatalf("polled=%d facts=%+v", p.polled, f)
	}
	h.Committed(f)
	if len(p.committed) != 1 {
		t.Fatal("серии не подтверждены трекеру")
	}
}

func TestHubEmptyIsNil(t *testing.T) {
	if f := (&Hub{}).Collect(context.Background()); f != nil {
		t.Fatalf("пустой сборщик дал блок: %+v", f)
	}
}

// P7 (ревью контроллера): WAN приходит из кеша wanfacts.Collector -- один и
// тот же указатель на несколько отчётов подряд. Clamp режет f.WAN.Links на
// месте; без копии перед Clamp это укоротило бы кеш коллектора навсегда.
func TestHubDoesNotMutateCachedWANOnClamp(t *testing.T) {
	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	links := make([]wire.WANLink, wire.MaxWANLinks+1)
	for i := range links {
		links[i] = wire.WANLink{Name: "if" + string(rune('a'+i))}
	}
	cached := &wire.WANFacts{At: now, Links: links}
	h := &Hub{WAN: func(context.Context) *wire.WANFacts { return cached }, Now: func() time.Time { return now }}
	f := h.Collect(context.Background())
	if f == nil || f.WAN == nil || len(f.WAN.Links) != wire.MaxWANLinks {
		t.Fatalf("clamp не применился: %+v", f)
	}
	if len(cached.Links) != wire.MaxWANLinks+1 {
		t.Fatalf("кеш коллектора испорчен обрезкой: len=%d", len(cached.Links))
	}
}

func TestHub_UnstickSentOnChangeOnly(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ev := []wire.UnstickEvent{{ID: "1-nwg0", TunnelID: "nwg0", From: "broken", Result: wire.UnstickFixed, At: now}}
	h := &Hub{Now: func() time.Time { return now }, Unstick: func() *wire.UnstickFacts {
		return &wire.UnstickFacts{Events: ev}
	}}
	f := h.Collect(context.Background())
	if f == nil || f.Unstick == nil || len(f.Unstick.Events) != 1 {
		t.Fatalf("first collect: %+v", f)
	}
	h.Committed(f)
	if f2 := h.Collect(context.Background()); f2 != nil && f2.Unstick != nil {
		t.Error("unchanged journal resent before refresh")
	}
}
