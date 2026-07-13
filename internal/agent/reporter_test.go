package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	mu              sync.Mutex
	last            wire.Report
	n               int
	returnCanonical string
}

func (s *fakeSender) SendReport(_ context.Context, r wire.Report) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = r
	s.n++
	return s.returnCanonical, nil
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

func TestReporterMigratesURLOnCanonicalDiff(t *testing.T) {
	var gotURL, gotConfig string
	old := reporterMigrateURL
	reporterMigrateURL = func(_ context.Context, newURL, configPath string) (string, error) {
		gotURL = newURL
		gotConfig = configPath
		return "ok", nil
	}
	defer func() { reporterMigrateURL = old }()

	s := &fakeSender{returnCanonical: "https://new.example.com"}
	r := NewReporter(ReporterConfig{
		Sender:     s,
		Version:    "test",
		Interval:   time.Hour,
		BackendURL: "https://old.example.com",
		ConfigPath: "/opt/etc/wg-monitor/config.yaml",
	})
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()

	if gotURL != "https://new.example.com" {
		t.Errorf("migrate URL: got %q, want https://new.example.com", gotURL)
	}
	if gotConfig != "/opt/etc/wg-monitor/config.yaml" {
		t.Errorf("migrate config: got %q", gotConfig)
	}
}

func TestReporterDoesNotRepeatSuccessfulCanonicalMigration(t *testing.T) {
	var calls atomic.Int32
	old := reporterMigrateURL
	reporterMigrateURL = func(_ context.Context, _, _ string) (string, error) {
		calls.Add(1)
		return "ok", nil
	}
	defer func() { reporterMigrateURL = old }()

	s := &fakeSender{returnCanonical: "https://new.example.com"}
	r := NewReporter(ReporterConfig{
		Sender:     s,
		Version:    "test",
		Interval:   time.Hour,
		BackendURL: "https://old.example.com",
		ConfigPath: "/opt/etc/wg-monitor/config.yaml",
	})
	r.sendOnce(context.Background())
	r.sendOnce(context.Background())

	if got := calls.Load(); got != 1 {
		t.Fatalf("successful canonical URL migration repeated %d times, want 1", got)
	}
}

func TestReporterSkipsMigrationWhenCanonicalMatches(t *testing.T) {
	called := false
	old := reporterMigrateURL
	reporterMigrateURL = func(_ context.Context, _, _ string) (string, error) {
		called = true
		return "", nil
	}
	defer func() { reporterMigrateURL = old }()

	s := &fakeSender{returnCanonical: "https://same.example.com"}
	r := NewReporter(ReporterConfig{
		Sender:     s,
		Version:    "test",
		Interval:   time.Hour,
		BackendURL: "https://same.example.com",
		ConfigPath: "/opt/etc/wg-monitor/config.yaml",
	})
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()

	if called {
		t.Error("migration must not be called when canonical URL matches config URL")
	}
}

func TestReporterSkipsMigrationWithNoConfigPath(t *testing.T) {
	called := false
	old := reporterMigrateURL
	reporterMigrateURL = func(_ context.Context, _, _ string) (string, error) {
		called = true
		return "", nil
	}
	defer func() { reporterMigrateURL = old }()

	s := &fakeSender{returnCanonical: "https://new.example.com"}
	r := NewReporter(ReporterConfig{
		Sender:     s,
		Version:    "test",
		Interval:   time.Hour,
		BackendURL: "https://old.example.com",
		// ConfigPath intentionally empty
	})
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()

	if called {
		t.Error("migration must not be called when config path is empty")
	}
}

func TestReporter_AuthReject_RecordsBreadcrumb(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "reporter-state.json")

	authErr := fmt.Errorf("%w: /v1/report status=401", ErrUnauthorized)
	sender := &stubSender{err: authErr}
	r := NewReporter(ReporterConfig{
		Sender:    sender,
		Version:   "test",
		Interval:  time.Minute,
		StatePath: statePath,
	})

	r.sendOnce(context.Background())
	r.sendOnce(context.Background())

	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	var st reporterState
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveAuthRejects != 2 {
		t.Fatalf("ConsecutiveAuthRejects = %d, want 2", st.ConsecutiveAuthRejects)
	}
	if st.LastAuthErrorAt.IsZero() {
		t.Fatal("LastAuthErrorAt should be set after a 401")
	}

	// A subsequent success resets the counter.
	sender.err = nil
	sender.url = ""
	r.sendOnce(context.Background())
	body, _ = os.ReadFile(statePath)
	// The breadcrumb keys must be entirely absent from a healthy agent's
	// state file (omitzero), so a single `cat` self-describes "no auth
	// problem" — assert on the raw bytes, not just the Go value, because a
	// zero time.Time under `omitempty` would silently persist as
	// "0001-01-01T00:00:00Z" and defeat the whole diagnosis goal.
	if bytes.Contains(body, []byte("last_auth_error_at")) {
		t.Fatalf("healthy state file must not contain last_auth_error_at: %s", body)
	}
	if bytes.Contains(body, []byte("consecutive_auth_rejects")) {
		t.Fatalf("healthy state file must not contain consecutive_auth_rejects: %s", body)
	}
	// Reset st: the breadcrumb keys are correctly absent from the JSON once
	// back to zero, but json.Unmarshal only overwrites fields present in the
	// source, so a stale value would otherwise survive from the earlier read.
	st = reporterState{}
	_ = json.Unmarshal(body, &st)
	if st.ConsecutiveAuthRejects != 0 {
		t.Fatalf("ConsecutiveAuthRejects after success = %d, want 0", st.ConsecutiveAuthRejects)
	}
	if st.LastReportAt.IsZero() {
		t.Fatal("LastReportAt should be set after a successful report")
	}
}

type stubSender struct {
	url string
	err error
}

func (s *stubSender) SendReport(context.Context, wire.Report) (string, error) {
	return s.url, s.err
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
