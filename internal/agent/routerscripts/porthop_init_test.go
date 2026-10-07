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
func TestPorthopInitStartStopIdempotent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("нет sh")
	}
	root := t.TempDir()
	script := filepath.Join(root, "awg-porthop.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	initPath := filepath.Join(root, "S99wg-monitor-porthop")
	if err := os.WriteFile(initPath, []byte(PorthopInit), 0o755); err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(root, "run", "porthop.pid")
	run := func(arg string) (string, error) {
		cmd := exec.Command("sh", initPath, arg)
		cmd.Env = append(os.Environ(), "PORTHOP_SCRIPT="+script, "PORTHOP_PIDFILE="+pidfile)
		done := make(chan struct{})
		var out []byte
		var err error
		go func() { out, err = cmd.CombinedOutput(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatalf("init %s hung (background child holds the output pipe?)", arg)
		}
		return string(out), err
	}
	pid := func() int {
		b, err := os.ReadFile(pidfile)
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}
	t.Cleanup(func() {
		if p := pid(); p > 0 {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	})

	if _, err := run("status"); err == nil {
		t.Fatal("status says running before start")
	}
	if out, err := run("start"); err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	first := pid()
	if first == 0 || syscall.Kill(first, 0) != nil {
		t.Fatalf("no live pid after start: %d", first)
	}
	if out, err := run("start"); err != nil || !strings.Contains(out, "already running") {
		t.Fatalf("second start: %v %q", err, out)
	}
	if pid() != first {
		t.Fatal("second start replaced the pid")
	}
	if out, err := run("status"); err != nil || !strings.Contains(out, "running") {
		t.Fatalf("status: %v %q", err, out)
	}
	if out, err := run("stop"); err != nil {
		t.Fatalf("stop: %v %s", err, out)
	}
	if syscall.Kill(first, 0) == nil {
		// Процесс мог остаться зомби у нашего sh -- ждём чуть-чуть.
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile left after stop: %v", err)
	}
	if _, err := run("status"); err == nil {
		t.Fatal("status says running after stop")
	}
}

// Каталог init.d исполняет всё, что начинается на S: путь к скрипту и pid --
// пути из спеки, иначе агент и init разойдутся.
func TestPorthopInitPaths(t *testing.T) {
	for _, want := range []string{
		"/opt/etc/wg-monitor/awg-porthop.sh",
		"/opt/var/run/wg-monitor-porthop.pid",
	} {
		if !strings.Contains(PorthopInit, want) {
			t.Fatalf("init lacks %s", want)
		}
	}
}
