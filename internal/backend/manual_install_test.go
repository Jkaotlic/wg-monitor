package backend

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/installtmpl"
)

var manualInstallSums = map[string]string{
	"wg-monitor-agent-linux-arm64":  strings.Repeat("a1", 32),
	"wg-monitor-agent-linux-mipsle": strings.Repeat("b2", 32),
}

func TestBuildManualInstallCommand(t *testing.T) {
	script := buildManualInstallCommand("https://backend.example.com/", "router-new", "tok-0123abcd", "v0.36.0", manualInstallSums)
	for _, want := range []string{
		"NICKNAME='router-new'",
		"BASE='https://backend.example.com/v1/releases/download/v0.36.0'",
		`  url: "https://backend.example.com"`,
		`  token: "tok-0123abcd"`,
		`  nickname: "router-new"`,
		`ASSET="wg-monitor-agent-linux-$ARCH"`,
		// Суммы -- константы, проверенные сервером по подписи, по архитектурам.
		"arm64) WANT='" + manualInstallSums["wg-monitor-agent-linux-arm64"] + "' ;;",
		"mipsle) WANT='" + manualInstallSums["wg-monitor-agent-linux-mipsle"] + "' ;;",
		"sha256sum",
		"<<'WG_MONITOR_AGENT_CONFIG'",
		installtmpl.InitScript(),
		`"$INIT" restart || "$INIT" start`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("в команде нет %q", want)
		}
	}
	// checksums.txt на роутере не скачивается: зеркало /v1/releases/download
	// подпись не проверяет, и сверка с файлом тем же путём ничего не защищает.
	if strings.Contains(script, "checksums.txt") {
		t.Error("команда качает checksums.txt -- суммы должны быть вписаны сервером")
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
}

func TestBuildManualInstallCommandEmptyWithoutSafeInputs(t *testing.T) {
	onlyArm := map[string]string{"wg-monitor-agent-linux-arm64": manualInstallSums["wg-monitor-agent-linux-arm64"]}
	notHex := map[string]string{
		"wg-monitor-agent-linux-arm64":  "x'; reboot; '",
		"wg-monitor-agent-linux-mipsle": manualInstallSums["wg-monitor-agent-linux-mipsle"],
	}
	for name, got := range map[string]string{
		"бэкенд без тега":      buildManualInstallCommand("https://backend.example.com", "router-new", "tok", "dev", manualInstallSums),
		"без адреса":           buildManualInstallCommand(" ", "router-new", "tok", "v0.36.0", manualInstallSums),
		"адрес не https":       buildManualInstallCommand("http://backend.example.com", "router-new", "tok", "v0.36.0", manualInstallSums),
		"сумм нет":             buildManualInstallCommand("https://backend.example.com", "router-new", "tok", "v0.36.0", nil),
		"одной суммы нет":      buildManualInstallCommand("https://backend.example.com", "router-new", "tok", "v0.36.0", onlyArm),
		"сумма не шестнадцать": buildManualInstallCommand("https://backend.example.com", "router-new", "tok", "v0.36.0", notHex),
	} {
		if got != "" {
			t.Errorf("%s: ждали пусто, получили %d байт", name, len(got))
		}
	}
}
