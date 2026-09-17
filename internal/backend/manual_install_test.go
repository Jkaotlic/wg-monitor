package backend

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/installtmpl"
)

func TestBuildManualInstallCommand(t *testing.T) {
	script := buildManualInstallCommand("https://backend.example.com/", "router-new", "tok-0123abcd", "v0.36.0")
	for _, want := range []string{
		"NICKNAME='router-new'",
		"BASE='https://backend.example.com/v1/releases/download/v0.36.0'",
		`  url: "https://backend.example.com"`,
		`  token: "tok-0123abcd"`,
		`  nickname: "router-new"`,
		`ASSET="wg-monitor-agent-linux-$ARCH"`,
		`"$BASE/checksums.txt"`,
		"sha256sum",
		"<<'WG_MONITOR_AGENT_CONFIG'",
		installtmpl.InitScript(),
		`"$INIT" restart || "$INIT" start`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("в команде нет %q", want)
		}
	}
	// Поверх чужого агента не ставим: у него свой токен и своё имя.
	if !strings.Contains(script, `if [ -f "$CONFIG" ]; then`) {
		t.Error("нет отказа при уже установленном агенте")
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sh -n: %v\n%s\n---\n%s", err, out, script)
		}
	}
	if got := buildManualInstallCommand("https://backend.example.com", "router-new", "tok", "dev"); got != "" {
		t.Errorf("бэкенд без тега: ждали пусто, получили %d байт", len(got))
	}
	if got := buildManualInstallCommand(" ", "router-new", "tok", "v0.36.0"); got != "" {
		t.Errorf("без адреса: ждали пусто, получили %d байт", len(got))
	}
}
