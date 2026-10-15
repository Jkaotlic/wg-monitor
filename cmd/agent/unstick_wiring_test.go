package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/unstick"
)

func TestBuildUnstick(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &agent.Config{}
	w, c := buildUnstick(cfg, awgmgr.New("http://127.0.0.1:1"), nil, log)
	if w == nil || c == nil || c.Name() != unstick.CheckName {
		t.Fatalf("default: w=%v c=%v", w, c)
	}
	if ch, ok := c.(unstick.Check); !ok || ch.Disabled || ch.Source == nil {
		t.Errorf("default check: %#v", c)
	}
	off := false
	cfg.Unstick.Enabled = &off
	w, c = buildUnstick(cfg, awgmgr.New("http://127.0.0.1:1"), nil, log)
	if w != nil {
		t.Error("disabled config still built a watcher")
	}
	if ch, ok := c.(unstick.Check); !ok || !ch.Disabled {
		t.Errorf("disabled check: %#v", c)
	}
}

func TestAwgmServiceRestart_DetachedWithDeadline(t *testing.T) {
	var got context.Context
	var errInside error
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		got, errInside = ctx, ctx.Err()
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := awgmServiceRestart(exec)(ctx); err != nil {
		t.Fatal(err)
	}
	if got == nil || errInside != nil {
		t.Fatalf("exec ctx must be detached from cancellation: %v", got)
	}
	dl, ok := got.Deadline()
	if !ok || time.Until(dl) < 80*time.Second || time.Until(dl) > 91*time.Second {
		t.Errorf("deadline %v ok=%v", dl, ok)
	}
}

func TestUnstickThreshold(t *testing.T) {
	for in, want := range map[int]time.Duration{-5: 0, 0: 0, 1: 30 * time.Second, 29: 30 * time.Second, 30: 30 * time.Second, 200: 200 * time.Second} {
		if got := unstickThreshold(in); got != want {
			t.Errorf("%d: %v want %v", in, got, want)
		}
	}
}

func TestUpgradeInProgress(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "opkg.lock")
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	f := upgradeInProgress(dir, func() time.Time { return now })
	if f() {
		t.Fatal("no lock dir -- no upgrade")
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !f() {
		t.Error("fresh lock dir (1h) must mean an upgrade is running")
	}
	if err := os.Chtimes(dir, now.Add(-3*time.Hour), now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f() {
		t.Error("lock dir older than 2h is abandoned")
	}
}

// AG-06: замок с pid мёртвого владельца (агента убили посреди opkg) --
// брошен сразу, а не через 2 ч; с pid живого -- обновление идёт, даже если
// каталог старше 2 ч.
func TestUpgradeInProgress_OwnerPid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "opkg.lock")
	now := time.Now()
	f := upgradeInProgress(dir, func() time.Time { return now })
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	writePid := func(pid int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	writePid(cmd.Process.Pid)
	if f() {
		t.Error("fresh lock of a dead owner must be abandoned")
	}
	writePid(os.Getpid())
	if !f() {
		t.Error("lock of a live owner must mean an upgrade is running")
	}
	// живой (или переиспользованный) pid держит не дольше прежнего потолка 2 ч
	if err := os.Chtimes(dir, now.Add(-3*time.Hour), now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f() {
		t.Error("live-pid lock older than 2h must not silence the watcher")
	}
}

func TestRunUnstick_NilIsNoop(t *testing.T) {
	start := time.Now()
	runUnstick(context.Background(), nil, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("nil watcher wait took %v", d)
	}
}

func TestRunLoopBounded_WaitsForFinish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	wait := runLoopBounded(ctx, func(c context.Context) {
		<-c.Done()
		time.Sleep(50 * time.Millisecond)
		close(finished)
	}, 5*time.Second, "unstick", slog.New(slog.NewTextHandler(io.Discard, nil)))
	cancel()
	wait()
	select {
	case <-finished:
	default:
		t.Fatal("wait returned before the loop finished its step")
	}
}

func TestRunLoopBounded_WaitIsBounded(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	wait := runLoopBounded(ctx, func(context.Context) {
		close(started)
		<-block
	}, 50*time.Millisecond, "unstick", slog.New(slog.NewTextHandler(io.Discard, nil)))
	<-started
	cancel()
	start := time.Now()
	wait()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("wait not bounded: %v", d)
	}
}
