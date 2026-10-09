package unstick

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

var ctx = context.Background()

// становится running после restart/start, stopped после stop
func healOn(actions ...string) func(string, string, *awgmgr.Tunnel) {
	return func(a, _ string, t *awgmgr.Tunnel) {
		if !slices.Contains(actions, a) {
			return
		}
		if a == "stop" {
			t.Status = "stopped"
		} else {
			t.Status = "running"
		}
	}
}

func TestTick_BelowThresholdDoesNothing(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(59 * time.Second)
	h.w.Tick(ctx)
	if c := h.awg.callList(); len(c) != 0 {
		t.Fatalf("calls before threshold: %v", c)
	}
}

func TestTick_BrokenFixedByRestart(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.awg.onAction = healOn("restart")
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if c := h.awg.callList(); !slices.Equal(c, []string{"restart:nwg0"}) {
		t.Fatalf("calls: %v", c)
	}
	if h.services != 0 {
		t.Errorf("service restarted on success at step 1")
	}
	f := h.w.Facts()
	if f == nil || len(f.Events) != 1 {
		t.Fatalf("facts: %+v", f)
	}
	ev := f.Events[0]
	if ev.Result != wire.UnstickFixed || ev.From != "broken" || ev.To != "running" ||
		!slices.Equal(ev.Steps, []string{"restart"}) || ev.TunnelName != "line-nwg0" {
		t.Errorf("event: %+v", ev)
	}
	if s := h.w.Snapshot(); len(s.GaveUp) != 0 || s.Active != "" || !s.Ready {
		t.Errorf("snapshot: %+v", s)
	}
}

func TestTick_ThresholdsPerKind(t *testing.T) {
	h := newHarness(tun("a", "starting", true), tun("b", "needs_start", true), tun("c", "needs_stop", false))
	h.awg.onAction = healOn("restart", "start", "stop")
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	got := h.awg.callList()
	if !slices.Equal(got, []string{"start:b", "stop:c"}) {
		t.Fatalf("after 61s: %v (starting must wait 5m)", got)
	}
	h.clk.advance(5 * time.Minute)
	h.w.Tick(ctx)
	if got := h.awg.callList(); !slices.Contains(got, "restart:a") {
		t.Fatalf("after 5m: %v", got)
	}
}

func TestTick_StatusChangeResetsSince(t *testing.T) {
	h := newHarness(tun("nwg0", "starting", true))
	h.w.Tick(ctx)
	h.clk.advance(4 * time.Minute)
	h.awg.set("nwg0", "broken")
	h.w.Tick(ctx)
	h.clk.advance(59 * time.Second)
	h.w.Tick(ctx)
	if c := h.awg.callList(); len(c) != 0 {
		t.Fatalf("since not reset on status change: %v", c)
	}
}

func TestTick_EscalatesToServiceThenFixed(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Deps().RestartService = func(context.Context) error {
		h.services++
		h.awg.set("nwg0", "running")
		return nil
	}
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if h.services != 1 {
		t.Fatalf("services = %d", h.services)
	}
	ev := h.w.Facts().Events[0]
	if ev.Result != wire.UnstickFixed || !slices.Equal(ev.Steps, []string{"restart", "service_restart"}) {
		t.Errorf("event: %+v", ev)
	}
}

func TestTick_GivesUpAndStaysQuiet(t *testing.T) {
	br := tun("nwg0", "broken", true)
	br.StatusDetails = "endpoint 203.0.113.20 unreachable"
	h := newHarness(br)
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	s := h.w.Snapshot()
	if len(s.GaveUp) != 1 || s.GaveUp[0].Status != "broken" {
		t.Fatalf("gave up: %+v", s)
	}
	if g := s.GaveUp[0]; g.Details == br.StatusDetails || !slices.Equal(g.Steps, []string{"restart", "service_restart"}) {
		t.Errorf("details must be redacted, steps both: %+v", g)
	}
	ev := h.w.Facts().Events[0]
	if ev.Result != wire.UnstickGaveUp {
		t.Errorf("event: %+v", ev)
	}
	before := len(h.awg.callList())
	h.clk.advance(time.Hour)
	h.w.Tick(ctx)
	if len(h.awg.callList()) != before || h.services != 1 {
		t.Fatalf("touched after giving up: %v services=%d", h.awg.callList(), h.services)
	}
	// переход в другое зависшее состояние «сдался» не снимает
	h.awg.set("nwg0", "starting")
	h.w.Tick(ctx)
	if len(h.w.Snapshot().GaveUp) != 1 {
		t.Error("gave_up released by a move into another stuck status")
	}
	// вышел из зависания -- снимает
	h.awg.set("nwg0", "running")
	h.w.Tick(ctx)
	if len(h.w.Snapshot().GaveUp) != 0 {
		t.Error("gave_up not cleared when the tunnel resolved")
	}
}

