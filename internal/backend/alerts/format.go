package alerts

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// HardArgs feeds FormatHard. Check carries the raw probe payload from the
// agent (post-pivot: rich Details from awg-manager). IsMobile gates the
// 📱 badge in the title.
//
// Neighbors is optional context — short summaries of OTHER tunnels of the
// same user, to help the operator see whether a tunnel-specific failure
// is isolated or part of a broader outage. Empty when not applicable
// (e.g. the failing check is dns or hydraroute, not a tunnel).
type HardArgs struct {
	Nickname    string
	CheckName   string
	ConsecFails int
	HardSince   time.Time
	IsMobile    bool
	Check       wire.Check
	Neighbors   []NeighborSummary
}

type NeighborSummary struct {
	CheckName     string // e.g. "tunnel_awg11"
	TunnelName    string // pretty name from Details
	Interface     string // "nwg1"
	Status        string // "alive" / "dead" / "ok" / "fail"
	HandshakeAge  int    // seconds, 0 if unknown
}

type RecoveryArgs struct {
	Nickname    string
	CheckName   string
	HardSince   time.Time
	RecoveredAt time.Time
}

// FormatHard renders a HARD alert. Returns plain text (no MarkdownV2 — too
// many escaping landmines in dynamic strings like endpoint hostnames).
func FormatHard(a HardArgs) string {
	var b strings.Builder
	mobileBadge := ""
	if a.IsMobile {
		mobileBadge = "📱 "
	}
	pretty := prettyCheckLabel(a.CheckName, a.Check.Details)
	fmt.Fprintf(&b, "🔴 %s[%s] %s — DOWN\n", mobileBadge, a.Nickname, pretty)

	switch checkCategory(a.CheckName) {
	case "tunnel":
		writeTunnelBody(&b, a.Check.Details)
	case "dns":
		writeDNSBody(&b, a.Check.Details)
	case "hydraroute":
		writeHydraRouteBody(&b, a.Check.Details)
	case "awg_manager":
		writeAwgManagerBody(&b, a.Check.Details)
	default:
		writeGenericBody(&b, a.Check.Details)
	}

	if len(a.Neighbors) > 0 {
		b.WriteString("\nДругие туннели:\n")
		writeNeighbors(&b, a.Neighbors)
	}

	fmt.Fprintf(&b, "\nFails: %d подряд · Hard since: %s",
		a.ConsecFails, a.HardSince.In(mscLoc()).Format("2006-01-02 15:04:05 МСК"))
	return b.String()
}

func FormatRecovery(a RecoveryArgs) string {
	d := a.RecoveredAt.Sub(a.HardSince).Round(time.Minute)
	return fmt.Sprintf(
		"✅ [%s] %s — RECOVERED\nDowntime: %s",
		a.Nickname, a.CheckName, durFmt(d),
	)
}

func FormatRouterOffline(nickname string, since time.Duration) string {
	return fmt.Sprintf("🔴 [%s] ROUTER OFFLINE — нет heartbeat'ов %s", nickname, durFmt(since.Round(time.Minute)))
}

type RealertArgs struct {
	Nickname     string
	CheckName    string
	HardSince    time.Time
	RealertCount int
}

func FormatRealert(args RealertArgs) string {
	age := time.Since(args.HardSince).Round(time.Minute)
	return fmt.Sprintf(
		"🔁 [%s] %s — STILL DOWN\nHard since: %s (%s ago)\nRe-alert #%d (every 6h)",
		args.Nickname,
		args.CheckName,
		args.HardSince.UTC().Format("2006-01-02 15:04 MST"),
		durFmt(age),
		args.RealertCount,
	)
}

// checkCategory classifies a check name so the formatter can dispatch.
// We don't trust the agent to label us — we infer from the name shape.
func checkCategory(name string) string {
	switch {
	case strings.HasPrefix(name, "tunnel_"):
		return "tunnel"
	case name == "dns":
		return "dns"
	case name == "hydraroute":
		return "hydraroute"
	case name == "awg_manager":
		return "awg_manager"
	}
	return "generic"
}

func prettyCheckLabel(name string, details map[string]any) string {
	if checkCategory(name) == "tunnel" {
		tname, _ := details["tunnel_name"].(string)
		iface, _ := details["interface"].(string)
		switch {
		case tname != "" && iface != "":
			return fmt.Sprintf("%s (%s)", tname, iface)
		case tname != "":
			return tname
		case iface != "":
			return iface
		}
	}
	return name
}

