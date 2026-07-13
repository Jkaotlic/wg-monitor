# Token Atomicity + Agent Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop a failed dashboard provision/repair from silently desyncing the agent token (backend), and make a dark agent diagnosable in one command (agent).

**Architecture:** Fix #2 defers the backend DB token commit until the engine observes the relay's `config_written` marker — the instant the router has persisted the new token — via a `CommitToken` closure on `provision.StartReq`. Fix #1 tees agent slog output to a size-rotating file and records an auth-reject breadcrumb in the reporter state file.

**Tech Stack:** Go (stdlib only, `CGO_ENABLED=0`), `log/slog`, sqlite via `internal/backend/db`, the async provision engine in `internal/backend/provision`.

## Global Constraints

- Module path: `github.com/anex/wg-monitor`. Verbatim in all imports.
- **No new dependencies.** Stdlib only (the agent relay is stdlib-only by policy; keep it that way).
- After every task: `go build ./...`, `go vet ./...`, `gofmt -l` (empty), and the touched package's tests must pass. Full `go test ./...` + `staticcheck ./...` must stay green by the final task.
- Spec: `docs/superpowers/specs/2026-07-13-token-atomicity-and-agent-observability-design.md`.
- Out of scope: any KeenDNS/infra change; any agent heartbeat DNS-resolution change; the wizard-deferred `cmd/deploy` path (already correct).
- Commit-point invariant (Fix #2): the DB token commit fires **iff** the `config_written` relay marker was observed — never before.

---

## Task 1: Provision engine — defer token commit to `config_written`

**Files:**
- Modify: `internal/backend/provision/runner.go`
- Test: `internal/backend/provision/runner_test.go`

**Interfaces:**
- Consumes: existing `RelayFunc`, `Deps`, `Store`, `Step*` constants, `scriptedRelay`/`fakeClock`/`waitForTerminal`/`stepStatus` test helpers.
- Produces: `StartReq.CommitToken func() error` — called exactly once by the worker when the `config_written` marker is applied; if it returns an error the job fails with the commit-failure hint. `errTokenCommit` package sentinel.

- [ ] **Step 1: Write the failing tests**

Add to `internal/backend/provision/runner_test.go`:

```go
// --- Task 1: deferred token commit at config_written -------------------

func TestDeps_Start_CommitToken_FiresOnceOnConfigWritten(t *testing.T) {
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
	commits := 0
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	deps := Deps{
		Store:    NewStore(),
		BaseCtx:  context.Background(),
		Relay:    relay.run,
		LastSeen: func(string) (time.Time, bool) { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), true },
		Now:      clock.now,
	}
	id, err := deps.Start(StartReq{
		Kind: KindProvision, Nickname: "router1",
		InstallBudget: 2 * time.Second, VerifyBudget: 2 * time.Second,
		CommitToken: func() error { commits++; return nil },
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := waitForTerminal(t, deps.Store, id, time.Second)
	if job.State != StateSuccess {
		t.Fatalf("State = %q, want success (hint=%q)", job.State, job.Hint)
	}
	if commits != 1 {
		t.Fatalf("CommitToken called %d times, want exactly 1", commits)
	}
}

func TestDeps_Start_CommitToken_NotCalledWhenFailBeforeConfigWritten(t *testing.T) {
	relay := &scriptedRelay{
		lines: []string{"__WG_STEP__ arch_detected arm64", "__WG_STEP__ downloading"},
		rc:    1, err: errors.New("download failed"),
	}
	commits := 0
	deps := Deps{
		Store: NewStore(), BaseCtx: context.Background(), Relay: relay.run,
		LastSeen: func(string) (time.Time, bool) { return time.Time{}, false },
	}
	id, err := deps.Start(StartReq{
		Kind: KindProvision, Nickname: "router1",
		InstallBudget: 2 * time.Second, VerifyBudget: 2 * time.Second,
		CommitToken: func() error { commits++; return nil },
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := waitForTerminal(t, deps.Store, id, time.Second)
	if job.State != StateFailed {
		t.Fatalf("State = %q, want failed", job.State)
	}
	if commits != 0 {
		t.Fatalf("CommitToken called %d times, want 0 (no config_written => DB untouched)", commits)
	}
}

func TestDeps_Start_CommitToken_ErrorFailsJob(t *testing.T) {
	relay := &scriptedRelay{
		lines: []string{
			"__WG_STEP__ arch_detected arm64", "__WG_STEP__ downloading",
			"__WG_STEP__ checksum_ok", "__WG_STEP__ config_written",
			"__WG_STEP__ init_installed", "__WG_STEP__ service_started",
		},
		rc: 0,
	}
	deps := Deps{
		Store: NewStore(), BaseCtx: context.Background(), Relay: relay.run,
		LastSeen: func(string) (time.Time, bool) { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), true },
	}
	id, err := deps.Start(StartReq{
		Kind: KindProvision, Nickname: "router1",
		InstallBudget: 2 * time.Second, VerifyBudget: 2 * time.Second,
		CommitToken: func() error { return errors.New("db down") },
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := waitForTerminal(t, deps.Store, id, time.Second)
	if job.State != StateFailed {
		t.Fatalf("State = %q, want failed", job.State)
	}
	if !strings.Contains(job.Hint, "БД") {
		t.Fatalf("Hint = %q, want the token-commit-failure hint", job.Hint)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/backend/provision/ -run TestDeps_Start_CommitToken -v`
Expected: compile error — `StartReq` has no field `CommitToken`.

- [ ] **Step 3: Add the `CommitToken` field to `StartReq`**

In `internal/backend/provision/runner.go`, add to the `StartReq` struct (after `RawToken`):

```go
	// CommitToken, when non-nil, is invoked exactly once by the worker the
	// moment the config_written marker is applied — i.e. the router has just
	// persisted config.yaml with this run's fresh token. It performs the
	// backend-side DB commit (UpsertEnrollment of the token hash + deploy
	// metadata). Deferring it here — rather than the handler committing before
	// Start — is what guarantees a failed install (e.g. a download that never
	// reaches config_written) can never leave the DB rotated while the router
	// keeps the old token (the 2026-07-13 token-desync fix). A nil CommitToken
	// (repoint, register) means "nothing to commit here".
	CommitToken func() error
```

- [ ] **Step 4: Add the sentinel + hint**

In `internal/backend/provision/runner.go`, add near `ErrAlreadyRunning`:

```go
// errTokenCommit wraps a CommitToken failure so hintFor can map it to the
// distinct "token not saved in the backend DB" hint instead of the generic
// post-checksum service hint.
var errTokenCommit = errors.New("token commit failed")
```

In `hintFor`, add as the first check inside the function (before the `DeadlineExceeded` branch):

```go
	if errors.Is(relayErr, errTokenCommit) {
		return "токен агента не сохранён в БД backend — повтори установку/ремонт"
	}
```

- [ ] **Step 5: Call `CommitToken` at `config_written` and fail on its error**

In `internal/backend/provision/runner.go`, in `run`, declare a commit-error holder before `onLine` (next to `var tail []string`):

```go
	var commitErr error
	committed := false
```

Inside `onLine`, after the `if applied { activeStep = name }` block, add:

```go
		if applied && name == StepConfigWritten && req.CommitToken != nil && !committed {
			committed = true
			if err := req.CommitToken(); err != nil {
				commitErr = fmt.Errorf("%w: %v", errTokenCommit, err)
			}
		}
```

After `rc, relayErr := d.Relay(...)`, before the existing `if rc != 0 || relayErr != nil` block, add:

```go
	if commitErr != nil {
		d.fail(jobID, req, StepConfigWritten, commitErr, noExitCode, tail)
		return
	}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/backend/provision/ -run TestDeps_Start_CommitToken -v`
Expected: PASS (all three). Then `go test ./internal/backend/provision/ -v` — all existing engine tests still pass (nil `CommitToken` is a no-op).

- [ ] **Step 7: Commit**

```bash
git add internal/backend/provision/runner.go internal/backend/provision/runner_test.go
git commit -m "fix(provision): commit agent token only at config_written (no desync on failed install)"
```

---

## Task 2: Provision handlers — defer the DB write into `CommitToken`

**Files:**
- Modify: `internal/backend/wizard_handler.go` (extract `mintProvisionToken`)
- Modify: `internal/backend/provision_handler.go` (`runProvisionInstallCore`, `dashboardHandleRepairReinstall`)
- Test: `internal/backend/provision_handler_test.go`

**Interfaces:**
- Consumes: Task 1's `provision.StartReq.CommitToken`; `newAgentEnrollmentToken()`, `enrollmentNicknameRe`, `db.IsValidKind`, `db.KindStatic`, `UpsertEnrollment`, `UpdateTelegramTopic`, `UpdateDeployInfo`, `provisionDeployInfo`, `fakeProvisionRelay`, `newProvisionTestHandler`.
- Produces: `mintProvisionToken(nickname, kind string) (rawToken, normNick, normKind string, err error)`.

- [ ] **Step 1: Write the failing regression test**

The existing `fakeProvisionRelay` ignores `onLine`. First extend it to optionally emit lines. In `internal/backend/provision_handler_test.go`, add a `lines []string` field to the `fakeProvisionRelay` struct and emit them at the top of `run` (before the block/return), so a test can drive `config_written`:

```go
// (add field to the fakeProvisionRelay struct definition)
	lines []string
```

In `func (f *fakeProvisionRelay) run(ctx context.Context, relayPath string, jobJSON []byte, onLine func(string)) (int, error)`, change the signature's `_ func(string)` to `onLine func(string)` and, immediately after `f.mu.Unlock()`, add:

```go
	for _, l := range f.linesSnapshot() {
		onLine(l)
	}
```

Add the helper:

```go
func (f *fakeProvisionRelay) linesSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}
```

Then add the regression test:

```go
func TestRepairReinstall_FailBeforeConfigWritten_LeavesTokenIntact(t *testing.T) {
	relay := &fakeProvisionRelay{
		lines: []string{"__WG_STEP__ arch_detected arm64", "__WG_STEP__ downloading"},
		rc:    1, err: errors.New("download failed"),
	}
	database, store, handler := newProvisionTestHandler(t, relay,
		func(string) (time.Time, bool) { return time.Time{}, false })

	// Existing, working agent with a known token.
	const oldToken = "old-token-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := database.Users().UpsertEnrollment("snektest", oldToken, db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo("snektest", db.DeployInfo{
		AWGMURL: "https://awg.snektest.example", DeployMode: "awgm",
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"mode":"reinstall","root_password":"x","awgm_login":"admin","awgm_password":"y"}`
	req := httptest.NewRequest("POST", "/v1/dashboard/agents/snektest/repair", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body=%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	waitProvisionJobTerminal(t, store, resp.JobID, time.Second) // failed

	// The install failed before config_written => the DB token must be UNCHANGED:
	// the old token still authenticates (no desync).
	if _, err := database.Users().GetByToken(oldToken); err != nil {
		t.Fatalf("old token must still authenticate after a failed reinstall, got: %v", err)
	}
}
```

If `provision_handler_test.go` has no local job-terminal poller, add one mirroring the engine's `waitForTerminal`:

```go
func waitProvisionJobTerminal(t *testing.T, store *provision.Store, jobID string, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for {
		job, ok := store.Get(jobID)
		if ok && job.State != provision.StateRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not reach a terminal state in %s", jobID, max)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backend/ -run TestRepairReinstall_FailBeforeConfigWritten -v`
Expected: FAIL — `GetByToken(oldToken)` errors, because today `createAgentEnrollment` overwrote `token_hash` synchronously before the (failing) relay ran.

- [ ] **Step 3: Add the mint-only helper**

In `internal/backend/wizard_handler.go`, add:

```go
// mintProvisionToken validates nickname/kind and returns a fresh raw enrollment
// token WITHOUT persisting it. The DB write is deferred to the provision
// engine's config_written commit hook (see the 2026-07-13 token-atomicity
// design) so a failed install never rotates a live agent's token.
func mintProvisionToken(nickname, kind string) (rawToken, normNick, normKind string, err error) {
	normNick = strings.TrimSpace(nickname)
	if !enrollmentNicknameRe.MatchString(normNick) {
		return "", "", "", errEnrollmentInvalidNickname
	}
	normKind = strings.TrimSpace(kind)
	if normKind == "" {
		normKind = db.KindStatic
	}
	if !db.IsValidKind(normKind) {
		return "", "", "", errEnrollmentInvalidKind
	}
	rawToken, err = newAgentEnrollmentToken()
	if err != nil {
		return "", "", "", fmt.Errorf("token gen: %w", err)
	}
	return rawToken, normNick, normKind, nil
}
```

Refactor `createAgentEnrollment` to reuse it (keeps `KindRegister` synchronous, DRY):

```go
func createAgentEnrollment(database *db.DB, nickname, kind string, threadID int64) (wizardEnrollmentResp, int64, error) {
	rawToken, normNick, normKind, err := mintProvisionToken(nickname, kind)
	if err != nil {
		return wizardEnrollmentResp{}, 0, err
	}
	userID, err := database.Users().UpsertEnrollment(normNick, rawToken, normKind, threadID)
	if err != nil {
		return wizardEnrollmentResp{}, 0, err
	}
	return wizardEnrollmentResp{Nickname: normNick, RawToken: rawToken}, userID, nil
}
```

- [ ] **Step 4: Defer the commit in `runProvisionInstallCore`**

In `internal/backend/provision_handler.go`, replace the block from `enrollment, userID, err := createAgentEnrollment(...)` through the `UpdateDeployInfo(...)` call (currently lines ~337-352) with a mint-only + deferred-commit build. Keep the surrounding `acquireProvisionMintLock`/`release` and the `job := awgmInstallJob{...}` build; only the enrollment/persist changes:

```go
	rawToken, nickname, kind, err := mintProvisionToken(p.Nickname, p.AgentKind)
	if err != nil {
		writeProvisionEnrollmentError(w, err)
		return
	}

	// Persist the token hash + topic + deploy metadata only once the router
	// has written config.yaml (the engine fires this at config_written), so a
	// failed install never rotates a live agent's token.
	deployInfo := provisionDeployInfo(p.Existing, p.AgentKind, p.ThreadID, p.AWGMURL, p.AWGMAuth)
	commit := func() error {
		userID, err := d.DB.Users().UpsertEnrollment(nickname, rawToken, kind, p.ThreadID)
		if err != nil {
			return err
		}
		if p.UpdateTopic {
			if err := d.DB.Users().UpdateTelegramTopic(userID, p.TelegramGroup, p.ThreadID); err != nil {
				return err
			}
		}
		return d.DB.Users().UpdateDeployInfo(nickname, deployInfo)
	}
```

Then in the `job := awgmInstallJob{...}` build, `RawToken: enrollment.RawToken` becomes `RawToken: rawToken` and `Nickname: enrollment.Nickname` becomes `Nickname: nickname`. In the `d.Provision.Start(provision.StartReq{...})` call, `RawToken: enrollment.RawToken` becomes `RawToken: rawToken`, `Nickname: enrollment.Nickname` becomes `Nickname: nickname`, and add `CommitToken: commit,`.

- [ ] **Step 5: Defer the commit in `dashboardHandleRepairReinstall`**

In `internal/backend/provision_handler.go`, replace the `enrollment, _, err := createAgentEnrollment(...)` block (currently ~598-602) with:

```go
	rawToken, _, kind, err := mintProvisionToken(nickname, user.Kind)
	if err != nil {
		writeProvisionEnrollmentError(w, err)
		return
	}
	commit := func() error {
		_, err := d.DB.Users().UpsertEnrollment(nickname, rawToken, kind, int64Value(user.TelegramThreadID))
		return err
	}
```

In the `job := awgmInstallJob{...}` build, `RawToken: enrollment.RawToken` becomes `RawToken: rawToken`. In the `d.Provision.Start(...)` call, `RawToken: enrollment.RawToken` becomes `RawToken: rawToken` and add `CommitToken: commit,`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/backend/ -run 'TestRepairReinstall_FailBeforeConfigWritten|Provision' -v`
Expected: PASS. The success-path provision tests must still pass (they use a relay that emits `config_written`, so `commit` runs and the token authenticates). Then `go test ./internal/backend/ ./internal/backend/provision/`.

- [ ] **Step 7: Commit**

```bash
git add internal/backend/provision_handler.go internal/backend/wizard_handler.go internal/backend/provision_handler_test.go
git commit -m "fix(provision): defer DB token commit into the config_written hook"
```

---

## Task 3: Agent — size-rotating log file writer

**Files:**
- Create: `internal/agent/logwriter.go`
- Test: `internal/agent/logwriter_test.go`

**Interfaces:**
- Produces: `newRotatingFile(path string, maxBytes int64) (*rotatingFile, error)`; `(*rotatingFile).Write([]byte) (int, error)`; `defaultAgentLogFile` const; `defaultLogMaxBytes` const.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/logwriter_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingFile_RotatesAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "agent.log") // sub dir must be created
	w, err := newRotatingFile(path, 64)
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	chunk := []byte("0123456789ABCDEF0123456789ABCDEF\n") // 33 bytes
	for i := 0; i < 3; i++ {                               // 99 bytes total => at least one rotation
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated file %s.1 should exist: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("live file: %v", err)
	}
	if info.Size() > 64 {
		t.Fatalf("live file size = %d, want <= maxBytes (64)", info.Size())
	}
}

func TestRotatingFile_UnwritablePathDegradesGracefully(t *testing.T) {
	// A path whose parent is a regular file cannot be created; constructor errors,
	// and callers fall back to stderr-only (tested in Task 4).
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newRotatingFile(filepath.Join(blocker, "agent.log"), 64); err == nil {
		t.Fatal("expected error creating a log file under a regular-file parent")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/ -run TestRotatingFile -v`
Expected: compile error — `newRotatingFile` undefined.

- [ ] **Step 3: Implement the rotating writer**

Create `internal/agent/logwriter.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"sync"
)

// defaultAgentLogFile is where the agent writes its log on Entware routers,
// where the S99 init sends stderr to /dev/null. Baked in (not required in
// config) so already-deployed agents gain a persisted log on the next binary
// update with no config.yaml rewrite.
const defaultAgentLogFile = "/opt/var/wg-monitor/agent.log"

// defaultLogMaxBytes caps the live log file before rotation (~1 MiB).
const defaultLogMaxBytes int64 = 1 << 20

// rotatingFile is a minimal size-rotating io.Writer. At maxBytes it renames the
// live file to <path>.1 (replacing any previous .1) and reopens an empty live
// file. Concurrency-safe: slog may write from multiple goroutines. Best-effort
// on error — a rotation or write failure never panics and never crashes the
// agent (logging must never take the process down).
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

func newRotatingFile(path string, maxBytes int64) (*rotatingFile, error) {
	if maxBytes <= 0 {
		maxBytes = defaultLogMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingFile{path: path, maxBytes: maxBytes, f: f, size: info.Size()}, nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil // degraded after a failed rotation: drop silently
	}
	if w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
		if w.f == nil {
			return len(p), nil
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate closes the live file, moves it to <path>.1 (removing any prior .1 so
// os.Rename works on Windows too), and reopens an empty live file. On any
// failure it leaves w.f nil; Write then drops silently until the next process
// start reopens the file.
func (w *rotatingFile) rotate() {
	_ = w.f.Close()
	_ = os.Remove(w.path + ".1")
	_ = os.Rename(w.path, w.path+".1")
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		w.f = nil
		return
	}
	w.f = f
	w.size = 0
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/agent/ -run TestRotatingFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/logwriter.go internal/agent/logwriter_test.go
git commit -m "feat(agent): size-rotating log file writer"
```

---

## Task 4: Agent — tee slog to stderr + the rotating file

**Files:**
- Modify: `internal/agent/config.go` (add `LoggingConfig`)
- Modify: `cmd/agent/main.go` (bootstrap logger; build the tee'd logger from config)
- Test: `internal/agent/config_test.go` (or a new `logging_config_test.go`)

**Interfaces:**
- Consumes: Task 3's `newRotatingFile`, `defaultAgentLogFile`.
- Produces: `Config.Logging LoggingConfig`; `(LoggingConfig).ResolveFile() string` (default path when unset; "" when explicitly disabled).

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/config_test.go` (create the file if absent, `package agent`):

```go
func TestLoggingConfig_ResolveFile(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", defaultAgentLogFile},         // omitted => default path
		{"  ", defaultAgentLogFile},       // blank => default path
		{"off", ""},                       // explicit disable
		{"none", ""},                      // explicit disable
		{"/tmp/custom.log", "/tmp/custom.log"},
	}
	for _, c := range cases {
		got := LoggingConfig{File: c.in}.ResolveFile()
		if got != c.want {
			t.Errorf("ResolveFile(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/ -run TestLoggingConfig_ResolveFile -v`
Expected: compile error — `LoggingConfig` undefined.

- [ ] **Step 3: Add `LoggingConfig` to the agent config**

In `internal/agent/config.go`, add a field to the `Config` struct:

```go
	Logging LoggingConfig `yaml:"logging"`
```

And the type + resolver (place near the other small config types):

```go
// LoggingConfig controls the agent's log destination. On Entware the S99 init
// sends stderr to /dev/null, so the agent additionally tees slog to a rotating
// file. File defaults to defaultAgentLogFile when unset; set it to "off"/"none"
// to disable the file sink (stderr only). MaxBytes 0 => defaultLogMaxBytes.
type LoggingConfig struct {
	File     string `yaml:"file"`
	MaxBytes int64  `yaml:"max_bytes"`
}

func (c LoggingConfig) ResolveFile() string {
	f := strings.TrimSpace(c.File)
	switch f {
	case "":
		return defaultAgentLogFile
	case "off", "none", "disabled":
		return ""
	default:
		return f
	}
}
```

(`strings` is already imported in `config.go`.)

- [ ] **Step 4: Run the config test to verify it passes**

Run: `go test ./internal/agent/ -run TestLoggingConfig_ResolveFile -v`
Expected: PASS.

- [ ] **Step 5: Wire the tee'd logger in `cmd/agent/main.go`**

Add `"io"` to the imports. Replace the current logger setup (lines ~36-47, the `logger := slog.New(...)` / `slog.SetDefault` / config-load block) so config is loaded under a bootstrap stderr logger, then the real logger tees stderr + file:

```go
	// Bootstrap logger to stderr until config (which may point the log file
	// elsewhere, or disable it) is loaded.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var loadOpts []agent.LoadOption
	if *allowHTTP {
		loadOpts = append(loadOpts, agent.WithAllowHTTP())
	}
	cfg, err := agent.LoadConfig(*configPath, loadOpts...)
	if err != nil {
		slog.Error("config load", "err", err, "path", *configPath)
		os.Exit(2)
	}

	// Real logger: stderr (journald on the VPS/dev) + a rotating file so the
	// agent's logs survive S99's stderr->/dev/null on Entware routers.
	logSink := io.Writer(os.Stderr)
	var logFileErr error
	if lf := cfg.Logging.ResolveFile(); lf != "" {
		if rf, ferr := newAgentLogFile(lf, cfg.Logging.MaxBytes); ferr != nil {
			logFileErr = ferr
		} else {
			logSink = io.MultiWriter(os.Stderr, rf)
		}
	}
	logger := slog.New(slog.NewTextHandler(logSink, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if logFileErr != nil {
		logger.Warn("agent log file unavailable; logging to stderr only", "err", logFileErr)
	}
```

`newRotatingFile` is unexported in package `agent`; expose a thin exported constructor for `cmd/agent` to call. Add to `internal/agent/logwriter.go`:

```go
// NewLogFile is the exported constructor cmd/agent uses to build the rotating
// log sink (rotatingFile itself stays unexported).
func NewLogFile(path string, maxBytes int64) (*rotatingFile, error) {
	return newRotatingFile(path, maxBytes)
}
```

...and in `main.go` call `agent.NewLogFile(lf, cfg.Logging.MaxBytes)` (rename the reference above from `newAgentLogFile` to `agent.NewLogFile`).

> Note: `NewLogFile` returns the unexported `*rotatingFile`. That is legal Go (exported func, unexported concrete return) and fine here — `main` only assigns it to `io.Writer`. If `staticcheck` flags the unexported return (it does not by default), change the return type to `io.Writer`.

- [ ] **Step 6: Verify build + full agent tests**

Run: `go build ./cmd/agent/ && go test ./internal/agent/ -v`
Expected: build OK; agent tests PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/agent/main.go internal/agent/config.go internal/agent/logwriter.go internal/agent/config_test.go
git commit -m "feat(agent): tee logs to stderr + rotating file (survive Entware /dev/null)"
```

---

## Task 5: Agent — auth-reject breadcrumb in reporter state

**Files:**
- Modify: `internal/agent/reporter.go`
- Test: `internal/agent/reporter_test.go` (add to the existing file)

**Interfaces:**
- Consumes: `ErrUnauthorized` (same package, `client.go`); existing `reporterState`, `Reporter`, `Sender`, `ReporterConfig`.
- Produces: extended `reporterState` with `LastAuthErrorAt time.Time` + `ConsecutiveAuthRejects int`, persisted on every report cycle.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/reporter_test.go`:

```go
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
```

Ensure the test file imports `encoding/json`, `fmt`, `os`, `path/filepath`, `context`, `time`, and `github.com/anex/wg-monitor/pkg/wire` (add any missing). If a `stubSender` already exists in `reporter_test.go`, reuse it instead of redefining.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/ -run TestReporter_AuthReject -v`
Expected: FAIL — `reporterState` has no `ConsecutiveAuthRejects`/`LastAuthErrorAt`, and the reporter neither counts nor persists on the auth-error path.

- [ ] **Step 3: Extend `reporterState` and add reporter fields**

In `internal/agent/reporter.go`, add `"errors"` to the imports. Extend the state struct:

```go
type reporterState struct {
	LastReportAt           time.Time `json:"last_report_at"`
	LastAuthErrorAt        time.Time `json:"last_auth_error_at,omitempty"`
	ConsecutiveAuthRejects int       `json:"consecutive_auth_rejects,omitempty"`
}
```

Add to the `Reporter` struct (next to `lastReportAt`):

```go
	lastAuthErrorAt        time.Time
	consecutiveAuthRejects int
```

- [ ] **Step 4: Load + persist the new fields; account for auth errors**

Change `loadLastReportAt` to load the whole state into the reporter's fields. Rename its use in `NewReporter` — replace `r.lastReportAt = r.loadLastReportAt()` with `r.loadState()`, and define:

```go
func (r *Reporter) loadState() {
	if r.statePath == "" {
		return
	}
	body, err := os.ReadFile(r.statePath)
	if err != nil {
		return
	}
	var s reporterState
	if err := json.Unmarshal(body, &s); err != nil {
		slog.Warn("reporter state corrupt; treating agent as freshly-started", "path", r.statePath, "err", err)
		return
	}
	r.lastReportAt = s.LastReportAt
	r.lastAuthErrorAt = s.LastAuthErrorAt
	r.consecutiveAuthRejects = s.ConsecutiveAuthRejects
}
```

Replace `persistLastReportAt(t time.Time)` with a whole-state persist that reads the reporter's current fields under the lock held by the caller's snapshot:

```go
func (r *Reporter) persistState(s reporterState) {
	if r.statePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0o755); err != nil {
		slog.Debug("reporter state mkdir", "err", err)
		return
	}
	body, _ := json.Marshal(s)
	tmp := r.statePath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		slog.Debug("reporter state write", "err", err)
		return
	}
	if err := os.Rename(tmp, r.statePath); err != nil {
		slog.Debug("reporter state rename", "err", err)
	}
}
```

In `sendOnceLocked`, replace the error branch and the success tail. The error branch (currently `slog.Warn("send report failed", "err", err); return`) becomes:

```go
	canonicalURL, err := r.sender.SendReport(ctx, report)
	if err != nil {
		slog.Warn("send report failed", "err", err)
		if errors.Is(err, ErrUnauthorized) {
			now := time.Now()
			r.mu.Lock()
			r.consecutiveAuthRejects++
			r.lastAuthErrorAt = now
			snap := reporterState{
				LastReportAt:           r.lastReportAt,
				LastAuthErrorAt:        r.lastAuthErrorAt,
				ConsecutiveAuthRejects: r.consecutiveAuthRejects,
			}
			r.mu.Unlock()
			r.persistState(snap)
		}
		return
	}
```

The success tail (currently sets `r.lastReportAt = now` then `r.persistLastReportAt(now)`) becomes:

```go
	now := time.Now()
	r.mu.Lock()
	r.lastReportAt = now
	r.consecutiveAuthRejects = 0
	r.lastAuthErrorAt = time.Time{}
	snap := reporterState{LastReportAt: now}
	r.mu.Unlock()
	r.persistState(snap)
```

(Leave the canonical-URL migration block that runs before this tail unchanged.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/agent/ -run TestReporter -v`
Expected: PASS. Then `go test ./internal/agent/ -v` — existing reporter tests still green (a successful report still persists `last_report_at`).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/reporter.go internal/agent/reporter_test.go
git commit -m "feat(agent): record auth-reject breadcrumb in reporter state"
```

---

## Task 6: Full gate + spec cross-check

**Files:** none (verification only)

- [ ] **Step 1: Run the full gate**

Run:
```bash
gofmt -l internal cmd
go vet ./...
go build ./...
go test ./...
staticcheck ./...
```
Expected: `gofmt -l` prints nothing; vet/build/test/staticcheck all clean.

- [ ] **Step 2: Manual cross-check against the spec**

Confirm each spec requirement maps to a shipped change:
- Failed install before `config_written` leaves DB `token_hash` untouched — Task 1 + Task 2 regression test.
- Commit fires exactly at `config_written` — Task 1.
- Agent logs to stderr + rotating file, default path baked, disable-able — Tasks 3-4.
- Auth-reject breadcrumb in reporter state, reset on success — Task 5.

- [ ] **Step 3: Commit any formatting fixes**

```bash
git add -A
git commit -m "chore: gofmt/vet cleanup for token-atomicity + observability" || echo "nothing to commit"
```

---

## Self-Review

**Spec coverage:** Fix #2 (defer commit to `config_written`) → Tasks 1-2. Fix #1A (stderr+rotating file, baked default, disable) → Tasks 3-4. Fix #1B (auth breadcrumb, reset on success) → Task 5. Out-of-scope items (KeenDNS, heartbeat DNS, wizard path) untouched. Covered.

**Placeholder scan:** No TBD/TODO; every code step carries complete code and exact run commands.

**Type consistency:** `CommitToken func() error` defined in Task 1, consumed in Task 2. `mintProvisionToken(nickname, kind) (rawToken, normNick, normKind string, err error)` defined + consumed in Task 2. `newRotatingFile`/`NewLogFile` defined in Task 3, consumed in Task 4. `reporterState` fields defined in Task 5 used by its own test. `ResolveFile()` defined + consumed in Task 4.
