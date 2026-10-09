package unstick

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/redact"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// AWG -- то, что сторож зовёт у awg-manager; *awgmgr.Client подходит.
type AWG interface {
	TunnelsAll(ctx context.Context) (*awgmgr.TunnelsAll, error)
	RestartTunnel(ctx context.Context, id string) error
	StartTunnel(ctx context.Context, id string) error
	StopTunnel(ctx context.Context, id string) error
}

// Deps -- побочные эффекты; nil Now/Sleep/Logger получают настоящие.
type Deps struct {
	AWG            AWG
	RestartService func(ctx context.Context) error // S99awg-manager restart
	Now            func() time.Time
	Sleep          func(ctx context.Context, d time.Duration) error
	Logger         *slog.Logger
}

// Snapshot -- что видит проверка awgm_unstick.
type Snapshot struct {
	Ready  bool   // был хотя бы один удачный опрос
	Active string // туннель, по которому идёт лесенка; "" -- никто
	GaveUp []GaveUpTunnel
}

type GaveUpTunnel struct {
	TunnelID string
	Name     string
	Status   string
	Details  string // statusDetails, уже redact + 300 рун
	Steps    []string
	Since    time.Time
}

// giveUp -- запись «сдался»; переживает рестарт агента (state.go).
type giveUp struct {
	Name    string    `json:"name,omitempty"`
	Status  string    `json:"status"`
	Details string    `json:"details,omitempty"`
	Steps   []string  `json:"steps"`
	Since   time.Time `json:"since"`
	At      time.Time `json:"at"`
}

type track struct {
	status  string
	enabled bool
	since   time.Time
}

type due struct {
	t      awgmgr.Tunnel
	from   string
	remedy Remedy
	since  time.Time
	steps  []string
}

const (
	maxEvents   = wire.MaxUnstickEvents
	eventsTTL   = 24 * time.Hour
	detailsRune = 300
)

// Команды бэкенда, после которых сторож молчит GuardWindow: по одному
// туннелю -- и по всему awg-manager/роутеру.
var (
	tunnelActions = map[string]bool{
		"tunnel_enable": true, "tunnel_disable": true, "tunnel_restart": true,
		"tunnel_delete": true, "tunnel_import": true, "tunnel_power": true,
	}
	routerActions = map[string]bool{
		"restart_tunnel": true, // RestartAll -- все туннели разом
		"awgm_update":    true, "service_restart": true,
		"firmware_install": true, "self_update": true,
	}
)

type Watcher struct {
	cfg Config
	d   Deps
	log *slog.Logger

	mu        sync.Mutex
	tracks    map[string]track
	gaveUp    map[string]giveUp
	serviceAt time.Time
	events    []wire.UnstickEvent
	cmdTunnel map[string]time.Time
	cmdRouter time.Time
	ready     bool
	active    string
	unknown   map[string]string // id -> незнакомый статус, уже записанный в журнал
	lastSaved []byte
}

func New(cfg Config, d Deps) *Watcher {
	w := &Watcher{
		cfg:       cfg.withDefaults(),
		d:         d,
		log:       d.Logger,
		tracks:    map[string]track{},
		gaveUp:    map[string]giveUp{},
		cmdTunnel: map[string]time.Time{},
		unknown:   map[string]string{},
	}
	if w.log == nil {
		w.log = slog.Default()
	}
	w.load()
	return w
}

// Deps -- для тестов пакета: подменить побочные эффекты после New.
func (w *Watcher) Deps() *Deps { return &w.d }

func (w *Watcher) now() time.Time {
	if w.d.Now != nil {
		return w.d.Now()
	}
	return time.Now()
}

