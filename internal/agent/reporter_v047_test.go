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

// recSender запоминает каждый отчёт (с меткой хука и Resumed).
type recSender struct {
	mu      sync.Mutex
	reports []wire.Report
	block   chan struct{} // не nil -- первая отправка ждёт, пока его не закроют
	blocked bool
}

func (s *recSender) HookReportsAllowed() bool { return true }

func (s *recSender) SendReport(_ context.Context, r wire.Report) (string, error) {
	s.mu.Lock()
	wait := s.block != nil && !s.blocked
	if wait {
		s.blocked = true
	}
	ch := s.block
	s.mu.Unlock()
	if wait {
		<-ch
	}
	s.mu.Lock()
	s.reports = append(s.reports, r)
	s.mu.Unlock()
	return "", nil
}

func (s *recSender) snapshot() []wire.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wire.Report(nil), s.reports...)
}

// I1: интерфейс флапает -- хук каждые 40 с при интервале 60 с. Внеочередные
// отчёты не двигают автомат тревог, поэтому не смеют откладывать плановые:
// за 5 минут плановых должно быть не меньше 4 (масштаб 1 с = 1 мс).
func TestReporterHookReportsDoNotPostponeRegularCadence(t *testing.T) {
	s := &recSender{}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: 60 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	stop := time.After(300 * time.Millisecond)
	tk := time.NewTicker(40 * time.Millisecond)
loop:
	for {
		select {
		case <-tk.C:
			r.RequestWake()
		case <-stop:
			break loop
		}
	}
	tk.Stop()
	cancel()
	<-done
	regular, hook := 0, 0
	for _, rep := range s.snapshot() {
		if rep.Trigger == wire.TriggerHook {
			hook++
		} else {
			regular++
		}
	}
	if hook == 0 {
		t.Fatal("хук-отчётов нет: тест ничего не проверяет")
	}
	if regular < 4 {
		t.Fatalf("плановых отчётов %d (хук-отчётов %d): флап интерфейса заморозил автомат тревог", regular, hook)
	}
}

// Метка хука выбирается под замком отправки: принудительный отчёт, ждущий
// очереди рядом с хук-отчётом, не может уйти помеченным hook (мимо автомата).
func TestReporterForceReportNeverLabelledHookUnderConcurrency(t *testing.T) {
	s := &recSender{block: make(chan struct{})}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); r.sendOnce(ctx) }() // держит замок отправки
	time.Sleep(30 * time.Millisecond)
	go func() { defer wg.Done(); r.wakeReport(ctx) }()
	time.Sleep(30 * time.Millisecond)
	go func() { defer wg.Done(); r.ForceResumed(ctx) }()
	time.Sleep(30 * time.Millisecond)
	close(s.block)
	wg.Wait()
	reps := s.snapshot()
	if len(reps) != 3 {
		t.Fatalf("отчётов %d, хотим 3", len(reps))
	}
	sawForced, sawHook := false, false
	for _, rep := range reps[1:] {
		if rep.Resumed {
			sawForced = true
			if rep.Trigger == wire.TriggerHook {
				t.Fatalf("принудительный отчёт ушёл с trigger=hook: %+v", rep)
			}
		}
		if rep.Trigger == wire.TriggerHook {
			sawHook = true
		}
	}
	if !sawForced || !sawHook {
		t.Fatalf("ждали принудительный и хук-отчёт: forced=%v hook=%v", sawForced, sawHook)
	}
}

// M5: после отвала >30 мин хук-отчёт не съедает метку Resumed -- её несёт
// первый ПЛАНОВЫЙ отчёт (на нём держится подавление стартовых провалов мобильных).
func TestReporterHookReportDoesNotConsumeResumed(t *testing.T) {
	s := &recSender{}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
	r.lastReportAt = time.Now().Add(-2 * ResumedThreshold)
	ctx := context.Background()
	if !r.wakeReport(ctx) {
		t.Fatal("хук-отчёт не ушёл")
	}
	r.sendOnce(ctx)
	reps := s.snapshot()
	if len(reps) != 2 {
		t.Fatalf("отчётов %d", len(reps))
	}
	if reps[0].Trigger != wire.TriggerHook {
		t.Fatalf("первый не хук: %+v", reps[0])
	}
	if !reps[1].Resumed || reps[1].Trigger != "" {
		t.Fatalf("первый плановый после отвала без Resumed: %+v", reps[1])
	}
}

// M5: принудительный Resumed (force_recheck) тоже не съедается хук-отчётом.
func TestReporterHookReportKeepsForceResumed(t *testing.T) {
	s := &recSender{}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
	r.lastReportAt = time.Now()
	r.mu.Lock()
	r.forceResumed = true
	r.mu.Unlock()
	ctx := context.Background()
	r.wakeReport(ctx)
	r.sendOnce(ctx)
	reps := s.snapshot()
	if len(reps) != 2 || !reps[1].Resumed {
		t.Fatalf("метку force съел хук-отчёт: %+v", reps)
	}
}

// M3: отчёт больше 60 КиБ уходит без фактов (413 = ErrReportRejected = откат
// обновления), а неотправленные факты не помечаются отправленными.
func TestReporterDropsOversizedFacts(t *testing.T) {
	huge := make(map[string]wire.ExitProbe)
	for i := 0; i < 2000; i++ {
		huge[string(rune('a'+i%26))+time.Duration(i).String()] = wire.ExitProbe{Source: "example.com", Err: "timeout talking to 203.0.113.10"}
	}
	facts := &fakeFacts{give: &wire.ReportFacts{Exit: &wire.ExitFacts{Tunnels: huge}}}
	s := &fakeSender{}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour, Facts: facts})
	r.sendOnce(context.Background())
	if s.n != 1 {
		t.Fatalf("отчёт не ушёл: n=%d", s.n)
	}
	if s.last.Facts != nil {
		t.Fatal("раздутые факты ушли в отчёте: бэкенд ответит 413")
	}
	if len(facts.committed) != 0 {
		t.Fatal("выброшенные факты помечены отправленными: серии пингчека потеряются")
	}
}
