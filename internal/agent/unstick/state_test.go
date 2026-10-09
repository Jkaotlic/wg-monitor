package unstick

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newOnDisk(t *testing.T, path string, h *harness) *Watcher {
	t.Helper()
	w := New(Config{Enabled: true, StatePath: path}, Deps{
		AWG:            h.awg,
		RestartService: func(context.Context) error { h.services++; return nil },
		Now:            h.clk.now,
		Sleep:          h.clk.sleep,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	w.clearStartGuard()
	return w
}

func TestState_SurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unstick-state.json")
	h := newHarness(tun("nwg0", "broken", true))
	h.w = newOnDisk(t, path, h)
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx) // сдался, ступень 2 записана
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm %v", info.Mode().Perm())
	}
	// «рестарт агента»
	w2 := newOnDisk(t, path, h)
	if s := w2.Snapshot(); len(s.GaveUp) != 1 {
		t.Fatalf("gave_up lost: %+v", s)
	}
	if f := w2.Facts(); f == nil || len(f.Events) != 1 {
		t.Fatalf("journal lost: %+v", f)
	}
	h.w = w2
	h.awg.set("nwg0", "running") // снимет «сдался»
	h.w.Tick(ctx)
	h.awg.set("nwg0", "broken")
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx)
	if h.services != 1 {
		t.Errorf("service-restart stamp lost across restart: services=%d", h.services)
	}
}

func TestState_CorruptFileIsCleanStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unstick-state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(tun("nwg0", "running", true))
	w := newOnDisk(t, path, h)
	if s := w.Snapshot(); len(s.GaveUp) != 0 {
		t.Fatalf("corrupt file produced state: %+v", s)
	}
}

func TestState_NoRewriteWithoutChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unstick-state.json")
	h := newHarness(tun("nwg0", "running", true))
	h.w = newOnDisk(t, path, h)
	h.w.Tick(ctx)
	st1, _ := os.Stat(path)
	if st1 == nil {
		t.Fatal("state file not written by the first Tick")
	}
	h.clk.advance(time.Minute)
	h.w.Tick(ctx)
	st2, _ := os.Stat(path)
	if st1 != nil && st2 != nil && !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("state rewritten without change (flash wear)")
	}
}

func TestState_StaleTmpKeepsNoOldMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unstick-state.json")
	if err := os.WriteFile(path+".tmp", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".tmp", 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(tun("nwg0", "running", true))
	h.w = newOnDisk(t, path, h)
	h.w.Tick(ctx)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm %v", info.Mode().Perm())
	}
}

func TestState_GaveUpForGoneTunnelIsCleared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unstick-state.json")
	h := newHarness(tun("nwg0", "broken", true))
	h.w = newOnDisk(t, path, h)
	h.w.Tick(ctx)
	h.clk.advance(61 * time.Second)
	h.w.Tick(ctx) // сдался
	if len(h.w.Snapshot().GaveUp) != 1 {
		t.Fatal("setup: no give-up")
	}
	h.awg.mu.Lock()
	h.awg.tunnels = nil
	h.awg.mu.Unlock()
	w2 := newOnDisk(t, path, h)
	w2.Tick(ctx)
	if s := w2.Snapshot(); len(s.GaveUp) != 0 {
		t.Fatalf("stale gave_up for a deleted tunnel: %+v", s)
	}
}
