package backend

import (
	"strings"
	"testing"
	"time"
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
		// B2: сопоставление по голому слову "repo_base" было шире, чем сама
		// ошибка агента (internal/agent/actions/self_update.go, единственный
		// источник этого текста -- releaseorigin.ValidateRepoBaseForBackendURL,
		// "... is not an allowed release origin"). Ошибка скачивания, чей URL
		// сам содержит слово "repo_base" (например, хост зеркала так назвали),
		// не должна перехватываться веткой «адрес не совпал».
		{`download wg-monitor-agent-linux-arm64: Get "https://repo_base_mirror.example.com/v1/releases/download/v0.20.0/wg-monitor-agent-linux-arm64": dial tcp: lookup repo_base_mirror.example.com: no such host`, "роутер не смог скачать обновление"},
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

func TestPendingDeployExpired(t *testing.T) {
	now := time.Date(2026, 12, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		since string
		want  bool
	}{
		{now.Add(-91 * 24 * time.Hour).Format(time.RFC3339), true},
		{now.Add(-90*24*time.Hour - time.Second).Format(time.RFC3339), true},
		{now.Add(-89 * 24 * time.Hour).Format(time.RFC3339), false},
		{"", false},
		{"вчера", false},
	}
	for _, c := range cases {
		if got := pendingDeployExpired(c.since, now); got != c.want {
			t.Errorf("pendingDeployExpired(%q) = %v, ждали %v", c.since, got, c.want)
		}
	}
}
