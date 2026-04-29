package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkOpkgRunner(t *testing.T, exec ExecFunc) *OpkgRunner {
	t.Helper()
	dir := t.TempDir()
	return &OpkgRunner{
		LockPath: filepath.Join(dir, "opkg.lock"),
		LockTTL:  100 * time.Millisecond,
		Exec:     exec,
		Now:      time.Now,
	}
}

func TestOpkg_DryRun_Empty_Ok(t *testing.T) {
	calls := []string{}
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		// `opkg list-upgradable` returns empty body when nothing to upgrade
		if args[0] == "list-upgradable" {
			return []byte(""), nil
		}
		return []byte("opkg update OK"), nil
	})
	status, output := o.DryRun(context.Background())
	if status != "ok" {
		t.Errorf("status: %q", status)
	}
	if !strings.Contains(output, "up to date") {
		t.Errorf("expected 'up to date' summary, got %q", output)
	}
	wantCalls := []string{"opkg update", "opkg list-upgradable"}
	if !equalStrings(calls, wantCalls) {
		t.Errorf("calls: got %v want %v", calls, wantCalls)
	}
}

func TestOpkg_DryRun_NonEmpty_Ok(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "list-upgradable" {
			return []byte("wg-monitor - 0.4.0 - 0.5.0\nlibssl - 3.0.0 - 3.0.1\n"), nil
		}
		return nil, nil
	})
	status, output := o.DryRun(context.Background())
	if status != "ok" {
		t.Errorf("status: %q", status)
	}
	if !strings.Contains(output, "wg-monitor") || !strings.Contains(output, "libssl") {
		t.Errorf("expected upgrade list in output, got %q", output)
	}
}

func TestOpkg_DryRun_Locked_WhenFreshLock(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("opkg should not be invoked when locked")
		return nil, nil
	})
	// Pre-create a fresh lock file (mtime now)
	if err := os.WriteFile(o.LockPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	status, output := o.DryRun(context.Background())
	if status != "locked" {
		t.Errorf("status: %q output: %q", status, output)
	}
}

func TestOpkg_DryRun_StaleLockReleased(t *testing.T) {
	called := 0
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		called++
		if args[0] == "list-upgradable" {
			return []byte(""), nil
		}
		return nil, nil
	})
	// Pre-create a STALE lock file (mtime 2 TTL ago)
	if err := os.WriteFile(o.LockPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-1 * time.Second)
	_ = os.Chtimes(o.LockPath, stale, stale)
	status, _ := o.DryRun(context.Background())
	if status != "ok" {
		t.Errorf("expected stale lock to be ignored, got status=%q", status)
	}
	if called == 0 {
		t.Errorf("expected opkg to be invoked despite stale lock")
	}
}

func TestOpkg_DryRun_OpkgUpdateFails_Errs(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "update" {
			return []byte("network down"), errors.New("exit 1")
		}
		return nil, nil
	})
	status, output := o.DryRun(context.Background())
	if status != "err" {
		t.Errorf("status: %q", status)
	}
	if !strings.Contains(output, "exit 1") && !strings.Contains(output, "network down") {
		t.Errorf("output should describe failure, got %q", output)
	}
}

func TestOpkg_DryRun_ReleasesLockOnExit(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(""), nil
	})
	_, _ = o.DryRun(context.Background())
	if _, err := os.Stat(o.LockPath); !os.IsNotExist(err) {
		t.Errorf("lock file should be removed after DryRun, got err=%v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
