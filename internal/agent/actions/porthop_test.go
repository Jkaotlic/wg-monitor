package actions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/routerscripts"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// porthopRouter -- поддельный роутер для PorthopManager: каталоги в
// t.TempDir(), процессы -- каталоги <proc>/<pid>/cmdline, команды -- Exec.
type porthopRouter struct {
	t       *testing.T
	m       *PorthopManager
	calls   []string
	ifaces  string
	allowed map[string]string
	// legacyStopKills -- какой pid гасит `S99awg-porthop stop`.
	legacyStopKills string
	// oursWrittenAtLegacyStop -- наш скрипт уже лежал на месте, когда
	// останавливали ручную копию.
	oursWrittenAtLegacyStop bool
}

func newPorthopRouter(t *testing.T) *porthopRouter {
	t.Helper()
	root := t.TempDir()
	r := &porthopRouter{
		t:       t,
		ifaces:  "opkgtun10 nwg0",
		allowed: map[string]string{"opkgtun10": "PUB\t0.0.0.0/0\n", "nwg0": "PUB\t10.8.0.0/24\n"},
	}
	r.m = &PorthopManager{
		ScriptPath:     filepath.Join(root, "etc", "wg-monitor", "awg-porthop.sh"),
		ConfPath:       filepath.Join(root, "etc", "wg-monitor", "porthop.conf"),
		InitPath:       filepath.Join(root, "etc", "init.d", "S99wg-monitor-porthop"),
		LogPath:        filepath.Join(root, "var", "log", "wg-monitor", "porthop.log"),
		PidPath:        filepath.Join(root, "var", "run", "wg-monitor-porthop.pid"),
		LegacyInitPath: filepath.Join(root, "etc", "init.d", "S99awg-porthop"),
		LegacyMoveDir:  filepath.Join(root, "etc", "wg-monitor", "legacy"),
		ProcDir:        filepath.Join(root, "proc"),
		Now:            func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) },
		Loc:            time.UTC,
		Sleep:          func(context.Context, time.Duration) error { return nil },
	}
	if err := os.MkdirAll(r.m.ProcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.m.Exec = r.exec
	return r
}

func (r *porthopRouter) proc(pid string, args ...string) {
	r.t.Helper()
	dir := filepath.Join(r.m.ProcDir, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *porthopRouter) kill(pid string) { _ = os.RemoveAll(filepath.Join(r.m.ProcDir, pid)) }

func (r *porthopRouter) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, key)
	switch {
	case key == "awg show interfaces":
		return []byte(r.ifaces + "\n"), nil
	case name == "awg" && len(args) == 3 && args[2] == "allowed-ips":
		if out, ok := r.allowed[args[1]]; ok {
			return []byte(out), nil
		}
		return nil, errors.New("no such interface")
	case key == r.m.InitPath+" restart" || key == r.m.InitPath+" start":
		r.kill("4242")
		r.proc("4242", "sh", r.m.ScriptPath)
		if err := os.MkdirAll(filepath.Dir(r.m.PidPath), 0o755); err != nil {
			return nil, err
		}
		return []byte("started\n"), os.WriteFile(r.m.PidPath, []byte("4242\n"), 0o644)
	case key == r.m.InitPath+" stop":
		r.kill("4242")
		_ = os.Remove(r.m.PidPath)
		return []byte("stopped\n"), nil
	case key == r.m.LegacyInitPath+" stop":
		if _, err := os.Stat(r.m.ScriptPath); err == nil {
			r.oursWrittenAtLegacyStop = true
		}
		if r.legacyStopKills != "" {
			r.kill(r.legacyStopKills)
		}
		return []byte("stopped\n"), nil
	case name == "kill" && len(args) == 1:
		r.kill(args[0])
		return nil, nil
	}
	return nil, errors.New("unexpected exec: " + key)
}

func (r *porthopRouter) count(prefix string) int {
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestPorthopInstallFreshWritesFilesAndStarts(t *testing.T) {
	r := newPorthopRouter(t)

	st, err := r.m.Install(context.Background(), nil, false)

	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		r.m.ScriptPath: routerscripts.Porthop,
		r.m.InitPath:   routerscripts.PorthopInit,
		r.m.ConfPath:   "IFACES=auto\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s: err=%v content=%q", path, err, got)
		}
	}
	for _, p := range []string{r.m.ScriptPath, r.m.InitPath} {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode %v", p, fi.Mode())
		}
	}
	// Временные файлы установки не остались (init.d исполняет всё подряд).
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(r.m.InitPath), ".*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if !st.Installed || !st.Running || !st.Auto || len(st.Ifaces) != 0 {
		t.Fatalf("status=%+v", st)
	}
	if strings.Join(st.Watched, ",") != "opkgtun10" {
		t.Fatalf("watched=%v, want only the 0.0.0.0/0 interface", st.Watched)
	}
	if r.count(r.m.InitPath+" restart") != 1 {
		t.Fatalf("calls=%v", r.calls)
	}
}

