package wakehook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScriptIsQuietAndHarmless(t *testing.T) {
	s := Script("/opt/var/run/wg-monitor.wake")
	want := "#!/bin/sh\n" +
		"# wg-monitor: будит агента при смене состояния интерфейса.\n" +
		"# Ставит и снимает агент сам (v0.47). Ничего не печатает и всегда выходит с 0,\n" +
		"# чтобы не мешать соседним хукам (awg-manager ставит свои).\n" +
		"touch /opt/var/run/wg-monitor.wake 2>/dev/null\n" +
		"exit 0\n"
	if s != want {
		t.Fatalf("скрипт:\n%s", s)
	}
	for _, bad := range []string{"kill", "echo", "curl", "ndmc"} {
		if strings.Contains(s, bad) {
			t.Fatalf("хук не должен делать %q -- только трогать файл", bad)
		}
	}
}

func TestEnsureInstallsIdempotently(t *testing.T) {
	dir := t.TempDir()
	wake := filepath.Join(t.TempDir(), "run", "wg-monitor.wake")
	if st, e := Ensure(dir, wake, true); st != StateInstalled || e != "" {
		t.Fatalf("state=%q err=%q", st, e)
	}
	path := filepath.Join(dir, ScriptName)
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("скрипт не исполняемый: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Dir(wake)); err != nil {
		t.Fatalf("каталог файла пробуждения не создан: %v", err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(path, old, old)
	if st, _ := Ensure(dir, wake, true); st != StateInstalled {
		t.Fatalf("второй запуск: %q", st)
	}
	fi, _ = os.Stat(path)
	if !fi.ModTime().Equal(old) {
		t.Fatal("одинаковый скрипт переписан заново: лишняя запись во флеш")
	}
}

func TestEnsureWithoutDirIsUnsupported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-ndm-hooks")
	if st, _ := Ensure(dir, filepath.Join(t.TempDir(), "w"), true); st != StateUnsupported {
		t.Fatalf("state=%q", st)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("каталог хуков создавать нельзя: его заводит прошивка")
	}
}

// awg-manager ставит свои хуки в тот же каталог -- их трогать нельзя.
func TestEnsureDisabledRemovesOwnFileOnly(t *testing.T) {
	dir := t.TempDir()
	wake := filepath.Join(t.TempDir(), "w")
	Ensure(dir, wake, true)
	neighbour := filepath.Join(dir, "10-awg-manager.sh")
	if err := os.WriteFile(neighbour, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, _ := Ensure(dir, wake, false); st != StateDisabled {
		t.Fatalf("state=%q", st)
	}
	if _, err := os.Stat(filepath.Join(dir, ScriptName)); !os.IsNotExist(err) {
		t.Fatal("свой хук не снят")
	}
	if _, err := os.Stat(neighbour); err != nil {
		t.Fatal("снят чужой хук")
	}
	if st, _ := Ensure(dir, wake, false); st != StateDisabled {
		t.Fatalf("повторное выключение: %q", st)
	}
}
