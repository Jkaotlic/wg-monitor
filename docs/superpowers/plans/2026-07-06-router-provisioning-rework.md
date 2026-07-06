# Router Provisioning & Repair Rework — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the three overlapping dashboard router flows (Add-agent / Deploy-to-router / Revive) with one guided, asynchronous, signature-verified provisioning engine that streams live per-step progress and confirms the agent is actually online.

**Architecture:** A new `internal/backend/provision` package runs each router operation (provision / register / repair-repoint / repair-reinstall) as a **server-owned async job** with a structured `[]Step` model. The awg-manager relay emits `__WG_STEP__` line markers that the backend streams and parses into job steps; the raw transcript is never surfaced and the minted token is redacted at the boundary. Every install path verifies the release signature first and ends with a backend-native "agent phoned home" check. The dashboard drives it with a stepper modal that polls a job endpoint.

**Tech Stack:** Go 1.26.4 (stdlib + existing `releasesig`, `db`, `installtmpl`, `awgmrelay`), Python 3 stdlib relay, vanilla JS/CSS dashboard (zero-build, `go:embed`, CSP-safe).

**Spec:** `docs/superpowers/specs/2026-07-06-router-provisioning-rework-design.md`

## Global Constraints

- Go 1.26.4; `CGO_ENABLED=0` for builds; `-race` runs in CI only.
- **No new Go module dependencies** — stdlib + existing internal packages only.
- Dashboard stays **vanilla / zero-build / `go:embed` / CSP-safe**: no CDNs, no bundler; every dynamic HTML insertion goes through the existing `escapeHTML` / `escapeAttr`; never `innerHTML` with router/agent-derived strings unescaped.
- User-facing strings **Russian**; code + comments **English**.
- **Never** return the raw relay transcript or any token to the browser. Any diagnostic tail is passed through `provision.RedactToken(tail, rawToken)` first.
- **All relay/subprocess operations use a server-owned context** (`d.BaseCtx` + a per-op `context.WithTimeout`) — never `r.Context()`.
- Release signature verification reuses the exact call used in `cmd/deploy/github.go` (`releasesig.…`) — no path may install an unverified binary.
- TDD throughout; frequent commits; every task ends with `go build ./... && go vet ./... && go test ./<touched-pkg>/...` green (and `node --check` for JS tasks).
- Work on a feature branch `feat/provisioning-rework` (never commit to `main`); open a PR at the end.

---

### Task 1: Verified release-checksums helper (P0 signature gate)

**Files:**
- Create: `internal/backend/release_verify.go`
- Test: `internal/backend/release_verify_test.go`
- Read first: `cmd/deploy/github.go` (the `fetchExpectedSha` / `releasesig.Verify…` pattern, ~lines 364-380) and `internal/backend/release_checksums.go` (existing unsigned `fetchReleaseChecksums`).

**Interfaces:**
- Produces: `func verifiedReleaseChecksums(ctx context.Context, base, version string) (map[string]string, error)` — fetches `checksums.txt` **and** `checksums.txt.sig` from the release, verifies the signature with the same `releasesig` call `cmd/deploy/github.go` uses, and only then parses+returns the `{asset: sha}` map. Returns a non-nil error (never a partial map) if the `.sig` is missing or invalid.
- Consumes: existing checksum-line parser in `release_checksums.go` (reuse it; do not duplicate parsing).

- [ ] **Step 1: Read the reference implementation.** Open `cmd/deploy/github.go` around the checksum-signature verification and note the exact `releasesig` function name + signature and the `.sig` asset URL convention. Open `internal/backend/release_checksums.go` and note the existing `fetchReleaseChecksums` fetch + parse.

- [ ] **Step 2: Write the failing test.**

```go
package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifiedReleaseChecksums_RejectsMissingSig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("abc123  wg-monitor-agent-linux-arm64\n"))
	}))
	defer srv.Close()
	_, err := verifiedReleaseChecksums(context.Background(), srv.URL, "v9.9.9")
	if err == nil {
		t.Fatal("expected error when checksums.txt.sig is missing, got nil")
	}
}
```

