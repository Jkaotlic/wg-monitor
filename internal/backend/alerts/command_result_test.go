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
	if strings.Contains(body, "📊 📊") {
		t.Errorf("doubled emoji in diag header: %s", body)
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
	if !strings.Contains(chunks[0], "связь живая, 12 мс") || !strings.Contains(chunks[0], "250") {
		t.Errorf("missing output or duration: %s", chunks[0])
	}
}

func TestFormatCommandResult_RestartTunnelOK(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "все туннели перезапущены"}
	chunks := FormatCommandResult("restart_tunnel", r, 3500)
	if !strings.Contains(chunks[0], "Перезапуск awg-manager") {
		t.Fatalf("global restart_tunnel should be labelled as awg-manager restart, got:\n%s", chunks[0])
	}
	if strings.Contains(chunks[0], "Перезапуск туннеля") || strings.Contains(chunks[0], "туннель перезапущен") {
		t.Fatalf("global restart_tunnel must not look like a per-tunnel restart, got:\n%s", chunks[0])
	}
	if !strings.Contains(chunks[0], "все туннели перезапущены") {
		t.Errorf("global restart output should be preserved, got:\n%s", chunks[0])
	}
}

func TestFormatCommandResult_TunnelRestartOK(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "restarted Wireguard3\n-- down --\nok\n-- up --\nok"}
	chunks := FormatCommandResult("tunnel_restart", r, 3500)
	if !strings.Contains(chunks[0], "туннель перезапущен: Wireguard3") {
		t.Errorf("bad: %s", chunks[0])
	}
	if strings.Contains(chunks[0], "-- down --") {
		t.Errorf("restart summary should hide ndmc transcript: %s", chunks[0])
	}
}

func TestFormatCommandResult_TunnelEnable_CleanSummary(t *testing.T) {
	// Agent emits "interface <ndms> -> up\n<ndmc raw stdout>". Backend must
	// strip the engineer-speak first line and drop the ndmc noise entirely.
	r := wire.CommandResult{
		Status: "ok",
		Output: "interface Wireguard1 -> up\nNetwork::Interface::Base: \"Wireguard1\": interface is up.\n",
	}
	chunks := FormatCommandResult("tunnel_enable", r, 3500)
	body := chunks[0]
	if !strings.Contains(body, "Wireguard1 → включён") {
		t.Errorf("clean summary missing: %s", body)
	}
	if strings.Contains(body, "->") {
		t.Errorf("engineer-speak arrow leaked: %s", body)
	}
	if strings.Contains(body, "Network::Interface") {
		t.Errorf("ndmc raw stdout must be hidden: %s", body)
	}
	if strings.Contains(body, "\n\n") {
		t.Errorf("toggle result should be single-line, found paragraph break: %s", body)
	}
}

func TestFormatCommandResult_TunnelDisable_Verb(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "interface Wireguard0 -> down\nstopped\n"}
	chunks := FormatCommandResult("tunnel_disable", r, 3500)
	if !strings.Contains(chunks[0], "Wireguard0 → выключен") {
		t.Errorf("disable verb missing: %s", chunks[0])
	}
}

func TestFormatCommandResult_TunnelImportShowsNextStep(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "✅ Туннель \"newtun\" создан (id=awg99)\n⚠️ Проверка запуска: туннель импортирован, но пока не ожил (id=awg99, status=stopped, enabled=true, handshake=none)", DurationMs: 65709}
	chunks := FormatCommandResult("tunnel_import", r, 3500)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	body := chunks[0]
	for _, want := range []string{"Импорт конфига", "newtun", "за 1м 06с", "Проверка запуска", "handshake=none", "🛣 Маршруты", "перенести правила"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestFormatCommandResult_TunnelEnable_UnknownOutput_VerbOnly(t *testing.T) {
	// Agent prefix changes or empty output → degrade to verb-only summary.
	r := wire.CommandResult{Status: "ok", Output: ""}
	chunks := FormatCommandResult("tunnel_enable", r, 3500)
	if !strings.Contains(chunks[0], "включён") {
		t.Errorf("verb-only fallback missing: %s", chunks[0])
	}
	if strings.Contains(chunks[0], "→") {
		t.Errorf("no arrow when ndms unknown: %s", chunks[0])
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

func TestFormatCommandResult_RouterDoctorPlainText(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: "🩺 Проверка роутера\n✅ awg-manager API"}
	chunks := FormatCommandResult("router_doctor", r, 3500)
	if len(chunks) != 1 || !strings.Contains(chunks[0], "awg-manager API") {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
}

func TestFormatCommandResult_RouterDoctorErrKeepsDetails(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "doctor\nOK awg-manager API\nFAIL tunnels: 0/4 running"}
	chunks := FormatCommandResult("router_doctor", r, 3500)
	body := chunks[0]
	if !strings.Contains(body, "FAIL tunnels") {
		t.Fatalf("router doctor error must preserve agent output, got:\n%s", body)
	}
	if strings.Contains(body, "Деталь") {
		t.Fatalf("router doctor error must not be collapsed to generic hint:\n%s", body)
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

func TestFormatCommandResult_DiagFallback_NeutralisesEmbeddedTripleBacktick(t *testing.T) {
	// Adversarial: raw body itself contains ``` which would terminate our
	// outer code-fence and leak the tail as plain text + broken Markdown.
	r := wire.CommandResult{
		Status: "ok",
		Output: "weird body with ``` literal backticks ``` inside — not JSON",
	}
	chunks := FormatCommandResult("diag_now", r, 3500)
	body := chunks[0]
	if strings.Contains(body, "```\nweird body with ```") {
		t.Errorf("unescaped inner ``` would break TG fence:\n%s", body)
	}
	if !strings.Contains(body, "'''") {
		t.Errorf("expected inner ``` to be replaced with ''':\n%s", body)
	}
	if !strings.Contains(body, "literal backticks") {
		t.Errorf("body content must be preserved:\n%s", body)
	}
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
