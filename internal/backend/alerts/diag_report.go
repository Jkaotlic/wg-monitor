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

// TestDetail is one diag check (e.g., "MTU интерфейса"), aggregated
// across tunnels. Slug ID is stable; Label is the human RU title used
// in button text. PerTunnel may be empty for global tests like
// "WAN up с gateway" — in that case the single aggregate KeyValues
// + Reason on the parent are the detail.
type TestDetail struct {
	ID        string            // "mtu" | "dns_leak" | "host_route" | ...
	Label     string            // "MTU интерфейса"
	Status    string            // "ok" | "fail" | "skip"
	PerTunnel []PerTunnelDetail // empty for global tests
}

// PerTunnelDetail is the body of one tunnel's row inside a TestDetail.
type PerTunnelDetail struct {
	TunnelLabel string            // "awg10"
	Status      string            // "ok" | "fail" | "skip"
	KeyValues   map[string]string // ordered display fields (current, expected, ...)
	Reason      string
}

// testSlugLabels maps short slug IDs to user-facing Russian labels.
// Slugs are stable across awg-mgr versions; labels track the screenshots.
var testSlugLabels = map[string]string{
	"mtu":               "MTU интерфейса",
	"dns_leak":          "DNS leak проверка",
	"dnsLeak":           "DNS leak проверка",
	"host_route":        "Host route до endpoint",
	"hostRoute":         "Host route до endpoint",
	"iptables":          "Правила iptables",
	"endpoint":          "Резолв endpoint",
	"endpoint_ping":     "Ping endpoint",
	"endpointPing":      "Ping endpoint",
	"handshake":         "Handshake свежий",
	"tunnel_conn":       "Связность через туннель",
	"tunnelConn":        "Связность через туннель",
	"awg_proxy":         "AWG Proxy статус",
	"awgProxy":          "AWG Proxy статус",
	"pingcheck":         "PingCheck статус",
	"validate_cfg":      "Валидация конфига",
	"validateCfg":       "Валидация конфига",
	"state_consistency": "Консистентность state",
	"stateConsistency":  "Консистентность state",
}

// ParseDiagTests extracts per-test details from the awg-mgr diag JSON.
// Returns nil on parse error or if no recognised test sections are
// present — callers should fall back to the existing summary path.
//
// TENTATIVE JSON shape (pending Task 1 preflight verification):
//
//	{ "tunnels": { "<tid>": { "<slug>": {"status":"...","reason":"...", ...kv} } } }
//
// When the actual awg-mgr 2.8.2 shape is verified on testkeen, this
// function may need to be refit. Defensive failure mode: returns nil
// so drill-down buttons simply don't appear; full diag JSON is still
// accessible via the existing raw button.
func ParseDiagTests(raw string) []TestDetail {
	var top struct {
		Tunnels map[string]map[string]json.RawMessage `json:"tunnels"`
	}
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return nil
	}
	if len(top.Tunnels) == 0 {
		return nil
	}
	// Group by slug across tunnels.
	bySlug := make(map[string]*TestDetail)
	for tid, sections := range top.Tunnels {
		for slug, body := range sections {
			det, ok := bySlug[slug]
			if !ok {
				det = &TestDetail{
					ID:    slug,
					Label: testSlugLabels[slug],
				}
				if det.Label == "" {
					det.Label = slug
				}
				bySlug[slug] = det
			}
			ptd := decodePerTunnel(tid, body)
			det.PerTunnel = append(det.PerTunnel, ptd)
		}
	}
	out := make([]TestDetail, 0, len(bySlug))
	for _, det := range bySlug {
		// Aggregate status: any fail → fail; else any skip → skip; else ok.
		agg := "ok"
		for _, p := range det.PerTunnel {
			if p.Status == "fail" {
				agg = "fail"
				break
			}
			if p.Status == "skip" && agg == "ok" {
				agg = "skip"
			}
		}
		det.Status = agg
		// Stable order within each test's tunnel list.
		sort.Slice(det.PerTunnel, func(i, j int) bool {
			return det.PerTunnel[i].TunnelLabel < det.PerTunnel[j].TunnelLabel
		})
		out = append(out, *det)
	}
	// Stable order across tests.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func decodePerTunnel(tid string, body json.RawMessage) PerTunnelDetail {
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		return PerTunnelDetail{TunnelLabel: tid}
	}
	out := PerTunnelDetail{TunnelLabel: tid, KeyValues: map[string]string{}}
	if s, ok := generic["status"].(string); ok {
		out.Status = s
	}
	if r, ok := generic["reason"].(string); ok {
		out.Reason = r
	}
	for k, v := range generic {
		if k == "status" || k == "reason" {
			continue
		}
		// Strip trailing ".0" that float64 default-decoding adds for ints.
		s := fmt.Sprintf("%v", v)
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			s = fmt.Sprintf("%d", int64(f))
		}
		out.KeyValues[k] = s
	}
	return out
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
