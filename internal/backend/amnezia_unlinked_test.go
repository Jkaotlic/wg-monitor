package backend

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// FRESH-06: бот не ходит в API кабинета Amnezia Premium (решение оператора
// 16.10), и клиента кабинета в сборке нет. Тест сторожит, чтобы он не вернулся.
func TestBackendDoesNotLinkAmneziaCabinet(t *testing.T) {
	goBin := runtime.GOROOT() + "/bin/go"
	if _, err := os.Stat(goBin); err != nil {
		t.Skipf("go не найден: %v", err)
	}
	cmd := exec.Command(goBin, "list", "-deps", "./cmd/backend")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("go list не отработал: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasSuffix(pkg, "/internal/backend/amnezia") {
			t.Fatalf("в сборку бэкенда вернулся клиент кабинета Amnezia: %s", pkg)
		}
	}
}
