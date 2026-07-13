package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// verifyPollInterval is the fixed poll cadence Start's worker hands to
// VerifyOnline. It is always this one positive constant — never 0, never
// caller-supplied — because VerifyOnline's loop calls sleep(poll) on every
// iteration that doesn't yet see a fresh report; a 0 poll would turn that
// into a real busy-loop spinning lastSeen as fast as the scheduler allows
// until the budget elapses. 3s matches the provisioning-rework spec's
// grounded default (sized off the live fleet's measured ~68s median report
// interval — see verify.go's own doc comment).
const verifyPollInterval = 3 * time.Second

// tailRingLimit bounds how many of the relay's most recent stdout/stderr
// lines Start's worker retains for a failed job's Tail. Only the failure
// path ever reads this buffer (see fail()); a successful job discards it
// once the goroutine returns. ~40 lines is enough to show the last few
// steps' worth of terminal output without the Tail growing unbounded across
// a multi-minute install.
const tailRingLimit = 40

// ErrAlreadyRunning is returned by Start when Store.TryLock reports that
// another provision/repair job already holds req.Nickname's single-flight
// lock. It is a package-level sentinel (rather than a formatted per-call
// error) so a caller can test for it with errors.Is regardless of wording;
// its text is already a friendly, ready-to-surface sentence — Task 8's HTTP
// handler is expected to hand its Error() text straight to the operator, the
// same way agent_revive.go's dashboardReviveAgentHandler surfaces a relay
// error's Error() text today. No Job is created for a rejected Start: the
// design spec calls for concurrent provision/repair of the same agent to be
// "rejected with a clear message", not queued or recorded as an
// instantly-failed Job.
var ErrAlreadyRunning = errors.New("установка/ремонт этого роутера уже выполняется — дождись завершения текущего задания")

// errTokenCommit wraps a CommitToken failure so hintFor can map it to the
// distinct "token not saved in the backend DB" hint instead of the generic
// post-checksum service hint.
var errTokenCommit = errors.New("token commit failed")

// verifySleep is the sleep func Start's worker hands to VerifyOnline in
// production (real time.Sleep). It is a package var — not a Deps field —
// because Deps' shape is fixed by the provisioning-rework plan (see the
// brief's interfaces block) and deliberately has no Sleep hook: every test
// scenario this task's brief calls for resolves VerifyOnline on its very
// first poll (LastSeen stubbed fresh from the start), so real sleeping is
// never actually exercised by this package's tests. The var exists only so a
// future test that legitimately needs a multi-poll VerifyOnline run (not
// required by this task) can swap in a non-blocking stub without changing
// Deps' public contract.
var verifySleep = time.Sleep

// Deps bundles the collaborators Start's async worker needs. Zero value is
// not usable: Store, Relay, and LastSeen are required (a nil Relay/LastSeen
// panics the moment the worker calls it, same as any other nil func value; a
// nil Store panics the same way any nil-map/nil-pointer access would). Now
// and Logger are optional — a nil Now falls back to time.Now (see nowFn), a
// nil Logger just skips logging — mirroring Store's own now field and
// agent_revive.go's `if d.Logger != nil` convention respectively.
//
// BaseCtx must be server-owned (the backend process's own long-lived
// context, cancelled only on shutdown — see cmd/backend/main.go's eventual
// wiring), never a request context: the worker derives its own
// timeout-bound contexts from it, so an HTTP handler that returns the
// moment Start hands back a job id (the design's "survives a browser
// disconnect" goal) cannot cancel a run already in flight.
type Deps struct {
	Store    *Store
	BaseCtx  context.Context
	Relay    RelayFunc
	LastSeen LastSeenFunc
	Now      func() time.Time
	Logger   *slog.Logger
}

// nowFn returns d.Now(), falling back to time.Now when d.Now is nil —
// mirrors Store.nowFn's exact convention (job.go) for the same reason: tests
// inject a fixed/virtual clock, production leaves Now unset.
func (d Deps) nowFn() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// StartReq is one request to run the provisioning engine: install a fresh
// router, re-point a repaired one's backend URL, or fully reinstall one.
// JobJSON is the already-marshalled relay job config the HTTP handler (Task
// 8) built for Kind — Start/its worker never construct or inspect it, just
// thread it through to Relay. RawToken is the raw enrollment token this run
// was invoked with, if any (empty for a repoint request, which carries no
// fresh credential); its only use anywhere in this package is
// RedactToken(tail, RawToken) in fail() — it must never reach any other
// field (Hint in particular is built by hintFor, which never even receives
// RawToken — see hintFor's own doc comment).
type StartReq struct {
	Kind          JobKind
	Nickname      string
	RelayPath     string
	JobJSON       []byte
	RawToken      string
	Version       string
	InstallBudget time.Duration
	VerifyBudget  time.Duration

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
}