Add a second test `TestVerifiedReleaseChecksums_RejectsBadSig` (serve a `.sig` with garbage bytes → expect error) and `TestVerifiedReleaseChecksums_OK` (serve a checksums file + a valid signature produced with a test key mirroring `releasesig` test helpers → expect the parsed map). Reuse any signing test helper that already exists in `internal/releasesig`.

- [ ] **Step 3: Run test, verify it fails.** `go test ./internal/backend/ -run TestVerifiedReleaseChecksums -v` → FAIL (undefined `verifiedReleaseChecksums`).

- [ ] **Step 4: Implement `verifiedReleaseChecksums`** in `release_verify.go`: fetch `<base>/<version>/checksums.txt` and `<base>/<version>/checksums.txt.sig`, call the same verify function `github.go` uses, parse via the existing parser, return the map. Bounded reads (reuse existing size limits).

- [ ] **Step 5: Run tests, verify pass.** `go test ./internal/backend/ -run TestVerifiedReleaseChecksums -v` → PASS.

- [ ] **Step 6: Commit.** `git add internal/backend/release_verify*.go && git commit -m "feat(provision): signature-verified release checksums helper"`

---

### Task 2: Job & Step model + store

**Files:**
- Create: `internal/backend/provision/job.go`
- Test: `internal/backend/provision/job_test.go`

**Interfaces:**
- Produces:
```go
package provision

type JobKind string
const (
	KindProvision       JobKind = "provision"        // install now, new router
	KindRegister        JobKind = "register"         // mint token only
	KindRepairRepoint   JobKind = "repair_repoint"   // rewrite backend.url + restart
	KindRepairReinstall JobKind = "repair_reinstall" // full reinstall
)

type JobState string
const (
	StateRunning JobState = "running"
	StateSuccess JobState = "success"
	StateFailed  JobState = "failed"
)

type StepStatus string
const (
	StepPending StepStatus = "pending"
	StepActive  StepStatus = "active"
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
)

type Step struct {
	Name   string     `json:"name"`
	Status StepStatus `json:"status"`
	Detail string     `json:"detail,omitempty"`
}

type Job struct {
	ID       string    `json:"id"`
	Nickname string    `json:"nickname"`
	Kind     JobKind   `json:"kind"`
	State    JobState  `json:"state"`
	Steps    []Step    `json:"steps"`
	Version  string    `json:"version,omitempty"`
	Hint     string    `json:"hint,omitempty"`
	Tail     string    `json:"tail,omitempty"`
	ended    time.Time
}

type Store struct { /* mu sync.Mutex; jobs map[string]*Job; locks map[string]bool; now func() time.Time */ }
func NewStore() *Store
func (s *Store) Create(kind JobKind, nick string, steps []Step) *Job          // deep-copies steps; assigns random ID
func (s *Store) Get(id string) (Job, bool)                                    // returns a copy (safe for JSON)
func (s *Store) Update(id string, mutate func(*Job))                          // mutate under lock
func (s *Store) TryLock(nick string) bool                                     // single-flight per nickname
func (s *Store) Unlock(nick string)
func (s *Store) Sweep()                                                       // evict terminal jobs older than TTL
```
- ID generation: reuse the existing random-token/id helper in the backend (grep for how `cmd/queue.go` or enrollments mint ids) — do not add a new dependency. `now` is injectable for tests (mirror `retention.Policy.Now`).
- TTL const: `const jobTTL = 30 * time.Minute`.

- [ ] **Step 1: Write failing tests** in `job_test.go`: (a) `Create` returns a job with `StateRunning` and the given steps as pending; `Get` returns an equal copy; mutating the returned copy does not change the stored job. (b) `TryLock` returns true once then false for the same nick until `Unlock`. (c) `Sweep` with an injected clock evicts a job whose `ended` is older than `jobTTL` but keeps a running one. Write concrete assertions.

- [ ] **Step 2: Run, verify fail.** `go test ./internal/backend/provision/ -v` → FAIL (package/undefined).

- [ ] **Step 3: Implement `job.go`** — the types above + a mutex-guarded store returning copies from `Get`, deep-copying `Steps` on `Create`/`Get` so callers can't mutate shared slices; `Sweep` sets `ended` on terminal transition (do that in `Update` when state becomes terminal) and evicts by TTL.

- [ ] **Step 4: Run, verify pass.** `go test ./internal/backend/provision/ -v` → PASS.

