package main

import (
	"io/fs"
	"os"
	"path/filepath"
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
