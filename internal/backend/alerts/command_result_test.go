package alerts

import (
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestFormatCommandResult_DiagOK(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "diagnostics:\nall green"}
	chunks := FormatCommandResult("diag_now", r, 3500)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0], "📊") || !strings.Contains(chunks[0], "Диагностика") {
		t.Errorf("missing label: %s", chunks[0])
	}
	if !strings.Contains(chunks[0], "```") {
		t.Errorf("diag must use code-fence: %s", chunks[0])
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

func TestFormatCommandResult_ErrorPrefix(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "tunnel not found"}
	chunks := FormatCommandResult("restart_tunnel", r, 3500)
	if !strings.Contains(chunks[0], "❌ Не удалось:") {
		t.Errorf("missing error prefix: %s", chunks[0])
	}
}

func TestFormatCommandResult_LockedAndTimeout(t *testing.T) {
	for _, st := range []string{"locked", "timeout"} {
		r := wire.CommandResult{Status: st, Output: ""}
		chunks := FormatCommandResult("diag_now", r, 3500)
		if !strings.Contains(chunks[0], "❌ Не удалось:") {
			t.Errorf("status=%s: missing error prefix", st)
		}
		if !strings.Contains(chunks[0], st) {
			t.Errorf("status=%s: status word missing in body", st)
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
