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

func TestParseDiagReport_GeneratedAtFormatted(t *testing.T) {
	_, bullets, _ := ParseDiagReport(fixtureDiagSuccess)
	joined := strings.Join(bullets, "\n")
	if strings.Contains(joined, "2026-05-13T07:20:40") {
		t.Errorf("generatedAt should be reformatted, not raw RFC3339: %s", joined)
	}
}
