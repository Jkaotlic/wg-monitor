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
//
// Layout philosophy (revised 2026-04-30 after user feedback "all in one heap"):
// emoji-marked metric lines instead of tabular `Label:    value` columns —
// Telegram is proportional-font and tab-style alignment renders as visual
// noise. One concept per line, blank line between header / body / neighbours /
// tail — gives breathing room without HTML/Markdown.
func FormatHard(a HardArgs) string {
	var b strings.Builder
	mobileBadge := ""
	if a.IsMobile {
		mobileBadge = "📱 "
	}
	pretty := prettyCheckLabel(a.CheckName, a.Check.Details)
	fmt.Fprintf(&b, "🔴 %s[%s] %s — DOWN\n", mobileBadge, a.Nickname, pretty)
	b.WriteString("\n")

	switch checkCategory(a.CheckName) {
	case "tunnel":
		writeTunnelBody(&b, a.Check.Details)
	case "dns":
		writeDNSBody(&b, a.Check.Details)
	case "hydraroute":
		writeHydraRouteBody(&b, a.Check.Details)
	case "awg_manager":
		writeAwgManagerBody(&b, a.Check.Details)
	case "awgmgr_api":
		writeAwgmgrAPIBody(&b, a.Check.Details)
	case "external_reach":
		writeExternalReachBody(&b, a.Check.Details)
	default:
		writeGenericBody(&b, a.Check.Details)
	}

	if len(a.Neighbors) > 0 {
		b.WriteString("\nСоседи:\n")
		writeNeighbors(&b, a.Neighbors)
	}

	fmt.Fprintf(&b, "\n%d fails подряд · с %s",
		a.ConsecFails, a.HardSince.In(mscLoc()).Format("02.01 15:04 МСК"))
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

// RealertArgs feeds FormatRealert. Check carries the LAST KNOWN payload from
// the agent (loaded by realert.Poller from events table) so the reminder can
// render the same rich body as the original HARD alert. Empty Check is OK —
// formatter degrades gracefully to a minimal 3-liner.
type RealertArgs struct {
	Nickname     string
	CheckName    string
	HardSince    time.Time
	RealertCount int
	IsMobile     bool
	Check        wire.Check
	Neighbors    []NeighborSummary
}

// FormatRealert renders a STILL-DOWN reminder using the same body writers as
// FormatHard so the operator sees the same rich context they got at HARD time
// — endpoint, handshake age, pingCheck stats, neighbours — instead of a bare
// "still down" line that's been the source of complaints since Stage 2.
func FormatRealert(args RealertArgs) string {
	var b strings.Builder
	mobileBadge := ""
	if args.IsMobile {
		mobileBadge = "📱 "
	}
	pretty := prettyCheckLabel(args.CheckName, args.Check.Details)
	fmt.Fprintf(&b, "🔁 %s[%s] %s — STILL DOWN\n", mobileBadge, args.Nickname, pretty)

	if args.Check.Name != "" {
		b.WriteString("\n")
		switch checkCategory(args.CheckName) {
		case "tunnel":
			writeTunnelBody(&b, args.Check.Details)
		case "dns":
			writeDNSBody(&b, args.Check.Details)
		case "hydraroute":
			writeHydraRouteBody(&b, args.Check.Details)
		case "awg_manager":
			writeAwgManagerBody(&b, args.Check.Details)
		case "awgmgr_api":
			writeAwgmgrAPIBody(&b, args.Check.Details)
		case "external_reach":
			writeExternalReachBody(&b, args.Check.Details)
		default:
			writeGenericBody(&b, args.Check.Details)
		}
	}

	if len(args.Neighbors) > 0 {
		b.WriteString("\nСоседи:\n")
		writeNeighbors(&b, args.Neighbors)
	}

	age := time.Since(args.HardSince).Round(time.Minute)
	fmt.Fprintf(&b, "\nс %s (%s назад) · Re-alert #%d / 6h",
		args.HardSince.In(mscLoc()).Format("02.01 15:04 МСК"),
		durFmt(age),
		args.RealertCount,
	)
	return b.String()
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
	case name == "tunnels":
		return "awgmgr_api"
	case name == "external_reach":
		return "external_reach"
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
		return name
	}
	switch name {
	case "dns":
		return "🔎 DNS-резолвинг"
	case "hydraroute":
		return "🚏 HydraRoute"
	case "awg_manager":
		return "🩺 Состояние роутера"
	case "tunnels":
		return "🛠 Реестр туннелей"
	case "external_reach":
		return "🌍 Иностранные сервисы"
	}
	return name
}

func writeTunnelBody(b *strings.Builder, d map[string]any) {
	if ep := strOrEmpty(d, "endpoint"); ep != "" {
		if isp := strOrEmpty(d, "isp_interface"); isp != "" {
			fmt.Fprintf(b, "🌐 %s (через %s)\n", ep, isp)
		} else {
			fmt.Fprintf(b, "🌐 %s\n", ep)
		}
	}
	if age, ok := intOrZero(d, "handshake_age_sec"); ok {
		fmt.Fprintf(b, "🤝 handshake: %s назад\n", humanAgeSec(age))
	} else {
		b.WriteString("🤝 handshake: ни разу\n")
	}
	if pc := strOrEmpty(d, "ping_check_status"); pc != "" {
		icon := "💀"
		if isLiveStatus(pc) {
			icon = "✅"
		}
		fc, _ := intOrZero(d, "ping_check_fail_count")
		ft, _ := intOrZero(d, "ping_check_fail_threshold")
		// Build "(restart 2x · last 8ms)" as parens-grouped extras so the
		// line doesn't sprawl when both metrics are zero.
		var extras []string
		if rc, _ := intOrZero(d, "ping_check_restart_count"); rc > 0 {
			extras = append(extras, fmt.Sprintf("auto-restart %dx", rc))
		}
		if lat, ok := intOrZero(d, "ping_check_last_latency_ms"); ok && lat > 0 {
			extras = append(extras, fmt.Sprintf("last %dms", lat))
		}
		fmt.Fprintf(b, "%s pingCheck %s — %d/%d fails", icon, pc, fc, ft)
		if len(extras) > 0 {
			fmt.Fprintf(b, " (%s)", strings.Join(extras, " · "))
		}
		b.WriteString("\n")
	}
	if conflict, ok := boolOrFalse(d, "address_conflict"); ok && conflict {
		b.WriteString("⚠ address conflict on interface\n")
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
		fmt.Fprintf(b, "⚙ %s\n", strings.Join(parts, " · "))
	}
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "❓ %s\n", errStr)
	}
}

