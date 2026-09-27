package actions

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// swapScenario прогоняет НАСТОЯЩИЙ скрипт замены через sh с подменёнными
// sleep/pidof/pgrep/ps/killall: время идёт по вызовам sleep, а сценарий
// решает, жив ли агент и какие метки появились на каком шаге.
//
// Шаги sleep: 1 и 2 -- паузы до замены, дальше каждый шаг -- 5-секундный
// тик опроса (i = шаг - 2).
type swapScenario struct {
	requireReport  bool
	diesAtTick     int // 0 -- не умирает
	okAtTick       int // 0 -- метки report-ok нет
	rejectedAtTick int
	// psOnly -- ни pidof, ни pgrep нет (только ps): в выводе ps всегда есть
	// сам скрипт замены из каталога /opt/var/wg-monitor, а агент -- пока жив.
	psOnly bool
}

func runSwapScenario(t *testing.T, sc swapScenario) (rolledBack bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	d := t.TempDir()
	bin := filepath.Join(d, "wg-monitor")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(bin, []byte("old"), 0o755))
	must(os.WriteFile(bin+".new", []byte("new"), 0o755))
	okMarker, rejMarker := filepath.Join(d, "report-ok"), filepath.Join(d, "report-rejected")
	shims := filepath.Join(d, "shims")
	must(os.MkdirAll(shims, 0o755))
	tickAction := func(tick int, cmd string) string {
		if tick <= 0 {
			return ""
		}
		return `[ "$i" -eq ` + strconv.Itoa(tick) + ` ] && ` + cmd + "\n"
	}
	sleepShim := `#!/bin/sh
n=$(cat "$D/sleeps" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$D/sleeps"
i=$((n-2))
[ "$n" -eq 2 ] && touch "$D/running"
` + tickAction(sc.diesAtTick, `rm -f "$D/running"`) +
		tickAction(sc.okAtTick, `touch "`+okMarker+`"`) +
		tickAction(sc.rejectedAtTick, `touch "`+rejMarker+`"`) + "exit 0\n"
	running := "#!/bin/sh\n[ -f \"$D/running\" ]\n"
	ps := "#!/bin/sh\nexit 0\n"
	if sc.psOnly {
		running = "#!/bin/sh\nexit 1\n"
		ps = "#!/bin/sh\necho '  PID USER       VSZ STAT COMMAND'\n" +
			"echo '   99 root      1000 S    sh /opt/var/wg-monitor/self-update-swap.sh'\n" +
			"[ -f \"$D/running\" ] && echo '  456 root     20000 S    " + bin + " -config /opt/etc/wg-monitor/config.yaml'\n" +
			"exit 0\n"
	}
	for name, body := range map[string]string{
		"sleep": sleepShim, "pidof": running, "pgrep": running,
		"ps":      ps,
		"killall": "#!/bin/sh\nrm -f \"$D/running\"\nexit 0\n",
	} {
		must(os.WriteFile(filepath.Join(shims, name), []byte(body), 0o755))
	}
	script := filepath.Join(d, "swap.sh")
	must(os.WriteFile(script, []byte(selfUpdateSwapScript(bin, okMarker, rejMarker, sc.requireReport)), 0o755))
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(), "PATH="+shims+":/usr/bin:/bin", "D="+d)
	// Код выхода не важен: init-скрипта /opt/etc/init.d нет в песочнице, и
	// последняя команда (start) завершается ошибкой. Итог -- бинарь на месте.
	out, _ := cmd.CombinedOutput()
	got, err := os.ReadFile(bin)
	must(err)
	sleeps, _ := os.ReadFile(filepath.Join(d, "sleeps"))
	t.Logf("sleep calls=%s output:\n%s", sleeps, out)
	return string(got) == "old"
}

// AGENT-11 (ревью): откат -- только если новый бинарь упал в первые 60 с
// или бэкенд ЯВНО отверг его отчёты (4xx: report-rejected), а успешного
// отчёта так и не было за 5 минут. Недоступный бэкенд, 5xx, сеть -- меток
// нет -- новый бинарь остаётся: иначе авария бэкенда откатывала бы парк.
func TestSelfUpdateSwapScriptRollbackRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		sc   swapScenario
		want bool
	}{
		{"reports fine", swapScenario{requireReport: true, okAtTick: 3}, false},
		{"dies within 60s", swapScenario{requireReport: true, diesAtTick: 4}, true},
		{"backend unreachable: no markers", swapScenario{requireReport: true}, false},
		{"reports rejected, never ok", swapScenario{requireReport: true, rejectedAtTick: 3}, true},
		{"rejected then ok", swapScenario{requireReport: true, rejectedAtTick: 3, okAtTick: 6}, false},
		{"dies after 60s is not the update's crash-on-start", swapScenario{requireReport: true, diesAtTick: 20}, false},
		{"pre-marker target: alive 60s is enough", swapScenario{rejectedAtTick: 3}, false},
		{"pre-marker target dies", swapScenario{diesAtTick: 5}, true},
		// Ревью: запасной ps-путь находил строку самого скрипта замены
		// (/opt/var/wg-monitor/...) и считал упавший агент живым.
		{"ps fallback: agent dies, script itself is not the agent", swapScenario{requireReport: true, diesAtTick: 4, psOnly: true}, true},
		{"ps fallback: agent alive and reporting", swapScenario{requireReport: true, okAtTick: 3, psOnly: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runSwapScenario(t, tc.sc); got != tc.want {
				t.Fatalf("rolled back = %v, want %v", got, tc.want)
			}
		})
	}
}
