package alerts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/internal/agent/dnswatch"
)

// Ключи проверок, которые агент присылает в отчёте (кроме tunnel_<id>), плюс
// вердикт бэкенда bypass_leak: у каждого -- ровно одна запись в таблице
// подписей, общей для тревог бота и экранов мини-аппа (v0.56, спека B4).
func agentCheckKeys() []string {
	return []string{
		checks.DNS{}.Name(),
		(&checks.DNSSplit{}).Name(),
		checks.AwgManagerCheck{}.Name(),
		checks.ExternalReachCheck{}.Name(),
		checks.HydraRouteCheck{}.Name(),
		checks.DNSRuName,
		dnswatch.CheckName,
		"tunnels",         // опись VPN-туннелей (checks/tunnels.go)
		"agent_heartbeat", // пульс (agent/reporter.go)
		"bypass_leak",     // вердикт бэкенда, тихий режим
	}
}

func TestCheckNamesEveryAgentKeyHasExactlyOneEntry(t *testing.T) {
	// Дубликаты ключей в самом файле json.Unmarshal прячет -- считаем токены.
	seen := map[string]int{}
	dec := json.NewDecoder(bytes.NewReader(checkNamesJSON))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		seen[k.(string)]++
		if _, err := dec.Token(); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range agentCheckKeys() {
		if seen[key] != 1 {
			t.Errorf("у ключа проверки %q записей в check_names.json: %d, нужна ровно одна", key, seen[key])
		}
	}
	// Лишних записей тоже нет: таблица не копит ключи, которых агент не шлёт.
	want := map[string]bool{}
	for _, key := range agentCheckKeys() {
		want[key] = true
	}
	for key := range seen {
		if !want[key] {
			t.Errorf("запись %q в check_names.json не соответствует ни одной проверке агента", key)
		}
	}
}

// Ключ, которого нет в списке выше, но агент уже шлёт: ищем имена прямо в
// исходниках проверок (OK/Fail/Unverified("имя"...), const ...Name = "имя").
func TestCheckNamesCoverSourceLiterals(t *testing.T) {
	re := regexp.MustCompile(`(?:OK|Fail|Unverified)\("([a-z_]+)"|Name\s*=\s*"([a-z_]+)"`)
	files, _ := filepath.Glob("../../agent/checks/*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			key := m[1] + m[2]
			if _, ok := CheckNames[key]; !ok {
				t.Errorf("%s: проверка %q без подписи в check_names.json", filepath.Base(f), key)
			}
		}
	}
}

func TestCheckNamesAreNounsAndBotUsesTheSameTable(t *testing.T) {
	for _, key := range agentCheckKeys() {
		label := CheckNames[key]
		if label == "" || !startsUpper(label) {
			t.Errorf("подпись %q (%s) пустая или не с заглавной", label, key)
		}
		if got := checkHumanName(key); got != label {
			t.Errorf("бот зовёт %s %q, в таблице %q", key, got, label)
		}
	}
}

func startsUpper(s string) bool {
	for _, r := range s {
		return strings.ToUpper(string(r)) == string(r) && strings.ToLower(string(r)) != string(r)
	}
	return false
}