func TestTick_NoHammeringBrokenStartingFlap(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx) // сдался
	n := len(h.awg.callList())
	svc := h.services
	for i := 0; i < 6; i++ {
		h.clk.advance(5 * time.Minute)
		h.awg.set("nwg0", "starting")
		h.w.Tick(ctx)
		h.clk.advance(5 * time.Minute)
		h.awg.set("nwg0", "broken")
		h.w.Tick(ctx)
		h.clk.advance(3 * time.Minute)
		h.w.Tick(ctx)
	}
	if len(h.awg.callList()) != n || h.services != svc {
		t.Fatalf("hammered a given-up tunnel: %v services=%d", h.awg.callList(), h.services)
	}
}

func TestTick_GaveUpReleasedWhenEnabledFlips(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx) // сдался
	h.awg.mu.Lock()
	h.awg.tunnels[0].Enabled = false
	h.awg.mu.Unlock()
	h.w.Tick(ctx)
	if len(h.w.Snapshot().GaveUp) != 0 {
		t.Error("gave_up kept after the owner flipped enabled")
	}
}

func assertAbortedSilently(t *testing.T, h *harness) {
	t.Helper()
	if h.services != 0 {
		t.Fatalf("service restarted under a guard: %d", h.services)
	}
	if s := h.w.Snapshot(); len(s.GaveUp) != 0 {
		t.Fatalf("false give-up under a guard: %+v", s.GaveUp)
	}
	if f := h.w.Facts(); f != nil {
		t.Fatalf("events under a guard: %+v", f.Events)
	}
	if !h.w.serviceAtZero() {
		t.Error("service stamp written although the call was skipped")
	}
	// окно кончилось -- лесенка идёт заново
	h.w.Deps().Sleep = h.clk.sleep
	n := len(h.awg.callList())
	h.clk.advance(15 * time.Minute)
	h.w.Tick(ctx)
	if len(h.awg.callList()) <= n {
		t.Fatalf("ladder not retried after the guard window: %v", h.awg.callList())
	}
}

func TestTick_ServiceAbortedWhenRouterCommandDuringWait(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		h.w.NoteCommand(wire.Command{Action: "awgm_update"})
		return nil
	}
	h.w.Tick(ctx)
	assertAbortedSilently(t, h)
}

func TestTick_ServiceAbortedWhenOtherTunnelCommandDuringWait(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true), tun("other", "running", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		h.w.NoteCommand(wire.Command{Action: "tunnel_import", Args: map[string]any{"target_id": "other"}})
		return nil
	}
	h.w.Tick(ctx)
	assertAbortedSilently(t, h)
}

func TestTick_AbortedLadderPausesUntilWindowEnds(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true), tun("other", "running", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		h.w.NoteCommand(wire.Command{Action: "tunnel_import", Args: map[string]any{"target_id": "other"}})
		return nil
	}
	h.w.Tick(ctx)
	h.w.Deps().Sleep = h.clk.sleep
	for i := 0; i < 19; i++ { // 9,5 минут опросов по 30 с внутри окна
		h.clk.advance(30 * time.Second)
		h.w.Tick(ctx)
	}
	n := 0
	for _, c := range h.awg.callList() {
		if c == "restart:nwg0" {
			n++
		}
	}
	if n != 1 || h.services != 0 {
		t.Fatalf("restarts inside the window = %d (want 1), services=%d", n, h.services)
	}
	h.clk.advance(2 * time.Minute)
	h.w.Tick(ctx)
	if got := h.awg.callList(); len(got) < 2 {
		t.Fatalf("ladder not retried after the window: %v", got)
	}
}

func TestTick_EnabledFlippedMidLadderDropsSilently(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		h.awg.mu.Lock()
		h.awg.tunnels[0].Enabled = false
		h.awg.tunnels[0].Status = "running"
		h.awg.mu.Unlock()
		return nil
	}
	h.w.Tick(ctx)
	if f := h.w.Facts(); f != nil {
		t.Fatalf("event after owner flipped enabled: %+v", f.Events)
	}
	if s := h.w.Snapshot(); len(s.GaveUp) != 0 || h.services != 0 {
		t.Fatalf("snapshot %+v services=%d", s, h.services)
	}
}

