package actions

import (
	"os"
	"strings"
	"testing"
)

// v0.57: скрипт очистки переехал из fmt.Sprintf в файл routerscripts. Поведение
// чистки не меняется: подстановка значений даёт прежний текст v0.56 слово в
// слово, кроме одной добавки -- места на /opt «до» в итоговой строке журнала.
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
