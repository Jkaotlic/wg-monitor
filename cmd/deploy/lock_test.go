package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockFileAlreadyExistsTreatsWindowsPermissionAsExistingLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wizard.pid")
	if err := os.WriteFile(path, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !lockFileAlreadyExists(path, fs.ErrPermission) {
		t.Fatal("expected permission error on existing pid file to be handled as existing lock")
	}
}

func TestFormatPIDLockHeldErrorGivesSafeDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wizard.pid")
	err := formatPIDLockHeldError(1234, path)
	text := err.Error()
	for _, want := range []string{
		"PID 1234",
		path,
		"Get-Process -Id 1234",
		"ps -p 1234",
		"Remove-Item -LiteralPath",
		"remove the lock only after confirming",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("lock error missing %q:\n%s", want, text)
		}
	}
}

func TestAcquirePIDLockFileRejectsSecondHolderInSameProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wizard.pid")
	release, err := acquirePIDLockFile(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	secondRelease, err := acquirePIDLockFile(path)
	if err == nil {
		secondRelease()
		t.Fatal("second lock succeeded; expected ErrAnotherWizardRunning")
	}
	if !errors.Is(err, ErrAnotherWizardRunning) {
		t.Fatalf("second lock error=%v, want ErrAnotherWizardRunning", err)
	}

	release()
	release = func() {}
	if releaseAgain, err := acquirePIDLockFile(path); err != nil {
		t.Fatalf("lock after release: %v", err)
	} else {
		releaseAgain()
	}
}
