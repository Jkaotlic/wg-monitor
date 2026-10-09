package unstick

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
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
	if n := countCalls(h, "restart:nwg0"); n != 1 || h.services != 0 {
		t.Fatalf("restarts inside the window = %d (want 1), services=%d", n, h.services)
	}
	h.clk.advance(2 * time.Minute)
	h.w.Tick(ctx)
	if n := countCalls(h, "restart:nwg0"); n != 2 {
		t.Fatalf("ladder not retried after the window: restarts=%d calls=%v", n, h.awg.callList())
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
	out := clipDetails(in)
	if n := len([]rune(out)); n != 300 || !strings.HasSuffix(out, "…") {
		t.Fatalf("runes = %d (want 300 including the ellipsis): %q", n, out)
	}
	if exact := strings.Repeat("я", 300); clipDetails(exact) != exact {
		t.Error("a string of exactly 300 runes must stay intact")
	}
}

// gaveUpHarness -- туннель, который не чинится: сторож сдался (restart +
// service_restart), services == 1.
func gaveUpHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if len(h.w.Snapshot().GaveUp) != 1 || h.services != 1 {
		t.Fatalf("setup: %+v services=%d", h.w.Snapshot(), h.services)
	}
	return h
}

func countCalls(h *harness, call string) int {
	n := 0
	for _, c := range h.awg.callList() {
		if c == call {
			n++
		}
	}
	return n
}

// Повтор через RetryAfterGiveUp: ступень 1 без перезапуска службы, пока он идёт,
// «сдался» остаётся в снимке (проверка не говорит «в порядке»), провал
// обновляет At -- следующий повтор через 6 ч.
func TestTick_RetryAfterSixHours_FailedKeepsGaveUpAndNoService(t *testing.T) {
	h := gaveUpHarness(t)
	h.clk.advance(6 * time.Hour)
	inLadder := 0
	h.w.Deps().Sleep = func(c context.Context, d time.Duration) error {
		if s := h.w.Snapshot(); len(s.GaveUp) != 1 || s.GaveUp[0].TunnelID != "nwg0" {
			t.Errorf("gave_up dropped during the retry ladder: %+v", s)
		}
		inLadder++
		return h.clk.sleep(c, d)
	}
	h.w.Tick(ctx)
	if inLadder == 0 {
		t.Fatal("no retry ladder ran after 6h")
	}
	if n := countCalls(h, "restart:nwg0"); n != 2 {
		t.Fatalf("restarts = %d; want 2 (first ladder + retry)", n)
	}
	if h.services != 1 {
		t.Fatalf("retry restarted awg-manager: services=%d", h.services)
	}
	if len(h.w.Snapshot().GaveUp) != 1 {
		t.Fatalf("gave_up lost after a failed retry: %+v", h.w.Snapshot())
	}
	g := h.w.gaveUp["nwg0"]
	if g.Retrying || h.clk.now().Sub(g.At) > 2*time.Minute {
		t.Errorf("entry not refreshed: %+v", g)
	}
	// следующий повтор -- через 6 ч от обновлённого At, не раньше
	h.w.Deps().Sleep = h.clk.sleep
	h.clk.advance(5*time.Hour + 58*time.Minute)
	h.w.Tick(ctx)
	if n := countCalls(h, "restart:nwg0"); n != 2 {
		t.Fatalf("retried again before 6h: restarts=%d", n)
	}
	h.clk.advance(3 * time.Minute)
	h.w.Tick(ctx)
	if n := countCalls(h, "restart:nwg0"); n != 3 {
		t.Fatalf("no second retry after 6h: restarts=%d", n)
	}
	if h.services != 1 {
		t.Fatalf("services = %d", h.services)
	}
}

func TestTick_RetryAfterSixHours_FixedDeletesEntry(t *testing.T) {
	h := gaveUpHarness(t)
	h.awg.onAction = healOn("restart")
	h.clk.advance(6 * time.Hour)
	h.w.Tick(ctx)
	if s := h.w.Snapshot(); len(s.GaveUp) != 0 {
		t.Fatalf("gave_up kept after a successful retry: %+v", s)
	}
	if _, ok := h.w.gaveUp["nwg0"]; ok {
		t.Fatal("entry not deleted")
	}
	evs := h.w.Facts().Events
	if last := evs[len(evs)-1]; last.Result != wire.UnstickFixed || !slices.Equal(last.Steps, []string{"restart"}) {
		t.Fatalf("last event: %+v", last)
	}
	if h.services != 1 {
		t.Fatalf("services = %d", h.services)
	}
}

