package alerts

import (
	"strings"
	"testing"
)

func TestHintFor_NoReport(t *testing.T) {
	sum, hint := HintFor("diag_now", "HTTP_400: {\"error\":true,\"code\":\"NO_REPORT\"}")
	if !strings.Contains(sum, "не сформирован") {
		t.Errorf("summary missing 'не сформирован': %q", sum)
	}
	if !strings.Contains(hint, "ещё раз") {
		t.Errorf("hint missing retry suggestion: %q", hint)
	}
}

func TestHintFor_DiagTimeout(t *testing.T) {
	sum, hint := HintFor("diag_now", "DIAG_TIMEOUT: triggered but no result after 36s")
	if !strings.Contains(sum, "36") {
		t.Errorf("timeout summary should mention 36s: %q", sum)
	}
	if !strings.Contains(hint, "30") || !strings.Contains(hint, "60") {
		t.Errorf("hint should reference 30-60s window: %q", hint)
	}
}

func TestHintFor_AwgmgrUnavailable(t *testing.T) {
	for _, code := range []string{"HTTP_502", "HTTP_503"} {
		raw := code + ": awgmgr down"
		sum, hint := HintFor("diag_now", raw)
		if !strings.Contains(sum, "awg-manager") {
			t.Errorf("%s: summary missing 'awg-manager': %q", code, sum)
		}
		if !strings.Contains(hint, "S99awg-manager") {
			t.Errorf("%s: hint missing service path: %q", code, hint)
		}
	}
}

func TestHintFor_AwgmgrUnauthorized(t *testing.T) {
	for _, code := range []string{"HTTP_401", "HTTP_403"} {
		_, hint := HintFor("diag_now", code+": denied")
		if !strings.Contains(hint, "Установить агент") {
			t.Errorf("%s: hint should suggest wizard reinstall: %q", code, hint)
		}
	}
}

func TestHintFor_ConnectionRefused(t *testing.T) {
	_, hint := HintFor("restart_tunnel", "dial tcp 127.0.0.1:2222: connection refused")
	if !strings.Contains(hint, "2222") {
		t.Errorf("hint should mention port 2222: %q", hint)
	}
}

func TestHintFor_Timeout(t *testing.T) {
	sum, hint := HintFor("opkg_upgrade", "TIMEOUT")
	if !strings.Contains(sum, "не уложился") {
		t.Errorf("timeout summary unexpected: %q", sum)
	}
	if !strings.Contains(hint, "logread") {
		t.Errorf("timeout hint should mention logread: %q", hint)
	}
}

func TestHintFor_Locked(t *testing.T) {
	_, hint := HintFor("diag_now", "LOCKED")
	if !strings.Contains(hint, "lock-файл") && !strings.Contains(hint, "lock") {
		t.Errorf("locked hint should mention lock: %q", hint)
	}
}

func TestHintFor_SqliteLocked(t *testing.T) {
	sum, _ := HintFor("admin_topics", "database is locked")
	if !strings.Contains(sum, "SQLite") {
		t.Errorf("sqlite-locked summary should mention SQLite: %q", sum)
	}
}

func TestHintFor_DefaultFallbackTrimsRaw(t *testing.T) {
	raw := strings.Repeat("X", 500) + "\nsecond line"
	sum, hint := HintFor("diag_now", raw)
	if !strings.Contains(sum, "что-то пошло не так") {
		t.Errorf("default summary unexpected: %q", sum)
	}
	if len(hint) > 350 {
		t.Errorf("default hint should trim raw to ~200 chars, got len=%d", len(hint))
	}
	if strings.Contains(hint, "\n") {
		t.Errorf("default hint should not contain newline (first line only): %q", hint)
	}
}

func TestHintFor_DefaultFallbackSanitizesCodeFence(t *testing.T) {
	raw := "weird ``` triple backticks ``` inside"
	_, hint := HintFor("diag_now", raw)
	if strings.Contains(hint, "```") {
		t.Errorf("default hint must strip triple-backticks to avoid fence break: %q", hint)
	}
}
