package routerscripts

import (
	"strings"
	"testing"
)

func TestRenderReplacesEachKeyOnce(t *testing.T) {
	got, err := Render("#!/bin/sh\nA=1\nAB=2\n  A=inner\n", [][2]string{{"A", "9"}, {"AB", `"x"`}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "#!/bin/sh\nA=9\nAB=\"x\"\n  A=inner\n" {
		t.Fatalf("got %q", got)
	}
}

// Пропавший или повторённый ключ -- ошибка, а не тихий скрипт со значением
// по умолчанию.
func TestRenderRejectsMissingOrDuplicateKey(t *testing.T) {
	if _, err := Render("A=1\n", [][2]string{{"B", "2"}}); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := Render("A=1\nA=2\n", [][2]string{{"A", "3"}}); err == nil {
		t.Fatal("duplicate key accepted")
	}
}

func TestEntwareCleanupHasSubstitutableKeys(t *testing.T) {
	for _, k := range []string{"LOG", "MIN_FREE_KB", "MIN_MEM_AVAILABLE_KB", "MAX_LOG_KB"} {
		if _, err := Render(EntwareCleanup, [][2]string{{k, "1"}}); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if !strings.HasPrefix(EntwareCleanup, "#!/bin/sh\n") {
		t.Fatal("no shebang")
	}
}
