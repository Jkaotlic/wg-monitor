package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The legacy provisioning client remains excluded. Manual owner-triggered
// country-file downloads use the separate premiumfiles interface.
func TestBackendDoesNotLinkLegacyAmneziaProvisioningClient(t *testing.T) {
	// go test кладёт свой GOROOT в окружение теста; запасной путь -- PATH.
	goBin := filepath.Join(os.Getenv("GOROOT"), "bin", "go")
	if _, err := os.Stat(goBin); err != nil || os.Getenv("GOROOT") == "" {
		p, lerr := exec.LookPath("go")
		if lerr != nil {
			if os.Getenv("CI") != "" {
				// В CI молчаливый пропуск отключил бы сторожа насовсем.
				t.Fatalf("go не найден: GOROOT=%q, PATH: %v", os.Getenv("GOROOT"), lerr)
			}
			t.Skipf("go не найден: GOROOT=%q, PATH: %v", os.Getenv("GOROOT"), lerr)
		}
		goBin = p
	}
	cmd := exec.Command(goBin, "list", "-deps", "./cmd/backend")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("go list не отработал: %v\n%s", err, out)
		}
		t.Skipf("go list не отработал: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasSuffix(pkg, "/internal/backend/amnezia") {
			t.Fatalf("в сборку бэкенда вернулся клиент кабинета Amnezia: %s", pkg)
		}
	}
}
