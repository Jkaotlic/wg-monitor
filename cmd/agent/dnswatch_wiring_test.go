package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/internal/agent/dnswatch"
)

func recordingExec(calls *[]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
}

// syncBuffer is a log sink safe to read while the watchdog goroutine writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func tempEnabledConfig(t *testing.T) *agent.Config {
	cfg := enabledAgentConfig()
	cfg.State.Path = filepath.Join(t.TempDir(), "reporter-state.json")
	return cfg
}

// TestRunDNSWatchdog_WaitsForTheLoopOnShutdown: main waits for the watchdog
// to finish the step in progress, so a shutdown can't kill the process in the
// middle of a switch.
func TestRunDNSWatchdog_WaitsForTheLoopOnShutdown(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	exec := func(context.Context, string, ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		return nil, nil
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	w, _ := buildDNSWatchdog(tempEnabledConfig(t), exec, logger)
	ctx, cancel := context.WithCancel(context.Background())
	wait := runDNSWatchdog(ctx, w, 5*time.Second, logger)
	for deadline := time.Now().Add(2 * time.Second); ; {
		mu.Lock()
		n := reads
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watchdog loop never ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	start := time.Now()
	wait()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("wait took %v for an idle loop", d)
	}
	if strings.Contains(logs.String(), "did not finish") {
		t.Fatalf("loop finished, no warning expected:\n%s", logs.String())
	}
}

// TestRunDNSWatchdog_WaitIsBounded: a hung step (ndmc ignoring its context)
// must not hold the agent's shutdown forever.
func TestRunDNSWatchdog_WaitIsBounded(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	entered := make(chan struct{}, 1)
	exec := func(context.Context, string, ...string) ([]byte, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
		return nil, nil
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	w, _ := buildDNSWatchdog(tempEnabledConfig(t), exec, logger)
	ctx, cancel := context.WithCancel(context.Background())
	wait := runDNSWatchdog(ctx, w, 50*time.Millisecond, logger)
	select { // the stop must arrive while a step is in progress
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog step never started")
	}
	cancel()
	start := time.Now()
	wait()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("wait not bounded: %v", d)
	}
	if !strings.Contains(logs.String(), "did not finish") {
		t.Fatalf("a hung step must be logged, logs:\n%s", logs.String())
	}
}

func TestRunDNSWatchdog_NilIsNoop(t *testing.T) {
	runDNSWatchdog(context.Background(), nil, time.Second, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))()
}

func TestBuildDNSWatchdog_OffByDefault(t *testing.T) {
	var calls []string
	w, c := buildDNSWatchdog(&agent.Config{}, recordingExec(&calls), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if w != nil || c != nil {
		t.Fatalf("disabled block must add neither the loop nor the check, got %v %v", w, c)
	}
	if len(calls) != 0 {
		t.Fatalf("no exec expected, got %q", calls)
	}
}

// TestBuildDNSWatchdog_ConfigErrorStaysOffAndIsLogged: a block LoadConfig
// switched off as unusable is logged loudly at start — silently off would
// look like a guarded router.
func TestBuildDNSWatchdog_ConfigErrorStaysOffAndIsLogged(t *testing.T) {
	var logs bytes.Buffer
	cfg := &agent.Config{DNSWatchdog: agent.DNSWatchdogConfig{ConfigError: "dns_watchdog.endpoint must be an https:// URL with a host"}}
	w, c := buildDNSWatchdog(cfg, recordingExec(new([]string)), slog.New(slog.NewTextHandler(&logs, nil)))
	if w != nil || c != nil {
		t.Fatalf("unusable block must stay off, got %v %v", w, c)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "must be an https:// URL") {
		t.Fatalf("config error must be logged loudly, logs:\n%s", logs.String())
	}
}

func enabledAgentConfig() *agent.Config {
	return &agent.Config{
		State: agent.StateConfig{Path: "/tmp/wgm-test/reporter-state.json"},
		DNSWatchdog: agent.DNSWatchdogConfig{
			Enabled:           true,
			Endpoint:          "https://dns.example.com/secret-path",
			CanaryDomain:      "probe.example.com",
			RUCanary:          "ya.ru",
			BootstrapIP:       "203.0.113.10",
			IntervalSec:       30,
			FailThreshold:     3,
			OKThreshold:       4,
			CooldownSec:       600,
			MaxForeign:        2,
			RUZones:           []string{"ru"},
			RUCandidates:      []string{"tls upstream common.dot.dns.yandex.net"},
			ForeignCandidates: []string{"https upstream https://dns.quad9.net/dns-query"},
			PinnedZones:       []string{"tmdb.org"},
			PinnedCandidate:   "https upstream https://cloudflare-dns.com/dns-query",
		},
	}
}

// TestBuildDNSWatchdog_EnabledAddsResolverGuard: an enabled block yields the
// loop and the thin resolver_guard check; building them touches nothing on the
// router (the loop acts only once main starts it).
func TestBuildDNSWatchdog_EnabledAddsResolverGuard(t *testing.T) {
	var calls []string
	w, c := buildDNSWatchdog(enabledAgentConfig(), recordingExec(&calls), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if w == nil || c == nil {
		t.Fatalf("enabled block must build the watchdog and its check, got %v %v", w, c)
	}
	if c.Name() != "resolver_guard" {
		t.Fatalf("check name = %q", c.Name())
	}
	got := c.Run(context.Background(), checks.Deps{})
	if got.Status != "ok" || got.Details["mode"] != "primary" {
		t.Fatalf("before the first tick: %s %#v", got.Status, got.Details)
	}
	if len(calls) != 0 {
		t.Fatalf("building must not run ndmc, got %q", calls)
	}
}

func TestDNSWatchdogConfig_MapsEveryField(t *testing.T) {
	cfg := enabledAgentConfig()
	got := dnsWatchdogConfig(cfg)
	w := cfg.DNSWatchdog
	want := dnswatch.Config{
		Endpoint:          w.Endpoint,
		CanaryDomain:      w.CanaryDomain,
		RUCanary:          w.RUCanary,
		BootstrapIP:       w.BootstrapIP,
		Interval:          30 * time.Second,
		FailThreshold:     3,
		OKThreshold:       4,
		Cooldown:          600 * time.Second,
		MaxForeign:        2,
		RUZones:           w.RUZones,
		RUCandidates:      w.RUCandidates,
		ForeignCandidates: w.ForeignCandidates,
		PinnedZones:       w.PinnedZones,
		PinnedCandidate:   w.PinnedCandidate,
		StatePath:         "/tmp/wgm-test/dns-watchdog-state.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dnsWatchdogConfig:\n got %+v\nwant %+v", got, want)
	}
}