- [ ] **Step 5: Commit.** `git commit -am "feat(provision): job/step model + in-memory store with TTL + single-flight"`

---

### Task 3: Step templates, marker protocol, redaction

**Files:**
- Create: `internal/backend/provision/steps.go`
- Test: `internal/backend/provision/steps_test.go`

**Interfaces:**
- Produces:
```go
const StepMarker = "__WG_STEP__"

// Step name constants (stable ids shared with the frontend label map).
const (
	StepTerminalConnected = "terminal_connected"
	StepArchDetected      = "arch_detected"
	StepDownloading       = "downloading"
	StepChecksumOK        = "checksum_ok"
	StepConfigWritten     = "config_written"
	StepInitInstalled     = "init_installed"
	StepServiceStarted    = "service_started"
	StepBackendURLRewrite = "backend_url_rewritten"
	StepServiceRestarted  = "service_restarted"
	StepVerifyOnline      = "verify_online"
)

// Template returns the pending step list for a job kind.
func Template(kind JobKind) []Step

// ParseStepLine extracts a step marker from one relay stdout line.
// "__WG_STEP__ arch_detected arm64" -> ("arch_detected","arm64",true).
func ParseStepLine(line string) (name, detail string, ok bool)

// RedactToken removes the raw token from any text before it leaves the backend.
func RedactToken(s, rawToken string) string
```

- [ ] **Step 1: Write failing tests:** `Template(KindProvision)` returns exactly the 8-step install sequence (terminal_connected…verify_online) all `StepPending`; `Template(KindRepairRepoint)` returns the 4-step repoint sequence; `ParseStepLine("__WG_STEP__ downloading")` → `("downloading","",true)`; `ParseStepLine("__WG_STEP__ arch_detected arm64")` → `("arch_detected","arm64",true)`; `ParseStepLine("random noise")` → `("","",false)`; `RedactToken("token: abc123 rest", "abc123")` → `"token: «redacted» rest"`; `RedactToken(x, "")` → `x` unchanged.

- [ ] **Step 2: Run, verify fail.** `go test ./internal/backend/provision/ -run 'Template|ParseStepLine|RedactToken' -v` → FAIL.

- [ ] **Step 3: Implement `steps.go`** — templates per kind, a marker parser that splits on the first two whitespace-delimited fields after the marker, and `RedactToken` (no-op on empty token, else `strings.ReplaceAll`).

- [ ] **Step 4: Run, verify pass.** → PASS.

- [ ] **Step 5: Commit.** `git commit -am "feat(provision): step templates, __WG_STEP__ parser, token redaction"`

---

### Task 4: Streaming relay abstraction (server-owned context)

**Files:**
- Create: `internal/backend/provision/relay.go`
- Test: `internal/backend/provision/relay_test.go`
- Read first: `internal/backend/agent_revive.go` (`runRelayProcess`, `resolveRelayScript`).

**Interfaces:**
- Produces:
```go
// RelayFunc runs the relay for a marshalled job and calls onLine for every
// stdout line as it arrives. Returns the script rc (0 == success) and error.
// Injectable so the engine can be tested without a real router.
type RelayFunc func(ctx context.Context, relayPath string, jobJSON []byte, onLine func(string)) (rc int, err error)

// DefaultRelay streams python3 relay stdout line-by-line via StdoutPipe+bufio.Scanner.
func DefaultRelay(ctx context.Context, relayPath string, jobJSON []byte, onLine func(string)) (int, error)
```
- Move the temp-file-write + `resolveRelayScript` logic here (or call the existing helpers). The key change vs `runRelayProcess`: use `cmd.StdoutPipe()` + `bufio.Scanner` and call `onLine` per line (also merge stderr via `cmd.Stderr = cmd.Stdout`-equivalent using an `io.MultiWriter`/second pipe, or set `cmd.Stderr = w` where lines are still delivered). Honor `ctx` cancellation (the caller supplies a server-owned `WithTimeout`).