// Start registers a Job for req and launches the async worker goroutine that
// drives it, returning the new Job's id immediately (before the relay has
// even connected) so an HTTP handler can respond right away and let the
// dashboard start polling GET /provision/{job_id} — the "survives a browser
// disconnect" property from the design spec. The single-flight lock
// (Store.TryLock) is acquired synchronously, before this method returns: if
// req.Nickname already has a run in flight, Start returns ErrAlreadyRunning
// and creates no Job at all, rather than launching a doomed worker or
// leaving a phantom "already failed" job in the Store.
func (d Deps) Start(req StartReq) (string, error) {
	if !d.Store.TryLock(req.Nickname) {
		if d.Logger != nil {
			d.Logger.Warn("provision start rejected: already running",
				"nickname", req.Nickname, "kind", req.Kind)
		}
		return "", ErrAlreadyRunning
	}

	job := d.Store.Create(req.Kind, req.Nickname, Template(req.Kind))
	if req.Version != "" {
		d.Store.Update(job.ID, func(j *Job) { j.Version = req.Version })
	}

	if d.Logger != nil {
		d.Logger.Info("provision job started", "job_id", job.ID, "nickname", req.Nickname, "kind", req.Kind)
	}

	go d.run(job.ID, req)

	return job.ID, nil
}

