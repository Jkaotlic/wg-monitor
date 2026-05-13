package alerts

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ParseDiagReport extracts headline facts from the awg-manager
// /api/diagnostics/result JSON (version "1.0"). Returns:
//   - summary: one-line headline ("отчёт получен (2 559 мс)")
//   - bullets: ordered display lines for Card.Details
//   - rawFallback: true if JSON parse failed OR no documented field
//     was present (caller should dump raw)
//
// Unknown fields are silently skipped.
func ParseDiagReport(raw string) (summary string, bullets []string, rawFallback bool) {
	var rep diagReportV1
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return "", nil, true
	}
	if !rep.hasAnyDocumentedField() {
		return "", nil, true
	}
	return rep.renderSummary(), rep.renderBullets(), false
}

type diagReportV1 struct {
	Version     string         `json:"version"`
	GeneratedAt string         `json:"generatedAt"`
	DurationMs  int64          `json:"durationMs"`
	System      diagSystem     `json:"system"`
	WAN         diagWAN        `json:"wan"`
	Route       map[string]any `json:"route"`
}

type diagSystem struct {
	AppVersion    string        `json:"appVersion"`
	KeeneticOS    string        `json:"keeneticOS"`
	Arch          string        `json:"arch"`
	Backend       string        `json:"backend"`
	TotalMemoryMB int64         `json:"totalMemoryMB"`
	Uptime        string        `json:"uptime"`
	KernelModule  diagKernelMod `json:"kernelModule"`
}

type diagKernelMod struct {
	Exists bool `json:"exists"`
	Loaded bool `json:"loaded"`
}

type diagWAN struct {
	AnyUp      bool                     `json:"anyUp"`
	Interfaces map[string]diagInterface `json:"interfaces"`
}

type diagInterface struct {
	Up    bool   `json:"up"`
	Label string `json:"label"`
}

func (r diagReportV1) hasAnyDocumentedField() bool {
	if r.Version != "" || r.GeneratedAt != "" || r.DurationMs != 0 {
		return true
	}
	if r.System.AppVersion != "" || r.System.Uptime != "" || r.System.TotalMemoryMB != 0 {
		return true
	}
	if len(r.WAN.Interfaces) > 0 {
		return true
	}
	return false
}

func (r diagReportV1) renderSummary() string {
	if r.DurationMs > 0 {
		return fmt.Sprintf("отчёт получен (%s мс)", thousandsRU(r.DurationMs))
	}
	return "отчёт получен"
}

func (r diagReportV1) renderBullets() []string {
	var out []string
	if r.GeneratedAt != "" {
		if t, err := time.Parse(time.RFC3339, r.GeneratedAt); err == nil {
			out = append(out, "📅 Снято: "+t.UTC().Format("2006-01-02 15:04:05 UTC"))
		}
	}
	if r.System.AppVersion != "" || r.System.Backend != "" || r.System.TotalMemoryMB > 0 {
		parts := []string{}
		if r.System.AppVersion != "" {
			parts = append(parts, "awg-manager "+r.System.AppVersion)
		}
		if r.System.Backend != "" {
			parts = append(parts, "backend "+r.System.Backend)
		}
		if r.System.TotalMemoryMB > 0 {
			parts = append(parts, fmt.Sprintf("RAM %d MB", r.System.TotalMemoryMB))
		}
		if len(parts) > 0 {
			out = append(out, "⚙ "+strings.Join(parts, ", "))
		}
	}
	if r.System.Uptime != "" {
		out = append(out, "⏱ Uptime: "+r.System.Uptime)
	}
	if len(r.WAN.Interfaces) > 0 {
		out = append(out, "🌐 WAN: "+renderWANInterfaces(r.WAN.Interfaces))
	}
	return out
}

func renderWANInterfaces(ifs map[string]diagInterface) string {
	names := make([]string, 0, len(ifs))
	for k := range ifs {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		icon := "⚪"
		if ifs[n].Up {
			icon = "✅"
		}
		parts = append(parts, fmt.Sprintf("%s %s", icon, n))
	}
	return strings.Join(parts, " · ")
}

// thousandsRU renders n with thin-space thousands separators
// ("2 559" for 2559). Russian-locale convention.
func thousandsRU(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	first := len(s) % 3
	if first > 0 {
		b.WriteString(s[:first])
	}
	for i := first; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteRune(' ')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