// Закрытое/снятое по-прежнему освобождает сразу, даже в режиме повтора.
func TestTick_RetryRelease_ResolvedFlipGone(t *testing.T) {
	h := gaveUpHarness(t)
	h.clk.advance(6 * time.Hour)
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		h.w.NoteCommand(wire.Command{Action: "awgm_update"}) // прервать лесенку, Retrying остаётся
		return nil
	}
	h.w.Tick(ctx)
	if g, ok := h.w.gaveUp["nwg0"]; !ok || !g.Retrying {
		t.Fatalf("aborted retry must stay retrying: %+v ok=%v", g, ok)
	}
	h.awg.set("nwg0", "running")
	h.w.Tick(ctx)
	if len(h.w.Snapshot().GaveUp) != 0 {
		t.Error("resolved tunnel not released")
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
	h := newHarness()
	h.awg.onAction = func(_, _ string, tt *awgmgr.Tunnel) { tt.Status = "running" }
	for i := 0; i < 25; i++ {
		// у каждого вывода свой туннель: один и тот же, зависший 3 раза за час,
		// сторож уже не лечит (flapFixes)
		id := fmt.Sprintf("x%d", i)
		h.awg.mu.Lock()
		h.awg.tunnels = append(h.awg.tunnels, tun(id, "running", true))
		h.awg.mu.Unlock()
		h.awg.set(id, "broken")
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

// Живая приёмка 09.10.2026 на workrouter: stop → enable оставил туннель в
// needs_stop при enabled=true на минуты, awg-manager сам не выходит (#669 в
// его исходниках: «nothing reconciles tunnels back»), start поднял за 10 с.
func TestTick_NeedsStopEnabledIsStarted(t *testing.T) {
	h := newHarness(tun("awg10", "needs_stop", true))
	h.awg.onAction = healOn("start")
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if c := h.awg.callList(); !slices.Equal(c, []string{"start:awg10"}) {
		t.Fatalf("calls: %v", c)
	}
	ev := h.w.Facts().Events[0]
	if ev.Result != wire.UnstickFixed || ev.From != "needs_stop" || ev.To != "running" || !slices.Equal(ev.Steps, []string{"start"}) {
		t.Errorf("event: %+v", ev)
	}
}

// fixCycle: туннель nwg0 ломается, сторож выводит его перезапуском.
func fixCycle(h *harness) {
	h.awg.set("nwg0", "broken")
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
}

func TestTick_FlappingGivesUpWithoutTouching(t *testing.T) {
	h := newHarness(tun("nwg0", "running", true))
	h.awg.onAction = healOn("restart")
	for i := 0; i < 3; i++ {
		fixCycle(h)
		h.clk.advance(5 * time.Minute)
	}
	before := h.awg.callList()
	if len(before) != 3 {
		t.Fatalf("setup calls: %v", before)
	}
	fixCycle(h)
	if got := h.awg.callList(); !slices.Equal(got, before) {
		t.Fatalf("watcher touched a flapping tunnel: %v", got)
	}
	if h.services != 0 {
		t.Errorf("services = %d", h.services)
	}
	s := h.w.Snapshot()
	if len(s.GaveUp) != 1 || !s.GaveUp[0].Flapping || s.GaveUp[0].Fixes != 3 || len(s.GaveUp[0].Steps) != 0 {
		t.Fatalf("snapshot: %+v", s.GaveUp)
	}
	evs := h.w.Facts().Events
	last := evs[len(evs)-1]
	if last.Result != wire.UnstickGaveUp || !slices.Equal(last.Steps, []string{"flapping"}) || last.TunnelID != "nwg0" {
		t.Errorf("last event: %+v", last)
	}
}

// spacedFixes: три вывода туннеля nwg0 с шагом spacing между началами, потом
// четвёртое зависание; возвращает возраст самого старого вывода к его началу
// лесенки (3*spacing - 1 минута).
func spacedFixes(spacing time.Duration) *harness {
	h := newHarness(tun("nwg0", "running", true))
	h.awg.onAction = healOn("restart")
	for i := 0; i < 3; i++ {
		fixCycle(h)   // занимает 121 с
		h.w.Tick(ctx) // опрос видит running, как в жизни
		h.clk.advance(spacing - 121*time.Second)
	}
	fixCycle(h)
	return h
}

// Окно 60 минут с обеих сторон: самый старый вывод 59 мин -- сдался сразу.
func TestTick_FlappingWindow_InsideGivesUp(t *testing.T) {
	h := spacedFixes(20 * time.Minute) // старейший вывод ~59 мин к четвёртому зависанию
	if n := countCalls(h, "restart:nwg0"); n != 3 {
		t.Fatalf("restarts = %d; want 3 (4th must not be touched)", n)
	}
	if s := h.w.Snapshot(); len(s.GaveUp) != 1 || !s.GaveUp[0].Flapping || s.GaveUp[0].Fixes != 3 {
		t.Fatalf("snapshot: %+v", s.GaveUp)
	}
}

// Старейший вывод чуть старше 60 мин -- в окне 2 вывода, обычная лесенка.
func TestTick_FlappingWindow_JustOutsideRunsLadder(t *testing.T) {
	h := spacedFixes(20*time.Minute + 30*time.Second) // ~60,5 мин
	if n := countCalls(h, "restart:nwg0"); n != 4 {
		t.Fatalf("restarts = %d; want 4", n)
	}
	if s := h.w.Snapshot(); len(s.GaveUp) != 0 {
		t.Fatalf("gave up: %+v", s.GaveUp)
	}
}

// Счёт зависаний -- по туннелю: три вывода РАЗНЫХ туннелей не глушат четвёртый.
func TestTick_FlappingCountIsPerTunnel(t *testing.T) {
	h := newHarness(tun("a", "running", true), tun("b", "running", true), tun("c", "running", true), tun("d", "running", true))
	h.awg.onAction = healOn("restart")
	for _, id := range []string{"a", "b", "c"} {
		h.awg.set(id, "broken")
		h.w.Tick(ctx)
		h.clk.advance(61 * time.Second)
		h.w.Tick(ctx)
		h.clk.advance(2 * time.Minute)
	}
	h.awg.set("d", "broken")
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if n := countCalls(h, "restart:d"); n != 1 {
		t.Fatalf("restart:d = %d; calls=%v", n, h.awg.callList())
	}
	if s := h.w.Snapshot(); len(s.GaveUp) != 0 {
		t.Fatalf("gave up: %+v", s.GaveUp)
	}
}

func TestTick_UpgradeInProgressSilencesWatcher(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	upgrading := true
	h.w.Deps().UpgradeInProgress = func() bool { return upgrading }
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	h.clk.advance(10 * time.Minute)
	h.w.Tick(ctx)
	if c := h.awg.callList(); len(c) != 0 {
		t.Fatalf("watcher acted during opkg upgrade: %v", c)
	}
	upgrading = false
	h.w.Tick(ctx)
	if c := h.awg.callList(); len(c) == 0 {
		t.Fatal("watcher stayed silent after the upgrade ended")
	}
}

func TestTick_UpgradeStartsDuringLadderAbortsBeforeService(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	upgrading := false
	h.w.Deps().UpgradeInProgress = func() bool { return upgrading }
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		upgrading = true
		return nil
	}
	h.w.Tick(ctx)
	upgrading = false
	// обновление кончилось, но пауза GuardWindow держится: через минуту тихо
	h.w.Deps().Sleep = h.clk.sleep
	n := len(h.awg.callList())
	h.clk.advance(time.Minute)
	h.w.Tick(ctx)
	if got := h.awg.callList(); len(got) != n {
		t.Fatalf("ladder restarted inside the guard window after the upgrade abort: %v", got)
	}
	assertAbortedSilently(t, h)
}

func TestTick_ServiceRestartRecordedForRetryTunnelToo(t *testing.T) {
	h := newHarness(tun("a", "broken", true), tun("b", "broken", true))
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx) // оба сдались, служба перезапущена один раз
	if len(h.w.Snapshot().GaveUp) != 2 || h.services != 1 {
		t.Fatalf("setup: %+v services=%d", h.w.Snapshot(), h.services)
	}
	h.clk.advance(5*time.Hour + 50*time.Minute)
	h.awg.set("a", "running")
	h.w.Tick(ctx) // a освобождён
	h.awg.set("a", "broken")
	h.w.Tick(ctx) // a снова на учёте
	h.clk.advance(10 * time.Minute)
	// b -- повтор, a -- свежий: службу перезапускают для обоих, и b выходит
	h.w.Deps().RestartService = func(context.Context) error {
		h.services++
		h.awg.set("b", "running")
		return nil
	}
	h.w.Tick(ctx)
	if h.services != 2 {
		t.Fatalf("services = %d; want 2", h.services)
	}
	var got *wire.UnstickEvent
	for _, e := range h.w.Facts().Events {
		if e.TunnelID == "b" && e.Result == wire.UnstickFixed {
			e := e
			got = &e
		}
	}
	if got == nil || !slices.Equal(got.Steps, []string{"restart", "service_restart"}) {
		t.Fatalf("retry tunnel event: %+v", got)
	}
}

