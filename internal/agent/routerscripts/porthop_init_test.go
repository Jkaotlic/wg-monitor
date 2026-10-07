package routerscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// init-скрипт гоняется по-настоящему: вместо awg-porthop.sh -- петля sleep.
// Проверяется то, на что опирается агент: start кладёт pid, повторный start
// второй копии не заводит, status говорит правду, stop гасит и чистит pid.
// /proc подменён каталогом (PORTHOP_PROC): на маке его нет, а на роутере
// init обязан сверять cmdline процесса из pid-файла с путём скрипта.
// Гоняется каждым найденным шеллом (sh, dash): на роутере -- busybox ash.
func TestPorthopInitStartStopIdempotent(t *testing.T) {
	for _, shell := range initShells(t) {
		t.Run(shell, func(t *testing.T) { testPorthopInitStartStop(t, shell) })
	}
}

func initShells(t *testing.T) []string {
	var res []string
	for _, s := range []string{"sh", "dash"} {
		if p, err := exec.LookPath(s); err == nil {
			res = append(res, p)
		}
	}
	if len(res) == 0 {
		t.Skip("нет sh")
	}
	return res
}

type initEnv struct {
	t                        *testing.T
	shell, initPath, pidfile string
	script, proc             string
}

func newInitEnv(t *testing.T, shell string) *initEnv {
	root := t.TempDir()
	e := &initEnv{
		t: t, shell: shell,
		script:   filepath.Join(root, "awg-porthop.sh"),
		initPath: filepath.Join(root, "S99wg-monitor-porthop"),
		pidfile:  filepath.Join(root, "run", "porthop.pid"),
		proc:     filepath.Join(root, "proc"),
	}
	if err := os.WriteFile(e.script, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.initPath, []byte(PorthopInit), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p := e.pid(); p > 0 {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	})
	return e
}

func (e *initEnv) run(arg string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(e.shell, e.initPath, arg)
	cmd.Env = append(os.Environ(), "PORTHOP_SCRIPT="+e.script, "PORTHOP_PIDFILE="+e.pidfile, "PORTHOP_PROC="+e.proc)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		e.t.Fatalf("init %s hung (background child holds the output pipe?)", arg)
	}
	return string(out), err
}

func (e *initEnv) pid() int {
	b, err := os.ReadFile(e.pidfile)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// fakeProc -- запись /proc/<pid>/cmdline в подменном каталоге.
func (e *initEnv) fakeProc(pid int, args ...string) {
	e.t.Helper()
	dir := filepath.Join(e.proc, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func testPorthopInitStartStop(t *testing.T, shell string) {
	e := newInitEnv(t, shell)
	if _, err := e.run("status"); err == nil {
		t.Fatal("status says running before start")
	}
	if out, err := e.run("start"); err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	first := e.pid()
	if first == 0 || syscall.Kill(first, 0) != nil {
		t.Fatalf("no live pid after start: %d", first)
	}
	e.fakeProc(first, "sh", e.script)
	if out, err := e.run("start"); err != nil || !strings.Contains(out, "already running") {
		t.Fatalf("second start: %v %q", err, out)
	}
	if e.pid() != first {
		t.Fatal("second start replaced the pid")
	}
	if out, err := e.run("status"); err != nil || !strings.Contains(out, "running") {
		t.Fatalf("status: %v %q", err, out)
	}
	if out, err := e.run("stop"); err != nil {
		t.Fatalf("stop: %v %s", err, out)
	}
	if _, err := os.Stat(e.pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile left after stop: %v", err)
	}
	if _, err := e.run("status"); err == nil {
		t.Fatal("status says running after stop")
	}
}

// pid-файл пережил перезагрузку (или процесс умер), а pid достался чужому
// живому процессу: init не считает его своим, не гасит его, pid-файл
// убирает и запускает свою копию.
func TestPorthopInitStalePidIsNotOurs(t *testing.T) {
	for _, shell := range initShells(t) {
		t.Run(shell, func(t *testing.T) {
			e := newInitEnv(t, shell)
			stranger := exec.Command("sleep", "30")
			if err := stranger.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stranger.Process.Kill(); _, _ = stranger.Process.Wait() })
			sp := stranger.Process.Pid
			e.fakeProc(sp, "sleep", "30")
			if err := os.MkdirAll(filepath.Dir(e.pidfile), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(e.pidfile, []byte(strconv.Itoa(sp)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if _, err := e.run("status"); err == nil {
				t.Fatal("stranger pid reported as running porthop")
			}
			if _, err := os.Stat(e.pidfile); !os.IsNotExist(err) {
				t.Fatal("stale pid file not removed")
			}
			if out, err := e.run("stop"); err != nil {
				t.Fatalf("stop: %v %s", err, out)
			}
			if syscall.Kill(sp, 0) != nil {
				t.Fatal("init killed a stranger process")
			}
			if out, err := e.run("start"); err != nil || strings.Contains(out, "already running") {
				t.Fatalf("start over stale pid: %v %q", err, out)
			}
			if p := e.pid(); p == 0 || p == sp {
				t.Fatalf("pid after start = %d", p)
			}
		})
	}
}

// Каталог init.d исполняет всё, что начинается на S: путь к скрипту и pid --
// пути из спеки, иначе агент и init разойдутся.
func TestPorthopInitPaths(t *testing.T) {
	for _, want := range []string{
		"/opt/etc/wg-monitor/awg-porthop.sh",
		"/tmp/wg-monitor-porthop.pid",
	} {
		if !strings.Contains(PorthopInit, want) {
			t.Fatalf("init lacks %s", want)
		}
	}
}
