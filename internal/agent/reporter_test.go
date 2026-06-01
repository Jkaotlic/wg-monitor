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

type fakeMultiCheck struct {
	group string
	calls *atomic.Int32
	out   []wire.Check
}

func (m *fakeMultiCheck) Group() string { return m.group }
func (m *fakeMultiCheck) Run(_ context.Context, _ checks.Deps) []wire.Check {
	m.calls.Add(1)
	return m.out
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

func TestReporterFansOutAcrossChecksAndMultiChecks(t *testing.T) {
	c1 := atomic.Int32{}
	c2 := atomic.Int32{}
	mc := atomic.Int32{}
	chks := []checks.Check{
		&fakeCheck{name: "awg_manager", status: "ok", calls: &c1},
		&fakeCheck{name: "dns", status: "fail", calls: &c2},
	}
	multi := []checks.MultiCheck{
		&fakeMultiCheck{group: "tunnels", calls: &mc, out: []wire.Check{
			{Name: "tunnel_awg11", Status: "ok"},
			{Name: "tunnel_awg12", Status: "fail"},
		}},
	}
	s := &fakeSender{}
	r := NewReporter(ReporterConfig{
		Sender:      s,
		Version:     "test",
		Interval:    10 * time.Millisecond,
		Checks:      chks,
		MultiChecks: multi,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(35 * time.Millisecond)
	cancel()
	if c1.Load() < 2 || c2.Load() < 2 || mc.Load() < 2 {
		t.Fatalf("not all ran: c1=%d c2=%d mc=%d", c1.Load(), c2.Load(), mc.Load())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 2 single + 2 from multi + 1 agent_heartbeat = 5
	if len(s.last.Checks) != 5 {
		t.Fatalf("checks in report: %d (%+v)", len(s.last.Checks), s.last.Checks)
	}
}

func TestReporterMarksResumedAfterGap(t *testing.T) {
	s := &fakeSender{}
	r := NewReporter(ReporterConfig{
		Sender:   s,
		Version:  "test",
		Interval: time.Hour, // long enough that only the eager first send fires
	})
	// Pretend the previous report happened > ResumedThreshold ago.
	r.lastReportAt = time.Now().Add(-2 * ResumedThreshold)

	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n != 1 {
		t.Fatalf("expected exactly one send, got %d", s.n)
	}
	if !s.last.Resumed {
		t.Fatalf("expected first report after gap to have Resumed=true: %+v", s.last)
	}
}

func TestReporterDoesNotMarkResumedAfterShortMobileJitter(t *testing.T) {
	s := &fakeSender{}
	r := NewReporter(ReporterConfig{
		Sender:   s,
		Version:  "test",
		Interval: time.Hour,
	})
	r.lastReportAt = time.Now().Add(-6 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n != 1 {
		t.Fatalf("expected exactly one send, got %d", s.n)
	}
	if s.last.Resumed {
		t.Fatalf("short mobile jitter must not be Resumed=true: %+v", s.last)
	}
}

func TestReporterFreshStartIsNotResumed(t *testing.T) {
	s := &fakeSender{}
	r := NewReporter(ReporterConfig{
		Sender:   s,
		Version:  "test",
		Interval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last.Resumed {
		t.Fatalf("fresh start should not be Resumed: %+v", s.last)
	}
}
