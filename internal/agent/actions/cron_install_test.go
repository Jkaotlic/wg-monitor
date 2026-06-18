package actions

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

const cronDetectCall = "sh -c test -x " + cronInitScript

// scriptedExec records every call and answers from a per-call handler so tests
// can model "cron absent on the first probe, present after install".
type scriptedExec struct {
	calls   []string
	handler func(call string, idx int) ([]byte, error)
}

func (s *scriptedExec) fn() ExecFunc {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		call := strings.Join(append([]string{name}, args...), " ")
		idx := len(s.calls)
		s.calls = append(s.calls, call)
		return s.handler(call, idx)
	}
}

func (s *scriptedExec) count(call string) int {
	n := 0
	for _, c := range s.calls {
		if c == call {
			n++
		}
	}
	return n
}

func TestEnsureCronInstalledSkipsWhenPresent(t *testing.T) {
	se := &scriptedExec{handler: func(call string, _ int) ([]byte, error) {
		if call == cronDetectCall {
			return nil, nil // present
		}
		return nil, errors.New("unexpected exec: " + call)
	}}

	installedNow, err := ensureCronInstalled(context.Background(), se.fn())
	if err != nil {
		t.Fatal(err)
	}
	if installedNow {
		t.Fatal("installedNow should be false when cron already present")
	}
	if se.count("opkg install cron") != 0 {
		t.Fatalf("opkg install must not run when cron present, calls=%v", se.calls)
	}
}

func TestEnsureCronInstalledInstallsWhenAbsent(t *testing.T) {
	detect := 0
	se := &scriptedExec{handler: func(call string, _ int) ([]byte, error) {
		switch call {
		case cronDetectCall:
			detect++
			if detect == 1 {
				return nil, errors.New("not executable") // absent on first probe
			}
			return nil, nil // present after install
		case "opkg install cron":
			return []byte("Installing cron\n"), nil
		default:
			return nil, errors.New("unexpected exec: " + call)
		}
	}}

	installedNow, err := ensureCronInstalled(context.Background(), se.fn())
	if err != nil {
		t.Fatal(err)
	}
	if !installedNow {
		t.Fatal("installedNow should be true after a fresh install")
	}
	if se.count("opkg install cron") != 1 {
		t.Fatalf("opkg install cron expected once, calls=%v", se.calls)
	}
	if se.count("opkg update") != 0 {
		t.Fatalf("opkg update must not run when first install succeeds, calls=%v", se.calls)
	}
}

func TestEnsureCronInstalledRetriesAfterOpkgUpdate(t *testing.T) {
	detect := 0
	install := 0
	se := &scriptedExec{handler: func(call string, _ int) ([]byte, error) {
		switch call {
		case cronDetectCall:
			detect++
			if detect == 1 {
				return nil, errors.New("absent")
			}
			return nil, nil
		case "opkg install cron":
			install++
			if install == 1 {
				return []byte("unknown package 'cron'\n"), errors.New("exit 255")
			}
			return []byte("Installing cron\n"), nil
		case "opkg update":
			return []byte("Updated lists\n"), nil
		default:
			return nil, errors.New("unexpected exec: " + call)
		}
	}}

	installedNow, err := ensureCronInstalled(context.Background(), se.fn())
	if err != nil {
		t.Fatal(err)
	}
	if !installedNow {
		t.Fatal("installedNow should be true after retry")
	}
	if se.count("opkg update") != 1 || se.count("opkg install cron") != 2 {
		t.Fatalf("want one update + two installs, calls=%v", se.calls)
	}
}

func TestEnsureCronInstalledFailsWhenStillMissing(t *testing.T) {
	se := &scriptedExec{handler: func(call string, _ int) ([]byte, error) {
		switch call {
		case cronDetectCall:
			return nil, errors.New("absent") // never becomes present
		case "opkg install cron":
			return []byte("done\n"), nil
		default:
			return nil, errors.New("unexpected exec: " + call)
		}
	}}

	if _, err := ensureCronInstalled(context.Background(), se.fn()); err == nil {
		t.Fatal("expected error when cron still missing after install")
	}
}

// TestOpkgCronInstallAutoInstallsCronPackage proves the manager installs the
// cron package before writing the schedule when the router has no cron yet.
func TestOpkgCronInstallAutoInstallsCronPackage(t *testing.T) {
	m := newTestOpkgCronManager(t)
	detect := 0
	var installCron, installedCrontab bool
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		switch {
		case key == "df -k /opt":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 75000 60% /opt\n"), nil
		case key == cronDetectCall:
			detect++
			if detect == 1 {
				return nil, errors.New("absent")
			}
			return nil, nil
		case key == "opkg install cron":
			installCron = true
			return []byte("Installing cron\n"), nil
		case key == "crontab -l":
			return []byte(""), nil
		case name == "crontab" && len(args) == 1:
			installedCrontab = true
			return nil, nil
		case key == "/opt/etc/init.d/S10cron start":
			return []byte("started\n"), nil
		default:
			return nil, errors.New("unexpected exec: " + key)
		}
	}

	status, err := m.Install(context.Background(), "30 4 * * *")
	if err != nil {
		t.Fatal(err)
	}
	if !installCron {
		t.Fatal("opkg install cron was not called for a router without cron")
	}
	if !installedCrontab {
		t.Fatal("crontab was not installed")
	}
	if _, statErr := os.Stat(m.ScriptPath); statErr != nil {
		t.Fatalf("script not written: %v", statErr)
	}
	if !status.Installed || !strings.Contains(status.CronService, "installed cron package") {
		t.Fatalf("status should note cron auto-install: %+v", status)
	}
}
