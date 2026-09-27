package checks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Бэкенд (canonicalizeReportedChecks) принимает только ok|fail и на любом
// другом статусе отвергает ВЕСЬ отчёт (422) -- роутер выглядит мёртвым.
// «Не проверено» уходит как ok + details.unverified. Страж: ни один файл
// проверок агента не задаёт статус, кроме ok/fail, и не зовёт Unknown.
func TestChecksEmitOnlyOkOrFailOnTheWire(t *testing.T) {
	statusLit := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])Status\s*[:=]\s*"([^"]*)"`)
	var files []string
	for _, dir := range []string{".", "../dnswatch", ".."} {
		m, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		files = append(files, m...)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(body)
		for _, m := range statusLit.FindAllStringSubmatch(src, -1) {
			if m[1] != "ok" && m[1] != "fail" {
				t.Errorf("%s: check status %q is not ok|fail — the backend drops the whole report", f, m[1])
			}
		}
		if strings.Contains(src, "StatusUnknown") || regexp.MustCompile(`\bUnknown\(`).MatchString(src) {
			t.Errorf("%s: emits an unknown status", f)
		}
	}
}

func TestUnverifiedIsOkWithFlag(t *testing.T) {
	c := Unverified("dns", timeNow(), "nothing probed", map[string]any{"endpoints": 0})
	if c.Status != "ok" || c.Details["unverified"] != true || c.Details["unverified_reason"] != "nothing probed" || c.Details["endpoints"] != 0 {
		t.Fatalf("Unverified = %+v", c)
	}
}
