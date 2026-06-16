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

func TestOpkgCronInstallRefusesLowOptSpace(t *testing.T) {
	m := newTestOpkgCronManager(t)
	m.MinFreeKB = 10_240
	m.Exec = fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt": {out: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 20000 19000 500 98% /opt\n"},
	})

	_, err := m.Install(context.Background(), "30 4 * * *")

	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("err=%v, want low-space refusal", err)
	}
	if _, statErr := os.Stat(m.ScriptPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("script should not be written on low space, stat err=%v", statErr)
	}
}

func TestOpkgCronInstallWritesScriptAndManagedCrontab(t *testing.T) {
	m := newTestOpkgCronManager(t)
	var installedCrontab string
	m.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		switch {
		case key == "df -k /opt":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 75000 60% /opt\n"), nil
		case key == "crontab -l":
			return []byte("# existing\n1 1 * * * /bin/true\n"), nil
		case name == "crontab" && len(args) == 1:
			b, err := os.ReadFile(args[0])
			if err != nil {
				return nil, err
			}
			installedCrontab = string(b)
			return []byte("installed\n"), nil
		case key == "/opt/etc/init.d/S10cron start":
			return []byte("started\n"), nil
		default:
			return nil, errors.New("unexpected exec: " + key)
		}
	}

	status, err := m.Install(context.Background(), "45 3 * * *")

	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(m.ScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "opkg update") || !strings.Contains(string(script), "opkg upgrade") {
		t.Fatalf("script missing opkg pipeline:\n%s", script)
	}
	if !strings.Contains(string(script), "MIN_FREE_KB=10240") || !strings.Contains(string(script), "trim_log") {
		t.Fatalf("script missing space guard or log cleanup:\n%s", script)
	}
	if !strings.Contains(installedCrontab, "# wg-monitor opkg auto-upgrade begin") ||
		!strings.Contains(installedCrontab, "45 3 * * * "+m.ScriptPath) ||
		!strings.Contains(installedCrontab, "1 1 * * * /bin/true") {
		t.Fatalf("bad crontab:\n%s", installedCrontab)
	}
	if !status.Installed || status.Schedule != "45 3 * * *" || status.FreeKB != 75000 || status.MinFreeKB != 10240 {
		t.Fatalf("status=%+v", status)
	}
}

func TestNormalizeOpkgCronScheduleRejectsUnsafeFields(t *testing.T) {
	for _, schedule := range []string{
		"* * * * ;",
		"*/5 * * * MON",
		"* * * * $(reboot)",
	} {
		t.Run(schedule, func(t *testing.T) {
			if _, err := NormalizeOpkgCronSchedule(schedule); err == nil {
				t.Fatal("expected unsafe schedule to be rejected")
			}
		})
	}
}

func TestOpkgCronStatusReadsManagedCrontabAndLogTail(t *testing.T) {
	m := newTestOpkgCronManager(t)
	if err := os.MkdirAll(filepath.Dir(m.ScriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ScriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(m.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	log := "2026-06-11T03:45:00Z start\n2026-06-11T03:45:02Z ok\n"
	if err := os.WriteFile(m.LogPath, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Exec = fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt": {out: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 75000 60% /opt\n"},
		"crontab -l": {out: "# wg-monitor opkg auto-upgrade begin\n15 5 * * * " + m.ScriptPath + "\n# wg-monitor opkg auto-upgrade end\n"},
	})

	status, err := m.Status(context.Background(), 20)

	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.Schedule != "15 5 * * *" || status.LastStatus != "ok" || !strings.Contains(status.LogTail, "ok") {
		t.Fatalf("status=%+v", status)
	}
}

func TestOpkgCronDfOptRejectsNonNumericFields(t *testing.T) {
	m := newTestOpkgCronManager(t)
	m.Exec = fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt": {out: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 nope 100000 also-nope 60% /opt\n"},
	})

	if _, _, err := m.dfOpt(context.Background()); err == nil {
		t.Fatal("dfOpt should reject non-numeric total/free fields")
	}
}

func TestOpkgCronRemoveDropsManagedCrontabAndFiles(t *testing.T) {
	m := newTestOpkgCronManager(t)
	if err := os.MkdirAll(filepath.Dir(m.ScriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ScriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var installedCrontab string
	m.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		switch {
		case key == "df -k /opt":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 75000 60% /opt\n"), nil
		case key == "crontab -l":
			return []byte("# keep\n# wg-monitor opkg auto-upgrade begin\n15 5 * * * " + m.ScriptPath + "\n# wg-monitor opkg auto-upgrade end\n"), nil
		case name == "crontab" && len(args) == 1:
			b, err := os.ReadFile(args[0])
			if err != nil {
				return nil, err
			}
			installedCrontab = string(b)
			return nil, nil
		default:
			return nil, errors.New("unexpected exec: " + key)
		}
	}

	status, err := m.Remove(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	if status.Installed {
		t.Fatalf("status=%+v", status)
	}
	if strings.Contains(installedCrontab, "wg-monitor opkg auto-upgrade") || !strings.Contains(installedCrontab, "# keep") {
		t.Fatalf("bad crontab after remove:\n%s", installedCrontab)
	}
	if _, err := os.Stat(m.ScriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("script should be removed, err=%v", err)
	}
}

type fakeExecResult struct {
	out string
	err error
}

func fakeOpkgCronExec(results map[string]fakeExecResult) ExecFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		res, ok := results[key]
		if !ok {
			return nil, errors.New("unexpected exec: " + key)
		}
		return []byte(res.out), res.err
	}
}

func newTestOpkgCronManager(t *testing.T) *OpkgCronManager {
	t.Helper()
	root := t.TempDir()
	return &OpkgCronManager{
		ScriptPath: filepath.Join(root, "etc", "wg-monitor", "opkg-auto-upgrade.sh"),
		LogPath:    filepath.Join(root, "var", "log", "wg-monitor", "opkg-auto-upgrade.log"),
		MinFreeKB:  10_240,
		MaxLogKB:   64,
		Now: func() time.Time {
			return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
		},
	}
}
