package provision

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// BACK-01: relay сообщает класс отказа структурной меткой __WG_ERR__ в
// stdout. Раньше подсказка искала подстроки в relayErr, а от DefaultRelay
// там только «exit status 1» — «root-пароль не подошёл» не выдавался никогда,
// и оживление повторяло вход со старым паролем.

func runErrMarkerJob(t *testing.T, relay RelayFunc, budget time.Duration) Job {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	deps := Deps{
		Store:    NewStore(),
		BaseCtx:  context.Background(),
		Relay:    relay,
		LastSeen: func(string) (time.Time, bool) { return clock.now(), true },
		Now:      clock.now,
	}
	id, err := deps.Start(StartReq{
		Kind:          KindProvision,
		Nickname:      "router-errmarker",
		InstallBudget: budget,
		VerifyBudget:  time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return waitForTerminal(t, deps.Store, id, 5*time.Second)
}

func TestRunner_RelayErrMarker_RootAuth(t *testing.T) {
	relay := &scriptedRelay{lines: []string{"\r__WG_ERR__ root_auth_failed\r"}, rc: 1, err: errors.New("exit status 1")}
	job := runErrMarkerJob(t, relay.run, 2*time.Second)
	if job.State != StateFailed {
		t.Fatalf("State = %q, want failed", job.State)
	}
	if job.Hint != HintRootAuthFailed {
		t.Fatalf("Hint = %q, want %q", job.Hint, HintRootAuthFailed)
	}
}

func TestRunner_RelayErrMarker_AuthFailed(t *testing.T) {
	relay := &scriptedRelay{lines: []string{"__WG_ERR__ auth_failed"}, rc: 1, err: errors.New("exit status 1")}
	job := runErrMarkerJob(t, relay.run, 2*time.Second)
	if job.Hint != HintAuthFailed {
		t.Fatalf("Hint = %q, want %q", job.Hint, HintAuthFailed)
	}
}

func TestRunner_RelayErrMarker_SessionActive(t *testing.T) {
	relay := &scriptedRelay{lines: []string{"__WG_ERR__ session_active"}, rc: 1, err: errors.New("exit status 1")}
	job := runErrMarkerJob(t, relay.run, 2*time.Second)
	want := terminalConnectHint(errors.New("session_active"))
	if job.Hint != want || job.Hint == terminalConnectHint(nil) {
		t.Fatalf("Hint = %q, want busy-terminal hint %q", job.Hint, want)
	}
}

func TestRunner_RelayErrMarker_TimeoutWins(t *testing.T) {
	relay := &scriptedRelay{lines: []string{"__WG_ERR__ root_auth_failed"}, hang: true}
	job := runErrMarkerJob(t, relay.run, 50*time.Millisecond)
	if job.State != StateFailed {
		t.Fatalf("State = %q, want failed", job.State)
	}
	if job.Hint != hintFor(StepTerminalConnected, context.DeadlineExceeded) {
		t.Fatalf("Hint = %q, want timeout hint", job.Hint)
	}
}

func TestRunner_RelayErrMarker_IgnoredOnSuccess(t *testing.T) {
	relay := &scriptedRelay{lines: []string{"__WG_ERR__ root_auth_failed"}, rc: 0}
	job := runErrMarkerJob(t, relay.run, 2*time.Second)
	if job.State == StateFailed {
		t.Fatalf("job failed on rc=0 with a stray marker: hint=%q", job.Hint)
	}
}

// Настоящий DefaultRelay: stderr слит со stdout, ошибка -- голый
// «exit status 1». Метка обязана дойти до подсказки именно по этому пути.
func TestDefaultRelay_ErrMarker(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found")
	}
	script := filepath.Join(t.TempDir(), "fake-relay.py")
	body := "import sys\n" +
		"print('__WG_ERR__ root_auth_failed', flush=True)\n" +
		"print('root_auth_failed: router terminal refused the root login', file=sys.stderr)\n" +
		"sys.exit(1)\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	relay := func(ctx context.Context, _ string, jobJSON []byte, onLine func(string)) (int, error) {
		return DefaultRelay(ctx, script, jobJSON, onLine)
	}
	job := runErrMarkerJob(t, relay, 10*time.Second)
	if job.Hint != HintRootAuthFailed {
		t.Fatalf("Hint = %q, want %q", job.Hint, HintRootAuthFailed)
	}
}

func TestParseErrLine(t *testing.T) {
	cases := []struct {
		in   string
		code string
		ok   bool
	}{
		{"__WG_ERR__ root_auth_failed", "root_auth_failed", true},
		{"__WG_ERR__ auth_failed", "auth_failed", true},
		{"__WG_ERR__ session_active", "session_active", true},
		{"__WG_ERR__ ", "", false},
		{"__WG_ERR__", "", false},
		{"__WG_ERR__ something_else", "", false},
		{"echo __WG_ERR__ root_auth_failed", "", false},
		{"__WG_STEP__ terminal_connected", "", false},
	}
	for _, c := range cases {
		code, ok := ParseErrLine(c.in)
		if code != c.code || ok != c.ok {
			t.Errorf("ParseErrLine(%q) = (%q,%v), want (%q,%v)", c.in, code, ok, c.code, c.ok)
		}
	}
}