func writeTunnelBody(b *strings.Builder, d map[string]any) {
	if ep := strOrEmpty(d, "endpoint"); ep != "" {
		isp := strOrEmpty(d, "isp_interface")
		if isp != "" {
			fmt.Fprintf(b, "Endpoint:       %s (через %s)\n", ep, isp)
		} else {
			fmt.Fprintf(b, "Endpoint:       %s\n", ep)
		}
	}
	if age, ok := intOrZero(d, "handshake_age_sec"); ok {
		fmt.Fprintf(b, "Last handshake: %s назад\n", humanAgeSec(age))
	} else {
		b.WriteString("Last handshake: ни разу\n")
	}
	if pc := strOrEmpty(d, "ping_check_status"); pc != "" {
		fc, _ := intOrZero(d, "ping_check_fail_count")
		ft, _ := intOrZero(d, "ping_check_fail_threshold")
		fmt.Fprintf(b, "PingCheck:      %s — failCount %d/%d", pc, fc, ft)
		if lat, ok := intOrZero(d, "ping_check_last_latency_ms"); ok && lat > 0 {
			fmt.Fprintf(b, " · last %dms", lat)
		}
		b.WriteString("\n")
		if rc, _ := intOrZero(d, "ping_check_restart_count"); rc > 0 {
			fmt.Fprintf(b, "Auto-restart:   %dx\n", rc)
		}
	}
	if conflict, ok := boolOrFalse(d, "address_conflict"); ok && conflict {
		b.WriteString("⚠ Address conflict on interface\n")
	}
	be := strOrEmpty(d, "backend")
	awgVer := strOrEmpty(d, "awg_version")
	mtu, _ := intOrZero(d, "mtu")
	if be != "" || awgVer != "" || mtu > 0 {
		var parts []string
		if be != "" {
			parts = append(parts, be)
		}
		if awgVer != "" {
			parts = append(parts, "AWG "+awgVer)
		}
		if mtu > 0 {
			parts = append(parts, fmt.Sprintf("MTU %d", mtu))
		}
		fmt.Fprintf(b, "Backend:        %s\n", strings.Join(parts, ", "))
	}
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "Reason:         %s\n", errStr)
	}
}

func writeDNSBody(b *strings.Builder, d map[string]any) {
	total, _ := intOrZero(d, "endpoints")
	failed, _ := intOrZero(d, "failed_count")
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")
	if total > 0 {
		fmt.Fprintf(b, "Endpoints:      %d total, %d unreachable\n", total, failed)
	}
	if rknProbed > 0 {
		marker := "✅ clean"
		if rknSus == rknProbed {
			marker = "⚠ suspected RKN block"
		} else if rknSus > 0 {
			marker = fmt.Sprintf("⚠ %d/%d suspect", rknSus, rknProbed)
		}
		fmt.Fprintf(b, "RKN probe:      %s\n", marker)
	}
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "Reason:         %s\n", errStr)
	}
}

func writeHydraRouteBody(b *strings.Builder, d map[string]any) {
	installed, _ := boolOrFalse(d, "installed")
	running, _ := boolOrFalse(d, "running")
	fmt.Fprintf(b, "HydraRoute:     installed=%v running=%v\n", installed, running)
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "Reason:         %s\n", errStr)
	}
}

func writeAwgManagerBody(b *strings.Builder, d map[string]any) {
	v := strOrEmpty(d, "version")
	fw := strOrEmpty(d, "firmware")
	be := strOrEmpty(d, "active_backend")
	if v != "" || fw != "" || be != "" {
		fmt.Fprintf(b, "awg-manager:    %s · %s · backend %s\n", v, fw, be)
	}
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "Reason:         %s\n", errStr)
	}
}

func writeGenericBody(b *strings.Builder, d map[string]any) {
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "Reason: %s\n", errStr)
	}
}

func writeNeighbors(b *strings.Builder, ns []NeighborSummary) {
	// Sort by check name for stable output (test-friendly).
	sort.Slice(ns, func(i, j int) bool { return ns[i].CheckName < ns[j].CheckName })
	for _, n := range ns {
		label := n.TunnelName
		if label == "" {
			label = n.CheckName
		}
		if n.Interface != "" {
			label = fmt.Sprintf("%s (%s)", label, n.Interface)
		}
		mark := "✅"
		if !isLiveStatus(n.Status) {
			mark = "🔴"
		}
		age := ""
		if n.HandshakeAge > 0 {
			age = " · " + humanAgeSec(n.HandshakeAge) + " назад"
		}
		fmt.Fprintf(b, "  %s %s — %s%s\n", mark, label, n.Status, age)
	}
}

func isLiveStatus(s string) bool {
	switch s {
	case "alive", "ok", "running":
		return true
	}
	return false
}

func strOrEmpty(d map[string]any, key string) string {
	if d == nil {
		return ""
	}
	if v, ok := d[key].(string); ok {
		return v
	}
	return ""
}

// intOrZero accepts JSON numbers (float64), Go ints, and int64s — JSON
// decoding into map[string]any always yields float64 for numerics, but
// in-process tests often build the map with int literals.
func intOrZero(d map[string]any, key string) (int, bool) {
	if d == nil {
		return 0, false
	}
	v, ok := d[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

func boolOrFalse(d map[string]any, key string) (bool, bool) {
	if d == nil {
		return false, false
	}
	if v, ok := d[key].(bool); ok {
		return v, true
	}
	return false, false
}

func humanAgeSec(s int) string {
	if s < 60 {
		return fmt.Sprintf("%dс", s)
	}
	m := s / 60
	rs := s % 60
	if m < 60 {
		if rs > 0 {
			return fmt.Sprintf("%d мин %d с", m, rs)
		}
		return fmt.Sprintf("%d мин", m)
	}
	h := m / 60
	rm := m % 60
	return fmt.Sprintf("%dч %dм", h, rm)
}

func durFmt(d time.Duration) string {
	if d < time.Minute {
		return "< 1m"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func mscLoc() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("МСК", 3*3600)
	}
	return loc
}
