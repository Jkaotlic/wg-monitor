package alerts

import (
	"strings"
	"testing"
)

// Настоящий отчёт и сводка владельцу — в diag_owner_test.go. Здесь — края:
// непонятный ответ, мусор, старая панель без списка проверок.

func TestParseDiagReport_FallbackOnMalformed(t *testing.T) {
	_, _, fallback := ParseDiagReport("this is not json")
	if !fallback {
		t.Errorf("expected fallback=true on malformed input")
	}
}

func TestParseDiagReport_FallbackWhenMissingAppVersion(t *testing.T) {
	_, _, fallback := ParseDiagReport(`{"unrelated": 42}`)
	if !fallback {
		t.Errorf("expected fallback=true when no documented field present")
	}
}

// Модуль AmneziaWG есть, но не загружен — владельцу есть что сделать самому:
// перезагрузить роутер. Эта строка остаётся в сводке и тогда, когда списка
// проверок в отчёте нет (старая панель).
func TestParseDiagReport_KernelModuleNotLoadedHintsReboot(t *testing.T) {
	const raw = `{
		"version": "1.0",
		"system": {
			"appVersion": "2.10.7",
			"backend": "native",
			"kernelModule": {"exists": true, "loaded": false}
		}
	}`
	_, bullets, fallback := ParseDiagReport(raw)
	if fallback {
		t.Fatalf("expected structured report, got fallback")
	}
	joined := strings.Join(bullets, "\n")
	for _, want := range []string{"модуль AmneziaWG не загружен", "перезагрузка роутера"} {
		if !strings.Contains(joined, want) {
			t.Errorf("kernel-module hint missing %q:\n%s", want, joined)
		}
	}
}

func TestParseDiagTests_LegacyJSONNoExtraFields(t *testing.T) {
	const raw = `{"version":"1.0","system":{"appVersion":"2.8.2"}}`
	tests := ParseDiagTests(raw)
	if len(tests) != 0 {
		t.Errorf("отчёт без списка проверок не должен давать проверок, получили %d", len(tests))
	}
}

func TestParseDiagTests_GarbageJSON(t *testing.T) {
	tests := ParseDiagTests("not even json")
	if tests != nil {
		t.Errorf("garbage should return nil, not panic")
	}
}
