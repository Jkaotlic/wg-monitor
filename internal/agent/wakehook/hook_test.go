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

// Временный файл лежит в каталоге, который ndm исполняет целиком: при ошибке
// он не должен оставаться (тем более исполняемым).
func TestEnsureRemovesTempOnError(t *testing.T) {
	dir := t.TempDir()
	wake := filepath.Join(t.TempDir(), "w")
	// Место хука занято непустым каталогом -- rename обязан упасть.
	if err := os.MkdirAll(filepath.Join(dir, ScriptName, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, _ := Ensure(dir, wake, true); st != StateError {
		t.Fatalf("state=%q, ждали error", st)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != ScriptName {
			t.Fatalf("после ошибки в каталоге хуков остался %q", e.Name())
		}
	}
}

func TestEnsureTempNameIsHidden(t *testing.T) {
	if !strings.HasPrefix(filepath.Base(tempPath(t.TempDir())), ".") {
		t.Fatal("временный файл хука без точки в начале имени: ndm может его исполнить")
	}
}

// Выключение снимает и хук, и забытый временный файл (текущий и старый v0.47-rc).
func TestEnsureDisabledRemovesTempFiles(t *testing.T) {
	dir := t.TempDir()
	wake := filepath.Join(t.TempDir(), "w")
	Ensure(dir, wake, true)
	for _, p := range []string{tempPath(dir), filepath.Join(dir, ScriptName+".tmp")} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if st, e := Ensure(dir, wake, false); st != StateDisabled {
		t.Fatalf("state=%q err=%q", st, e)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("после выключения остались: %v", entries)
	}
}
