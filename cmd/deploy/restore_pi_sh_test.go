package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Скрипт замены на Pi прогоняется настоящим sh над временным каталогом; docker
// и chown -- заглушки (пишут в журнал), mv и cp -- обёртки над настоящими,
// умеющие упасть на заданном аргументе.
type piShRun struct {
	base, log string
	rc        int
}

func runPiScriptInSh(t *testing.T, setup func(data string), failMV, failCP string, failRM ...string) piShRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("нужен POSIX sh")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("нет sh")
	}
	realMV, err1 := exec.LookPath("mv")
	realCP, err2 := exec.LookPath("cp")
	realRM, err3 := exec.LookPath("rm")
	if err1 != nil || err2 != nil || err3 != nil {
		t.Skip("нет mv/cp")
	}
	root := t.TempDir()
	base := filepath.Join(root, "wg-monitor")
	bin := filepath.Join(root, "bin")
	log := filepath.Join(root, "log")
	for _, d := range []string{filepath.Join(base, "data", "restore-tmp"), bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shims := map[string]string{
		"docker": "#!/bin/sh\necho \"docker $*\" >> '" + log + "'\n",
		"chown":  "#!/bin/sh\necho \"chown $*\" >> '" + log + "'\n",
		"mv":     "#!/bin/sh\ncase \"$*\" in *\"$FAIL_MV\"*) [ -n \"$FAIL_MV\" ] && { echo \"mv: fail $*\" >&2; exit 1; };; esac\nexec '" + realMV + "' \"$@\"\n",
		"rm":     "#!/bin/sh\ncase \"$*\" in *\"$FAIL_RM\"*) [ -n \"$FAIL_RM\" ] && { echo \"rm: fail $*\" >&2; exit 1; };; esac\nexec '" + realRM + "' \"$@\"\n",
		"cp":     "#!/bin/sh\ncase \"$*\" in *\"$FAIL_CP\"*) [ -n \"$FAIL_CP\" ] && { echo \"cp: fail $*\" >&2; exit 1; };; esac\nexec '" + realCP + "' \"$@\"\n",
	}
	for name, body := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setup(filepath.Join(base, "data"))
	scriptPath := filepath.Join(root, "restore.sh")
	script := buildPiRestoreScript(base, "20261026T010203Z", []string{"awg3-panels.json"})
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, scriptPath)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAIL_MV="+failMV, "FAIL_CP="+failCP, "FAIL_RM="+strings.Join(failRM, ""))
	out, err := cmd.CombinedOutput()
	rc := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	t.Logf("rc=%d output:\n%s", rc, out)
	return piShRun{base: base, log: log, rc: rc}
}

func (r piShRun) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.base, rel))
	if err != nil {
		return "<нет>"
	}
	return string(b)
}

