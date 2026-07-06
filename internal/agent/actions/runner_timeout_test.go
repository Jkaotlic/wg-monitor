package actions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// blockingExec simulates a wedged subprocess (e.g. ndmc stuck during a
// firmware install or a CPU spike). A real exec.CommandContext kills the
// child and CombinedOutput returns once ctx is done, so the faithful fake
// blocks on ctx.Done() and surfaces ctx.Err() — it never returns on its own.
func blockingExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRunner_Execute_WedgedExec_TimesOutInsteadOfHanging is the regression
// test for audit finding A1: Execute used to run the dispatch switch with
// the caller's raw ctx, which in production
// (cmd/agent/main.go's signal.NotifyContext) never carries a deadline —
// only SIGINT/SIGTERM cancel it. A single hung subprocess therefore blocked
// Execute forever, and because cmdloop.Run is single-goroutine, every
// command after it silently died while heartbeats kept flowing.
//
// ActionTimeout is injected as a few milliseconds so this test proves the
// plumbing (ctx actually gets bound to a deadline, dispatch actually honors
// it) without waiting anywhere near the real 45s production default. The
// outer select+timer is a safety net: if the fix regresses, this test fails
// fast with a clear message instead of hanging the whole test binary.
func TestRunner_Execute_WedgedExec_TimesOutInsteadOfHanging(t *testing.T) {
	r := Runner{
		Exec:          blockingExec,
		ActionTimeout: func(string) time.Duration { return 20 * time.Millisecond },
	}

	done := make(chan wire.CommandResult, 1)
	go func() {
		done <- r.Execute(context.Background(), wire.Command{
			ID:     "wedge1",
			Action: "tunnel_enable",
			Args:   map[string]any{"ndms_name": "nwg1"},
		})
	}()

	select {
	case res := <-done:
		if res.Status != "timeout" {
			t.Fatalf("status = %q, want %q (output=%q)", res.Status, "timeout", res.Output)
		}
		if res.ID != "wedge1" {
			t.Errorf("id not preserved: %q", res.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return within 2s of a 20ms action timeout — a wedged subprocess would wedge the whole command loop forever")
	}
}

// TestActionTimeoutFor_Overrides pins the exact per-action budgets. The 300s
// group is every action that runs an opkg update/install/SmartUpgrade pipeline
// or streams a binary over the network; firmware_install gets 600s; everything
// else — including the read-only/local-file siblings of the long actions —
// stays at the 45s default. No waiting needed: actionTimeoutFor is a pure
// lookup. The quick-action rows are load-bearing: they assert we did NOT
// widen the budget for the sibling actions in the opkg_cron/entware_clean
// families that don't touch opkg or the network.
func TestActionTimeoutFor_Overrides(t *testing.T) {
	cases := []struct {
		action string
		want   time.Duration
	}{
		// Long: opkg / SmartUpgrade pipelines.
		{"opkg_upgrade", 300 * time.Second},
		{"opkg_feed_disable", 300 * time.Second},
		{"opkg_cron_install", 300 * time.Second},
		{"entware_clean_install", 300 * time.Second},
		// Long: multi-step network pipelines.
		{"tunnel_import", 300 * time.Second},
		{"self_update", 300 * time.Second},
		// Long: firmware install triggers a reboot.
		{"firmware_install", 600 * time.Second},
		// Quick: sibling actions in the same families that only read state or
		// touch local files must stay at the default (proves we did not widen
		// the whole family).
		{"opkg_cron_status", 45 * time.Second},
		{"opkg_cron_logs", 45 * time.Second},
		{"opkg_cron_remove", 45 * time.Second},
		{"entware_clean_status", 45 * time.Second},
		{"entware_clean_run", 45 * time.Second},
		{"entware_clean_logs", 45 * time.Second},
		{"entware_clean_remove", 45 * time.Second},
		// Quick: unrelated actions and the unknown-action fallback.
		{"restart_tunnel", 45 * time.Second},
		{"router_doctor", 45 * time.Second},
		{"some_unknown_action", 45 * time.Second},
	}
	for _, c := range cases {
		if got := actionTimeoutFor(c.action); got != c.want {
			t.Errorf("actionTimeoutFor(%q) = %v, want %v", c.action, got, c.want)
		}
	}
}

// TestRunner_WithActionTimeout_LongActionsGetExtendedBudget drives the
// coordinator's explicit ask: assert withActionTimeout (the method the
// production Execute path actually calls) returns a budget strictly greater
// than the 45s default for every long action, and that the bound deadline
// matches the expected value. Checks the deadline, never waits for it.
func TestRunner_WithActionTimeout_LongActionsGetExtendedBudget(t *testing.T) {
	cases := []struct {
		action string
		want   time.Duration
	}{
		{"opkg_feed_disable", 300 * time.Second},
		{"opkg_cron_install", 300 * time.Second},
		{"entware_clean_install", 300 * time.Second},
		{"self_update", 300 * time.Second},
		{"opkg_upgrade", 300 * time.Second},
		{"tunnel_import", 300 * time.Second},
		{"firmware_install", 600 * time.Second},
	}
	r := Runner{}
	for _, c := range cases {
		before := time.Now()
		ctx, cancel := r.withActionTimeout(context.Background(), c.action)
		dl, ok := ctx.Deadline()
		if !ok {
			cancel()
			t.Errorf("%s: expected a deadline on the returned context", c.action)
			continue
		}
		got := dl.Sub(before)
		if got <= defaultActionTimeout {
			t.Errorf("%s: budget %v must exceed the %v default", c.action, got, defaultActionTimeout)
		}
		// Tolerate scheduling jitter; the point is the resolved bucket, not the ns.
		if got < c.want-2*time.Second || got > c.want+2*time.Second {
			t.Errorf("%s: deadline ~%v from now, want ~%v", c.action, got, c.want)
		}
		cancel()
	}
}

// TestRunner_WithActionTimeout_BindsDeadline checks withActionTimeout binds
// the returned context's deadline to the resolved budget — again without
// waiting for any deadline to actually elapse.
func TestRunner_WithActionTimeout_BindsDeadline(t *testing.T) {
	r := Runner{}
	before := time.Now()
	ctx, cancel := r.withActionTimeout(context.Background(), "firmware_install")
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline on the returned context")
	}
	got := dl.Sub(before)
	// Generous slack for scheduling jitter; we only care this is ~600s, not 45s.
	if got < 590*time.Second || got > 610*time.Second {
		t.Errorf("firmware_install deadline ~%v from now, want ~600s", got)
	}
}

// TestRunner_Execute_FastAction_NotRelabeledTimeout guards the status ==
// "err" gate in Execute: a normal (non-timeout) failure must stay "err", not
// get relabeled "timeout" just because some other unrelated deadline logic
// runs.
func TestRunner_Execute_FastAction_NotRelabeledTimeout(t *testing.T) {
	r := Runner{
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("boom"), errors.New("simulated exec failure")
		},
		ActionTimeout: func(string) time.Duration { return time.Hour },
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "fast-err",
		Action: "tunnel_enable",
		Args:   map[string]any{"ndms_name": "nwg1"},
	})
	if res.Status != "err" {
		t.Fatalf("status = %q, want %q (output=%q)", res.Status, "err", res.Output)
	}
}
