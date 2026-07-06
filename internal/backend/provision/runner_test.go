package provision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- shared test helpers -----------------------------------------------

// fakeClock is a small monotonically-advancing fake clock: each now() call
// nudges the instant forward by 1ms. Start's worker samples it once for
// `since` (job start) and VerifyOnline samples it again per poll — advancing
// a little on every call means "later" samples are always genuinely later,
// without needing wall-clock sleeps or a virtualClock's explicit sleep log
// (verify_test.go's own virtualClock already pins VerifyOnline's poll-loop
// timing in isolation; these tests only need "time moves forward", not
// exact poll-count/interval assertions).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

// scriptedRelay is a stub RelayFunc that streams a fixed list of lines to
// onLine and then either returns (rc, err) immediately, blocks on ctx.Done()
// to simulate a wedged subprocess only a server-owned timeout can kill
// (hang), or blocks on a test-controlled channel to hold Start's
// single-flight lock open across a second Start call (unblock).
type scriptedRelay struct {
	lines   []string
	rc      int
	err     error
	hang    bool
	unblock chan struct{}
}

func (s *scriptedRelay) run(ctx context.Context, relayPath string, jobJSON []byte, onLine func(string)) (int, error) {
	for _, l := range s.lines {
		onLine(l)
	}
	switch {
	case s.hang:
		<-ctx.Done()
		// Mirrors the real-world Windows symptom the cross-task requirement
		// is about: TerminateProcess surfaces as the same generic
		// "exit status 1" a genuine in-script `exit 1` would, so the engine
		// cannot tell timeout from failure by inspecting rc/err text alone —
		// it must check ctx.Err() itself.
		return 1, errors.New("exit status 1")
	case s.unblock != nil:
		select {
		case <-s.unblock:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	return s.rc, s.err
}

// waitForTerminal polls store.Get(jobID) on a short real-time interval until
// the job leaves StateRunning or max elapses. The worker goroutine under
// test always runs on a real goroutine (only VerifyOnline's own internal
// loop supports a virtual clock via Deps.Now), so driving it to completion
// needs a real, bounded poll rather than a fake sleep.
func waitForTerminal(t *testing.T, store *Store, jobID string, max time.Duration) Job {
	t.Helper()
	deadline := time.Now().Add(max)
	for {
		job, ok := store.Get(jobID)
		if !ok {
			t.Fatalf("job %s: not found in store", jobID)
		}
		if job.State != StateRunning {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not reach a terminal state within %s (last: state=%s steps=%+v)",
				jobID, max, job.State, job.Steps)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func stepStatus(steps []Step, name string) StepStatus {
	for _, s := range steps {
		if s.Name == name {
			return s.Status
		}
	}
	return ""
}

func stepDetail(steps []Step, name string) string {
	for _, s := range steps {
		if s.Name == name {
			return s.Detail
		}
	}
	return ""
}

// --- (a) success path ----------------------------------------------------

func TestDeps_Start_SuccessRunsAllStepsAndVerifiesOnline(t *testing.T) {
	relay := &scriptedRelay{
		lines: []string{
			"__WG_STEP__ arch_detected arm64",
			"__WG_STEP__ downloading",
			"__WG_STEP__ checksum_ok",
			"__WG_STEP__ config_written",
			"__WG_STEP__ init_installed",
			"__WG_STEP__ service_started",
		},
		rc: 0,
	}
	fresh := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // trivially after any `since` this clock produces
	lastSeenCalls := 0
	lastSeen := func(nick string) (time.Time, bool) {
		lastSeenCalls++
		if nick != "router1" {
			t.Errorf("lastSeen nick = %q, want %q", nick, "router1")
		}
		return fresh, true
	}
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	deps := Deps{
		Store:    NewStore(),
		BaseCtx:  context.Background(),
		Relay:    relay.run,
		LastSeen: lastSeen,
		Now:      clock.now,
	}

	id, err := deps.Start(StartReq{
		Kind:          KindProvision,
		Nickname:      "router1",
		InstallBudget: 2 * time.Second,
		VerifyBudget:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if id == "" {
		t.Fatal("Start returned an empty job id")
	}

	job := waitForTerminal(t, deps.Store, id, time.Second)

	if job.State != StateSuccess {
		t.Fatalf("State = %q, want %q (hint=%q tail=%q)", job.State, StateSuccess, job.Hint, job.Tail)
	}
	if lastSeenCalls != 1 {
		t.Errorf("LastSeen called %d times, want exactly 1 (stub resolves fresh on the first poll)", lastSeenCalls)
	}
	if len(job.Steps) != 8 {
		t.Fatalf("len(Steps) = %d, want 8 (the full install sequence)", len(job.Steps))
	}
	for _, st := range job.Steps {
		if st.Status != StepDone {
			t.Errorf("step %q: Status = %q, want %q", st.Name, st.Status, StepDone)
		}
	}
	if got := stepDetail(job.Steps, StepArchDetected); got != "arm64" {
		t.Errorf("arch_detected Detail = %q, want %q", got, "arm64")
	}
	if got := stepDetail(job.Steps, StepVerifyOnline); got == "" {
		t.Error("verify_online Detail is empty, want a non-empty \"first report Ns ago\"-style detail")
	}
	if job.Hint != "" {
		t.Errorf("Hint = %q, want empty on success", job.Hint)
	}
	if job.Tail != "" {
		t.Errorf("Tail = %q, want empty on success", job.Tail)
	}
}

// PTY echo adds leading indentation and a trailing \r around each real line,
// and the relay's own echo/printf-emitted marker line follows right after an
// echoed COMMAND line that merely *contains* the marker text as an argument
// (not as its own prefix) — cross-task requirement from B3's review.
func TestDeps_Start_TrimsWhitespaceAndCRBeforeParsingMarkers(t *testing.T) {
	relay := &scriptedRelay{
		lines: []string{
			"  __WG_STEP__ arch_detected arm64\r", // leading indent + trailing CR
			"\t__WG_STEP__ downloading\r",
			"echo __WG_STEP__ checksum_ok", // echoed COMMAND line: wrong prefix, must NOT match
			"__WG_STEP__ checksum_ok",      // the actual output line: this one must match
			"__WG_STEP__ config_written",
			"__WG_STEP__ init_installed",
			"__WG_STEP__ service_started",
		},
		rc: 0,
	}
	fresh := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	lastSeen := func(string) (time.Time, bool) { return fresh, true }
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	deps := Deps{
		Store:    NewStore(),
		BaseCtx:  context.Background(),
		Relay:    relay.run,
		LastSeen: lastSeen,
		Now:      clock.now,
	}

	id, err := deps.Start(StartReq{
		Kind:          KindProvision,
		Nickname:      "router-trim",
		InstallBudget: time.Second,
		VerifyBudget:  time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := waitForTerminal(t, deps.Store, id, time.Second)

	if job.State != StateSuccess {
		t.Fatalf("State = %q, want %q (hint=%q)", job.State, StateSuccess, job.Hint)
	}
	if got := stepDetail(job.Steps, StepArchDetected); got != "arm64" {
		t.Errorf("arch_detected Detail = %q, want %q (leading whitespace/CR must not break parsing)", got, "arm64")
	}
	for _, name := range []string{StepArchDetected, StepDownloading, StepChecksumOK, StepConfigWritten, StepInitInstalled, StepServiceStarted} {
		if got := stepStatus(job.Steps, name); got != StepDone {
			t.Errorf("step %q: Status = %q, want %q", name, got, StepDone)
		}
	}
}

// --- (b) mid-run failure + redaction -------------------------------------

func TestDeps_Start_MidFailMarksActiveStepFailedAndRedactsToken(t *testing.T) {
	const rawToken = "wgm_secret_9f8e7d6c5b4a"
	relay := &scriptedRelay{
		lines: []string{
			"__WG_STEP__ arch_detected arm64",
			"__WG_STEP__ downloading",
			"fetching https://backend.example/dl?token=" + rawToken,
		},
		rc:  14,
		err: errors.New("script exited: bad checksum fetch"),
	}
	lastSeenCalls := 0
	lastSeen := func(string) (time.Time, bool) {
		lastSeenCalls++
		return time.Time{}, false
	}
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	deps := Deps{
		Store:    NewStore(),
		BaseCtx:  context.Background(),
		Relay:    relay.run,
		LastSeen: lastSeen,
		Now:      clock.now,
	}

	id, err := deps.Start(StartReq{
		Kind:          KindProvision,
		Nickname:      "router2",
		RawToken:      rawToken,
		InstallBudget: 2 * time.Second,
		VerifyBudget:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	job := waitForTerminal(t, deps.Store, id, time.Second)

	if job.State != StateFailed {
		t.Fatalf("State = %q, want %q", job.State, StateFailed)
	}
	if got := stepStatus(job.Steps, StepDownloading); got != StepFailed {
		t.Errorf("downloading step Status = %q, want %q", got, StepFailed)
	}
	// Every step before the failed one must still be Done (a later marker
	// means everything before it finished), and every step after must still
	// be Pending (never reached).
	if got := stepStatus(job.Steps, StepArchDetected); got != StepDone {
		t.Errorf("arch_detected step Status = %q, want %q", got, StepDone)
	}
	if got := stepStatus(job.Steps, StepChecksumOK); got != StepPending {
		t.Errorf("checksum_ok step Status = %q, want %q (never reached)", got, StepPending)
	}
	if job.Hint == "" {
		t.Error("Hint is empty, want a friendly message")
	}
	if job.Tail == "" {
		t.Error("Tail is empty, want the redacted recent-lines transcript")
	}
	if strings.Contains(job.Tail, rawToken) {
		t.Errorf("Tail contains the raw token — redaction failed: %q", job.Tail)
	}
	if !strings.Contains(job.Tail, "«redacted»") {
		t.Errorf("Tail does not contain the redaction marker in place of the token: %q", job.Tail)
	}
	if !strings.Contains(job.Tail, "fetching https://backend.example/dl?token=") {
		t.Errorf("Tail lost the surrounding context around the redacted token: %q", job.Tail)
	}
	if lastSeenCalls != 0 {
		t.Errorf("LastSeen called %d times, want 0 — verify_online must not run after an install failure", lastSeenCalls)
	}
}

// --- (c) ctx-timeout vs script-failure ------------------------------------

func TestDeps_Start_CtxTimeoutIsLabeledDistinctlyFromScriptFailure(t *testing.T) {
	relay := &scriptedRelay{
		lines: []string{"__WG_STEP__ arch_detected arm64", "__WG_STEP__ downloading"},
		hang:  true,
	}
	lastSeenCalls := 0
	lastSeen := func(string) (time.Time, bool) {
		lastSeenCalls++
		return time.Time{}, false
	}
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	deps := Deps{
		Store:    NewStore(),
		BaseCtx:  context.Background(),
		Relay:    relay.run,
		LastSeen: lastSeen,
		Now:      clock.now,
	}

	id, err := deps.Start(StartReq{
		Kind:          KindProvision,
		Nickname:      "router3",
		InstallBudget: 50 * time.Millisecond, // real wall-clock budget: context.WithTimeout always uses the real clock
		VerifyBudget:  time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	job := waitForTerminal(t, deps.Store, id, 2*time.Second)

	if job.State != StateFailed {
		t.Fatalf("State = %q, want %q", job.State, StateFailed)
	}
	if got := stepStatus(job.Steps, StepDownloading); got != StepFailed {
		t.Errorf("downloading step Status = %q, want %q", got, StepFailed)
	}
	if job.Hint == "" {
		t.Fatal("Hint is empty on timeout")
	}
	if !strings.Contains(job.Hint, "таймаут") {
		t.Errorf("Hint = %q, want it to mention таймаут (timeout), distinctly from a plain step failure", job.Hint)
	}
	// The cross-task requirement this test exists for: on Windows, a
	// ctx-kill and a script exit-1 are indistinguishable by rc/err alone
	// (TerminateProcess surfaces as the same "exit status 1" this stub
	// returns) — so the timeout label must come from checking ctx.Err()
	// directly, not from the relay's rc/err. Prove it by comparing against
	// the hint a genuine (non-timeout) failure on the very same step, with
	// the exact same error text, would produce.
	genericHint := hintFor(StepDownloading, errors.New("exit status 1"))
	if job.Hint == genericHint {
		t.Errorf("timeout Hint == plain-failure Hint (%q) — timeout must be labeled distinctly", job.Hint)
	}
	if lastSeenCalls != 0 {
		t.Errorf("LastSeen called %d times, want 0 on an install timeout", lastSeenCalls)
	}
}

// --- (d) single-flight -----------------------------------------------------

func TestDeps_Start_SingleFlightRejectsSecondStartForSameNickname(t *testing.T) {
	unblock := make(chan struct{})
	blocking := &scriptedRelay{
		lines:   []string{"__WG_STEP__ arch_detected arm64"},
		rc:      0,
		unblock: unblock,
	}
	fresh := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	lastSeen := func(string) (time.Time, bool) { return fresh, true }
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	deps := Deps{
		Store:    NewStore(),
		BaseCtx:  context.Background(),
		Relay:    blocking.run,
		LastSeen: lastSeen,
		Now:      clock.now,
	}

	id1, err := deps.Start(StartReq{
		Kind: KindProvision, Nickname: "router4", InstallBudget: time.Second, VerifyBudget: time.Second,
	})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if id1 == "" {
		t.Fatal("first Start returned an empty job id")
	}

	id2, err2 := deps.Start(StartReq{
		Kind: KindProvision, Nickname: "router4", InstallBudget: time.Second, VerifyBudget: time.Second,
	})
	if err2 == nil {
		t.Fatal("second Start for a nickname whose first run still holds the lock: want an error, got nil")
	}
	if !errors.Is(err2, ErrAlreadyRunning) {
		t.Errorf("second Start error = %v, want errors.Is(_, ErrAlreadyRunning)", err2)
	}
	if id2 != "" {
		t.Errorf("second (rejected) Start returned a job id (%q), want empty", id2)
	}

	// A different nickname's lock is entirely independent of router4's.
	id3, err3 := deps.Start(StartReq{
		Kind: KindProvision, Nickname: "router5", InstallBudget: time.Second, VerifyBudget: time.Second,
	})
	if err3 != nil {
		t.Fatalf("Start for an unrelated nickname must not be blocked by router4's lock: %v", err3)
	}

	close(unblock) // release both router4's and router5's blocked relay stub goroutines
	job1 := waitForTerminal(t, deps.Store, id1, time.Second)
	if job1.State != StateSuccess {
		t.Errorf("router4 job State = %q, want %q (hint=%q)", job1.State, StateSuccess, job1.Hint)
	}
	job3 := waitForTerminal(t, deps.Store, id3, time.Second)
	if job3.State != StateSuccess {
		t.Errorf("router5 job State = %q, want %q (hint=%q)", job3.State, StateSuccess, job3.Hint)
	}

	// The lock must be released once the worker finishes — Start for
	// router4 again now succeeds (proves the deferred Unlock ran).
	id4, err4 := deps.Start(StartReq{
		Kind: KindProvision, Nickname: "router4", InstallBudget: time.Second, VerifyBudget: time.Second,
	})
	if err4 != nil {
		t.Fatalf("Start for router4 after its first job finished: %v", err4)
	}
	waitForTerminal(t, deps.Store, id4, time.Second)
}

// --- hintFor -----------------------------------------------------------

func TestHintFor_MapsEachStepToItsSpecDefinedHint(t *testing.T) {
	cases := []struct {
		step string
		err  error
		want string
	}{
		{StepArchDetected, errors.New("unsupported arch: mips64"), "роутер сообщил неподдерживаемую архитектуру"},
		{StepDownloading, errors.New("http 502"), "роутер не смог скачать бинарь с backend"},
		{StepChecksumOK, errors.New("sha256 mismatch"), "бинарь не сошёлся с подписанной суммой — релиз/зеркало битые"},
		{StepVerifyOnline, nil, "поставлен, но не позвонил домой за 120с — проверь связь роутер→backend"},
		{StepConfigWritten, errors.New("disk full"), "init-скрипт не поднял сервис"},
		{StepInitInstalled, errors.New("boom"), "init-скрипт не поднял сервис"},
		{StepServiceStarted, errors.New("boom"), "init-скрипт не поднял сервис"},
		{StepServiceRestarted, errors.New("boom"), "init-скрипт не поднял сервис"},
		{StepBackendURLRewrite, errors.New("boom"), "init-скрипт не поднял сервис"},
	}
	for _, c := range cases {
		if got := hintFor(c.step, c.err); got != c.want {
			t.Errorf("hintFor(%q, %v) = %q, want %q", c.step, c.err, got, c.want)
		}
	}
}

func TestHintFor_TerminalConnectedKeysOffRelayErrorText(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"active session phrase", errors.New("AWG Manager terminal already has an active session; retry"),
			"AWG Manager terminal занят — закрой сессию в web-UI и повтори"},
		{"explicit session_active token", errors.New("relay: session_active"),
			"AWG Manager terminal занят — закрой сессию в web-UI и повтори"},
		{"explicit auth_failed token", errors.New("relay: auth_failed"),
			"root-пароль или awgm-логин не подошёл"},
		{"login success=false", errors.New("awgm login: success=false"),
			"root-пароль или awgm-логин не подошёл"},
		{"http 401", errors.New("awgm GET /x: HTTP 401: nope"),
			"root-пароль или awgm-логин не подошёл"},
		{"unrecognized text", errors.New("dial tcp: connection refused"),
			"не удалось подключиться к AWG Manager terminal — проверь awgm_url и доступность роутера"},
		{"nil error", nil,
			"не удалось подключиться к AWG Manager terminal — проверь awgm_url и доступность роутера"},
	}
	for _, c := range cases {
		if got := hintFor(StepTerminalConnected, c.err); got != c.want {
			t.Errorf("%s: hintFor(terminal_connected, %v) = %q, want %q", c.name, c.err, got, c.want)
		}
	}
}

func TestHintFor_TimeoutIsDistinctFromEveryStepsPlainHint(t *testing.T) {
	steps := []string{StepTerminalConnected, StepArchDetected, StepDownloading, StepChecksumOK, StepVerifyOnline, "unknown_step"}
	for _, step := range steps {
		timeout := hintFor(step, context.DeadlineExceeded)
		plain := hintFor(step, errors.New("exit status 1"))
		if timeout == plain {
			t.Errorf("step %q: timeout hint == plain-failure hint (%q)", step, timeout)
		}
		if !strings.Contains(timeout, "таймаут") {
			t.Errorf("step %q: timeout hint %q does not mention таймаут", step, timeout)
		}
		if !strings.Contains(timeout, step) {
			t.Errorf("step %q: timeout hint %q does not name the step it happened on", step, timeout)
		}
	}
}

func TestHintFor_NeverLeaksRelayErrorTextIntoTheHint(t *testing.T) {
	secret := "wgm_super_secret_token_ABC123"
	steps := []string{
		StepTerminalConnected, StepArchDetected, StepDownloading, StepChecksumOK,
		StepConfigWritten, StepInitInstalled, StepServiceStarted, StepServiceRestarted,
		StepBackendURLRewrite, StepVerifyOnline, "totally_unknown_step",
	}
	for _, step := range steps {
		err := fmt.Errorf("relay failed while talking to backend?token=%s", secret)
		if got := hintFor(step, err); strings.Contains(got, secret) {
			t.Errorf("step %q: hintFor leaked the raw relay error text into the hint: %q", step, got)
		}
	}
}

func TestHintFor_UnknownStepReturnsSanitizedGeneric(t *testing.T) {
	got := hintFor("some_future_step_name", errors.New("whatever"))
	if got == "" {
		t.Fatal("hintFor(unknown step) returned an empty string")
	}
	if got == "роутер сообщил неподдерживаемую архитектуру" {
		t.Errorf("unknown step got the arch_detected hint verbatim: %q", got)
	}
}

// --- verifyPollInterval --------------------------------------------------

// Pins cross-task requirement #3 (from B5's review) as an executable
// assertion: VerifyOnline must never be called with poll==0 (which would
// busy-loop it), so the engine hands it a fixed positive constant rather
// than a caller-supplied value.
func TestVerifyPollInterval_IsPositive(t *testing.T) {
	if verifyPollInterval <= 0 {
		t.Fatalf("verifyPollInterval = %v, must be a fixed positive constant", verifyPollInterval)
	}
}