func (r piShRun) started(t *testing.T) bool {
	b, _ := os.ReadFile(r.log)
	return strings.Contains(string(b), "docker start wg-monitor-backend")
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func piOldAndNew(t *testing.T) func(string) {
	return func(data string) {
		writeFiles(t, data, map[string]string{"state.db": "OLD", "state.db-wal": "OLDWAL", "awg3-panels.json": "OLDSTORE"})
		writeFiles(t, filepath.Join(data, "restore-tmp"), map[string]string{"state.db": "NEW", "awg3-panels.json": "NEWSTORE"})
	}
}

func TestPiRestoreScriptInShSuccess(t *testing.T) {
	r := runPiScriptInSh(t, piOldAndNew(t), "", "")
	if r.rc != 0 {
		t.Fatalf("rc=%d", r.rc)
	}
	for rel, want := range map[string]string{
		"data/state.db":                              "NEW",
		"data/state.db-wal":                          "<нет>",
		"data/awg3-panels.json":                      "NEWSTORE",
		"data/state.db.bak.20261026T010203Z":         "OLD",
		"data/state.db.bak.20261026T010203Z-wal":     "OLDWAL",
		"data/awg3-panels.json.bak.20261026T010203Z": "OLDSTORE",
		"data/restore-tmp/state.db":                  "<нет>",
	} {
		if got := r.read(t, rel); got != want {
			t.Fatalf("%s=%q, want %q", rel, got, want)
		}
	}
	if st, err := os.Stat(filepath.Join(r.base, "data", "state.db")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("режим state.db: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(r.base, "data", "restore-tmp")); !os.IsNotExist(err) {
		t.Fatalf("restore-tmp не убран: %v", err)
	}
	if !r.started(t) {
		t.Fatal("контейнер не запущен")
	}
}

func TestPiRestoreScriptInShMvFailureRollsBack(t *testing.T) {
	r := runPiScriptInSh(t, piOldAndNew(t), "restore-tmp/awg3-panels.json", "")
	if r.rc != piScriptFailedAfterReplace {
		t.Fatalf("rc=%d, want %d", r.rc, piScriptFailedAfterReplace)
	}
	for rel, want := range map[string]string{
		"data/state.db":         "OLD",
		"data/state.db-wal":     "OLDWAL",
		"data/awg3-panels.json": "OLDSTORE",
	} {
		if got := r.read(t, rel); got != want {
			t.Fatalf("%s=%q, want %q (откат)", rel, got, want)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(r.base, "data", "*.rb.*")); len(matches) != 0 {
		t.Fatalf("остались временные файлы отката: %v", matches)
	}
	if !r.started(t) {
		t.Fatal("контейнер не запущен после отката")
	}
}

func TestPiRestoreScriptInShMissingFileLeavesAllUntouched(t *testing.T) {
	r := runPiScriptInSh(t, func(data string) {
		writeFiles(t, data, map[string]string{"state.db": "OLD", "state.db-wal": "OLDWAL"})
	}, "", "")
	if r.rc != piScriptFailedBeforeReplace {
		t.Fatalf("rc=%d, want %d", r.rc, piScriptFailedBeforeReplace)
	}
	if r.read(t, "data/state.db") != "OLD" || r.read(t, "data/state.db-wal") != "OLDWAL" {
		t.Fatal("база тронута")
	}
	if r.read(t, "data/state.db.bak.20261026T010203Z") != "<нет>" {
		t.Fatal("копия сделана до проверки")
	}
	if !r.started(t) {
		t.Fatal("контейнер не запущен")
	}
}

func TestPiRestoreScriptInShRollbackFailureDoesNotStart(t *testing.T) {
	r := runPiScriptInSh(t, piOldAndNew(t), "restore-tmp/awg3-panels.json", ".rb.")
	if r.rc != piScriptRollbackFailed {
		t.Fatalf("rc=%d, want %d", r.rc, piScriptRollbackFailed)
	}
	if r.started(t) {
		t.Fatal("при неудачном откате контейнер запускаться не должен")
	}
	if r.read(t, "data/state.db.bak.20261026T010203Z") != "OLD" {
		t.Fatal("копия прежней базы должна остаться для ручного возврата")
	}
}

func TestPiRestoreScriptInShFailedCleanupStillStarts(t *testing.T) {
	// Уборка restore-tmp в ловушке падает -- код и запуск контейнера не теряются.
	r := runPiScriptInSh(t, piOldAndNew(t), "restore-tmp/awg3-panels.json", "", "-rf data/restore-tmp")
	if r.rc != piScriptFailedAfterReplace {
		t.Fatalf("rc=%d, want %d", r.rc, piScriptFailedAfterReplace)
	}
	if !r.started(t) {
		t.Fatal("контейнер не запущен из-за провала уборки")
	}
	ok := runPiScriptInSh(t, piOldAndNew(t), "", "", "-rf data/restore-tmp")
	if ok.rc != 0 || !ok.started(t) {
		t.Fatalf("успех с провалом уборки: rc=%d started=%v", ok.rc, ok.started(t))
	}
}

func TestPiRestoreScriptInShHalfBackupRemovedBeforeReplace(t *testing.T) {
	// Копия -wal не легла (кончилось место): замены не было -- копии этого
	// прогона убираются, база и хранилища прежние.
	r := runPiScriptInSh(t, piOldAndNew(t), "", "data/state.db.bak.20261026T010203Z-wal")
	if r.rc != piScriptFailedBeforeReplace {
		t.Fatalf("rc=%d, want %d", r.rc, piScriptFailedBeforeReplace)
	}
	if r.read(t, "data/state.db") != "OLD" || r.read(t, "data/state.db-wal") != "OLDWAL" || r.read(t, "data/awg3-panels.json") != "OLDSTORE" {
		t.Fatal("база тронута")
	}
	if matches, _ := filepath.Glob(filepath.Join(r.base, "data", "*.bak.*")); len(matches) != 0 {
		t.Fatalf("остались половинные копии этого прогона: %v", matches)
	}
	if !r.started(t) {
		t.Fatal("контейнер не запущен")
	}
}

func TestPiRestoreScriptInShKeepsOtherRunsBackups(t *testing.T) {
	// Копии прошлых восстановлений (другая метка) уборка не трогает.
	r := runPiScriptInSh(t, func(data string) {
		piOldAndNew(t)(data)
		writeFiles(t, data, map[string]string{"state.db.bak.20250101T000000Z": "PREV"})
	}, "", "data/state.db.bak.20261026T010203Z-wal")
	if r.read(t, "data/state.db.bak.20250101T000000Z") != "PREV" {
		t.Fatal("чужая копия убрана")
	}
}

// localSh -- remoteExec, который выполняет команду настоящим sh на этой машине.
type localSh struct{}

func (localSh) Run(cmd string) (string, string, int, error) {
	var stderr strings.Builder
	c := exec.Command("sh", "-c", cmd)
	c.Stderr = &stderr
	out, err := c.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), stderr.String(), ee.ExitCode(), nil
	}
	return string(out), stderr.String(), 0, err
}
func (l localSh) MustRun(cmd string) (string, error) {
	out, _, _, err := l.Run(cmd)
	return out, err
}
func (localSh) UploadSFTPMode(string, []byte, string) error { return nil }

func TestPiCheckFreeSpaceCommandInSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("нужен POSIX sh")
	}
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, filepath.Join(base, "data"), map[string]string{"state.db": strings.Repeat("x", 3000), "state.db-wal": "yy"})
	if err := piCheckFreeSpace(localSh{}, base, 1); err != nil {
		t.Fatalf("место есть, а проверка отказала: %v", err)
	}
	err := piCheckFreeSpace(localSh{}, base, 1<<62)
	if err == nil || !strings.Contains(err.Error(), "мало места на диске Pi: нужно ") {
		t.Fatalf("err=%v", err)
	}
}
