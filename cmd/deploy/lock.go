package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pidLockPath returns the location of the wizard's process lock file.
func pidLockPath() string {
	return filepath.Join(defaultCacheDir(), "wizard.pid")
}

// ErrAnotherWizardRunning is returned when AcquirePIDLock detects a live
// wizard process already holding the lock. Caller surfaces a clear "another
// wizard is running (PID N)" message and aborts.
var ErrAnotherWizardRunning = errors.New("another wizard is already running")

// AcquirePIDLock atomically claims the wizard.pid file. Returns:
//   - nil + release-fn on success; release-fn deletes the lock file (caller
//     defers it).
//   - ErrAnotherWizardRunning when an existing pid file points to a live
//     process.
//   - other errors only on filesystem failure (cache dir not writable etc.).
//
// Stale lock recovery: if wizard.pid exists but its PID is dead (e.g. previous
// run was kill-9'd), we steal the lock and write our own PID. This is safe
// because mid-state writes are atomic (temp+rename) — there's no half-file
// to clean up.
func AcquirePIDLock() (release func(), err error) {
	if err := os.MkdirAll(defaultCacheDir(), 0o700); err != nil {
		return nil, err
	}
	path := pidLockPath()
	mypid := os.Getpid()

	// Try exclusive create first — happy path on a clean system.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", mypid)
		_ = f.Close()
		return func() { _ = os.Remove(path) }, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}

	// File exists — read PID and check liveness.
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		return nil, rerr
	}
	other, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil || other <= 0 {
		// Corrupt PID file — overwrite (no live owner can be there).
		return forcePIDLockTakeover(path, mypid)
	}
	if other == mypid {
		// Re-entry — same process; should not happen in practice.
		return func() { _ = os.Remove(path) }, nil
	}
	if pidIsLive(other) {
		return nil, fmt.Errorf("%w (PID %d). Lock file: %s. "+
			"Если ты уверен что тот процесс мёртв (Task Manager / ps -ef показывает иначе), удали файл вручную.",
			ErrAnotherWizardRunning, other, path)
	}
	// Dead PID — steal.
	return forcePIDLockTakeover(path, mypid)
}

func forcePIDLockTakeover(path string, mypid int) (func(), error) {
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", mypid)), 0o600); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}
