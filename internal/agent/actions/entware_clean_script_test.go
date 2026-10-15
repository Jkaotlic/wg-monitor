package actions

import (
	"os"
	"strings"
	"testing"
)

// v0.57: скрипт очистки переехал из fmt.Sprintf в файл routerscripts. Поведение
// чистки не меняется: подстановка значений даёт прежний текст v0.56 слово в
// слово, кроме места на /opt «до» в итоговой строке журнала и замка с pid (v0.61).
func TestEntwareCleanScriptMatchesV056ExceptFreeBefore(t *testing.T) {
	golden, err := os.ReadFile("testdata/entware-cleanup.v056.golden")
	if err != nil {
		t.Fatal(err)
	}
	const oldOK = `log "status=ok mem_before_kb=$before mem_after_kb=$after freed_kb=$freed opt_free_kb=$free_after"`
	const newOK = `log "status=ok mem_before_kb=$before mem_after_kb=$after freed_kb=$freed opt_free_before_kb=$free opt_free_kb=$free_after"`
	if strings.Count(string(golden), oldOK) != 1 {
		t.Fatalf("golden lost its ok line")
	}
	want := strings.Replace(string(golden), oldOK, newOK, 1)
	// v0.61 (AG-01): замок пишет pid владельца и снимает брошенный (kill -0);
	// путь замка подставляется, как LOG, в кавычках.
	const oldLockLine = "LOCK=/tmp/wg-monitor-entware-cleanup.lock\n"
	const newLockLine = "LOCK=\"/tmp/wg-monitor-entware-cleanup.lock\"\n"
	const oldLock = `if ! mkdir "$LOCK" 2>/dev/null; then
  log "status=locked another run is active"
  trim_log
  exit 0
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM
`
	const newLock = `if ! mkdir "$LOCK" 2>/dev/null; then
  # владелец замка мёртв (убит по сроку, OOM) -- замок брошен, снимаем
  owner="$(cat "$LOCK/pid" 2>/dev/null || true)"
  case "$owner" in
    ''|*[!0-9]*) owner="" ;;
  esac
  if [ -n "$owner" ] && ! kill -0 "$owner" 2>/dev/null; then
    log "status=stale_lock_removed pid=$owner"
    rm -f "$LOCK/pid"
    rmdir "$LOCK" 2>/dev/null || true
  fi
  if ! mkdir "$LOCK" 2>/dev/null; then
    log "status=locked another run is active"
    # и в вывод: агент по нему отвечает «уже идёт другая очистка»
    echo "status=locked another run is active"
    trim_log
    exit 0
  fi
fi
echo $$ > "$LOCK/pid"
trap 'rm -f "$LOCK/pid"; rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM
`
	for _, r := range [][2]string{{oldLockLine, newLockLine}, {oldLock, newLock}} {
		if strings.Count(want, r[0]) != 1 {
			t.Fatalf("golden lost %q", r[0])
		}
		want = strings.Replace(want, r[0], r[1], 1)
	}

	got, err := (&EntwareCleanManager{}).scriptText()
	if err != nil {
		t.Fatal(err)
	}

	if got != want {
		t.Fatalf("rendered cleanup script differs from v0.56 + opt_free_before_kb:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// Значения подставляются агентом в строки-заглушки сверху; путь журнала --
// в кавычках, как раньше (%q).
func TestEntwareCleanScriptSubstitutesValues(t *testing.T) {
	m := &EntwareCleanManager{LogPath: "/tmp/x y/clean.log", MinFreeKB: 111, MinMemAvailableKB: 222, MaxLogKB: 33}

	got, err := m.scriptText()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"\nLOG=\"/tmp/x y/clean.log\"\n", "\nMIN_FREE_KB=111\n", "\nMIN_MEM_AVAILABLE_KB=222\n", "\nMAX_LOG_KB=33\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("script missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/opt/var/log/wg-monitor/entware-cleanup.log") {
		t.Fatalf("default log path survived substitution:\n%s", got)
	}
}
