//go:build unix

package fileown

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type chownCall struct{ uid, gid int }

func fake(t *testing.T, euid int) *[]chownCall {
	t.Helper()
	var calls []chownCall
	origEuid, origChown := geteuid, chown
	geteuid = func() int { return euid }
	chown = func(_ *os.File, uid, gid int) error {
		calls = append(calls, chownCall{uid, gid})
		return nil
	}
	t.Cleanup(func() { geteuid, chown = origEuid, origChown })
	return &calls
}

func tempFile(t *testing.T) (*os.File, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, dir
}

// Контейнер бэкенда работает от root, а бэкап -- юнит пользователя на хосте:
// файл, созданный от root, бэкап прочитать не может (Pi, 06.10.2026).
func TestMatchDirAsRootTakesDirOwner(t *testing.T) {
	calls := fake(t, 0)
	f, dir := tempFile(t)
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	sys := st.Sys().(*syscall.Stat_t)
	if sys.Uid == 0 && sys.Gid == 0 {
		t.Skip("каталог теста принадлежит root -- отдавать некому")
	}
	if err := MatchDir(f, dir); err != nil {
		t.Fatalf("MatchDir: %v", err)
	}
	want := chownCall{int(sys.Uid), int(sys.Gid)}
	if len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("chown = %v, want [%v]", *calls, want)
	}
}

func TestMatchDirNotRootDoesNothing(t *testing.T) {
	calls := fake(t, 1000)
	f, dir := tempFile(t)
	if err := MatchDir(f, dir); err != nil {
		t.Fatalf("MatchDir: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("chown вызван не от root: %v", *calls)
	}
}

func TestMatchDirMissingDirIsError(t *testing.T) {
	fake(t, 0)
	f, dir := tempFile(t)
	if err := MatchDir(f, filepath.Join(dir, "нет")); err == nil {
		t.Fatal("ждали ошибку на отсутствующем каталоге")
	}
}
