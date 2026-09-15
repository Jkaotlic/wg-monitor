package backend

import (
	"strings"
	"testing"
)

// Причина неудачи уходит людям и на экран «Парк». Машинная строка агента
// по-английски человеку ничего не говорит, а внутренние имена -- запрещены.
func TestDeployFailureTextSpeaksRussian(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"self_update: insufficient /opt space: 1200 KB free, need 2048 KB for the new binary plus 2048 KB headroom (4096 KB total)", "мало свободного места в разделе /opt"},
		{"self_update: df /opt: exit status 1", "мало свободного места в разделе /opt"},
		{`self_update: repo_base "https://203.0.113.9/v1/releases/download" is not an allowed release origin`, "адрес загрузки не совпал с адресом сервера"},
		{"sha256 mismatch: want 0123456789abcdef got fedcba9876543210", "файл обновления не прошёл проверку подлинности"},
		{"verify checksums.txt signature: bad signature", "файл обновления не прошёл проверку подлинности"},
		{"checksums.txt: no entry for wg-monitor-agent-linux-mipsle", "в выпуске нет файла для этого роутера"},
		{"self_update: target version v0.31.0 is older than the running v0.32.0 — pass allow_downgrade to override", "на роутере уже стоит версия новее"},
		{`self_update: unsupported GOARCH "386" (expected arm64 or mipsle)`, "архитектура роутера не поддерживается"},
		{"download checksums.txt: HTTP 502 for https://backend.example.com/v1/releases/download/v0.32.0/checksums.txt", "роутер не смог скачать обновление"},
		{`download wg-monitor-agent-linux-arm64: Get "https://backend.example.com": dial tcp: lookup backend.example.com: no such host`, "роутер не смог скачать обновление"},
		{"", "агент не сообщил причину"},
		{"status err", "агент сообщил об ошибке установки"},
	}
	for _, c := range cases {
		got := deployFailureText(c.raw)
		if got != c.want {
			t.Errorf("deployFailureText(%q) = %q, ждали %q", c.raw, got, c.want)
		}
		for _, banned := range []string{"self_update", "pending", "repo_base", "HTTP"} {
			if strings.Contains(got, banned) {
				t.Errorf("в тексте для людей %q есть %q", got, banned)
			}
		}
	}
}