- [ ] **Step 1: Write a failing test** for `DefaultRelay` using a tiny **stub script** instead of the real relay: write a temp `.py`/`.sh` that prints two lines then exits N. Because Windows lacks a guaranteed `python3`, make `DefaultRelay` take the interpreter/argv from a small unexported seam OR test the streaming loop directly by extracting it into `func streamLines(r io.Reader, onLine func(string))` and testing that with a `strings.Reader` of `"a\nb\n"` → onLine called with "a","b". Prefer the `streamLines` extraction (portable, no exec in the unit test).

```go
func TestStreamLines(t *testing.T) {
	var got []string
	streamLines(strings.NewReader("line1\nline2\n"), func(s string) { got = append(got, s) })
	if len(got) != 2 || got[0] != "line1" || got[1] != "line2" {
		t.Fatalf("got %v", got)
	}
}
```

- [ ] **Step 2: Run, verify fail.** `go test ./internal/backend/provision/ -run TestStreamLines -v` → FAIL.

- [ ] **Step 3: Implement** `streamLines` + `DefaultRelay` (write job JSON to a 0600 temp file, resolve the relay script via the existing embedded-relay logic, `exec.CommandContext(ctx, "python3", script, jobfile)`, stream stdout+stderr through `streamLines`, wait, derive rc from `exec.ExitError.ExitCode()`). Delete temp files on return.

- [ ] **Step 4: Run, verify pass.** → PASS. Also `go vet ./internal/backend/provision/`.

- [ ] **Step 5: Commit.** `git commit -am "feat(provision): streaming relay runner (StdoutPipe, server-owned ctx)"`

---

### Task 5: Verify-online (backend-native)

**Files:**
- Create: `internal/backend/provision/verify.go`
- Test: `internal/backend/provision/verify_test.go`

**Interfaces:**
- Produces:
```go
// LastSeenFunc returns the agent's most recent report time (backend receives
// reports, so it is the source of truth). Injectable for tests.
type LastSeenFunc func(nick string) (time.Time, bool)

// VerifyOnline polls lastSeen until a report newer than `since` arrives or the
// budget elapses. detail is e.g. "first report 4s ago". now injectable.
func VerifyOnline(ctx context.Context, nick string, since time.Time, budget, poll time.Duration,
	lastSeen LastSeenFunc, now func() time.Time, sleep func(time.Duration)) (detail string, ok bool)
```
- Budget default `120 * time.Second`, poll `3 * time.Second` (constants in the engine; grounded: fleet report median ≈68s).

- [ ] **Step 1: Write failing tests:** (a) lastSeen returns a time after `since` on the 2nd poll → `ok==true`, detail contains "report". (b) lastSeen always before `since` → after budget, `ok==false`. Use a fake `now`/`sleep` that advances a virtual clock so the test is instant.

- [ ] **Step 2: Run, verify fail.** → FAIL.

- [ ] **Step 3: Implement `VerifyOnline`** — loop: if `lastSeen(nick)` returns a time `> since`, return ok with `detail = fmt.Sprintf("first report %ds ago", int(now-seen))`; else `sleep(poll)` until `now-start >= budget` (or ctx done) → not ok.

- [ ] **Step 4: Run, verify pass.** → PASS.

- [ ] **Step 5: Commit.** `git commit -am "feat(provision): backend-native verify-online poll"`

---

### Task 6: Provision engine (Runner) — orchestration

**Files:**
- Create: `internal/backend/provision/runner.go`
- Test: `internal/backend/provision/runner_test.go`

**Interfaces:**
- Consumes: Tasks 2–5 (Store, Template, ParseStepLine, RedactToken, RelayFunc, VerifyOnline/LastSeenFunc).
- Produces:
```go
type Deps struct {
	Store    *Store
	BaseCtx  context.Context          // server-owned; never a request context
	Relay    RelayFunc
	LastSeen LastSeenFunc
	Now      func() time.Time
	Logger   *slog.Logger
}
type StartReq struct {
	Kind          JobKind
	Nickname      string
	RelayPath     string
	JobJSON       []byte // marshalled relay job (built by the handler)
	RawToken      string // for redaction (empty for repoint)
	Version       string
	InstallBudget time.Duration
	VerifyBudget  time.Duration
}
// Start registers a job and launches the async worker. Returns the job id.
func (d Deps) Start(req StartReq) (string, error)
// hintFor maps a failed step + raw relay error to a friendly Russian hint.
func hintFor(step string, relayErr error) string
```
- Worker: acquire single-flight (`Store.TryLock`, defer Unlock); `ctx,cancel := context.WithTimeout(d.BaseCtx, req.InstallBudget)`; mark `terminal_connected` active; run `d.Relay(ctx, relayPath, jobJSON, onLine)` where `onLine` calls `ParseStepLine` and, on a known step, marks the prior step done + this one active/`Detail`; keep a bounded ring buffer of recent lines for the failure tail. On rc==0: mark all install steps done, run `verify_online` (via `VerifyOnline` using `d.LastSeen`), set `StateSuccess`/`StateFailed`. On rc!=0 or error: mark the active step `StepFailed`, set `Hint = hintFor(activeStep, err)`, `Tail = RedactToken(recentTail, req.RawToken)`, `StateFailed`.

