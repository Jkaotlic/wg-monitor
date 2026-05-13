package alerts

import (
	"strings"
	"testing"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestFormatCommandResult_DiagOK_ParsedReport(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: fixtureDiagSuccess}
	chunks := FormatCommandResult("diag_now", r, 3500)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	body := chunks[0]
	if !strings.Contains(body, "📊 Диагностика") {
		t.Errorf("missing label: %s", body)
	}
	if !strings.Contains(body, "📊") {
		t.Errorf("expected 📊 badge: %s", body)
	}
	if !strings.Contains(body, "2.8.2") {
		t.Errorf("expected parsed appVersion: %s", body)
	}
	if strings.Contains(body, "```") {
		t.Errorf("parsed diag must not use code-fence: %s", body)
	}
}

func TestFormatCommandResult_DiagOK_FallbackToRaw(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "diagnostics:\nall green"}
	chunks := FormatCommandResult("diag_now", r, 3500)
	if !strings.Contains(chunks[0], "```") {
		t.Errorf("unparseable diag should fall back to code-fence: %s", chunks[0])
	}
	if !strings.Contains(chunks[0], "all green") {
		t.Errorf("raw body must be preserved on fallback: %s", chunks[0])
	}
}

func TestFormatCommandResult_DiagErr_NoReportHint(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "HTTP_400: NO_REPORT"}
	chunks := FormatCommandResult("diag_now", r, 3500)
	body := chunks[0]
	if !strings.Contains(body, "❌") {
		t.Errorf("error body missing ❌ badge: %s", body)
	}
	if !strings.Contains(body, "💡") {
		t.Errorf("error body missing hint marker 💡: %s", body)
	}
	if strings.Contains(body, "HTTP_400") {
		t.Errorf("typed prefix must NOT leak to user: %s", body)
	}
	if strings.Contains(body, "```") {
		t.Errorf("error must not be code-fenced: %s", body)
	}
}

func TestFormatCommandResult_DiagErr_DiagTimeoutHint(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "DIAG_TIMEOUT: triggered but no result after 12 iterations"}
	chunks := FormatCommandResult("diag_now", r, 3500)
	body := chunks[0]
	if !strings.Contains(body, "36") {
		t.Errorf("timeout summary should mention 36с: %s", body)
	}
	if !strings.Contains(body, "💡") {
		t.Errorf("timeout body missing hint: %s", body)
	}
}

func TestFormatCommandResult_PingcheckOneLiner(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "alive 12 ms", DurationMs: 250}
	chunks := FormatCommandResult("pingcheck_now", r, 3500)
	if len(chunks) != 1 {
		t.Fatal("want 1 chunk")
	}
	if strings.Count(chunks[0], "\n") > 1 {
		t.Errorf("pingcheck should be one-liner-ish, got %d newlines:\n%s", strings.Count(chunks[0], "\n"), chunks[0])
	}
	if !strings.Contains(chunks[0], "alive 12 ms") || !strings.Contains(chunks[0], "250") {
		t.Errorf("missing output or duration: %s", chunks[0])
	}
}

func TestFormatCommandResult_RestartTunnelOK(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "restarted nwg0"}
	chunks := FormatCommandResult("restart_tunnel", r, 3500)
	if !strings.Contains(chunks[0], "🔁") || !strings.Contains(chunks[0], "restarted nwg0") {
		t.Errorf("bad: %s", chunks[0])
	}
}

func TestFormatCommandResult_OpkgPaginated(t *testing.T) {
	body := strings.Repeat("X", 12000)
	r := wire.CommandResult{Status: "ok", Output: body}
	chunks := FormatCommandResult("opkg_upgrade", r, 4000)
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks for 12000 chars at 4000-cap, got %d", len(chunks))
	}
	for i, c := range chunks {
		prefix := "(" + itoa1(i+1) + "/3)"
		if !strings.HasPrefix(c, prefix) && !strings.HasPrefix(strings.TrimLeft(c, " "), prefix) {
			t.Errorf("chunk %d missing %q prefix:\n%s", i, prefix, c[:20])
		}
		if len(c) > 4096 {
			t.Errorf("chunk %d exceeds TG limit: %d", i, len(c))
		}
	}
}

func TestFormatCommandResult_ErrorBadge(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "tunnel not found"}
	chunks := FormatCommandResult("restart_tunnel", r, 3500)
	if !strings.Contains(chunks[0], "❌") {
		t.Errorf("missing error badge: %s", chunks[0])
	}
}

func TestFormatCommandResult_LockedAndTimeout(t *testing.T) {
	for _, st := range []string{"locked", "timeout"} {
		r := wire.CommandResult{Status: st, Output: ""}
		chunks := FormatCommandResult("diag_now", r, 3500)
		body := chunks[0]
		if !strings.Contains(body, "❌") {
			t.Errorf("status=%s: missing ❌ badge: %s", st, body)
		}
		if !strings.Contains(body, "💡") {
			t.Errorf("status=%s: missing hint: %s", st, body)
		}
	}
}

func itoa1(n int) string { // local int→str without importing strconv into the test
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestFormatCommandResult_HardCapAt4096(t *testing.T) {
	// Edge case: caller passes maxChars=4096 (the TG hard limit). The
	// rendered chunk includes a UTF-8 header prefix (~50 bytes) on top of
	// the body slice. Without the protective truncation in paginate, this
	// would produce chunks of ~4146 bytes and TG would reject them.
	body := strings.Repeat("Y", 12000)
	r := wire.CommandResult{Status: "ok", Output: body}
	chunks := FormatCommandResult("opkg_upgrade", r, 4096)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for i, c := range chunks {
		if len(c) > 4096 {
			t.Errorf("chunk %d busts TG hard limit: %d bytes", i, len(c))
		}
	}
}
