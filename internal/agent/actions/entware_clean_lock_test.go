package actions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// AG-01: «Почистить сейчас» обрывалась сроком по умолчанию (45 с), а мини-апп
// ждёт 6 минут. Своего срока должно хватать на find+rm+sync на медленной
// флешке и при этом кончаться раньше ожидания мини-аппа (360 с).
func TestEntwareCleanRunTimeoutFitsMiniappWait(t *testing.T) {
	got := actionTimeoutFor("entware_clean_run")
	if got < 300*time.Second || got >= 360*time.Second {
		t.Fatalf("entware_clean_run timeout = %v, want [300s, 360s)", got)
	}
}

// runEntwareCleanScript прогоняет НАСТОЯЩИЙ скрипт очистки через sh: замок и
// журнал -- в песочнице, df -- подставной. Возвращает журнал и вывод.
func runEntwareCleanScript(t *testing.T, lock string) (log, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	d := t.TempDir()
	shims := filepath.Join(d, "shims")
	if err := os.MkdirAll(shims, 0o755); err != nil {
		t.Fatal(err)
	}
	df := "#!/bin/sh\necho 'Filesystem 1K-blocks Used Available Use% Mounted'\necho '/dev/sda1 1000000 1000 900000 1% /opt'\n"
	if err := os.WriteFile(filepath.Join(shims, "df"), []byte(df), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(d, "clean.log")
	s, err := (&EntwareCleanManager{LogPath: logPath, LockDir: lock}).scriptText()
	if err != nil {
		t.Fatal(err)
	}
	const oldPath = "PATH=/opt/bin:/opt/sbin:/usr/bin:/usr/sbin:/bin:/sbin\n"
	if !strings.Contains(s, oldPath) {
		t.Fatalf("script has no %q", oldPath)
	}
	s = strings.Replace(s, oldPath, "PATH="+shims+":/usr/bin:/bin:/usr/sbin:/sbin\n", 1)
	script := filepath.Join(d, "clean.sh")
	if err := os.WriteFile(script, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command("sh", script).CombinedOutput()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, b)
	}
	lb, _ := os.ReadFile(logPath)
	return string(lb), string(b)
}

func entwareLockWithPid(t *testing.T, pid int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wg-monitor-entware-cleanup.lock")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// AG-01: замок, оставленный убитым по сроку скриптом (pid мёртв), следующий
// запуск снимает и чистит; замок живого -- «занято», и это видно в выводе.
func TestEntwareCleanScript_AbandonedLockTakenOver(t *testing.T) {
	t.Run("owner dead", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		lock := entwareLockWithPid(t, cmd.Process.Pid)
		log, out := runEntwareCleanScript(t, lock)
		if !strings.Contains(log, "status=ok") || strings.Contains(log, "status=locked") || strings.Contains(out, "status=locked") {
			t.Fatalf("abandoned lock blocked the cleanup: out=%q log:\n%s", out, log)
		}
		if _, err := os.Stat(lock); !os.IsNotExist(err) {
			t.Fatalf("lock not released after the run: %v", err)
		}
	})
	t.Run("owner alive", func(t *testing.T) {
		lock := entwareLockWithPid(t, os.Getpid())
		log, out := runEntwareCleanScript(t, lock)
		if !strings.Contains(log, "status=locked") || !strings.Contains(out, "status=locked") {
			t.Fatalf("live owner's lock ignored: out=%q log:\n%s", out, log)
		}
		if b, err := os.ReadFile(filepath.Join(lock, "pid")); err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
			t.Fatalf("live owner's lock touched: %q %v", b, err)
		}
	})
	t.Run("own pid written and released", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "wg-monitor-entware-cleanup.lock")
		s, err := (&EntwareCleanManager{LockDir: lock}).scriptText()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`echo $$ > "$LOCK/pid"`, `rm -f "$LOCK/pid"`, "LOCK=" + strconv.Quote(lock) + "\n"} {
			if !strings.Contains(s, want) {
				t.Fatalf("script missing %q", want)
			}
		}
		if _, out := runEntwareCleanScript(t, lock); strings.Contains(out, "locked") {
			t.Fatalf("free lock reported busy: %q", out)
		}
		if _, err := os.Stat(lock); !os.IsNotExist(err) {
			t.Fatalf("lock left after a normal run: %v", err)
		}
	})
}

// fakeEntwareExec -- df/meminfo/crontab подставные, сам скрипт отвечает out.
func fakeEntwareExec(m *EntwareCleanManager, scriptOut string, ran *bool) ExecFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		switch key {
		case m.ScriptPath:
			*ran = true
			return []byte(scriptOut), nil
		case "df -k /opt":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 76000 60% /opt\n"), nil
		case "cat /proc/meminfo":
			return []byte("MemTotal:         250000 kB\nMemAvailable:      90000 kB\n"), nil
		case "crontab -l":
			return nil, nil
		}
		return nil, errors.New("unexpected exec: " + key)
	}
}