func writeDNSBody(b *strings.Builder, d map[string]any) {
	total, _ := intOrZero(d, "endpoints")
	failed, _ := intOrZero(d, "failed_count")
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")
	if total > 0 {
		fmt.Fprintf(b, "🌐 endpoints: %d total · %d unreachable\n", total, failed)
	}
	if rknProbed > 0 {
		switch {
		case rknSus == 0:
			fmt.Fprintf(b, "🚫 RKN probe: ✅ clean (%d probed)\n", rknProbed)
		case rknSus == rknProbed:
			b.WriteString("🚫 RKN probe: ⚠ suspected block on every endpoint\n")
		default:
			fmt.Fprintf(b, "🚫 RKN probe: ⚠ %d/%d suspect\n", rknSus, rknProbed)
		}
	}
	// Per-endpoint failure rows: which resolver actually died, with why.
	// Replaces the previous '❓ <error>' line that just echoed the metric
	// summary ("2/5 endpoints unreachable") and gave no actionable info.
	if failed > 0 {
		for _, ep := range mapsSlice(d, "endpoints_detail") {
			reachable, _ := ep["reachable"].(bool)
			if reachable {
				continue
			}
			tp, _ := ep["type"].(string)
			tg, _ := ep["target"].(string)
			ndms, _ := ep["ndms_name"].(string)
			errStr, _ := ep["err"].(string)
			label := tg
			if ndms != "" {
				label = fmt.Sprintf("%s (%s)", tg, ndms)
			}
			if tp != "" {
				label = tp + " " + label
			}
			fmt.Fprintf(b, "  ✗ %s — %s\n", label, trimErr(errStr))
		}
	}
}

// trimErr crops over-long network errors so the alert stays under TG's
// 4096-byte limit when many endpoints fail at once.
func trimErr(s string) string {
	const max = 80
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func writeHydraRouteBody(b *strings.Builder, d map[string]any) {
	installed, _ := boolOrFalse(d, "installed")
	running, _ := boolOrFalse(d, "running")
	state := "✅"
	if !installed || !running {
		state = "❌"
	}
	fmt.Fprintf(b, "📦 HydraRoute %s installed=%v running=%v\n", state, installed, running)
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "❓ %s\n", errStr)
	}
}

func writeAwgManagerBody(b *strings.Builder, d map[string]any) {
	v := strOrEmpty(d, "version")
	fw := strOrEmpty(d, "firmware")
	be := strOrEmpty(d, "active_backend")
	if v != "" || fw != "" || be != "" {
		var parts []string
		if v != "" {
			parts = append(parts, "v"+v)
		}
		if fw != "" {
			parts = append(parts, "fw "+fw)
		}
		if be != "" {
			parts = append(parts, "backend "+be)
		}
		fmt.Fprintf(b, "📦 awg-manager · %s\n", strings.Join(parts, " · "))
	}
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "❓ %s\n", errStr)
	}
}

func writeAwgmgrAPIBody(b *strings.Builder, d map[string]any) {
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "❓ %s\n", trimBodyDump(errStr))
	}
	if cnt, ok := intOrZero(d, "tunnel_count"); ok && cnt > 0 {
		fmt.Fprintf(b, "📊 туннелей видно: %d\n", cnt)
	}
}

func writeExternalReachBody(b *strings.Builder, d map[string]any) {
	failed := mapsSlice(d, "targets_failed")
	okList := strSlice(d, "targets_ok")
	total, _ := intOrZero(d, "targets_total")
	if total > 0 {
		fmt.Fprintf(b, "🎯 целей: %d · недоступно: %d\n", total, len(failed))
	}
	for _, t := range failed {
		name, _ := t["name"].(string)
		errStr, _ := t["err"].(string)
		fmt.Fprintf(b, "  ✗ %s — %s\n", name, errStr)
	}
	if len(okList) > 0 {
		fmt.Fprintf(b, "  ✓ работает: %s\n", strings.Join(okList, ", "))
	}
	if iface, _ := d["via_interface"].(string); iface != "" {
		fmt.Fprintf(b, "🛣 через %s\n", iface)
	}
}

func writeGenericBody(b *strings.Builder, d map[string]any) {
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "❓ %s\n", errStr)
	}
}

// trimBodyDump strips a trailing "(body=…)" segment from awgmgr error
// messages — useful in agent logs but visual noise in Telegram alerts.
func trimBodyDump(s string) string {
	if i := strings.Index(s, " (body="); i >= 0 {
		return s[:i]
	}
	return s
}

func strSlice(d map[string]any, key string) []string {
	v, ok := d[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func mapsSlice(d map[string]any, key string) []map[string]any {
	v, ok := d[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []map[string]any:
		return x
	case []any:
		out := make([]map[string]any, 0, len(x))
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
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
