package main

import (
	"errors"
	"fmt"
	"io"
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
// wizard process already holding the lock.
var ErrAnotherWizardRunning = errors.New("another wizard is already running")

// AcquirePIDLock atomically claims the wizard.pid file. The PID file is backed
// by a non-blocking OS file lock so stale-lock takeover is serialized.
func AcquirePIDLock() (release func(), err error) {
	if err := os.MkdirAll(defaultCacheDir(), 0o700); err != nil {
		return nil, err
	}
	return acquirePIDLockFile(pidLockPath())
}

func acquirePIDLockFile(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	mypid := os.Getpid()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		if lockFileAlreadyExists(path, err) {
			data, _ := os.ReadFile(path)
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 0 {
				return nil, formatPIDLockHeldError(pid, path)
			}
			return nil, fmt.Errorf("%w. Lock file: %s", ErrAnotherWizardRunning, path)
		}
		return nil, err
	}
	if err := tryExclusivePIDFileLock(f); err != nil {
		_ = f.Close()
		data, _ := os.ReadFile(path)
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 0 {
			return nil, formatPIDLockHeldError(pid, path)
		}
		return nil, fmt.Errorf("%w. Lock file: %s", ErrAnotherWizardRunning, path)
	}

	release = func() {
		_ = unlockPIDFile(f)
		_ = f.Close()
		_ = os.Remove(path)
	}

	data, err := readLockedPIDFile(f)
	if err != nil {
		release()
		return nil, err
	}
	if other, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && other > 0 && other != mypid && pidIsLive(other) {
		release()
		return nil, formatPIDLockHeldError(other, path)
	}
	if err := f.Truncate(0); err != nil {
		release()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		release()
		return nil, err
	}
	if _, err := fmt.Fprintf(f, "%d\n", mypid); err != nil {
		release()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func readLockedPIDFile(f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func formatPIDLockHeldError(pid int, path string) error {
	return fmt.Errorf("%w (PID %d). Lock file: %s.\n"+
		"Check the lock owner:\n"+
		"  Windows: Get-Process -Id %d | Select-Object Id,ProcessName,Path\n"+
		"  Linux/macOS: ps -p %d -o pid,comm,args\n"+
		"If this is not deploy wizard, or the PID is already dead, remove the lock only after confirming that above:\n"+
		"  Windows: Remove-Item -LiteralPath %q\n"+
		"  Linux/macOS: rm %q",
		ErrAnotherWizardRunning, pid, path, pid, pid, path, path)
}

func lockFileAlreadyExists(path string, err error) bool {
	if os.IsExist(err) {
		return true
	}
	if os.IsPermission(err) {
		if _, statErr := os.Stat(path); statErr == nil {
			return true
		}
	}
	return false
}
