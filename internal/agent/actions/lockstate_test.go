package actions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestMkdirLockState(t *testing.T) {
	const stale = 2 * time.Hour
	now := time.Now()
	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		pid  string // "" -- файла pid нет
		age  time.Duration
		want LockState
	}{
		{"owner alive", strconv.Itoa(os.Getpid()), time.Minute, LockHeld},
		{"owner alive, old dir", strconv.Itoa(os.Getpid()), 3 * time.Hour, LockHeld},
		{"owner dead, fresh dir", strconv.Itoa(dead.Process.Pid), time.Minute, LockAbandoned},
		{"no pid, fresh", "", time.Minute, LockHeldUnknownOwner},
		{"no pid, old", "", 3 * time.Hour, LockAbandoned},
		{"garbage pid, fresh", "abc", time.Minute, LockHeldUnknownOwner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "x.lock")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.pid != "" {
				if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(tc.pid+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			at := now.Add(-tc.age)
			if err := os.Chtimes(dir, at, at); err != nil {
				t.Fatal(err)
			}
			if got := MkdirLockState(dir, now, stale); got != tc.want {
				t.Fatalf("state = %v; want %v", got, tc.want)
			}
		})
	}
	if got := MkdirLockState(filepath.Join(t.TempDir(), "none"), now, stale); got != LockFree || got.Held() {
		t.Fatalf("missing dir: %v", got)
	}
}