// Повтор с теми же настройками -- те же файлы и без перезапуска; смена
// списка -- перезапуск.
func TestPorthopInstallIdempotentRestartsOnlyOnChange(t *testing.T) {
	r := newPorthopRouter(t)
	ctx := context.Background()
	if _, err := r.m.Install(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Install(ctx, []string{"auto"}, false); err != nil {
		t.Fatal(err)
	}
	if n := r.count(r.m.InitPath + " restart"); n != 1 {
		t.Fatalf("unchanged install restarted: %d, calls=%v", n, r.calls)
	}

	st, err := r.m.Install(ctx, []string{"opkgtun10", "opkgtun12"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := r.count(r.m.InitPath + " restart"); n != 2 {
		t.Fatalf("changed conf did not restart: %d", n)
	}
	conf, _ := os.ReadFile(r.m.ConfPath)
	if string(conf) != "IFACES=\"opkgtun10 opkgtun12\"\n" {
		t.Fatalf("conf=%q", conf)
	}
	if st.Auto || strings.Join(st.Ifaces, ",") != "opkgtun10,opkgtun12" || strings.Join(st.Watched, ",") != "opkgtun10,opkgtun12" {
		t.Fatalf("status=%+v", st)
	}

	// Не работает -- поднимаем, даже если файлы те же.
	r.kill("4242")
	if _, err := r.m.Install(ctx, []string{"opkgtun10", "opkgtun12"}, false); err != nil {
		t.Fatal(err)
	}
	if n := r.count(r.m.InitPath + " restart"); n != 3 {
		t.Fatalf("dead process not restarted: %d", n)
	}
}

func TestPorthopInstallRejectsBadIfaces(t *testing.T) {
	r := newPorthopRouter(t)
	for _, bad := range [][]string{
		{"../etc"},
		{"opkgtun10; reboot"},
		{"Opkg"},
		{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
		{"averyveryverylongname"},
	} {
		if _, err := r.m.Install(context.Background(), bad, false); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if _, err := os.Stat(r.m.ScriptPath); !os.IsNotExist(err) {
		t.Fatal("files written for bad input")
	}
}

// Ручная копия работает -- без replace_legacy ничего не меняется.
func TestPorthopInstallRefusesWhileLegacyRuns(t *testing.T) {
	r := newPorthopRouter(t)
	r.proc("777", "/bin/sh", "/opt/bin/awg-porthop.sh", "opkgtun11", "opkgtun10")

	_, err := r.m.Install(context.Background(), nil, false)

	if err == nil || !strings.HasPrefix(err.Error(), PorthopLegacyRunningCode+":") {
		t.Fatalf("err=%v, want %s", err, PorthopLegacyRunningCode)
	}
	if _, serr := os.Stat(r.m.ScriptPath); !os.IsNotExist(serr) {
		t.Fatal("script written despite legacy copy")
	}
	if r.count(r.m.InitPath) != 0 || r.count("kill") != 0 {
		t.Fatalf("mutating calls: %v", r.calls)
	}
	st, _ := r.m.Status(context.Background(), 0)
	if !st.Legacy.Found || !st.Legacy.Running || st.Legacy.Path != "/opt/bin/awg-porthop.sh" {
		t.Fatalf("legacy=%+v", st.Legacy)
	}
}

// Init ручной копии на месте, процесс не работает: она поднимется при
// загрузке и подерётся с нашей -- тоже отказ.
func TestPorthopInstallRefusesLegacyInitEvenWhenStopped(t *testing.T) {
	r := newPorthopRouter(t)
	writeTestFile(t, r.m.LegacyInitPath, "#!/bin/sh\n", 0o755)

	_, err := r.m.Install(context.Background(), nil, false)

	if err == nil || !strings.HasPrefix(err.Error(), PorthopLegacyRunningCode+":") {
		t.Fatalf("err=%v", err)
	}
	st, _ := r.m.Status(context.Background(), 0)
	if !st.Legacy.Found || st.Legacy.Running || st.Legacy.Path != r.m.LegacyInitPath {
		t.Fatalf("legacy=%+v", st.Legacy)
	}
}

func TestPorthopInstallReplacesLegacy(t *testing.T) {
	r := newPorthopRouter(t)
	writeTestFile(t, r.m.LegacyInitPath, "#!/bin/sh\nARGS=\"opkgtun11 opkgtun10\"\n", 0o755)
	r.proc("777", "/bin/sh", "/opt/bin/awg-porthop.sh", "opkgtun11", "opkgtun10")
	r.legacyStopKills = "777"

	st, err := r.m.Install(context.Background(), nil, true)

	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(r.m.LegacyMoveDir, "S99awg-porthop")
	if b, err := os.ReadFile(moved); err != nil || !strings.Contains(string(b), "opkgtun11") {
		t.Fatalf("legacy init not moved: %v", err)
	}
	if _, err := os.Stat(r.m.LegacyInitPath); !os.IsNotExist(err) {
		t.Fatal("legacy init still in init.d")
	}
	// Остановлена её же init, до установки своей.
	stop, restart := -1, -1
	for i, c := range r.calls {
		if c == r.m.LegacyInitPath+" stop" {
			stop = i
		}
		if c == r.m.InitPath+" restart" {
			restart = i
		}
	}
	if stop < 0 || restart < 0 || stop > restart {
		t.Fatalf("order: %v", r.calls)
	}
	// Сначала свои файлы, потом остановка ручной: не записалось -- ручная
	// копия продолжает работать.
	if !r.oursWrittenAtLegacyStop {
		t.Fatal("legacy copy stopped before our files were written")
	}
	if st.Legacy.Found || st.Legacy.Running || st.Legacy.MovedTo != moved || !st.Running {
		t.Fatalf("status=%+v", st)
	}
}

// Ручная копия запущена руками, без init: гасится по pid.
func TestPorthopInstallReplacesLegacyProcessWithoutInit(t *testing.T) {
	r := newPorthopRouter(t)
	r.proc("777", "/bin/sh", "/opt/bin/awg-porthop.sh")

	st, err := r.m.Install(context.Background(), nil, true)

	if err != nil {
		t.Fatal(err)
	}
	if r.count("kill 777") != 1 || st.Legacy.Running {
		t.Fatalf("calls=%v legacy=%+v", r.calls, st.Legacy)
	}
}

func TestPorthopRemoveStopsAndKeepsLog(t *testing.T) {
	r := newPorthopRouter(t)
	ctx := context.Background()
	if _, err := r.m.Install(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, r.m.LogPath, "2026-10-07 11:00:00 старт: auto\n", 0o644)

	st, err := r.m.Remove(ctx)

	if err != nil {
		t.Fatal(err)
	}
	if r.count(r.m.InitPath+" stop") != 1 {
		t.Fatalf("calls=%v", r.calls)
	}
	for _, p := range []string{r.m.ScriptPath, r.m.InitPath, r.m.ConfPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left", p)
		}
	}
	if _, err := os.Stat(r.m.LogPath); err != nil {
		t.Fatal("log removed")
	}
	if st.Installed || st.Running {
		t.Fatalf("status=%+v", st)
	}
}

func TestPorthopStatusCounts24hFromLog(t *testing.T) {
	r := newPorthopRouter(t)
	log := strings.Join([]string{
		"2026-10-06 11:59:59 opkgtun10: порт 30000 -> 41000, поток ожил (хендшейк 3 с)", // старше суток
		"мусор без даты",
		"2026-10-06 12:00:01 opkgtun10: порт 41000 -> 42000, поток ожил (хендшейк 3 с)",
		"2026-10-07 09:00:00 opkgtun10: порт 42000 -> 43000, не ожил (хендшейк 99999 с)",
		"2026-10-07 09:00:10 opkgtun10: ОШИБКА смены порта 43000 -> 44000 (rc=1)",
		"2026-10-07 09:00:20 opkgtun10: [dry-run] сменил бы порт 43000 -> 45000",
		"2026-10-07 10:00:00 opkgtun12: порт 50000 -> 51000, поток ожил (хендшейк 2 с)",
		"2026-10-07 11:00:00 opkgtun12: снова жив (хендшейк 5 с)",
		"",
	}, "\n")
	writeTestFile(t, r.m.LogPath, log, 0o644)

	st, err := r.m.Status(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Recovered24h != 2 || st.Failed24h != 2 || st.Hops24h != 4 {
		t.Fatalf("counts: hops=%d rec=%d fail=%d", st.Hops24h, st.Recovered24h, st.Failed24h)
	}
	if st.LastEvent != "2026-10-07 11:00:00 opkgtun12: снова жив (хендшейк 5 с)" {
		t.Fatalf("last=%q", st.LastEvent)
	}
	if st.LogTail != "" {
		t.Fatal("status carries a log tail; only porthop_logs should")
	}
	logs, _ := r.m.Logs(context.Background(), 2)
	if !strings.HasSuffix(logs.LogTail, "снова жив (хендшейк 5 с)") || strings.Count(logs.LogTail, "\n") != 1 {
		t.Fatalf("tail=%q", logs.LogTail)
	}
}

// Правило auto в Go (watched в статусе) обязано совпадать с правилом в
// скрипте: та же awk-программа гоняется на тех же выводах allowed-ips.
func TestPorthopWatchRuleMatchesScript(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("нет awk")
	}
	m := regexp.MustCompile(`awk '(\{ for \(k = 2[^']*)'`).FindStringSubmatch(routerscripts.Porthop)
	if m == nil {
		t.Fatal("auto rule not found in the script")
	}
	for _, out := range []string{
		"PUB\t0.0.0.0/0\n",
		"PUB\t0.0.0.0/0 ::/0\n",
		"PUB\t::/0 0.0.0.0/0\n",
		"PUB\t10.8.0.0/24\n",
		"PUB\t10.0.0.0/0x\n",
		"PUB\t10.0.0.0/1 128.0.0.0/1\n",
		"A\t10.8.0.0/24\nB\t0.0.0.0/0\n",
		"0.0.0.0/0\n", // ключ пира, а не маршрут: первое поле не в счёт
		"",
	} {
		cmd := exec.Command("awk", m[1])
		cmd.Stdin = strings.NewReader(out)
		scriptSays := cmd.Run() == nil
		if got := porthopFullRoute(out); got != scriptSays {
			t.Errorf("allowed-ips %q: go=%v script=%v", out, got, scriptSays)
		}
	}
}

func TestRunnerPorthopStatusDispatches(t *testing.T) {
	r := newPorthopRouter(t)
	runner := Runner{Now: mockNow(), Exec: r.exec, Porthop: r.m}

	res := runner.Execute(context.Background(), wire.Command{ID: "p1", Action: "porthop_install", Args: map[string]any{
		"ifaces": []any{"opkgtun10"}, "replace_legacy": false,
	}})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%s", res.Status, res.Output)
	}
	var st wire.PorthopStatus
	if err := json.Unmarshal([]byte(res.Output), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Running || strings.Join(st.Ifaces, ",") != "opkgtun10" {
		t.Fatalf("st=%+v", st)
	}
	for _, action := range []string{"porthop_status", "porthop_logs", "porthop_remove"} {
		res := runner.Execute(context.Background(), wire.Command{ID: action, Action: action})
		if res.Status != "ok" {
			t.Fatalf("%s: %q %s", action, res.Status, res.Output)
		}
	}

	r.proc("777", "/bin/sh", "/opt/bin/awg-porthop.sh")
	res = runner.Execute(context.Background(), wire.Command{ID: "p2", Action: "porthop_install"})
	if res.Status != "err" || !strings.HasPrefix(res.Output, "legacy_running:") {
		t.Fatalf("status=%q output=%s", res.Status, res.Output)
	}
}

func TestPorthopTimeouts(t *testing.T) {
	for _, a := range []string{"porthop_install", "porthop_remove"} {
		if actionTimeoutFor(a) < 60*time.Second {
			t.Errorf("%s timeout %v < 60s", a, actionTimeoutFor(a))
		}
	}
}

func writeTestFile(t *testing.T, path, body string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
}

// Пути агента и init/скрипта одни и те же: иначе агент искал бы pid и
// скрипт не там, где их кладёт init.
func TestPorthopDefaultPathsMatchScripts(t *testing.T) {
	m := &PorthopManager{}
	if m.pidPath() != "/tmp/wg-monitor-porthop.pid" {
		t.Fatalf("pid path %s: want tmpfs", m.pidPath())
	}
	for _, p := range []string{m.pidPath(), m.scriptPath()} {
		if !strings.Contains(routerscripts.PorthopInit, p) {
			t.Errorf("init does not use %s", p)
		}
	}
	for _, p := range []string{m.logPath(), m.confPath()} {
		if !strings.Contains(routerscripts.Porthop, p) {
			t.Errorf("script does not use %s", p)
		}
	}
}

// Свои файлы не записались -- ручная копия остаётся как была: работает,
// init на месте, ни stop, ни kill.
func TestPorthopReplaceLegacyKeepsItWhenWriteFails(t *testing.T) {
	r := newPorthopRouter(t)
	writeTestFile(t, r.m.LegacyInitPath, "#!/bin/sh\n", 0o755)
	r.proc("777", "/bin/sh", "/opt/bin/awg-porthop.sh")
	// Каталог скрипта занят файлом -- записать скрипт нельзя.
	writeTestFile(t, filepath.Dir(r.m.ScriptPath), "not a dir", 0o644)

	if _, err := r.m.Install(context.Background(), nil, true); err == nil {
		t.Fatal("install succeeded without its files")
	}
	if _, err := os.Stat(r.m.LegacyInitPath); err != nil {
		t.Fatal("legacy init moved although our files were not written")
	}
	if r.count(r.m.LegacyInitPath) != 0 || r.count("kill") != 0 {
		t.Fatalf("legacy touched: %v", r.calls)
	}
}