func (w *Watcher) sleep(ctx context.Context, d time.Duration) error {
	if w.d.Sleep != nil {
		return w.d.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run -- петля сторожа до отмены ctx.
func (w *Watcher) Run(ctx context.Context) {
	for {
		w.Tick(ctx)
		if err := w.sleep(ctx, w.cfg.Poll); err != nil {
			return
		}
	}
}

// NoteCommand -- хук actions.Runner: бэкенд что-то делает с туннелем или
// с awg-manager -- сторож молчит GuardWindow.
func (w *Watcher) NoteCommand(cmd wire.Command) {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case routerActions[cmd.Action]:
		w.cmdRouter = now
	case tunnelActions[cmd.Action]:
		id, _ := cmd.Args["tunnel_id"].(string)
		if id == "" {
			id, _ = cmd.Args["target_id"].(string)
		}
		if id == "" {
			// не знаем, какой туннель, -- молчим по всем
			w.cmdRouter = now
			return
		}
		w.cmdTunnel[id] = now
	}
}

// Tick -- один опрос и, если есть зависшие дольше порога, одна лесенка.
func (w *Watcher) Tick(ctx context.Context) {
	all, err := w.d.AWG.TunnelsAll(ctx)
	if err != nil {
		w.log.Debug("unstick: awg-manager не ответил", "err", err)
		return
	}
	list := w.observe(all.Tunnels, w.now())
	if len(list) > 0 {
		w.ladder(ctx, list)
	}
	w.save()
}

// observe обновляет учёт и возвращает туннели, которым пора лесенку.
func (w *Watcher) observe(tunnels []awgmgr.Tunnel, now time.Time) []*due {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready = true
	seen := map[string]bool{}
	var out []*due
	for _, t := range tunnels {
		if t.ID == "" {
			continue
		}
		seen[t.ID] = true
		tr, ok := w.tracks[t.ID]
		if !ok || tr.status != t.Status || tr.enabled != t.Enabled {
			tr = track{status: t.Status, enabled: t.Enabled, since: now}
			w.tracks[t.ID] = tr
		}
		if g, ok := w.gaveUp[t.ID]; ok && (g.Status != t.Status || now.Sub(g.At) >= w.cfg.RetryAfterGiveUp) {
			delete(w.gaveUp, t.ID)
		}
		if !KnownStatus(t.Status) {
			if w.unknown[t.ID] != t.Status {
				w.unknown[t.ID] = t.Status
				w.log.Warn("unstick: незнакомый статус awg-manager -- не трогаю", "tunnel", t.ID, "status", t.Status)
			}
			continue
		}
		delete(w.unknown, t.ID)
		kind, remedy := Classify(t.Status, t.Enabled)
		if kind == KindNone || now.Sub(tr.since) < w.cfg.threshold(kind) {
			continue
		}
		if t.Locked {
			continue
		}
		if _, ok := w.gaveUp[t.ID]; ok {
			continue
		}
		if now.Sub(w.cmdRouter) < w.cfg.GuardWindow || now.Sub(w.cmdTunnel[t.ID]) < w.cfg.GuardWindow {
			continue
		}
		out = append(out, &due{t: t, from: t.Status, remedy: remedy, since: tr.since})
	}
	for id := range w.tracks {
		if !seen[id] {
			delete(w.tracks, id)
			delete(w.gaveUp, id)
			delete(w.unknown, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].t.ID < out[j].t.ID })
	return out
}

// ladder: ступень 1 всем, проверка; оставшимся -- ступень 2 (если
// положена), проверка; оставшиеся -- сдался.
func (w *Watcher) ladder(ctx context.Context, list []*due) {
	start := w.now()
	defer w.setActive("")
	for _, d := range list {
		w.setActive(d.t.ID)
		d.steps = append(d.steps, w.apply(ctx, d.t.ID, d.remedy))
	}
	if err := w.sleep(ctx, w.cfg.Verify1); err != nil {
		return
	}
	list = w.recheck(ctx, list, start)
	if len(list) == 0 {
		return
	}
	if w.serviceAllowed() {
		for _, d := range list {
			d.steps = append(d.steps, "service_restart")
		}
		if w.d.RestartService != nil {
			if err := w.d.RestartService(ctx); err != nil {
				w.log.Warn("unstick: перезапуск awg-manager не удался", "err", err)
			}
		}
		if err := w.sleep(ctx, w.cfg.Verify2); err != nil {
			return
		}
		list = w.recheck(ctx, list, start)
	}
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, d := range list {
		w.gaveUp[d.t.ID] = giveUp{
			Name: d.t.Name, Status: d.t.Status, Details: clipDetails(d.t.StatusDetails),
			Steps: d.steps, Since: d.since, At: now,
		}
		w.addEventLocked(start, d, wire.UnstickGaveUp, now)
		w.log.Warn("unstick: не выводится", "tunnel", d.t.ID, "status", d.t.Status, "steps", d.steps)
	}
}

func (w *Watcher) apply(ctx context.Context, id string, r Remedy) string {
	var err error
	step := string(r)
	switch r {
	case RemedyStart:
		err = w.d.AWG.StartTunnel(ctx, id)
	case RemedyStop:
		err = w.d.AWG.StopTunnel(ctx, id)
	case RemedyRestart:
		err = w.d.AWG.RestartTunnel(ctx, id)
		if awgmgr.IsEndpointMissing(err) {
			// старый awg-manager без /api/control/restart
			step = "stop_start"
			if err = w.d.AWG.StopTunnel(ctx, id); err == nil {
				if err = w.sleep(ctx, 2*time.Second); err == nil {
					err = w.d.AWG.StartTunnel(ctx, id)
				}
			}
		}
	}
	if err != nil {
		w.log.Warn("unstick: действие не удалось", "tunnel", id, "step", step, "err", err)
	} else {
		w.log.Info("unstick: действие", "tunnel", id, "step", step)
	}
	return step
}

// recheck перечитывает туннели; вышедшие -- событие fixed, удалённые --
// забыты, остальные возвращаются со свежим состоянием. Не прочитали --
// никто не вышел (без доказательства «починил» не говорим).
func (w *Watcher) recheck(ctx context.Context, list []*due, start time.Time) []*due {
	all, err := w.d.AWG.TunnelsAll(ctx)
	if err != nil {
		w.log.Warn("unstick: перечитать не удалось", "err", err)
		return list
	}
	byID := map[string]awgmgr.Tunnel{}
	for _, t := range all.Tunnels {
		byID[t.ID] = t
	}
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	var rest []*due
	for _, d := range list {
		t, ok := byID[d.t.ID]
		if !ok {
			continue
		}
		d.t = t
		if Resolved(t.Status, t.Enabled) {
			w.addEventLocked(start, d, wire.UnstickFixed, now)
			continue
		}
		rest = append(rest, d)
	}
	return rest
}

func (w *Watcher) serviceAllowed() bool {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.serviceAt.IsZero() && now.Sub(w.serviceAt) < w.cfg.ServiceEvery {
		return false
	}
	// время -- ДО вызова: сломанный init-скрипт не долбим каждый тик
	w.serviceAt = now
	return true
}

func (w *Watcher) addEventLocked(start time.Time, d *due, result string, now time.Time) {
	w.events = append(w.events, wire.UnstickEvent{
		ID:         fmt.Sprintf("%d-%s", start.UnixNano(), d.t.ID),
		TunnelID:   d.t.ID,
		TunnelName: d.t.Name,
		From:       d.from,
		Steps:      append([]string(nil), d.steps...),
		Result:     result,
		To:         d.t.Status,
		At:         now,
	})
	w.pruneEventsLocked(now)
}

func (w *Watcher) pruneEventsLocked(now time.Time) {
	kept := w.events[:0]
	for _, e := range w.events {
		if now.Sub(e.At) < eventsTTL {
			kept = append(kept, e)
		}
	}
	if len(kept) > maxEvents {
		kept = kept[len(kept)-maxEvents:]
	}
	w.events = append([]wire.UnstickEvent(nil), kept...)
}

func (w *Watcher) setActive(id string) {
	w.mu.Lock()
	w.active = id
	w.mu.Unlock()
}

func clipDetails(s string) string {
	s = redact.Text(s)
	r := []rune(s)
	if len(r) > detailsRune {
		return string(r[:detailsRune]) + "…"
	}
	return s
}

func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := Snapshot{Ready: w.ready, Active: w.active}
	for id, g := range w.gaveUp {
		s.GaveUp = append(s.GaveUp, GaveUpTunnel{
			TunnelID: id, Name: g.Name, Status: g.Status, Details: g.Details,
			Steps: append([]string(nil), g.Steps...), Since: g.Since,
		})
	}
	sort.Slice(s.GaveUp, func(i, j int) bool { return s.GaveUp[i].TunnelID < s.GaveUp[j].TunnelID })
	return s
}

// Facts -- журнал для отчёта; nil -- событий за сутки нет.
func (w *Watcher) Facts() *wire.UnstickFacts {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneEventsLocked(now)
	if len(w.events) == 0 {
		return nil
	}
	out := make([]wire.UnstickEvent, len(w.events))
	for i, e := range w.events {
		e.Steps = append([]string(nil), e.Steps...)
		out[i] = e
	}
	return &wire.UnstickFacts{Events: out}
}
