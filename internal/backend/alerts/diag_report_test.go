package alerts

import (
	"strings"
	"testing"
)

const fixtureDiagSuccess = `{
	"version": "1.0",
	"generatedAt": "2026-05-13T07:20:40.31519112Z",
	"durationMs": 2559,
	"route": {"mode": "direct"},
	"system": {
		"appVersion": "2.8.2",
		"keeneticOS": "5.0+",
		"isOS5": true,
		"arch": "arm64",
		"backend": "kernel",
		"kernelModule": {"exists": true, "loaded": true},
		"totalMemoryMB": 489,
		"uptime": "1d 17h 30m"
	},
	"wan": {
		"interfaces": {
			"apcli0": {"up": false, "label": "Wi-Fi клиент 2.4 ГГц"},
			"eth3":   {"up": true,  "label": "Подключение Ethernet"}
		},
		"anyUp": true
	}
}`

func TestParseDiagReport_HappyPath(t *testing.T) {
	summary, bullets, fallback := ParseDiagReport(fixtureDiagSuccess)
	if fallback {
		t.Fatalf("expected fallback=false on well-formed JSON")
	}
	if !strings.Contains(summary, "2559") && !strings.Contains(summary, "2 559") {
		t.Errorf("summary should include durationMs: %q", summary)
	}
	joined := strings.Join(bullets, "\n")
	for _, want := range []string{"2.8.2", "kernel", "489", "1d 17h 30m", "eth3", "apcli0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bullets missing %q:\n%s", want, joined)
		}
	}
	for _, want := range []string{"модуль AWG загружен", "память", "аптайм"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ordinary-user wording missing %q:\n%s", want, joined)
		}
	}
	for _, bad := range []string{"kernel module: loaded", "Uptime:", "RAM "} {
		if strings.Contains(joined, bad) {
			t.Errorf("technical wording %q should not leak:\n%s", bad, joined)
		}
	}
	if !strings.Contains(joined, "✅") {
		t.Errorf("bullets should include ✅ for up interface: %s", joined)
	}
	if !strings.Contains(joined, "⚪") && !strings.Contains(joined, "🔴") {
		t.Errorf("bullets should mark down interface with ⚪ or 🔴: %s", joined)
	}
}

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

func TestParseDiagReport_SkipsMissingFieldsGracefully(t *testing.T) {
	partial := `{"system":{"appVersion":"2.8.2"}}`
	summary, bullets, fallback := ParseDiagReport(partial)
	if fallback {
		t.Errorf("partial-but-recognized JSON must not fall back")
	}
	if !strings.Contains(strings.Join(bullets, "\n"), "2.8.2") {
		t.Errorf("appVersion bullet missing from: %v", bullets)
	}
	for _, absent := range []string{"Uptime", "WAN"} {
		if strings.Contains(strings.Join(bullets, "\n"), absent) {
			t.Errorf("did not expect %q in bullets when field absent: %v", absent, bullets)
		}
	}
	if summary == "" {
		t.Errorf("summary should at minimum say something when minimal: %q", summary)
	}
}

func TestParseDiagReport_ProgramLogsHighlightsWarnings(t *testing.T) {
	const raw = `{
		"version": "1.0",
		"system": {"appVersion": "2.10.7"},
		"programLogs": [
			"INFO health check ok",
			"WARN iptables-restore: line 12 failed",
			"ERROR AmneziaWG params save failed"
		],
		"singboxLogs": [
			"ERROR sing-box noise ignored because fleet does not use sing-box"
		]
	}`
	_, bullets, fallback := ParseDiagReport(raw)
	if fallback {
		t.Fatalf("expected structured report, got fallback")
	}
	joined := strings.Join(bullets, "\n")
	for _, want := range []string{"iptables-restore", "AmneziaWG params"} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs summary missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "sing-box noise") {
		t.Errorf("sing-box logs must be ignored for this bot:\n%s", joined)
	}
}

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
	for _, want := range []string{"модуль AWG есть, но не загружен", "перезагрузка роутера"} {
		if !strings.Contains(joined, want) {
			t.Errorf("kernel-module hint missing %q:\n%s", want, joined)
		}
	}
}

func TestParseDiagReport_GeneratedAtFormatted(t *testing.T) {
	_, bullets, _ := ParseDiagReport(fixtureDiagSuccess)
	joined := strings.Join(bullets, "\n")
	if strings.Contains(joined, "2026-05-13T07:20:40") {
		t.Errorf("generatedAt should be reformatted, not raw RFC3339: %s", joined)
	}
}

func TestParseDiagTests_PerTunnelFailures(t *testing.T) {
	const raw = `{
		"version": "1.0",
		"tunnels": {
			"awg10": {
				"mtu":  {"status":"fail","current":1280,"expected":1380,"reason":"path-mtu=1280"},
				"endpoint": {"status":"ok","resolved":"1.2.3.4"},
				"dnsLeak":  {"status":"skip","reason":"public DNS"}
			},
			"awg11": {
				"mtu":  {"status":"fail","current":1280,"expected":1380,"reason":"same"}
			}
		}
	}`
	tests := ParseDiagTests(raw)
	// Find MTU
	var mtu *TestDetail
	for i := range tests {
		if tests[i].ID == "mtu" {
			mtu = &tests[i]
			break
		}
	}
	if mtu == nil {
		t.Fatal("MTU test not found")
	}
	if mtu.Label == "" {
		t.Error("Label should be human-readable")
	}
	if mtu.Status != "fail" {
		t.Errorf("MTU aggregate status should be fail, got %q", mtu.Status)
	}
	if len(mtu.PerTunnel) != 2 {
		t.Errorf("MTU should have 2 per-tunnel entries, got %d", len(mtu.PerTunnel))
	}
	// awg10 detail must include reason
	var awg10 *PerTunnelDetail
	for i := range mtu.PerTunnel {
		if mtu.PerTunnel[i].TunnelLabel == "awg10" {
			awg10 = &mtu.PerTunnel[i]
		}
	}
	if awg10 == nil || awg10.Reason == "" || awg10.Reason != "path-mtu=1280" {
		t.Errorf("awg10 reason wrong: %+v", awg10)
	}
	if awg10.KeyValues["current"] != "1280" || awg10.KeyValues["expected"] != "1380" {
		t.Errorf("awg10 KeyValues wrong: %+v", awg10.KeyValues)
	}
}

func TestParseDiagTests_LegacyJSONNoExtraFields(t *testing.T) {
	const raw = `{"version":"1.0","system":{"appVersion":"2.8.2"}}`
	tests := ParseDiagTests(raw)
	if len(tests) != 0 {
		t.Errorf("legacy JSON without per-tunnel sections should yield zero tests, got %d", len(tests))
	}
}

func TestParseDiagTests_GarbageJSON(t *testing.T) {
	tests := ParseDiagTests("not even json")
	if tests != nil {
		t.Errorf("garbage should return nil, not panic")
	}
}