func TestTick_FailedRetryAccumulatesSteps(t *testing.T) {
	h := gaveUpHarness(t)
	h.clk.advance(6 * time.Hour)
	h.w.Tick(ctx) // повтор не помог
	want := []string{"restart", "service_restart", "restart"}
	if g := h.w.gaveUp["nwg0"]; !slices.Equal(g.Steps, want) {
		t.Fatalf("steps after the 1st retry = %v; want %v", g.Steps, want)
	}
	h.clk.advance(6 * time.Hour)
	h.w.Tick(ctx) // и второй: подряд идущий restart не дублируется
	if g := h.w.gaveUp["nwg0"]; !slices.Equal(g.Steps, want) {
		t.Fatalf("steps after the 2nd retry = %v; want %v", g.Steps, want)
	}
	// не больше 8: остаются последние
	g := h.w.gaveUp["nwg0"]
	g.Steps = []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8"}
	h.w.gaveUp["nwg0"] = g
	h.clk.advance(6 * time.Hour)
	h.w.Tick(ctx)
	g = h.w.gaveUp["nwg0"]
	if !slices.Equal(g.Steps, []string{"s2", "s3", "s4", "s5", "s6", "s7", "s8", "restart"}) {
		t.Fatalf("capped steps = %v", g.Steps)
	}
}

