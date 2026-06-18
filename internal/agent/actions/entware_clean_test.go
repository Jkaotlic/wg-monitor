package actions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestEntwareCleanInstallWritesSafeScriptAndManagedCrontab(t *testing.T) {
	m := newTestEntwareCleanManager(t)
	var installedCrontab string
	m.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		switch {
		case key == "df -k /opt":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 75000 60% /opt\n"), nil
		case key == "sh -c test -x "+cronInitScript:
			return nil, nil // cron package already present
		case key == "cat /proc/meminfo":
			return []byte("MemTotal:         250000 kB\nMemAvailable:      72000 kB\n"), nil
		case key == "crontab -l":
			if installedCrontab != "" {
				return []byte(installedCrontab), nil
			}
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

	status, err := m.Install(context.Background(), "05:15")

	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(m.ScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(script)
	for _, want := range []string{"MemAvailable", "drop_caches", "/opt/tmp", "/opt/var/tmp", "MIN_MEM_AVAILABLE_KB=65536", "trim_log"} {
		if !strings.Contains(s, want) {
			t.Fatalf("script missing %q:\n%s", want, s)
		}
	}
	for _, unsafe := range []string{"opkg remove", "kill ", "rm -rf /opt", "/opt/etc/*", "/opt/etc/"} {
		if strings.Contains(s, unsafe) {
			t.Fatalf("script contains unsafe fragment %q:\n%s", unsafe, s)
		}
	}
	if !strings.Contains(installedCrontab, "# wg-monitor entware cleanup begin") ||
		!strings.Contains(installedCrontab, "15 5 * * * "+m.ScriptPath) ||
		!strings.Contains(installedCrontab, "1 1 * * * /bin/true") {
		t.Fatalf("bad crontab:\n%s", installedCrontab)
	}
	if !status.Installed || status.Schedule != "15 5 * * *" || status.FreeKB != 75000 || status.MemAvailableKB != 72000 {
		t.Fatalf("status=%+v", status)
	}
}

func TestNormalizeEntwareCleanScheduleRejectsUnsafeFields(t *testing.T) {
	for _, schedule := range []string{
		"* * * * ;",
		"*/5 * * * MON",
		"* * * * $(reboot)",
	} {
		t.Run(schedule, func(t *testing.T) {
			if _, err := NormalizeEntwareCleanSchedule(schedule); err == nil {
				t.Fatal("expected unsafe schedule to be rejected")
			}
		})
	}
}

func TestEntwareCleanStatusReadsManagedCrontabMemoryAndLogTail(t *testing.T) {
	m := newTestEntwareCleanManager(t)
	if err := os.MkdirAll(filepath.Dir(m.ScriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ScriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(m.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	log := "2026-06-11T05:15:00Z status=start mem_before_kb=40000\n2026-06-11T05:15:02Z status=ok mem_before_kb=40000 mem_after_kb=82000 freed_kb=42000 opt_free_kb=76000\n"
	if err := os.WriteFile(m.LogPath, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Exec = fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt":        {out: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 76000 60% /opt\n"},
		"cat /proc/meminfo": {out: "MemTotal:         250000 kB\nMemAvailable:      82000 kB\n"},
		"crontab -l":        {out: "# wg-monitor entware cleanup begin\n15 5 * * * " + m.ScriptPath + "\n# wg-monitor entware cleanup end\n"},
	})

	status, err := m.Status(context.Background(), 20)

	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.Schedule != "15 5 * * *" || status.LastStatus != "ok" ||
		status.LastFreedKB != 42000 || status.MemAvailableKB != 82000 || !strings.Contains(status.LogTail, "freed_kb=42000") {
		t.Fatalf("status=%+v", status)
	}
}

func TestEntwareCleanDfOptRejectsNonNumericFields(t *testing.T) {
	m := newTestEntwareCleanManager(t)
	m.Exec = fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt": {out: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 nope 100000 also-nope 60% /opt\n"},
	})

	if _, _, err := m.dfOpt(context.Background()); err == nil {
		t.Fatal("dfOpt should reject non-numeric total/free fields")
	}
}

func TestEntwareCleanRunInvokesManagedScriptAndReturnsStatus(t *testing.T) {
	m := newTestEntwareCleanManager(t)
	if err := os.MkdirAll(filepath.Dir(m.ScriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ScriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var ran bool
	m.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		switch key {
		case m.ScriptPath:
			ran = true
			return []byte("ok\n"), nil
		case "df -k /opt":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 76000 60% /opt\n"), nil
		case "cat /proc/meminfo":
			return []byte("MemTotal:         250000 kB\nMemAvailable:      90000 kB\n"), nil
		case "crontab -l":
			return []byte("# wg-monitor entware cleanup begin\n15 5 * * * " + m.ScriptPath + "\n# wg-monitor entware cleanup end\n"), nil
		default:
			return nil, errors.New("unexpected exec: " + key)
		}
	}

	status, err := m.Run(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	if !ran || !status.Installed || status.MemAvailableKB != 90000 {
		t.Fatalf("ran=%v status=%+v", ran, status)
	}
}

func TestRunnerEntwareCleanStatusDispatches(t *testing.T) {
	exec := fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt":        {out: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 75000 60% /opt\n"},
		"cat /proc/meminfo": {out: "MemTotal:         250000 kB\nMemAvailable:      72000 kB\n"},
		"crontab -l":        {out: ""},
	})
	r := Runner{Now: mockNow(), Exec: exec}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "entware-status",
		Action: "entware_clean_status",
		Args:   map[string]any{"lines": float64(10)},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%s", res.Status, res.Output)
	}
	var status wire.EntwareCleanStatus
	if err := json.Unmarshal([]byte(res.Output), &status); err != nil {
		t.Fatalf("decode: %v output=%s", err, res.Output)
	}
	if status.ScriptPath == "" || status.FreeKB != 75000 || status.MemAvailableKB != 72000 {
		t.Fatalf("status=%+v", status)
	}
}

func newTestEntwareCleanManager(t *testing.T) *EntwareCleanManager {
	t.Helper()
	root := t.TempDir()
	return &EntwareCleanManager{
		ScriptPath:        filepath.Join(root, "etc", "wg-monitor", "entware-cleanup.sh"),
		LogPath:           filepath.Join(root, "var", "log", "wg-monitor", "entware-cleanup.log"),
		MinFreeKB:         2048,
		MinMemAvailableKB: 65_536,
		MaxLogKB:          64,
		Now: func() time.Time {
			return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
		},
	}
}
