package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExecFunc is the contract for invoking external programs from OpkgRunner.
// Returns combined-output bytes plus an error (matching exec.Cmd.CombinedOutput).
// Injectable so tests don't need a real opkg binary.
type ExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultExec uses os/exec with combined stderr+stdout capture.
func DefaultExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// OpkgRunner enforces a lock-file so two opkg actions don't overlap and
// performs the dry-run preflight (`opkg update` + `opkg list-upgradable`).
//
// The actual `opkg upgrade` step is intentionally NOT executed yet — that's
// deferred to a later iteration where the TG flow includes a confirm step.
// Today's purpose: surface "what would change" to the admin in TG without
// touching the system.
type OpkgRunner struct {
	LockPath string
	LockTTL  time.Duration
	Exec     ExecFunc
	Now      func() time.Time
}

func (o *OpkgRunner) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// DryRun returns ("locked", ...) if a fresh lock-file exists, otherwise
// runs the preflight, releases the lock, and returns ("ok", listing) or
// ("err", explanation).
func (o *OpkgRunner) DryRun(ctx context.Context) (status, output string) {
	if held, age, ok := o.lockHeldFresh(); ok {
		return "locked", fmt.Sprintf("opkg lock held by another op (age %v, lock file: %s)", age.Round(time.Second), held)
	}
	if err := o.takeLock(); err != nil {
		return "err", "acquire lock: " + err.Error()
	}
	defer o.releaseLock()

	if out, err := o.Exec(ctx, "opkg", "update"); err != nil {
		return "err", "opkg update failed: " + err.Error() + "\n" + string(out)
	}
	listing, err := o.Exec(ctx, "opkg", "list-upgradable")
	if err != nil {
		return "err", "opkg list-upgradable failed: " + err.Error() + "\n" + string(listing)
	}
	if strings.TrimSpace(string(listing)) == "" {
		return "ok", "all packages up to date"
	}
	return "ok", "upgradable packages:\n" + string(listing)
}

func (o *OpkgRunner) lockHeldFresh() (path string, age time.Duration, ok bool) {
	st, err := os.Stat(o.LockPath)
	if err != nil {
		return "", 0, false
	}
	age = o.now().Sub(st.ModTime())
	if age < o.LockTTL {
		return o.LockPath, age, true
	}
	return "", age, false
}

func (o *OpkgRunner) takeLock() error {
	if err := os.MkdirAll(parentDir(o.LockPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(o.LockPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	if cerr := f.Close(); cerr != nil {
		return cerr
	}
	// Touch mtime to "now" — covers the case where the file existed but was stale.
	now := o.now()
	if err := os.Chtimes(o.LockPath, now, now); err != nil {
		return err
	}
	return nil
}

func (o *OpkgRunner) releaseLock() {
	if err := os.Remove(o.LockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		// best-effort: nothing meaningful to do; next call will see stale-lock.
	}
}

// parentDir returns filepath.Dir without importing path/filepath at the top
// (keep imports tight). For lock paths like "/opt/var/wg-monitor/opkg.lock"
// this yields "/opt/var/wg-monitor".
func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
