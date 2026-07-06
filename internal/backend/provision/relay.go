package provision

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/anex/wg-monitor/internal/awgmrelay"
)

// RelayFunc runs the relay for a marshalled job and calls onLine for every
// stdout line as it arrives. Returns the script rc (0 == success) and error.
// Injectable so the engine can be tested without a real router.
type RelayFunc func(ctx context.Context, relayPath string, jobJSON []byte, onLine func(string)) (rc int, err error)

// noExitCode is returned as rc whenever the relay process never produced a
// real exit status of its own: ctx was already canceled, script/job-file
// provisioning failed, or Start failed. It reuses exec.ExitError.ExitCode's
// own documented sentinel for "process hasn't exited or was terminated by a
// signal" (-1), so a caller comparing rc against a genuine process exit code
// (always 0-255) can't mistake this for one.
const noExitCode = -1

// DefaultRelay streams python3 relay stdout line-by-line via StdoutPipe+bufio.Scanner.
//
// It is the streaming counterpart of backend.runRelayProcess (see
// internal/backend/agent_revive.go), which buffers the whole run behind
// CombinedOutput and only returns once the process exits. That's fine for a
// single-shot revive call, but the provisioning engine needs to react to
// __WG_STEP__ markers (steps.go) as they happen, so it can move a Job's
// checklist forward live while the dashboard is polling — hence onLine.
//
// ctx must be a server-owned context.WithTimeout supplied by the caller,
// never a request context: an HTTP request's context dies with the
// response, but the relay run has to outlive it. DefaultRelay never
// constructs a background/never-cancel context of its own — it only ever
// acts on the one it is given. exec.CommandContext kills the child the
// moment ctx is done, so the caller's timeout is what bounds a hung or dark
// router; DefaultRelay also checks ctx up front, before touching the
// filesystem, so an already-expired ctx fails fast with no side effects.
//
// relayPath is an optional wizard-installed override (see
// resolveRelayScript); jobJSON is the already-marshalled job config the
// relay reads from a temp file. Both the resolved script (when
// self-provisioned) and the job file are removed before DefaultRelay
// returns, success or failure alike — the job file in particular may carry
// transient credentials that must not persist (mirrors runAWGMRelayJob's
// existing contract in agent_revive.go).
func DefaultRelay(ctx context.Context, relayPath string, jobJSON []byte, onLine func(string)) (int, error) {
	if err := ctx.Err(); err != nil {
		return noExitCode, err
	}

	scriptPath, cleanupScript, err := resolveRelayScript(relayPath)
	if err != nil {
		return noExitCode, fmt.Errorf("provision awgm relay: %w", err)
	}
	defer cleanupScript()

	jobPath, cleanupJob, err := writeJobFile(jobJSON)
	if err != nil {
		return noExitCode, err
	}
	defer cleanupJob()

	cmd := exec.CommandContext(ctx, "python3", scriptPath, jobPath)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return noExitCode, err
	}
	// Alias stderr onto the exact same *os.File value as stdout so
	// exec.Cmd's internal childStderr wiring reuses stdout's single pipe
	// instead of opening a second one (it special-cases "c.Stderr ==
	// c.Stdout" via a plain pointer-equality check to share one fd — see
	// os/exec's childStderr/interfaceEqual). That gives one merged stream,
	// drained by the single streamLines call below, so onLine is only ever
	// invoked from this one goroutine: a caller's onLine (the engine uses it
	// to mutate a Job under Store.Update) never has to be safe for
	// concurrent invocation from two readers. It also interleaves
	// stdout/stderr bytes in true kernel write() order — the same guarantee
	// shell's "2>&1" gives — rather than racing two independent Go pipes
	// against goroutine scheduling.
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return noExitCode, err
	}

	// Drain to natural EOF — the child closing its fd, whether on normal
	// exit or on ctx-triggered Kill — before calling Wait: StdoutPipe's own
	// doc requires every read to finish first, since Wait closes the pipe
	// itself once it reaps the process, which would otherwise race this
	// Read.
	streamLines(stdout, onLine)

	waitErr := cmd.Wait()
	return exitCode(waitErr), waitErr
}

// streamLines reads r line-by-line — bufio.Scanner's default ScanLines
// split, which also strips a trailing '\r' so a CRLF-emitting script behaves
// identically to an LF-only one — and calls onLine for each line as it
// arrives. This is the piece of DefaultRelay's streaming loop that can be
// unit-tested without exec: DefaultRelay wires it to a live process's merged
// stdout/stderr pipe, but the split/dispatch logic only needs an io.Reader.
//
// A Scan error (a line over bufio.MaxScanTokenSize, or the pipe breaking
// when ctx kills the process mid-write) just ends the loop early rather than
// panicking or returning an error of its own — DefaultRelay's caller still
// learns the run failed via cmd.Wait's error/exit code, so staying silent
// here doesn't hide anything.
func streamLines(r io.Reader, onLine func(string)) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		onLine(scanner.Text())
	}
}

// exitCode derives a relay run's rc from cmd.Wait's returned error: 0 on
// success, the process's real exit code when it ran and exited non-zero
// (exec.ExitError — e.g. the relay's own convention for a failed step), or
// noExitCode for anything else (an error that is not an ExitError: the
// process was never started, or did not exit normally).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return noExitCode
}

// resolveRelayScript returns the path to the awgm-relay.py to execute.
//
// Ported from backend.resolveRelayScript (internal/backend/agent_revive.go)
// rather than called directly: package backend will import this package
// (the provisioning engine + its HTTP handlers), so the reverse import would
// cycle. The two copies must be kept in sync by hand until a later task
// retires the original — agent_revive.go's resolveRelayScript/
// runRelayProcess are untouched by this change, still serving the existing
// dashboard "Revive via AWG Manager" path.
//
// A wizard-installed copy at override is used as-is when it exists;
// otherwise the embedded relay is written to a 0700 temp file, so this path
// needs no wizard step. cleanup removes the temp file (a no-op for the
// override path).
func resolveRelayScript(override string) (path string, cleanup func(), err error) {
	if override != "" {
		if _, statErr := os.Stat(override); statErr == nil {
			return override, func() {}, nil
		}
	}
	f, err := os.CreateTemp("", "wg-monitor-awgm-relay-*.py")
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	cleanup = func() { _ = os.Remove(name) }
	if err := f.Chmod(0o700); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := f.Write(awgmrelay.Script); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return name, cleanup, nil
}

// writeJobFile writes jobJSON to a 0600 temp file for the relay to read —
// mirrors the temp-file block in backend.runRelayProcess
// (internal/backend/agent_revive.go), ported for the same import-cycle
// reason as resolveRelayScript. cleanup removes the file: the job may carry
// transient credentials (e.g. a revive job's router root password, or an
// enrollment token) that must not outlive this single relay run.
func writeJobFile(jobJSON []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "wg-monitor-relayjob-*.json")
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	cleanup = func() { _ = os.Remove(name) }
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := f.Write(jobJSON); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return name, cleanup, nil
}