// AG-01: скрипт сказал «занято» -- Run не говорит «готово»: ответ раннера
// locked (мини-апп показывает «на роутере уже идёт обслуживание»).
func TestEntwareCleanRun_LockedIsNotOK(t *testing.T) {
	m := newTestEntwareCleanManager(t)
	var ran bool
	m.Exec = fakeEntwareExec(m, "status=locked another run is active\n", &ran)
	if _, err := m.Run(context.Background()); !errors.Is(err, ErrEntwareCleanBusy) {
		t.Fatalf("Run err = %v, want ErrEntwareCleanBusy", err)
	}
	if !ran {
		t.Fatal("script was not run")
	}

	r := Runner{Exec: m.Exec, Now: mockNow(), entwareClean: m}
	res := r.Execute(context.Background(), wire.Command{ID: "c1", Action: "entware_clean_run"})
	if res.Status != "locked" {
		t.Fatalf("status=%q output=%q, want locked", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "уже идёт другая очистка") {
		t.Fatalf("output = %q", res.Output)
	}
}

// Старый скрипт pid не писал: его замок, брошенный по сроку, kill -0 не
// распознать. Run снимает замок без pid старше entwareCleanLockStaleAfter.
// Свежий замок без pid -- занят (владелец мог не успеть записать pid).
func TestEntwareCleanRun_ClearsAbandonedPidlessLock(t *testing.T) {
	m := newTestEntwareCleanManager(t)
	if err := os.MkdirAll(m.LockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := m.Now().Add(-entwareCleanLockStaleAfter - time.Minute)
	if err := os.Chtimes(m.LockDir, old, old); err != nil {
		t.Fatal(err)
	}
	var ran bool
	m.Exec = fakeEntwareExec(m, "", &ran)
	if _, err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.LockDir); !os.IsNotExist(err) {
		t.Fatalf("abandoned pid-less lock not removed: %v", err)
	}

	if err := os.Mkdir(m.LockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fresh := m.Now().Add(-time.Minute)
	if err := os.Chtimes(m.LockDir, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.LockDir); err != nil {
		t.Fatalf("fresh lock removed: %v", err)
	}
}

// Уже установленный скрипт старого образца (без kill -0) переписывается:
// при старте агента (для cron) и перед «Почистить сейчас».
func TestEntwareClean_RefreshScript(t *testing.T) {
	m := newTestEntwareCleanManager(t)
	if changed, err := m.RefreshScript(); err != nil || changed {
		t.Fatalf("not installed: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(m.ScriptPath); !os.IsNotExist(err) {
		t.Fatalf("script installed where it was not: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.ScriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ScriptPath, []byte("#!/bin/sh\nmkdir \"$LOCK\" || exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	changed, err := m.RefreshScript()
	if err != nil || !changed {
		t.Fatalf("outdated: changed=%v err=%v", changed, err)
	}
	want, _ := m.scriptText()
	if b, _ := os.ReadFile(m.ScriptPath); string(b) != want {
		t.Fatal("script not rewritten to the current text")
	}
	if changed, err := m.RefreshScript(); err != nil || changed {
		t.Fatalf("second run: changed=%v err=%v (must be idempotent)", changed, err)
	}

	// Run перед запуском тоже приводит скрипт к текущему тексту.
	if err := os.WriteFile(m.ScriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var ran bool
	m.Exec = fakeEntwareExec(m, "", &ran)
	if _, err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(m.ScriptPath); string(b) != want {
		t.Fatal("Run ran an outdated script")
	}
}

// Ревью Task 7: очистка столько не идёт -- замок старше 2 ч брошен, даже если
// его pid жив (на busybox pid переиспользуются): иначе чужой процесс с тем же
// номером держал бы очистку до перезагрузки.
func TestEntwareCleanScript_OldLockWithLivePidTakenOver(t *testing.T) {
	lock := entwareLockWithPid(t, os.Getpid())
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	log, out := runEntwareCleanScript(t, lock)
	if strings.Contains(out, "status=locked") || !strings.Contains(log, "status=ok") {
		t.Fatalf("2h-old lock blocked the cleanup: out=%q log:\n%s", out, log)
	}
}

func TestEntwareCleanRun_ClearsOldLockWithLivePid(t *testing.T) {
	m := newTestEntwareCleanManager(t)
	if err := os.MkdirAll(m.LockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.LockDir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	set := func(age time.Duration) {
		ts := m.Now().Add(-age)
		if err := os.Chtimes(m.LockDir, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	set(time.Hour) // живой и моложе 2 ч -- занят
	m.ClearAbandonedLock()
	if _, err := os.Stat(m.LockDir); err != nil {
		t.Fatalf("live 1h lock removed: %v", err)
	}
	set(entwareCleanLockMaxAge + time.Minute)
	m.ClearAbandonedLock()
	if _, err := os.Stat(m.LockDir); !os.IsNotExist(err) {
		t.Fatalf("live lock older than %v not removed: %v", entwareCleanLockMaxAge, err)
	}
}
