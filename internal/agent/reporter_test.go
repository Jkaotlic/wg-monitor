package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeSender struct {
	mu      sync.Mutex
	reports []wire.Report
	hits    int32
	errOn   int32
	err     error
}

func (f *fakeSender) SendReport(ctx context.Context, r wire.Report) error {
	n := atomic.AddInt32(&f.hits, 1)
	f.mu.Lock()
	f.reports = append(f.reports, r)
	f.mu.Unlock()
	if f.errOn > 0 && n <= f.errOn {
		return f.err
	}
	return nil
}

func TestReporter_TicksAndSends(t *testing.T) {
	fs := &fakeSender{}
	rep := NewReporter(fs, "0.1.0", 30*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
	defer cancel()
	rep.Run(ctx)
	hits := atomic.LoadInt32(&fs.hits)
	if hits < 3 || hits > 5 {
		t.Errorf("hits=%d want 3..5 (3 ticks + initial)", hits)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.reports) == 0 {
		t.Fatal("no reports recorded")
	}
	first := fs.reports[0]
	if first.AgentVersion != "0.1.0" {
		t.Errorf("agent_version: %q", first.AgentVersion)
	}
	if len(first.Checks) != 1 || first.Checks[0].Name != "agent_heartbeat" || first.Checks[0].Status != "ok" {
		t.Errorf("expected single agent_heartbeat=ok check, got: %+v", first.Checks)
	}
}

func TestReporter_StopsOnContextCancel(t *testing.T) {
	fs := &fakeSender{}
	rep := NewReporter(fs, "0.1.0", 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rep.Run(ctx); close(done) }()
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestReporter_ContinuesAfterSendError(t *testing.T) {
	fs := &fakeSender{errOn: 1, err: testErr("temp fail")}
	rep := NewReporter(fs, "0.1.0", 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	rep.Run(ctx)
	if atomic.LoadInt32(&fs.hits) < 3 {
		t.Errorf("only %d hits — reporter should keep ticking despite first failure", fs.hits)
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }
