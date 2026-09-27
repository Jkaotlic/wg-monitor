package awgmrelay

import (
	"os"
	"os/exec"
	"testing"
)

// Питоновские тесты релея (test_*.py) раньше не запускал никто: ни go test,
// ни CI. Этот тест гоняет их через python3 -m unittest. На CI отсутствие
// python3 -- провал, а не молчаливый пропуск (см. macos-skips-python-tests).
func TestRelayPythonUnittests(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("python3 not found on CI: relay tests would be silently skipped")
		}
		t.Skip("python3 not found")
	}
	cmd := exec.Command(py, "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("relay python tests failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