- [ ] **Step 1: Write failing tests** with a **stub RelayFunc** that emits scripted lines:
  - Success: emits `__WG_STEP__ arch_detected arm64`, `downloading`, `checksum_ok`, `config_written`, `init_installed`, `service_started`, returns rc 0; stub `LastSeen` returns a fresh time → assert final `State==success`, all steps done, `verify_online` detail set.
  - Mid-fail: emits up to `downloading` then returns rc 14 → assert `State==failed`, `downloading` is `StepFailed`, `Hint` non-empty, `Tail` present and **contains no raw token** (pass a rawToken that appears in an emitted line and assert it's redacted).
  - Single-flight: second `Start` for the same nick while the first holds the lock → second returns an error or a job that immediately fails with a "already running" hint (pick one; test it).
  Drive the async worker to completion with a poll-until-terminal helper (bounded loop on `Store.Get`).

- [ ] **Step 2: Run, verify fail.** → FAIL.

- [ ] **Step 3: Implement `runner.go`** per the worker description + `hintFor` (map `checksum_ok`→checksum hint, `downloading`→download hint, `verify_online`→`agent_offline` hint, `terminal_connected`→auth/session hints keyed off the relay error text, default→sanitized generic).

- [ ] **Step 4: Run, verify pass.** `go test ./internal/backend/provision/ -v` (all) → PASS; `go vet ./internal/backend/provision/`.

- [ ] **Step 5: Commit.** `git commit -am "feat(provision): async provisioning engine with step streaming + redaction"`

---

### Task 7: Relay emits `__WG_STEP__` markers

**Files:**
- Modify: `internal/awgmrelay/awgm-relay.py` (`build_deferred_bootstrap_script` install script; `run_bootstrap`/`run_install_bootstrap`/`login_terminal` to emit `terminal_connected` and `arch_detected`).
- Modify: `internal/awgmrelay/test_install_mode.py`

**Interfaces:**
- Produces (contract with Task 3's parser): the relay's stdout contains lines `__WG_STEP__ <name> [detail]` for `arch_detected <arch>`, `downloading`, `checksum_ok`, `config_written`, `init_installed`, `service_started`; and the relay itself prints `__WG_STEP__ terminal_connected` once the shell prompt is reached and `__WG_STEP__ arch_detected <arch>` after `system_info`.

- [ ] **Step 1: Write/extend the failing Python test** in `test_install_mode.py`: render the install bootstrap script and assert it contains `echo __WG_STEP__ downloading`, `checksum_ok`, `config_written`, `init_installed`, `service_started` at the right points; and assert the **rendered script + config never emits the raw token via echo** in the structured marker lines (the token lives only inside the quoted heredoc, never after a `__WG_STEP__`). Run: `python3 internal/awgmrelay/test_install_mode.py` (or the repo's Python test entrypoint) → FAIL.

- [ ] **Step 2: Add `echo __WG_STEP__ …` lines** in `build_deferred_bootstrap_script` between phases (before fetch → `downloading`; after checksum match → `checksum_ok`; after config mv → `config_written`; after init mv → `init_installed`; in `start_block` → `service_started`). In `login_terminal`, emit `print("__WG_STEP__ terminal_connected")` when it returns on a shell prompt; in `run_install_bootstrap`/`run_deferred_bootstrap`, `print("__WG_STEP__ arch_detected " + arch)` after `normalize_arch`. Keep the repoint script (`buildReviveScript`, Go side) emitting `backend_url_rewritten` + `service_restarted` (Task 10).

- [ ] **Step 3: Run the Python test, verify pass.** Also re-run existing relay tests.

- [ ] **Step 4: Commit.** `git commit -am "feat(relay): emit __WG_STEP__ progress markers on bootstrap"`

---

### Task 8: HTTP handlers — provision / poll / repair

**Files:**
- Create: `internal/backend/provision_handler.go`
- Test: `internal/backend/provision_handler_test.go`
- Read first: `internal/backend/agent_deploy_router.go`, `agent_revive.go`, `dashboard_handler.go` (auth middleware, `decodeWizardJSON`, `writeJSONError`, `createAgentEnrollment`, `validateDashboardAWGMURL`, `lookupDashboardLatestVersion`).

**Interfaces:**
- Produces three `http.HandlerFunc`s built from `Deps` + the provision engine:
  - `POST /v1/dashboard/provision` — decode `{kind,nickname,agent_kind,telegram_group,thread_id,awgm_url?,awgm_auth?,root_password?,awgm_login?,awgm_password?,awgm_api_key?,version?}`. For `register`: mint enrollment, persist metadata, return `{ok, raw_token, backend_url, …}` (+ an instant completed `register` job). For `provision`: validate awgm_url + root_password; resolve version (default `lookupDashboardLatestVersion`); **`verifiedReleaseChecksums`** (Task 1); mint enrollment; build the relay `awgmInstallJob` JSON (mode `bootstrap_install`, `InitScript`, checksums); `engine.Start(...)`; return `{job_id, steps}`.
  - `GET /v1/dashboard/provision/{job_id}` → `Store.Get` → JSON (404 if unknown).
  - `POST /v1/dashboard/agents/{nickname}/repair` — decode `{mode:"repoint"|"reinstall", root_password, new_backend_url?, version?, awgm_*?}`. `repoint`: build the repoint relay job (`buildReviveScript`, default relay mode); `engine.Start(KindRepairRepoint,…)`. `reinstall`: same as provision-install but for an existing nick (re-mint token, verify checksums, `bootstrap_install`).
- **Anti-downgrade:** for provision/reinstall, reject `version` below the agent's `LastDeployedVersion` unless `req.AllowDowngrade` — reuse `internal/backend/upstream` compare if available; otherwise a semver compare helper (add to `provision` with a test).

- [ ] **Step 1: Write failing handler tests** (mirror `agent_deploy_router_test.go` style): register path returns a raw token + no relay call; provision path with a **stub engine** (inject a fake `Start` that records the job JSON) returns `{job_id, steps}` and the job JSON carries the verified checksums + `bootstrap_install` mode; provision with missing root_password → 400; poll unknown id → 404; repair `repoint` builds a repoint job; anti-downgrade rejects an older version → 400.

- [ ] **Step 2: Run, verify fail.** → FAIL.

- [ ] **Step 3: Implement `provision_handler.go`.** Route all credential handling exactly like the current handlers (transient, never logged). No raw output/token in any response — the client only ever sees `{job_id, steps}` then polls.

- [ ] **Step 4: Run, verify pass.** `go test ./internal/backend/ -run Provision -v` → PASS.

- [ ] **Step 5: Commit.** `git commit -am "feat(provision): provision/poll/repair HTTP handlers"`

---

### Task 9: Wire routes, BaseCtx, store sweeper

**Files:**
- Modify: `internal/backend/dashboard_handler.go` (route registration block ~lines 74-89)
- Modify: `cmd/backend/main.go` (build the provision `Store` + engine `Deps`; pass the process context as `BaseCtx`; start a `Sweep` ticker; provide `LastSeen` backed by the DB)
- Modify: `internal/backend/handler.go`/`Deps` struct as needed to carry `BaseCtx` + provision engine.

- [ ] **Step 1: Write a failing integration-ish test** in `cmd/backend` or `internal/backend` asserting `POST /v1/dashboard/provision` (register kind) end-to-end through the real mux returns a token (using the existing backend test harness in `handler_test_helpers_test.go`). → FAIL (route not registered).

- [ ] **Step 2: Register the three routes** under `dashAuth`, wire `Deps.BaseCtx = ctx` (the `main.go` root context), construct the engine with `LastSeen` = a closure over `db.Users().GetByNickname(nick).LastSeenAt`, start `go` sweeper ticking `Store.Sweep()` (mirror `cmd/queue.go` Sweep wiring at `cmd/backend/main.go:312`).

- [ ] **Step 3: Run, verify pass.** `go test ./internal/backend/ ./cmd/backend/ -run Provision -v` → PASS; `go build ./...`.

- [ ] **Step 4: Commit.** `git commit -am "feat(provision): wire routes, server BaseCtx, job sweeper"`

---

### Task 10: Old endpoints become thin adapters

**Files:**
- Modify: `internal/backend/agent_deploy_router.go` (`dashboardDeployRouterHandler` → delegate to the engine, keeping its request shape) 
- Modify: `internal/backend/agent_revive.go` (`dashboardReviveAgentHandler` → engine `KindRepairRepoint`; ensure `buildReviveScript` emits `__WG_STEP__ backend_url_rewritten` + `service_restarted`)

- [ ] **Step 1: Update `buildReviveScript`** to `echo __WG_STEP__ backend_url_rewritten` after the `mv`, and `__WG_STEP__ service_restarted` after the restart; update its test.

- [ ] **Step 2: Make the two old handlers delegate** to the engine (so any in-flight frontend keeps working during the transition) — they now return `{job_id, steps}` too. Update their existing tests to the new async contract (they no longer return raw `output`).

- [ ] **Step 3: Run, verify pass.** `go test ./internal/backend/ -v`; `go vet ./...`.

- [ ] **Step 4: Commit.** `git commit -am "refactor(provision): route deploy-router+revive through the engine"`

---

### Task 11: Frontend — Provision wizard (modal + mode fork + register-only)

**Files:**
- Modify: `internal/backend/dashboard_static/index.html` (replace `#addAgentModal`, `#deployRouterModal` with one `#provisionModal` stepper; keep `#reviveModal` markup only until Task 13)
- Modify: `internal/backend/dashboard_static/app.js`
- Modify: `internal/backend/dashboard_static/app.css`

- [ ] **Step 1: Build the stepper modal** in `index.html`: panels `data-step="mode|identity|access|run"`; mode buttons `⚡ Поставить сейчас` / `🎫 Только зарегать токен`; identity fields (nickname, kind, group, topic); access fields (awgm_url, awgm_auth, root_password, awgm_login/password/api-key under `<details>`, version). All labels Russian.

- [ ] **Step 2: Wire `openProvision()` / step navigation / `submitProvision()`** in `app.js`: `register` → `POST /provision {kind:"register"}` → render token card via `formatEnrollmentResult`; `provision` → `POST /provision {kind:"provision"}` → hand the returned `{job_id, steps}` to the progress view (Task 12). Every dynamic value through `escapeHTML`/`escapeAttr`. Replace the topbar "Add agent" button with "Provision router".

- [ ] **Step 3: Verify** `node --check internal/backend/dashboard_static/app.js`; load in Playwright with a mocked `/provision` register response → token card renders, no console errors.

- [ ] **Step 4: Commit.** `git commit -am "feat(dashboard): Provision wizard (mode fork + register-only)"`

---

### Task 12: Frontend — progress view + polling + shared step render

**Files:**
- Modify: `internal/backend/dashboard_static/app.js`, `app.css`

**Interfaces:**
- Produces: `renderJobProgress(job)` (declarative `STEP_LABELS` map name→{label,icon}; renders each step ⏳/✓/✗ + detail); `pollJob(jobId)` (poll `GET /v1/dashboard/provision/{id}` every 1500ms until `state!=="running"`, re-render, then show success/fail card with `job.hint` + collapsible redacted `job.tail`). Replaces the raw `<pre>` result rendering for provision/repair.

- [ ] **Step 1: Implement `STEP_LABELS` + `renderJobProgress` + `pollJob`.** On terminal success show `✓ Агент онлайн — <version>, <verify detail>`; on fail show `✗ Упало на «<label>»` + hint + `<details>` tail. Pause auto-refresh while a job modal/progress is open (extend `overlayOpen()` to include the provision/repair modals — also fixes the drawer-clobber finding for these).

- [ ] **Step 2: Verify** `node --check`; Playwright with a mocked job that transitions pending→running→success across polls, then one that ends failed → assert checklist fills in and **no raw token** appears in the DOM.

- [ ] **Step 3: Commit.** `git commit -am "feat(dashboard): live job progress checklist + polling"`

---

### Task 13: Frontend — Repair wizard + remove legacy UI

**Files:**
- Modify: `index.html` (add `#repairModal` with mode fork; remove `#addAgentModal`, `#deployRouterModal`, `#reviveModal` and their handlers)
- Modify: `app.js` (drawer Recovery: single "Repair" button → `openRepair(nick)`; mode `🔗 Быстрый re-point URL` / `♻️ Полная переустановка`; `submitRepair()` → `POST /agents/{nick}/repair` → progress view. Delete `openRevive/closeRevive/submitRevive/showReviveResult`, `openDeployRouter/closeDeployRouter/submitDeployRouter/showDeployRouterResult`.)

- [ ] **Step 1: Add the Repair modal + `openRepair/submitRepair`**, reusing the progress view. Remove the three legacy modals + their JS. Update `els` lookups and event wiring; grep app.js for dangling references to removed ids.

- [ ] **Step 2: Verify** `node --check`; Playwright: drawer "Repair" → repoint mode → mocked job → progress renders; confirm removed buttons are gone.

- [ ] **Step 3: Commit.** `git commit -am "feat(dashboard): Repair wizard; remove Add-agent/Deploy/Revive legacy UI"`

---

### Task 14: Remove old backend endpoints + dead code

**Files:**
- Modify: `dashboard_handler.go` (delete `/deploy-router` + `/revive` routes)
- Delete/trim: dead handler funcs in `agent_deploy_router.go` / `agent_revive.go` no longer referenced (keep `buildReviveScript`, `reviveNewBackendURL`, `resolveRelayScript` if the engine uses them).

- [ ] **Step 1:** Remove the two routes + now-unreferenced handlers/tests. `grep` to confirm nothing else calls them.
- [ ] **Step 2:** `go build ./... && go vet ./... && go test ./...` all green.
- [ ] **Step 3: Commit.** `git commit -am "chore(provision): remove superseded deploy-router/revive endpoints"`

---

### Task 15: Full verification + smoke

- [ ] **Step 1:** `go build ./... && go vet ./... && go test ./... -count=1` → all green; `node --check internal/backend/dashboard_static/app.js`.
- [ ] **Step 2:** Playwright end-to-end against a locally-run backend with a **stubbed engine** (env flag or a fake relay) driving: Provision→install (job runs to success), Provision→register (token), Repair→repoint, Repair→reinstall→fail path. Assert no token/transcript ever in the DOM.
- [ ] **Step 3:** Manual live smoke (operator): provision onto one router (e.g. a dark/stale agent from the fleet: rc59/rc70 candidates) through the awg-manager terminal; watch the checklist reach `✓ агент онлайн`. This is the first real-hardware run — keep the old adapter path revertable until this passes.
- [ ] **Step 4: Open PR** `feat/provisioning-rework` → main with a summary linking the spec.

---

## Self-Review

- **Spec coverage:** engine (T2,T6) · async server-ctx job (T4,T6,T9) · step markers+stream (T3,T4,T7) · redaction (T3,T6,T12) · signature verify (T1,T8) · verify-online (T5,T6,T9) · endpoints (T8,T9) · IA/wizard (T11,T12,T13) · repair both modes (T8,T10,T13) · error hints (T6,T12) · single-flight/anti-downgrade (T2,T6,T8) · rollout adapters→switch→remove (T10,T13,T14) · tests (every task + T15). All spec sections mapped.
- **Placeholder scan:** none — every task carries file paths, interfaces, and concrete test/impl steps. Load-bearing code (types, marker protocol, redaction, streaming, verify) is written out; obvious wiring references exact existing symbols to mirror.
- **Type consistency:** step-name constants defined once in T3 and reused by T7 (relay) + T12 (frontend `STEP_LABELS`); `JobKind`/`JobState`/`StepStatus` defined in T2 and used consistently; `RelayFunc`/`LastSeenFunc` signatures stable across T4/T5/T6.
