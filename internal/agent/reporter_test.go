package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent/checks"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeCheck struct {
	name   string
	calls  *atomic.Int32
	status string
}

func (f *fakeCheck) Name() string { return f.name }
func (f *fakeCheck) Run(_ context.Context, _ checks.Deps) wire.Check {
	f.calls.Add(1)
	return wire.Check{Name: f.name, Status: f.status, DurationMs: 1}
}

type fakeSender struct {
	mu   sync.Mutex
	last wire.Report
	n    int
}

func (s *fakeSender) SendReport(_ context.Context, r wire.Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = r
	s.n++
	return nil
}

func TestReporterFansOutAcrossChecks(t *testing.T) {
	c1 := atomic.Int32{}
	c2 := atomic.Int32{}
	chks := []checks.Check{
		&fakeCheck{name: "awg_handshake", status: "ok", calls: &c1},
		&fakeCheck{name: "dns_doh", status: "fail", calls: &c2},
	}
	s := &fakeSender{}
	r := NewReporter(s, "test", 10*time.Millisecond, chks, checks.Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(35 * time.Millisecond)
	cancel()
	if c1.Load() < 2 || c2.Load() < 2 {
		t.Fatalf("checks were not all run: c1=%d c2=%d", c1.Load(), c2.Load())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.last.Checks) != 3 { // 2 user-defined + agent_heartbeat
		t.Fatalf("checks in report: %d (%+v)", len(s.last.Checks), s.last.Checks)
	}
}