func TestTick_EmptyRecheckListIsNotFixedNotGone(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		h.awg.mu.Lock()
		h.awg.tunnels = nil
		h.awg.mu.Unlock()
		return nil
	}
	h.w.Tick(ctx)
	if h.services != 1 {
		t.Fatalf("empty list must act like a read error and escalate; services=%d", h.services)
	}
	for _, e := range h.w.Facts().Events {
		if e.Result == wire.UnstickFixed {
			t.Fatalf("fixed without proof: %+v", e)
		}
	}
}

func TestTick_TransitionThresholdBoundary(t *testing.T) {
	h := newHarness(tun("a", "starting", true))
	h.w.Tick(ctx)
	h.clk.advance(4*time.Minute + 59*time.Second)
	h.w.Tick(ctx)
	if c := h.awg.callList(); len(c) != 0 {
		t.Fatalf("acted at 4:59: %v", c)
	}
	h.clk.advance(2 * time.Second)
	h.w.Tick(ctx)
	if c := h.awg.callList(); !slices.Equal(c, []string{"restart:a"}) {
		t.Fatalf("at 5:01: %v", c)
	}
}

func TestClipDetails_Limit(t *testing.T) {
	in := strings.Repeat("я", 400)
	if n := len([]rune(clipDetails(in))); n > 301 {
		t.Fatalf("runes = %d", n)
	}
}

func TestTick_RetryAfterSixHours(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx) // сдался
	n := len(h.awg.callList())
	h.clk.advance(6 * time.Hour)
	h.w.Tick(ctx)
	if len(h.awg.callList()) == n {
		t.Fatal("no retry after 6h")
	}
}

func TestTick_ServiceAtMostHourly(t *testing.T) {
	h := newHarness(tun("a", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx) // ступень 2 #1, сдался
	h.awg.tunnels = append(h.awg.tunnels, tun("b", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx) // b: ступень 2 не положена (меньше часа)
	if h.services != 1 {
		t.Fatalf("services = %d; want 1", h.services)
	}
	var b wire.UnstickEvent
	for _, e := range h.w.Facts().Events {
		if e.TunnelID == "b" {
			b = e
		}
	}
	if b.Result != wire.UnstickGaveUp || !slices.Equal(b.Steps, []string{"restart"}) {
		t.Errorf("b: %+v", b)
	}
}

func TestTick_TwoStuck_OneServiceRestart(t *testing.T) {
	h := newHarness(tun("a", "broken", true), tun("b", "needs_start", true))
	h.w.Deps().RestartService = func(context.Context) error {
		h.services++
		h.awg.set("a", "running")
		h.awg.set("b", "running")
		return nil
	}
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if h.services != 1 {
		t.Fatalf("services = %d", h.services)
	}
	evs := h.w.Facts().Events
	if len(evs) != 2 {
		t.Fatalf("events: %+v", evs)
	}
	for _, e := range evs {
		if e.Result != wire.UnstickFixed || e.Steps[len(e.Steps)-1] != "service_restart" {
			t.Errorf("event: %+v", e)
		}
	}
	if evs[0].ID == evs[1].ID {
		t.Error("event ids collide")
	}
}

func TestTick_RecheckErrorIsNotFixed(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.awg.onAction = func(string, string, *awgmgr.Tunnel) { h.awg.readErr = errors.New("connection refused") }
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	for _, e := range h.w.Facts().Events {
		if e.Result == wire.UnstickFixed {
			t.Fatalf("fixed without proof: %+v", e)
		}
	}
	if h.services != 1 {
		t.Errorf("unread after step 1 must escalate; services=%d", h.services)
	}
}

func TestTick_TunnelGoneMidLadder(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true), tun("keep", "running", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	// туннель удаляют, пока сторож ждёт после ступени 1
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		h.awg.mu.Lock()
		h.awg.tunnels = h.awg.tunnels[1:]
		h.awg.mu.Unlock()
		return nil
	}
	h.w.Tick(ctx)
	if f := h.w.Facts(); f != nil && len(f.Events) != 0 {
		t.Fatalf("events for a deleted tunnel: %+v", f.Events)
	}
	if s := h.w.Snapshot(); len(s.GaveUp) != 0 {
		t.Fatalf("gave up on a deleted tunnel: %+v", s)
	}
}

func TestTick_RestartMissingFallsBackToStopStart(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.awg.restart404 = true
	h.awg.onAction = healOn("start")
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	c := h.awg.callList()
	if !slices.Equal(c, []string{"restart404:nwg0", "stop:nwg0", "start:nwg0"}) {
		t.Fatalf("calls: %v", c)
	}
	if ev := h.w.Facts().Events[0]; !slices.Equal(ev.Steps, []string{"stop_start"}) || ev.Result != wire.UnstickFixed {
		t.Errorf("event: %+v", ev)
	}
}

func TestTick_Guards(t *testing.T) {
	locked := tun("l", "broken", true)
	locked.Locked = true
	h := newHarness(locked, tun("t", "broken", true), tun("u", "broken", true))
	h.w.Tick(ctx)
	h.w.NoteCommand(wire.Command{Action: "tunnel_import", Args: map[string]any{"target_id": "t"}})
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if c := h.awg.callList(); !slices.Equal(c, []string{"restart:u"}) {
		t.Fatalf("guards: %v", c)
	}
	// окно кончилось -- t чинится; запертый -- нет никогда
	h.clk.advance(10 * time.Minute)
	h.w.Tick(ctx)
	if c := h.awg.callList(); !slices.Contains(c, "restart:t") || slices.Contains(c, "restart:l") {
		t.Fatalf("after window: %v", c)
	}
}

func TestTick_RouterWideCommandSilencesAll(t *testing.T) {
	h := newHarness(tun("a", "broken", true))
	h.w.Tick(ctx)
	h.w.NoteCommand(wire.Command{Action: "awgm_update"})
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if c := h.awg.callList(); len(c) != 0 {
		t.Fatalf("acted during router-wide command window: %v", c)
	}
	for _, a := range []string{"restart_tunnel", "service_restart", "firmware_install", "self_update", "opkg_upgrade"} {
		h2 := newHarness(tun("a", "broken", true))
		h2.w.Tick(ctx)
		h2.w.NoteCommand(wire.Command{Action: a})
		h2.clk.advance(61 * time.Second)
		h2.w.Tick(ctx)
		if c := h2.awg.callList(); len(c) != 0 {
			t.Errorf("%s did not silence the watcher: %v", a, c)
		}
	}
}

func TestTick_UnknownStatusUntouched(t *testing.T) {
	h := newHarness(tun("x", "connected", true))
	h.w.Tick(ctx)
	h.clk.advance(time.Hour)
	h.w.Tick(ctx)
	if c := h.awg.callList(); len(c) != 0 {
		t.Fatalf("touched unknown status: %v", c)
	}
}

func TestTick_ReadErrorIsSkip(t *testing.T) {
	h := newHarness(tun("x", "broken", true))
	h.awg.readErr = errors.New("connection refused")
	h.w.Tick(ctx)
	if s := h.w.Snapshot(); s.Ready {
		t.Error("ready without a successful read")
	}
}

func TestEvents_Retention(t *testing.T) {
	h := newHarness(tun("x", "broken", true))
	h.awg.onAction = func(_, _ string, tt *awgmgr.Tunnel) { tt.Status = "running" }
	for i := 0; i < 25; i++ {
		h.awg.set("x", "broken")
		h.w.Tick(ctx)
		h.clk.advance(61 * time.Second)
		h.w.Tick(ctx)
	}
	if n := len(h.w.Facts().Events); n != 20 {
		t.Fatalf("events kept: %d", n)
	}
	h.clk.advance(25 * time.Hour)
	h.w.Tick(ctx)
	if f := h.w.Facts(); f != nil {
		t.Fatalf("events older than 24h kept: %d", len(f.Events))
	}
}

// Перезапуск агента (self_update, firmware_install, opkg) стирает окна тишины:
// старт считается командой по всему роутеру.
func TestNew_StartCountsAsRouterWideCommand(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w = New(Config{Enabled: true}, Deps{
		AWG: h.awg, Now: h.clk.now, Sleep: h.clk.sleep,
		RestartService: func(context.Context) error { h.services++; return nil },
	})
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second) // давно за порогом
	h.w.Tick(ctx)
	h.clk.advance(8 * time.Minute)
	h.w.Tick(ctx)
	if c := h.awg.callList(); len(c) != 0 || h.services != 0 {
		t.Fatalf("acted inside the start guard: %v services=%d", c, h.services)
	}
	h.clk.advance(2*time.Minute + time.Second) // GuardWindow (10 мин) от старта прошло
	h.w.Tick(ctx)
	if c := h.awg.callList(); !slices.Equal(c, []string{"restart:nwg0"}) {
		t.Fatalf("no action after the start guard: %v", c)
	}
}