func TestTick_ContextCancelledMidLadderIsSilent(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Tick(ctx)
	since := h.w.tracks["nwg0"].since
	h.clk.advance(61 * time.Second)
	h.w.Deps().Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	h.w.Tick(ctx)
	if c := h.awg.callList(); !slices.Equal(c, []string{"restart:nwg0"}) {
		t.Fatalf("calls: %v", c)
	}
	if h.services != 0 || !h.w.serviceAtZero() {
		t.Errorf("service touched: services=%d", h.services)
	}
	if s := h.w.Snapshot(); len(s.GaveUp) != 0 {
		t.Errorf("gave up on shutdown: %+v", s.GaveUp)
	}
	if f := h.w.Facts(); f != nil {
		t.Errorf("events on shutdown: %+v", f.Events)
	}
	if got := h.w.tracks["nwg0"].since; !got.Equal(since) {
		t.Errorf("since reset: %v -> %v", since, got)
	}
}

// retryingHarness -- «сдался» в режиме повтора (лесенка прервана командой).
func retryingHarness(t *testing.T) *harness {
	t.Helper()
	h := gaveUpHarness(t)
	h.clk.advance(6 * time.Hour)
	h.w.Deps().Sleep = func(_ context.Context, d time.Duration) error {
		h.clk.advance(d)
		h.w.NoteCommand(wire.Command{Action: "awgm_update"})
		return nil
	}
	h.w.Tick(ctx)
	if g, ok := h.w.gaveUp["nwg0"]; !ok || !g.Retrying {
		t.Fatalf("setup: %+v ok=%v", g, ok)
	}
	h.w.Deps().Sleep = h.clk.sleep
	return h
}

func TestTick_RetryRelease_EnabledFlip(t *testing.T) {
	h := retryingHarness(t)
	h.awg.mu.Lock()
	h.awg.tunnels[0].Enabled = false
	h.awg.mu.Unlock()
	h.w.Tick(ctx)
	if len(h.w.Snapshot().GaveUp) != 0 {
		t.Error("retrying entry kept after enabled flipped")
	}
}

func TestTick_RetryRelease_TunnelGone(t *testing.T) {
	h := retryingHarness(t)
	h.awg.mu.Lock()
	h.awg.tunnels = []awgmgr.Tunnel{tun("other", "running", true)}
	h.awg.mu.Unlock()
	h.w.Tick(ctx)
	if len(h.w.Snapshot().GaveUp) != 0 {
		t.Error("retrying entry kept after the tunnel vanished")
	}
}

func TestSnapshot_FlappingStepsAreEmptyListNotNull(t *testing.T) {
	h := newHarness(tun("nwg0", "running", true))
	h.awg.onAction = healOn("restart")
	for i := 0; i < 3; i++ {
		fixCycle(h)
		h.w.Tick(ctx)
		h.clk.advance(5 * time.Minute)
	}
	fixCycle(h)
	s := h.w.Snapshot()
	if len(s.GaveUp) != 1 || s.GaveUp[0].Steps == nil {
		t.Fatalf("snapshot steps must be [] not nil: %+v", s.GaveUp)
	}
	c := Check{Source: h.w}.Run(ctx, checks.Deps{})
	body, err := json.Marshal(c.Details)
	if err != nil || !strings.Contains(string(body), `"steps":[]`) {
		t.Fatalf("check details: %s err=%v", body, err)
	}
}

func TestTick_NoServiceRestartStepWithoutRestartFunc(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Deps().RestartService = nil
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	g := h.w.Snapshot().GaveUp
	if len(g) != 1 || !slices.Equal(g[0].Steps, []string{"restart"}) {
		t.Fatalf("steps without a restart func: %+v", g)
	}
}

func TestTick_FailedServiceRestartStillRecorded(t *testing.T) {
	h := newHarness(tun("nwg0", "broken", true))
	h.w.Deps().RestartService = func(context.Context) error { return errors.New("init script failed") }
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	g := h.w.Snapshot().GaveUp
	if len(g) != 1 || !slices.Equal(g[0].Steps, []string{"restart", "service_restart"}) {
		t.Fatalf("a failed attempt still counts: %+v", g)
	}
}
