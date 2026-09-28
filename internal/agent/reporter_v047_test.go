package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type hookSender struct {
	*fakeSender
	allowed bool
}

func (h *hookSender) HookReportsAllowed() bool { return h.allowed }

func waitSends(t *testing.T, s *fakeSender, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := s.n
		s.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("не дождались %d отправок", n)
}

func TestReporterWakeSendsHookReportWhenBackendAllows(t *testing.T) {
	s := &hookSender{fakeSender: &fakeSender{}, allowed: true}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	waitSends(t, s.fakeSender, 1)
	r.RequestWake()
	waitSends(t, s.fakeSender, 2)
	s.mu.Lock()
	trig := s.last.Trigger
	s.mu.Unlock()
	if trig != wire.TriggerHook {
		t.Fatalf("внеочередной отчёт без trigger=hook: %q", trig)
	}
	r.ForceResumed(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last.Trigger != "" {
		t.Fatalf("метка хука прилипла к следующему отчёту: %q", s.last.Trigger)
	}
}

// Бэкенд v0.46 не знает trigger и посчитал бы отчёт в автомат тревог.
func TestReporterWakeIgnoredWithoutBackendSupport(t *testing.T) {
	for name, s := range map[string]Sender{
		"старый отправитель": &fakeSender{},
		"бэкенд не объявил":  &hookSender{fakeSender: &fakeSender{}},
	} {
		r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
		ctx, cancel := context.WithCancel(context.Background())
		go r.Run(ctx)
		time.Sleep(50 * time.Millisecond)
		r.RequestWake()
		time.Sleep(100 * time.Millisecond)
		cancel()
		var fs *fakeSender
		switch v := s.(type) {
		case *fakeSender:
			fs = v
		case *hookSender:
			fs = v.fakeSender
		}
		fs.mu.Lock()
		n := fs.n
		fs.mu.Unlock()
		if n != 1 {
			t.Errorf("%s: отправок %d, хотим только первую", name, n)
		}
	}
}

type fakeFacts struct {
	mu        sync.Mutex
	give      *wire.ReportFacts
	committed []*wire.ReportFacts
}

func (f *fakeFacts) Collect(context.Context) *wire.ReportFacts { return f.give }
func (f *fakeFacts) Committed(s *wire.ReportFacts) {
	f.mu.Lock()
	f.committed = append(f.committed, s)
	f.mu.Unlock()
}

type errSender struct{}

func (errSender) SendReport(context.Context, wire.Report) (string, error) {
	return "", errors.New("backend down")
}

func TestReporterAttachesFactsAndCommitsOnSuccess(t *testing.T) {
	facts := &fakeFacts{give: &wire.ReportFacts{Hooks: &wire.HookFacts{State: "installed"}}}
	s := &fakeSender{}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour, Facts: facts})
	r.sendOnce(context.Background())
	if s.last.Facts != facts.give {
		t.Fatalf("факты не приложены: %+v", s.last.Facts)
	}
	if len(facts.committed) != 1 || facts.committed[0] != facts.give {
		t.Fatalf("committed = %+v", facts.committed)
	}
}

func TestReporterDoesNotCommitFactsOnFailure(t *testing.T) {
	facts := &fakeFacts{give: &wire.ReportFacts{Hooks: &wire.HookFacts{State: "installed"}}}
	r := NewReporter(ReporterConfig{Sender: errSender{}, Version: "test", Interval: time.Hour, Facts: facts})
	r.sendOnce(context.Background())
	if len(facts.committed) != 0 {
		t.Fatal("неотправленные факты помечены отправленными: серии пингчека потеряются")
	}
}