// run is the worker goroutine body launched by Start. It always releases
// req.Nickname's single-flight lock and always cancels both contexts it
// creates, on every return path — the "worker goroutine must not leak"
// contract (both defers run even if a step below panics).
func (d Deps) run(jobID string, req StartReq) {
	defer d.Store.Unlock(req.Nickname)

	// Server-owned, budget-bound context for the relay run. NEVER a request
	// context (see Deps.BaseCtx's doc) — this is what lets a wedged terminal
	// get killed on a clean timeout instead of hanging forever, and what
	// lets the run keep going after the HTTP request that started it has
	// already returned.
	installCtx, installCancel := context.WithTimeout(d.BaseCtx, req.InstallBudget)
	defer installCancel()

	since := d.nowFn()

	d.Store.Update(jobID, func(j *Job) {
		if idx := stepIndex(j.Steps, StepTerminalConnected); idx >= 0 {
			j.Steps[idx].Status = StepActive
		}
	})

	activeStep := StepTerminalConnected
	var tail []string
	var commitErr error
	committed := false

	onLine := func(line string) {
		tail = appendTail(tail, line)

		// Cross-task requirement (from Task 3/B3's review): trim leading and
		// trailing whitespace and \r before parsing — a PTY echoes the
		// terminal's own indentation/CR noise around each real line, and
		// ParseStepLine requires the marker as a strict prefix. The relay
		// emits markers via echo/printf (Task 7), so an echoed COMMAND line
		// itself starts with "echo"/"printf", not the marker — trimmed
		// strict-prefix matching already leaves that line alone with no
		// extra dedup logic needed.
		name, detail, ok := ParseStepLine(strings.TrimSpace(line))
		if !ok {
			return
		}

		applied := false
		d.Store.Update(jobID, func(j *Job) {
			idx := stepIndex(j.Steps, name)
			if idx < 0 {
				return // unrecognized: not part of this job kind's template
			}
			if j.Steps[idx].Status != StepPending {
				return // duplicate/out-of-order marker: already active/done/failed
			}
			for i := 0; i < idx; i++ {
				j.Steps[i].Status = StepDone // a marker means everything before it finished
			}
			j.Steps[idx].Status = StepActive
			j.Steps[idx].Detail = detail
			applied = true
		})
		if applied {
			activeStep = name
		}
		if applied && name == StepConfigWritten && req.CommitToken != nil && !committed {
			committed = true
			if err := req.CommitToken(); err != nil {
				commitErr = fmt.Errorf("%w: %v", errTokenCommit, err)
			}
		}
	}

	rc, relayErr := d.Relay(installCtx, req.RelayPath, req.JobJSON, onLine)

	if commitErr != nil {
		d.fail(jobID, req, StepConfigWritten, commitErr, noExitCode, tail)
		return
	}

	if rc != 0 || relayErr != nil {
		// Cross-task requirement (from Task 4/B4's review): a ctx-kill and a
		// genuine script failure can return an identically-shaped (rc, err)
		// — on Windows, TerminateProcess surfaces as the same "exit status
		// 1" an in-script `exit 1` would. ctx.Err() is the only reliable
		// signal, so it is checked directly rather than inspecting
		// relayErr's text/type. Substituting context.DeadlineExceeded here
		// (discarding whatever relayErr actually said) is what routes
		// hintFor to the distinct timeout wording instead of a per-step
		// failure hint.
		effErr := relayErr
		if errors.Is(installCtx.Err(), context.DeadlineExceeded) {
			effErr = context.DeadlineExceeded
		}
		d.fail(jobID, req, activeStep, effErr, rc, tail)
		return
	}

	d.Store.Update(jobID, func(j *Job) {
		for i := range j.Steps {
			if j.Steps[i].Name == StepVerifyOnline {
				j.Steps[i].Status = StepActive
			} else {
				j.Steps[i].Status = StepDone
			}
		}
	})

	// verify_online gets its own budget/context, independent of installCtx:
	// InstallBudget bounds the relay subprocess, VerifyBudget bounds this
	// poll — a slow-but-successful install must not eat into the agent's
	// dedicated confirmation window. Still rooted in d.BaseCtx, so a server
	// shutdown still aborts it promptly.
	verifyCtx, verifyCancel := context.WithTimeout(d.BaseCtx, req.VerifyBudget)
	defer verifyCancel()

	detail, ok := VerifyOnline(verifyCtx, req.Nickname, since, req.VerifyBudget, verifyPollInterval,
		d.LastSeen, d.nowFn, verifySleep)
	if !ok {
		// Not a "timeout vs failure" ambiguity: VerifyOnline's own ok==false
		// already means exactly one thing either way (no fresh report seen
		// in time), so this always gets the plain agent_offline wording —
		// pass nil rather than verifyCtx.Err() so hintFor's ctx-deadline
		// branch can never mistakenly trigger here.
		d.fail(jobID, req, StepVerifyOnline, nil, noExitCode, tail)
		return
	}

	d.Store.Update(jobID, func(j *Job) {
		for i := range j.Steps {
			if j.Steps[i].Name == StepVerifyOnline {
				j.Steps[i].Status = StepDone
				j.Steps[i].Detail = detail
			}
		}
		j.State = StateSuccess
	})

	if d.Logger != nil {
		d.Logger.Info("provision job succeeded", "job_id", jobID, "nickname", req.Nickname, "kind", req.Kind)
	}
}

// fail records a terminal failure on jobID: the step active when the run
// ended is marked StepFailed, State becomes StateFailed, Hint is computed by
// hintFor, and Tail is the redacted ring buffer. rc is logged only (never
// surfaced on the Job) — it has no meaning once verify_online is the failing
// step, so run passes noExitCode there.
func (d Deps) fail(jobID string, req StartReq, activeStep string, hintErr error, rc int, tail []string) {
	hint := hintFor(activeStep, hintErr)
	tailStr := RedactToken(strings.Join(tail, "\n"), req.RawToken)

	d.Store.Update(jobID, func(j *Job) {
		if idx := stepIndex(j.Steps, activeStep); idx >= 0 {
			j.Steps[idx].Status = StepFailed
		}
		j.State = StateFailed
		j.Hint = hint
		j.Tail = tailStr
	})

	if d.Logger != nil {
		errText := ""
		if hintErr != nil {
			// Redact defensively even in the log: hintErr is occasionally
			// the raw relayErr (see run's rc!=0/err!=nil branch), and while
			// RelayFunc's real error shape is a low-level process error (not
			// stdout content), there is no structural guarantee of that from
			// this package's own types — cheap enough to redact regardless.
			errText = RedactToken(hintErr.Error(), req.RawToken)
		}
		d.Logger.Warn("provision job failed", "job_id", jobID, "nickname", req.Nickname, "kind", req.Kind,
			"step", activeStep, "hint", hint, "rc", rc, "err", errText)
	}
}

