package alerts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Ключи проверок, которые агент присылает в отчёте (кроме tunnel_<id>),
// находятся СКАНОМ исходников, чтобы новая проверка без подписи роняла тест:
//   - checks/*.go и dnswatch/*.go: OK/Fail/Unverified("имя"...),
//     `Name() string { return "имя" }`, const ...Name = "имя";
//   - agent/reporter.go: wire.Check{Name: "имя"} (пульс).
//
// Вердикт бэкенда bypass_leak агент не шлёт -- он добавлен явно.
func agentCheckKeys(t *testing.T) []string {
	t.Helper()
	call := regexp.MustCompile(`(?:OK|Fail|Unverified)\("([a-z_]+)"`)
	method := regexp.MustCompile(`Name\(\) string \{ return "([a-z_]+)" \}`)
	constant := regexp.MustCompile(`\bconst\s+\w*Name\s*=\s*"([a-z_]+)"|^\s*\w*Name\s*=\s*"([a-z_]+)"`)
	literal := regexp.MustCompile(`Name:\s+"([a-z_]+)"`)

	var files []string
	for _, pat := range []string{"../../agent/checks/*.go", "../../agent/dnswatch/*.go"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	files = append(files, "../../agent/reporter.go")

	found := map[string]bool{"bypass_leak": true}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		res := []*regexp.Regexp{call, method}
		for _, line := range strings.Split(string(src), "\n") {
			for _, re := range res {
				for _, m := range re.FindAllStringSubmatch(line, -1) {
					found[m[1]] = true
				}
			}
			for _, m := range constant.FindAllStringSubmatch(line, -1) {
				found[m[1]+m[2]] = true
			}
			if strings.HasSuffix(f, "reporter.go") {
				for _, m := range literal.FindAllStringSubmatch(line, -1) {
					found[m[1]] = true
				}
			}
		}
	}
	keys := make([]string, 0, len(found))
	for k := range found {
		keys = append(keys, k)
	}
	return keys
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
	for _, key := range agentCheckKeys(t) {
		if seen[key] != 1 {
			t.Errorf("у ключа проверки %q записей в check_names.json: %d, нужна ровно одна", key, seen[key])
		}
	}
	// Лишних записей тоже нет: таблица не копит ключи, которых агент не шлёт.
	want := map[string]bool{}
	for _, key := range agentCheckKeys(t) {
		want[key] = true
	}
	for key := range seen {
		if !want[key] {
			t.Errorf("запись %q в check_names.json не соответствует ни одной проверке агента", key)
		}
	}
}

func TestCheckNamesAreNounsAndBotUsesTheSameTable(t *testing.T) {
	for _, key := range agentCheckKeys(t) {
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

// Подпись external_reach не врёт про «интернет»: ломается обход, а тела
// тревог говорят про сервисы через VPN-туннель.
func TestExternalReachLabelMatchesAlertBody(t *testing.T) {
	label := CheckNames["external_reach"]
	if strings.Contains(strings.ToLower(label), "интернет") || !strings.Contains(label, "VPN-туннель") {
		t.Errorf("подпись %q говорит не про обход через VPN-туннель", label)
	}
	body := FormatHard(HardArgs{
		Nickname: "vasya", CheckName: "external_reach", HardSince: time.Now(),
		Check: wire.Check{Name: "external_reach", Status: "fail", Details: map[string]any{
			"targets_total": 1, "via_interface": "awg12",
			"targets_failed": []any{map[string]any{"name": "youtube", "err": "i/o timeout"}},
		}},
	})
	if !strings.Contains(body, "VPN-туннел") || !strings.Contains(body, "проверка: "+label) {
		t.Errorf("тело тревоги расходится с подписью %q:\n%s", label, body)
	}
}
