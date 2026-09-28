// Package pingruns сворачивает журнал пингчека awg-manager в серии неудач.
//
// Журнал -- запись на каждую пробу (~45 с на VPN-туннель, ~1900 в сутки) и
// буфер ~2 часа (сверка С1). Пересылать его нельзя, поэтому агент читает
// буфер каждый отчёт с курсором по времени и шлёт только серии «от первой
// неудачи до первого успеха». Серии за время, когда агент лежал дольше
// буфера, не восстановимы -- это принятое ограничение.
//
// Курсор живёт в памяти: после перезапуска буфер перечитывается целиком, и
// уже отправленные серии уходят повторно. Бэкенд пишет их идемпотентно
// (PRIMARY KEY user_id, tunnel_id, from_ts), а флеш роутера не трогается.
package pingruns

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const (
	StateOK          = "ok"
	StateUnsupported = "unsupported"
	StateError       = "error"
	// maxClosedKept -- сколько закрытых серий держать, пока бэкенд недоступен.
	maxClosedKept = 200
)

// Source -- откуда брать журнал (*awgmgr.Client подходит).
type Source interface {
	PingCheckLogs(ctx context.Context) ([]awgmgr.PingCheckLogEntry, error)
}

type Tracker struct {
	Src Source
	Now func() time.Time

	mu      sync.Mutex
	cursor  map[string]time.Time
	open    map[string]*wire.PingRun
	closed  []wire.PingRun
	seen    map[string]bool
	state   string
	errText string
	since   time.Time
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now().UTC()
}

func (t *Tracker) init() {
	if t.cursor == nil {
		t.cursor = map[string]time.Time{}
		t.open = map[string]*wire.PingRun{}
		t.seen = map[string]bool{}
	}
}

type logItem struct {
	e  awgmgr.PingCheckLogEntry
	id string
	ts time.Time
}

// Poll читает буфер и сворачивает новые записи. Зовётся раз на отчёт.
func (t *Tracker) Poll(ctx context.Context) {
	entries, err := t.Src.PingCheckLogs(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	if err != nil {
		if errors.Is(err, awgmgr.ErrUnsupportedByRouter) {
			t.state, t.errText = StateUnsupported, ""
		} else {
			t.state, t.errText = StateError, err.Error()
		}
		return
	}
	if t.state != StateOK {
		// P2: любой переход в StateOK после неудачного/unsupported чтения
		// обнуляет since -- покрытие журнала не обязано перекрывать разрыв
		// чтения, а «с» в PingLogFacts должно честно значить «с этого
		// момента снова читаем», а не тянуться из прошлого до разрыва.
		t.state, t.errText = StateOK, ""
		t.since = t.now()
	}
	items := make([]logItem, 0, len(entries))
	for _, e := range entries {
		id := strings.TrimSpace(e.TunnelID)
		ts, ok := e.Time()
		if id == "" || !ok {
			continue
		}
		items = append(items, logItem{e: e, id: id, ts: ts})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ts.Before(items[j].ts) })
	for _, it := range items {
		t.seen[it.id] = true
		if !it.ts.After(t.cursor[it.id]) {
			continue
		}
		t.cursor[it.id] = it.ts
		t.apply(it)
	}
}

func (t *Tracker) apply(it logItem) {
	run := t.open[it.id]
	if !it.e.Success {
		if run == nil {
			run = &wire.PingRun{TunnelID: it.id, TunnelName: it.e.TunnelName, From: it.ts}
			t.open[it.id] = run
		}
		run.To = it.ts
		run.Fails++
		if it.e.Error != "" {
			run.Error = it.e.Error
		}
		// stateChange у awg-manager непуст на смене состояния; порог --
		// запасной признак для сборок, где поле пустое.
		if it.e.StateChange != "" || (it.e.Threshold > 0 && it.e.FailCount >= it.e.Threshold) {
			run.WentDown = true
		}
		return
	}
	if run != nil {
		run.To = it.ts
		run.Recovered = true
		t.closed = append(t.closed, *run)
		if len(t.closed) > maxClosedKept {
			t.closed = t.closed[len(t.closed)-maxClosedKept:]
		}
		delete(t.open, it.id)
	}
}

// Pending -- что отправить: открытые серии (их upsert дорастит на бэкенде) и
// закрытые, ещё не подтверждённые. Не больше wire.MaxPingRuns.
func (t *Tracker) Pending() []wire.PingRun {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.open))
	for id := range t.open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]wire.PingRun, 0, len(ids)+len(t.closed))
	for _, id := range ids {
		out = append(out, *t.open[id])
	}
	out = append(out, t.closed...)
	if len(out) > wire.MaxPingRuns {
		out = out[:wire.MaxPingRuns]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func runKey(r wire.PingRun) string { return r.TunnelID + "|" + r.From.UTC().Format(time.RFC3339Nano) }

// Commit -- бэкенд принял отчёт: отправленные закрытые серии больше не шлём.
func (t *Tracker) Commit(sent []wire.PingRun) {
	t.mu.Lock()
	defer t.mu.Unlock()
	done := map[string]bool{}
	for _, r := range sent {
		if r.Recovered {
			done[runKey(r)] = true
		}
	}
	kept := t.closed[:0]
	for _, r := range t.closed {
		if !done[runKey(r)] {
			kept = append(kept, r)
		}
	}
	t.closed = kept
}

// DownSince -- открытая серия, которую awg-manager уже признал падением. Для
// строки тревоги «по awg-manager -- с ЧЧ:ММ».
func (t *Tracker) DownSince(tunnelID string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if run := t.open[tunnelID]; run != nil && run.WentDown {
		return run.From, true
	}
	return time.Time{}, false
}

// Log -- блок ping_log: читает ли агент журнал и у каких VPN-туннелей он есть.
func (t *Tracker) Log() *wire.PingLogFacts {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == "" {
		return nil
	}
	f := &wire.PingLogFacts{At: t.since, State: t.state, Err: t.errText}
	for id := range t.seen {
		f.Tunnels = append(f.Tunnels, id)
	}
	sort.Strings(f.Tunnels)
	return f
}