// stepIndex returns the index of the step named name in steps, or -1 if
// absent — used both to look up a marker's target step and to find the
// currently-active step to fail.
func stepIndex(steps []Step, name string) int {
	for i, s := range steps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// appendTail appends line to buf, keeping only the most recent
// tailRingLimit entries — a simple bounded ring buffer of every line the
// relay has streamed so far, for a failed job's redacted Tail. Lines are
// stored exactly as streamed (not the whitespace-trimmed copy used for
// marker parsing) so the Tail shown on failure is a faithful copy of what
// the terminal actually printed.
func appendTail(buf []string, line string) []string {
	buf = append(buf, line)
	if len(buf) > tailRingLimit {
		buf = buf[len(buf)-tailRingLimit:]
	}
	return buf
}

// hintFor maps a failed step + the error responsible (which may be nil) to
// a friendly Russian hint, using the wording from the provisioning-rework
// design spec's "Error handling" section verbatim wherever it applies.
//
// It never copies relayErr's text into its return value — it only tests it
// for known substrings (terminalConnectHint) — so a caller can pass it the
// raw, unredacted relay error (as run's callers do) with no risk of a secret
// travelling from relayErr into the Hint field: unlike Tail, Hint has no
// RawToken to redact against (hintFor's signature is fixed to (step,
// relayErr) with no token parameter), so the only safe design is to never
// embed arbitrary error text in the returned string at all. The one piece of
// data that IS embedded is step itself — always one of this package's own
// step name constants (run only ever calls hintFor with activeStep, which
// starts at StepTerminalConnected and is only ever advanced to a name
// ParseStepLine + stepIndex have already validated against the job's own
// template), never anything derived from relayErr.
func hintFor(step string, relayErr error) string {
	if errors.Is(relayErr, errTokenCommit) {
		return "токен агента не сохранён в БД backend — повтори установку/ремонт"
	}

	if errors.Is(relayErr, context.DeadlineExceeded) {
		return fmt.Sprintf("истёк таймаут на шаге «%s» — терминал или роутер не ответили вовремя, попробуй ещё раз", step)
	}

	switch step {
	case StepTerminalConnected:
		return terminalConnectHint(relayErr)
	case StepArchDetected:
		return "роутер сообщил неподдерживаемую архитектуру"
	case StepDownloading:
		return "роутер не смог скачать бинарь с backend"
	case StepChecksumOK:
		return "бинарь не сошёлся с подписанной суммой — релиз/зеркало битые"
	case StepConfigWritten, StepInitInstalled, StepServiceStarted, StepServiceRestarted, StepBackendURLRewrite:
		// The design spec's Error-handling section maps this whole class of
		// steps — anything past checksum verification that still didn't end
		// with a running service, whether a fresh install or a repoint's
		// restart — to one hint: service_not_started.
		return "init-скрипт не поднял сервис"
	case StepVerifyOnline:
		return "поставлен, но не позвонил домой за 120с — проверь связь роутер→backend"
	default:
		return fmt.Sprintf("что-то пошло не так на шаге «%s» — подробности смотри в логе ниже", step)
	}
}

// terminalConnectHint distinguishes the two relay-reported terminal_connected
// failure classes the design spec calls out by keying off relayErr's text —
// there is no structured error code from the relay to switch on instead
// (RelayFunc returns a plain error). A nil relayErr, or text matching
// neither known class, gets a generic connectivity hint rather than one of
// the two specific ones.
func terminalConnectHint(relayErr error) string {
	if relayErr == nil {
		return "не удалось подключиться к AWG Manager terminal — проверь awgm_url и доступность роутера"
	}
	msg := strings.ToLower(relayErr.Error())
	switch {
	case strings.Contains(msg, "session_active") || strings.Contains(msg, "active session"):
		return "AWG Manager terminal занят — закрой сессию в web-UI и повтори"
	case strings.Contains(msg, "auth_failed") || strings.Contains(msg, "success=false") ||
		strings.Contains(msg, "unauthorized") || strings.Contains(msg, "401") || strings.Contains(msg, "403"):
		return "root-пароль или awgm-логин не подошёл"
	default:
		return "не удалось подключиться к AWG Manager terminal — проверь awgm_url и доступность роутера"
	}
}
