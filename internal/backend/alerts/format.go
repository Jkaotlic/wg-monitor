package alerts

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// HardArgs feeds FormatHard. Check carries the raw probe payload from the
// agent (post-pivot: rich Details from awg-manager). IsMobile gates the
// 📱 badge in the title.
//
// Neighbors is optional context — short summaries of OTHER tunnels of the
// same user. Used both as a source of correlation hints in the diagnose
// helper and as a "других туннелей" hint in the advice line.
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
	CheckName    string // e.g. "tunnel_awg11"
	TunnelName   string // pretty name from Details
	NDMSName     string // Keenetic interface name, e.g. "Wireguard3"
	Interface    string // "nwg1"
	Status       string // "alive" / "dead" / "ok" / "fail"
	HandshakeAge int    // seconds, 0 if unknown
}

type RecoveryArgs struct {
	Nickname    string
	CheckName   string
	HardSince   time.Time
	RecoveredAt time.Time
	// Check carries the LAST KNOWN event payload (status==ok recovery event).
	// Optional — empty Details degrades the recovery message to the bare
	// one-liner. For tunnel_* checks this lets us name the tunnel and echo
	// how many DNS/Static rules just came back online.
	Check wire.Check
}

// FormatHard renders a HARD alert as three sections — «что не работает /
// что я думаю / что делать». Returns plain text (no MarkdownV2 — too many
// escaping landmines in dynamic strings like endpoint hostnames).
func FormatHard(a HardArgs) string {
	mobileBadge := ""
	if a.IsMobile {
		mobileBadge = "📱"
	}
	tone := toneFor(a.CheckName, a.Check.Details, a.Neighbors)
	headline := categoryHeadline(a.CheckName, a.Check.Details, a.Neighbors)

	sections := []CardSection{{
		Title: tone.ProblemTitle,
		Lines: linesFromWriter(func(b *strings.Builder) {
			writeWhatBroke(b, a.CheckName, a.Check.Details, a.Neighbors)
		}),
	}}
	if impact := impactFor(a.CheckName, a.Check.Details); impact != "" {
		sections = append(sections, CardSection{Title: tone.ImpactTitle, Lines: []string{impact}})
	}

	if h := diagnose(a.CheckName, a.Check.Details, a.Neighbors); h != "" {
		sections = append(sections, CardSection{Title: "Что я думаю", Lines: []string{h}})
	}

	if adv := suggestAction(a.CheckName, a.Check.Details, a.Neighbors); adv != "" {
		sections = append(sections, CardSection{Title: "Что делать", Lines: []string{adv}})
	}

	meta := []string{
		KV("проверка", a.CheckName),
	}
	if a.ConsecFails > 0 {
		meta = append(meta, fmt.Sprintf("%d fails", a.ConsecFails))
	}
	if !a.HardSince.IsZero() {
		meta = append(meta, "с "+a.HardSince.In(mscLoc()).Format("02.01 15:04 МСК"))
	}
	label := fmt.Sprintf("[%s]", a.Nickname)
	if mobileBadge != "" {
		label = mobileBadge + " " + label
	}
	return Card{
		Badge:    tone.Badge,
		Label:    label,
		Summary:  headline,
		Meta:     meta,
		Sections: sections,
	}.Render(CardOpts{})
}

// FormatRecovery renders a recovery message in the same human tone as
// FormatHard — short, no AI-style sections. For tunnel_* checks the
// previously-known Details (tunnel name, linked routes count) are echoed
// so the operator sees what specifically came back, not just "что-то".
func FormatRecovery(a RecoveryArgs) string {
	d := a.RecoveredAt.Sub(a.HardSince).Round(time.Minute)
	headline := recoveryHeadline(a.CheckName, a.Check.Details)
	lines := []string{fmt.Sprintf("Простой: %s", durFmt(d))}
	if strings.HasPrefix(a.CheckName, "tunnel_") {
		lines = append(lines, linesFromWriter(func(b *strings.Builder) {
			writeTunnelRecoveryFooter(b, a.Check.Details)
		})...)
	}
	meta := []string{KV("проверка", a.CheckName)}
	if !a.RecoveredAt.IsZero() {
		meta = append(meta, KV("когда", a.RecoveredAt.In(mscLoc()).Format("02.01 15:04 МСК")))
	}
	return Card{
		Badge:    "🟢",
		Label:    fmt.Sprintf("[%s]", a.Nickname),
		Summary:  headline,
		Meta:     meta,
		Sections: []CardSection{{Title: "Итог", Lines: lines}},
	}.Render(CardOpts{})
}

// writeTunnelRecoveryFooter appends a one-line summary of what rides this
// tunnel — same Details keys as the HARD-side blast-radius line. Silent
// when both counts are zero (or the agent didn't report them).
func writeTunnelRecoveryFooter(b *strings.Builder, d map[string]any) {
	rDNS, _ := intOrZero(d, "routes_dns")
	rStatic, _ := intOrZero(d, "routes_static")
	if rDNS == 0 && rStatic == 0 {
		return
	}
	var parts []string
	if rDNS > 0 {
		parts = append(parts, fmt.Sprintf("%d DNS", rDNS))
	}
	if rStatic > 0 {
		parts = append(parts, fmt.Sprintf("%d Static", rStatic))
	}
	fmt.Fprintf(b, "\nВернулись правила: %s", strings.Join(parts, ", "))
}

// FormatRouterOffline renders a router-offline message (heartbeat watcher).
// Includes a short hint at what to check first.
func FormatRouterOffline(nickname string, since time.Duration) string {
	return Card{
		Badge:   "🔴",
		Label:   fmt.Sprintf("[%s]", nickname),
		Summary: "Роутер не на связи",
		Meta:    []string{KV("нет heartbeat", durFmt(since.Round(time.Minute)))},
		Sections: []CardSection{
			{Title: "Что не работает", Lines: []string{"Нет heartbeat'ов " + durFmt(since.Round(time.Minute))}},
			{Title: "Что я думаю", Lines: []string{"Либо роутер выключен/перезагружается, либо у него отвалился WAN, либо упал агент wg-monitor."}},
			{Title: "Что делать", Lines: []string{"Проверь питание роутера, потом WAN/4G. Если железо живо — зайди по SSH и глянь /opt/etc/init.d/S99wg-monitor status."}},
		},
	}.Render(CardOpts{})
}

// RealertArgs feeds FormatRealert. Check carries the LAST KNOWN payload from
// the agent so the reminder shows the same context as the original alert.
type RealertArgs struct {
	Nickname     string
	CheckName    string
	HardSince    time.Time
	RealertCount int
	IsMobile     bool
	Check        wire.Check
	Neighbors    []NeighborSummary
	RealertEvery time.Duration
}

// FormatRealert renders a STILL-DOWN reminder. Skips the "что я думаю" /
// "что делать" sections — the operator already saw them in the original
// HARD alert. Just rolls forward the time + repeats the broken-stuff list.
func FormatRealert(args RealertArgs) string {
	mobileBadge := ""
	if args.IsMobile {
		mobileBadge = "📱"
	}
	tone := toneFor(args.CheckName, args.Check.Details, args.Neighbors)
	headline := categoryHeadline(args.CheckName, args.Check.Details, args.Neighbors)

	var sections []CardSection
	if args.Check.Name != "" {
		sections = append(sections, CardSection{
			Title: tone.ProblemTitle,
			Lines: linesFromWriter(func(b *strings.Builder) {
				writeWhatBroke(b, args.CheckName, args.Check.Details, args.Neighbors)
			}),
		})
		if impact := impactFor(args.CheckName, args.Check.Details); impact != "" {
			sections = append(sections, CardSection{Title: tone.ImpactTitle, Lines: []string{impact}})
		}
		if adv := suggestAction(args.CheckName, args.Check.Details, args.Neighbors); adv != "" {
			sections = append(sections, CardSection{Title: "Что делать", Lines: []string{adv}})
		}
	}

	age := time.Since(args.HardSince).Round(time.Minute)
	cadence := args.RealertEvery
	if cadence <= 0 {
		cadence = 6 * time.Hour
	}
	label := fmt.Sprintf("[%s]", args.Nickname)
	if mobileBadge != "" {
		label = mobileBadge + " " + label
	}
	return Card{
		Badge:   "🔁" + tone.Badge,
		Label:   label,
		Summary: tone.RealertPrefix + headline,
		Meta: []string{
			KV("проверка", args.CheckName),
			"с " + args.HardSince.In(mscLoc()).Format("02.01 15:04 МСК"),
			durFmt(age) + " назад",
			"напомню снова через " + shortDur(cadence),
			fmt.Sprintf("#%d", args.RealertCount),
		},
		Sections: sections,
	}.Render(CardOpts{})
}

type alertTone struct {
	Badge         string
	ProblemTitle  string
	ImpactTitle   string
	RealertPrefix string
}

func toneFor(checkName string, d map[string]any, ns []NeighborSummary) alertTone {
	badge := categorySeverity(checkName, d, ns)
	if badge == "🟡" {
		return alertTone{
			Badge:         badge,
			ProblemTitle:  "На что обратить внимание",
			ImpactTitle:   "Что может пострадать",
			RealertPrefix: "Всё ещё требует внимания: ",
		}
	}
	return alertTone{
		Badge:         badge,
		ProblemTitle:  "Что не работает",
		ImpactTitle:   "Что это ломает",
		RealertPrefix: "Всё ещё: ",
	}
}

func linesFromWriter(write func(*strings.Builder)) []string {
	var b strings.Builder
	write(&b)
	var out []string
	for _, line := range strings.Split(b.String(), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "• ")
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// categorySeverity returns 🔴 / 🟡 based on how broken things actually are.
// FSM already decided this is HARD; severity is a visual hint about scale,
// not a gate on alerting.
func categorySeverity(checkName string, d map[string]any, ns []NeighborSummary) string {
	switch checkCategory(checkName) {
	case "tunnel":
		if len(ns) > 0 && !neighborsAlive(ns) {
			return "🔴"
		}
		return "🟡"
	case "dns":
		total, _ := intOrZero(d, "endpoints")
		failed, _ := intOrZero(d, "failed_count")
		rknSus, _ := intOrZero(d, "rkn_suspect")
		rknProbed, _ := intOrZero(d, "rkn_probed")
		if rknProbed > 0 && rknSus == rknProbed {
			return "🔴"
		}
		if total > 0 && failed > 0 && failed < total {
			return "🟡" // часть серверов жива — деградация, не полный отказ
		}
		if total > 0 && failed == total && len(ns) > 0 && neighborsAlive(ns) {
			return "🟡"
		}
	case "hydraroute":
		installed, _ := boolOrFalse(d, "installed")
		running, _ := boolOrFalse(d, "running")
		if installed && !running {
			return "🟡"
		}
	case "external_reach":
		total, _ := intOrZero(d, "targets_total")
		failed := mapsSlice(d, "targets_failed")
		if total >= 3 && len(failed)*2 < total {
			return "🟡"
		}
	}
	return "🔴"
}

// categoryHeadline returns the human-readable problem statement for the
// header — e.g. "DNS работает частично" / "Туннель amnezia_for_awg не на
// связи". Replaces the old "<check name> — DOWN" pattern.
func categoryHeadline(checkName string, d map[string]any, ns []NeighborSummary) string {
	switch checkCategory(checkName) {
	case "tunnel":
		tname, _ := d["tunnel_name"].(string)
		iface, _ := d["interface"].(string)
		switch {
		case tname != "" && iface != "":
			return fmt.Sprintf("Туннель %s (%s) не на связи", tname, iface)
		case tname != "":
			return fmt.Sprintf("Туннель %s не на связи", tname)
		case iface != "":
			return fmt.Sprintf("Туннель %s не на связи", iface)
		}
		return "Туннель не на связи"
	case "dns":
		total, _ := intOrZero(d, "endpoints")
		failed, _ := intOrZero(d, "failed_count")
		if total > 0 && failed > 0 && failed < total {
			return "DNS-резолвинг частично не работает"
		}
		if total > 0 && failed == total && len(ns) > 0 && neighborsAlive(ns) {
			return "DNS-резолвинг деградирует"
		}
		return "DNS-резолвинг не работает"
	case "hydraroute":
		installed, _ := boolOrFalse(d, "installed")
		running, _ := boolOrFalse(d, "running")
		switch {
		case !installed:
			return "HydraRoute не установлен"
		case !running:
			return "HydraRoute остановлен"
		}
		return "HydraRoute даёт сбой"
	case "awg_manager":
		return "awg-manager не отвечает"
	case "awgmgr_api":
		return "Реестр туннелей awg-manager недоступен"
	case "external_reach":
		total, _ := intOrZero(d, "targets_total")
		failed := mapsSlice(d, "targets_failed")
		if total > 0 && len(failed) > 0 && len(failed) < total {
			return "Часть внешних сервисов недоступна"
		}
		return "Внешние сервисы недоступны через туннель"
	}
	return "Проверка " + checkName + " падает"
}

func recoveryHeadline(checkName string, d map[string]any) string {
	switch checkCategory(checkName) {
	case "tunnel":
		if d != nil {
			tname, _ := d["tunnel_name"].(string)
			if tname != "" {
				return "Туннель " + tname + " снова на связи"
			}
		}
		return "Туннель снова на связи"
	case "dns":
		return "DNS-резолвинг восстановился"
	case "hydraroute":
		return "HydraRoute снова работает"
	case "awg_manager":
		return "awg-manager снова отвечает"
	case "awgmgr_api":
		return "Реестр туннелей снова доступен"
	case "external_reach":
		return "Внешние сервисы снова доступны"
	}
	return "Проверка " + checkName + " снова в норме"
}

// writeWhatBroke writes the "что не работает" body for a check category.
// Translates raw Go errors into human labels (timeout/refused/no-route/etc.)
// and drops internal IPs/socket pairs that operators flagged as noise.
func writeWhatBroke(b *strings.Builder, checkName string, d map[string]any, ns []NeighborSummary) {
	switch checkCategory(checkName) {
	case "tunnel":
		writeTunnelWhatBroke(b, d)
	case "dns":
		writeDNSWhatBroke(b, d, ns)
	case "hydraroute":
		writeHydraRouteWhatBroke(b, d)
	case "awg_manager":
		writeAwgManagerWhatBroke(b, d)
	case "awgmgr_api":
		writeAwgmgrAPIWhatBroke(b, d)
	case "external_reach":
		writeExternalReachWhatBroke(b, d)
	default:
		writeGenericWhatBroke(b, d)
	}
}

func writeTunnelWhatBroke(b *strings.Builder, d map[string]any) {
	if ep := strOrEmpty(d, "endpoint"); ep != "" {
		if isp := strOrEmpty(d, "isp_interface"); isp != "" {
			fmt.Fprintf(b, "  Сервер туннеля: %s (провайдерский выход: %s)\n", ep, isp)
		} else {
			fmt.Fprintf(b, "  Сервер туннеля: %s\n", ep)
		}
	}
	if age, ok := intOrZero(d, "handshake_age_sec"); ok {
		fmt.Fprintf(b, "  Последний обмен ключами: %s назад\n", humanAgeSec(age))
	} else {
		b.WriteString("  Туннель ни разу не установил связь\n")
	}
	if pc := strOrEmpty(d, "ping_check_status"); pc != "" {
		fc, _ := intOrZero(d, "ping_check_fail_count")
		ft, _ := intOrZero(d, "ping_check_fail_threshold")
		var extras []string
		if rc, _ := intOrZero(d, "ping_check_restart_count"); rc > 0 {
			extras = append(extras, fmt.Sprintf("авто-рестартов: %d", rc))
		}
		if lat, ok := intOrZero(d, "ping_check_last_latency_ms"); ok && lat > 0 {
			extras = append(extras, fmt.Sprintf("последний ping %d мс", lat))
		}
		fmt.Fprintf(b, "  Проверка связи: %s — неудачных попыток %d из %d", humanPingStatus(pc), fc, ft)
		if len(extras) > 0 {
			fmt.Fprintf(b, " (%s)", strings.Join(extras, " · "))
		}
		b.WriteString("\n")
	}
	if conflict, ok := boolOrFalse(d, "address_conflict"); ok && conflict {
		b.WriteString("  ⚠ конфликт адресов на интерфейсе\n")
	}
	writeTunnelLinkedRoutes(b, d)
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
		fmt.Fprintf(b, "  Параметры: %s\n", strings.Join(parts, " · "))
	}
}

// writeTunnelLinkedRoutes renders the "Связано правил:" line when the agent
// reported how many DNS / Static rules ride this tunnel. Silent when both
// counts are zero or the agent is pre-rc6 (no fields). Helps the operator
// see the blast radius — especially when a default-route tunnel with many
// fall-through HR-Neo rules dies and DNS resolution stalls for everything.
func writeTunnelLinkedRoutes(b *strings.Builder, d map[string]any) {
	rDNS, _ := intOrZero(d, "routes_dns")
	rHR, _ := intOrZero(d, "routes_dns_hr")
	rStatic, _ := intOrZero(d, "routes_static")
	if rDNS == 0 && rStatic == 0 {
		return
	}
	var parts []string
	if rDNS > 0 {
		switch {
		case rHR > 0 && rHR == rDNS:
			parts = append(parts, fmt.Sprintf("%d DNS (HR-Neo)", rDNS))
		case rHR > 0:
			parts = append(parts, fmt.Sprintf("%d DNS (HR-Neo: %d)", rDNS, rHR))
		default:
			parts = append(parts, fmt.Sprintf("%d DNS", rDNS))
		}
	}
	if rStatic > 0 {
		parts = append(parts, fmt.Sprintf("%d Static", rStatic))
	}
	fmt.Fprintf(b, "  Связано правил: %s\n", strings.Join(parts, ", "))
}

func writeDNSWhatBroke(b *strings.Builder, d map[string]any, ns []NeighborSummary) {
	total, _ := intOrZero(d, "endpoints")
	failed, _ := intOrZero(d, "failed_count")
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")

	if total > 0 {
		switch {
		case failed == 0 && rknProbed > 0 && rknSus == rknProbed:
			fmt.Fprintf(b, "  Серверы отвечают (%d), но похоже трафик подменяется\n", total)
		case failed == 0:
			fmt.Fprintf(b, "  Серверы отвечают, но результат проверки всё равно плохой (всего %d)\n", total)
		case failed == total:
			fmt.Fprintf(b, "  Не отвечает ни один из %s\n", pluralServers(total))
		default:
			fmt.Fprintf(b, "  Не отвечают %d из %s\n", failed, pluralServers(total))
		}
	}
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
			if tp != "" {
				label = tp + " " + label
			}
			if ndms != "" {
				label += " через " + humanTunnelLabelByNDMS(ndms, ns)
			}
			fmt.Fprintf(b, "    • %s — %s\n", label, humaniseNetErr(errStr))
		}
	}
	if rknProbed > 0 {
		switch {
		case rknSus == 0:
			fmt.Fprintf(b, "  RKN-блокировок не видно (проверено %d)\n", rknProbed)
		case rknSus == rknProbed:
			b.WriteString("  RKN-блокировка похоже на ВСЕХ серверах\n")
		default:
			fmt.Fprintf(b, "  RKN-блокировка похоже на %d из %d\n", rknSus, rknProbed)
		}
	}
}

func writeHydraRouteWhatBroke(b *strings.Builder, d map[string]any) {
	installed, _ := boolOrFalse(d, "installed")
	running, _ := boolOrFalse(d, "running")
	switch {
	case installed && running:
		b.WriteString("  HydraRoute установлен и запущен, но проверка всё равно видит сбой\n")
	case installed:
		b.WriteString("  HydraRoute установлен, но сервис остановлен\n")
	default:
		b.WriteString("  HydraRoute не установлен\n")
	}
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "  Сообщение: %s\n", errStr)
	}
}

func writeAwgManagerWhatBroke(b *strings.Builder, d map[string]any) {
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
		fmt.Fprintf(b, "  awg-manager · %s\n", strings.Join(parts, " · "))
	}
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "  Сообщение: %s\n", errStr)
	}
}

func writeAwgmgrAPIWhatBroke(b *strings.Builder, d map[string]any) {
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "  Ошибка API: %s\n", trimBodyDump(errStr))
	}
	if cnt, ok := intOrZero(d, "tunnel_count"); ok && cnt > 0 {
		fmt.Fprintf(b, "  Туннелей видно: %d\n", cnt)
	}
}

func writeExternalReachWhatBroke(b *strings.Builder, d map[string]any) {
	failed := mapsSlice(d, "targets_failed")
	okList := strSlice(d, "targets_ok")
	degraded := mapsSlice(d, "targets_degraded")
	total, _ := intOrZero(d, "targets_total")
	if total > 0 {
		fmt.Fprintf(b, "  Не отвечают %d из %d целей\n", len(failed), total)
	}
	for _, t := range failed {
		name, _ := t["name"].(string)
		errStr, _ := t["err"].(string)
		fmt.Fprintf(b, "    • %s — %s\n", name, humaniseNetErr(errStr))
	}
	if len(okList) > 0 {
		fmt.Fprintf(b, "  Работают: %s\n", strings.Join(okList, ", "))
	}
	// Reachable-but-refused targets: the network path works, the service just
	// answered 4xx (typically bot-detection 403/429). Surface them with the code
	// so the operator isn't misled into thinking the tunnel is the problem.
	if len(degraded) > 0 {
		var parts []string
		for _, t := range degraded {
			name, _ := t["name"].(string)
			status, _ := intOrZero(t, "status")
			parts = append(parts, fmt.Sprintf("%s (%d)", name, status))
		}
		fmt.Fprintf(b, "  Доступны, но вернули отказ: %s — это не сбой связи, сервис отверг бота\n", strings.Join(parts, ", "))
	}
	if iface, _ := d["via_interface"].(string); iface != "" {
		fmt.Fprintf(b, "  Через интерфейс: %s\n", iface)
	}
}

func writeGenericWhatBroke(b *strings.Builder, d map[string]any) {
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "  %s\n", errStr)
	} else {
		b.WriteString("  Проверка сказала, что есть сбой, но агент не прислал подробностей.\n")
	}
}

func humanTunnelLabelByNDMS(ndms string, ns []NeighborSummary) string {
	if ndms == "" {
		return ""
	}
	for _, n := range ns {
		if n.NDMSName != ndms && n.Interface != ndms {
			continue
		}
		return humanTunnelLabel(n)
	}
	return ndms
}

func humanTunnelLabel(n NeighborSummary) string {
	name := strings.TrimSpace(n.TunnelName)
	ndms := strings.TrimSpace(n.NDMSName)
	if ndms == "" {
		ndms = strings.TrimSpace(n.Interface)
	}
	if name == "" {
		return ndms
	}
	var refs []string
	if ndms != "" && ndms != name {
		refs = append(refs, ndms)
	}
	if n.Interface != "" && n.Interface != name && n.Interface != ndms {
		refs = append(refs, n.Interface)
	}
	if len(refs) == 0 {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, strings.Join(refs, " / "))
}

func humanTunnelNameByNDMS(ndms string, ns []NeighborSummary) string {
	if ndms == "" {
		return ""
	}
	for _, n := range ns {
		if n.NDMSName != ndms && n.Interface != ndms {
			continue
		}
		if strings.TrimSpace(n.TunnelName) != "" {
			return strings.TrimSpace(n.TunnelName)
		}
		return humanTunnelLabel(n)
	}
	return ndms
}

func impactFor(checkName string, d map[string]any) string {
	switch checkCategory(checkName) {
	case "dns":
		return "Домены могут не открываться или уходить не туда: сайты, приложения и правила HR-Neo зависят от DNS."
	case "tunnel":
		var parts []string
		if rDNS, _ := intOrZero(d, "routes_dns"); rDNS > 0 {
			parts = append(parts, fmt.Sprintf("%d DNS-правил", rDNS))
		}
		if rStatic, _ := intOrZero(d, "routes_static"); rStatic > 0 {
			parts = append(parts, fmt.Sprintf("%d static-маршрутов", rStatic))
		}
		if len(parts) > 0 {
			return "Через этот туннель завязаны " + strings.Join(parts, " и ") + "; они могут не работать до восстановления туннеля."
		}
		return "Трафик, который должен идти через этот туннель, может не доходить до нужных сервисов."
	case "hydraroute":
		return "DNS/HR-Neo правила могут перестать направлять домены в нужные туннели; часть сайтов пойдёт обычным маршрутом или не откроется."
	case "awg_manager", "awgmgr_api":
		return "бот не может управлять туннелями через awg-manager: диагностика, маршруты и кнопки ремонта могут не сработать."
	case "external_reach":
		return "Сервисы снаружи не открываются через выбранный туннель; проблема либо в самом туннеле, либо в маршрутизации через него."
	}
	return ""
}

func humanPingStatus(s string) string {
	switch s {
	case "alive", "ok", "running":
		return "живая"
	case "dead", "fail", "failed":
		return "падает"
	case "disabled":
		return "выключена"
	}
	return s
}

// diagnose returns a one-paragraph hypothesis about the root cause —
// «Что я думаю». Looks at neighbors, RKN, all-via-one-interface patterns.
// Empty string if there's nothing useful to add (rare).
func diagnose(checkName string, d map[string]any, ns []NeighborSummary) string {
	switch checkCategory(checkName) {
	case "dns":
		return diagnoseDNS(d, ns)
	case "tunnel":
		return diagnoseTunnel(d, ns)
	case "hydraroute":
		return diagnoseHydraRoute(d)
	case "awg_manager":
		return "awg-manager не отвечает на запрос статуса. Обычно это значит, что сервис упал или сильно загружен — версии и прошивка не считываются."
	case "awgmgr_api":
		return "Список туннелей читается через API awg-manager. Если он недоступен — либо awg-manager упал, либо изменился порт или доступ к API."
	case "external_reach":
		return diagnoseExternalReach(d, ns)
	}
	return ""
}

func diagnoseDNS(d map[string]any, ns []NeighborSummary) string {
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")
	if rknProbed > 0 && rknSus == rknProbed {
		return "На всех проверенных серверах ответы похожи на RKN-блокировку. DNS-серверы живы, но трафик подменяется по дороге — нужен DoH или другой апстрим."
	}

	failed := mapsSlice(d, "endpoints_detail")
	failedIfaces := map[string]int{}
	for _, ep := range failed {
		reachable, _ := ep["reachable"].(bool)
		if reachable {
			continue
		}
		ndms, _ := ep["ndms_name"].(string)
		if ndms != "" {
			failedIfaces[ndms]++
		}
	}
	total, _ := intOrZero(d, "endpoints")
	failedCount, _ := intOrZero(d, "failed_count")
	if len(failedIfaces) == 1 && failedCount > 0 {
		var ndms string
		for k := range failedIfaces {
			ndms = k
		}
		label := humanTunnelLabelByNDMS(ndms, ns)
		name := humanTunnelNameByNDMS(ndms, ns)
		prefix := "Все упавшие DNS-серверы"
		if failedCount == 2 {
			prefix = "Оба упавших DNS-сервера"
		}
		if neighborsAlive(ns) && len(ns) > 0 {
			return fmt.Sprintf("%s идут через %s. Остальные туннели выглядят живыми, значит это не общий WAN. Скорее всего деградировал именно %s: DNS просто первым это заметил.", prefix, label, name)
		}
		if !neighborsAlive(ns) && len(ns) > 0 {
			return fmt.Sprintf("%s идут через %s, и соседние туннели тоже не на связи. Похоже на проблему уровнем выше: WAN, провайдер или сам роутер.", prefix, label)
		}
		return fmt.Sprintf("%s идут через %s. Похоже на сбой этого маршрута или туннеля, а не самого DNS.", prefix, label)
	}

	// Если все упавшие endpoint'ы прибиты к одному ndms_name (= один туннель/интерфейс),
	// а соседи живы — диагноз не про DNS, а про этот туннель.
	if len(failedIfaces) == 1 && failedCount > 0 {
		var iface string
		for k := range failedIfaces {
			iface = k
		}
		if neighborsAlive(ns) && len(ns) > 0 {
			return fmt.Sprintf(
				"Все упавшие DNS-серверы идут через один интерфейс — %s. Соседние туннели живы, так что это не WAN. Скорее всего деградировал именно %s — DNS просто первый это заметил.",
				iface, iface)
		}
		if !neighborsAlive(ns) && len(ns) > 0 {
			return fmt.Sprintf(
				"Упавшие DNS-серверы идут через %s, и соседние туннели тоже не на связи. Похоже на проблему уровнем выше — WAN или провайдер.",
				iface)
		}
		return fmt.Sprintf(
			"Все упавшие DNS-серверы идут через один интерфейс — %s. Похоже на сбой именно этого туннеля, не самого DNS.",
			iface)
	}

	if failedCount > 0 && failedCount < total {
		return "Лежит часть серверов, остальные отвечают. Резолв в целом работает — это деградация, не полный отказ."
	}
	if failedCount == total && total > 0 {
		if len(ns) > 0 && neighborsAlive(ns) {
			return "DNS endpoint'ы не ответили, но соседние туннели живы. Это похоже на локальную проблему DNS-апстрима, выключенного/старого правила или привязки маршрута, а не на общий обрыв WAN."
		}
		return "Не отвечает ни один сервер. Либо у роутера нет связи наружу, либо DNS-апстримы разом легли (что бывает редко)."
	}
	return ""
}

func diagnoseTunnel(d map[string]any, ns []NeighborSummary) string {
	age, hasAge := intOrZero(d, "handshake_age_sec")
	pc := strOrEmpty(d, "ping_check_status")
	conflict, hasConflict := boolOrFalse(d, "address_conflict")

	var parts []string
	if hasConflict && conflict {
		parts = append(parts, "На интерфейсе конфликт адресов — это почти всегда означает что туннель пытается подняться с тем же адресом что и другой интерфейс.")
	}
	switch {
	case !hasAge:
		parts = append(parts, "Обмена ключами не было ни разу с момента старта — туннель так и не поднялся. Чаще всего это неправильный адрес сервера, AWG-параметры или закрытый порт у провайдера.")
	case age > 600:
		parts = append(parts, fmt.Sprintf("Обмена ключами нет уже %s — туннель явно лежит, не просто моргнул.", humanAgeSec(age)))
	case age > 180:
		parts = append(parts, "Обмен ключами устарел, но не катастрофически. Возможно провайдер режет UDP, либо сервер туннеля временно недоступен.")
	}
	if pc == "dead" {
		parts = append(parts, "Проверка связи показывает сбой — пакеты не доходят даже после авто-рестартов.")
	}
	if len(parts) == 0 && len(ns) > 0 && neighborsAlive(ns) {
		parts = append(parts, "Соседние туннели живы, так что WAN/роутер целы. Проблема локальная — этот сервер туннеля или его настройки.")
	}
	if len(parts) == 0 && len(ns) > 0 && !neighborsAlive(ns) {
		parts = append(parts, "Соседние туннели тоже не на связи. Похоже на проблему уровнем выше — WAN, провайдер или сам роутер.")
	}
	return strings.Join(parts, " ")
}

func diagnoseHydraRoute(d map[string]any) string {
	installed, _ := boolOrFalse(d, "installed")
	running, _ := boolOrFalse(d, "running")
	switch {
	case !installed:
		return "HydraRoute не установлен — пакет hrneo либо отсутствует, либо удалён. Без него selective-роуты не работают."
	case !running:
		return "HydraRoute установлен, но демон не запущен. Видимо он упал или был остановлен вручную."
	}
	return "HydraRoute запущен, но проверка возвращает ошибку. Скорее всего сбой в конфиге — какое-то правило ссылается на несуществующий туннель."
}

func diagnoseExternalReach(d map[string]any, ns []NeighborSummary) string {
	iface, _ := d["via_interface"].(string)
	failed := mapsSlice(d, "targets_failed")
	total, _ := intOrZero(d, "targets_total")
	switch {
	case total > 0 && len(failed) == total && iface != "":
		return fmt.Sprintf("Через %s не достижимо ни одной цели — туннель не пропускает трафик наружу. Это либо сам туннель, либо его маршрутизация.", iface)
	case len(failed) > 0 && len(failed) < total:
		return "Часть целей живы, часть нет — это похоже на блокировку конкретных сервисов, а не общий сбой связи."
	}
	if len(ns) > 0 && !neighborsAlive(ns) {
		return "Соседние туннели тоже не на связи — похоже WAN или провайдер."
	}
	return ""
}

// suggestAction returns a one-paragraph next-step advice — «Что делать».
// References the inline buttons attached to this alert and other panels
// (Maintenance, smart-reply) where appropriate.
func suggestAction(checkName string, d map[string]any, ns []NeighborSummary) string {
	switch checkCategory(checkName) {
	case "dns":
		return adviseDNS(d, ns)
	case "tunnel":
		return adviseTunnel(d, ns)
	case "hydraroute":
		return adviseHydraRoute(d)
	case "awg_manager", "awgmgr_api":
		return "Открой 🛠 Обслуживание и нажми «Перезапустить awg-manager». Если не помогло — глянь логи: ssh root@router 'logread | grep awg-manager'."
	case "external_reach":
		return adviseExternalReach(d, ns)
	}
	return "Открой 📊 Что происходит? — там общая сводка по роутеру."
}

func adviseDNS(d map[string]any, ns []NeighborSummary) string {
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")
	if rknProbed > 0 && rknSus == rknProbed {
		return "Поменяй DNS-апстримы на DoH (например cloudflare-dns.com), либо проверь что DNS-запросы реально уходят через туннель — открой 🛣 Маршруты."
	}

	failed := mapsSlice(d, "endpoints_detail")
	failedIfaces := map[string]int{}
	for _, ep := range failed {
		reachable, _ := ep["reachable"].(bool)
		if reachable {
			continue
		}
		ndms, _ := ep["ndms_name"].(string)
		if ndms != "" {
			failedIfaces[ndms]++
		}
	}
	if len(failedIfaces) == 1 {
		var iface string
		for k := range failedIfaces {
			iface = k
		}
		label := humanTunnelLabelByNDMS(iface, ns)
		name := humanTunnelNameByNDMS(iface, ns)
		if neighborsAlive(ns) {
			return fmt.Sprintf("Открой 🎛 Туннели и проверь %s. Если он есть в списке, начни с перезапуска или диагностики именно этого туннеля.", label)
		}
		return fmt.Sprintf("Сначала проверь WAN: 🌍 Через туннель? и 🇷🇺 Напрямую?. Если связь есть только напрямую, начни с %s.", name)
	}
	return "Подожди минуту — иногда апстримы временно отвечают таймаутом. Если не вернётся — открой 📊 Что происходит? и глянь общую картину по роутеру."
}

func adviseTunnel(d map[string]any, _ []NeighborSummary) string {
	age, hasAge := intOrZero(d, "handshake_age_sec")
	conflict, hasConflict := boolOrFalse(d, "address_conflict")
	if hasConflict && conflict {
		return "Открой 🎛 Туннели, найди этот туннель и проверь его адрес. Скорее всего он совпадает с другим интерфейсом — поменяй на свободную /24."
	}
	if !hasAge {
		return "Тыкни «📊 Диагностика» — она покажет сервер туннеля, AWG-параметры и попытается поднять связь. Если сервер правильный, но обмен ключами не идёт — провайдер может резать UDP."
	}
	var base string
	if age > 600 {
		base = "Жми «🔁 Перезапуск туннеля» — обычно помогает. Если нет — «📊 Диагностика», там увидишь конкретный сбой."
	} else {
		base = "Подожди ещё минуту — handshake мог моргнуть. Если не вернётся за 2-3 минуты, тыкни «🔁 Перезапуск туннеля»."
	}
	// pingCheck выключен → бот судит о связи только по возрасту handshake, а он
	// у idle-туннеля устаревает сам по себе. Подсказываем включить активную
	// проверку, чтобы отличать простой от настоящего обрыва.
	if pingCheckIsDisabled(d) {
		base += " Заодно включи pingCheck (🛡 PingCheck в панели) — сейчас он выключен, и бот видит только возраст handshake; с pingCheck он активно проверяет связь и не путает простой с обрывом."
	}
	return base
}

// pingCheckIsDisabled reports whether the tunnel's awg-manager pingCheck watchdog
// is turned off. When it is, handshake age is the only liveness signal — which
// goes stale on idle tunnels even when they're healthy.
func pingCheckIsDisabled(d map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(strOrEmpty(d, "ping_check_status")), "disabled")
}

func adviseHydraRoute(d map[string]any) string {
	installed, _ := boolOrFalse(d, "installed")
	running, _ := boolOrFalse(d, "running")
	switch {
	case !installed:
		return "Установи hrneo через opkg, либо открой 🛠 Обслуживание → «Установить компоненты»."
	case !running:
		return "Открой 🛠 Обслуживание и нажми «Перезапустить hrneo»."
	}
	return "Проверь правила HR-Neo (🛣 Маршруты) — возможно одно из них ссылается на удалённый туннель."
}

func adviseExternalReach(d map[string]any, ns []NeighborSummary) string {
	iface, _ := d["via_interface"].(string)
	failed := mapsSlice(d, "targets_failed")
	total, _ := intOrZero(d, "targets_total")
	if total > 0 && len(failed) == total && iface != "" {
		return fmt.Sprintf("Туннель %s не пропускает наружу. Нажми «🔁 Перезапуск туннеля» в его сообщении или открой 🎛 Туннели и перезапусти его оттуда.", iface)
	}
	if len(ns) > 0 && !neighborsAlive(ns) {
		return "Сначала проверь WAN: 🇷🇺 Напрямую? — если и без туннеля наружу не выходит, проблема у провайдера."
	}
	return "Открой 🎛 Туннели и проверь, через какой интерфейс уходит трафик до этих целей."
}

// neighborsAlive returns true when at least one neighbor is in alive/ok status.
// Used to distinguish "местная проблема туннеля" from "WAN-сбой".
func neighborsAlive(ns []NeighborSummary) bool {
	for _, n := range ns {
		if isLiveStatus(n.Status) {
			return true
		}
	}
	return false
}

// humaniseNetErr collapses a raw Go network error into a short human label.
// Operators don't need «read: read udp 100.87.154.163:58948->100.64.0.1:53:
// i/o timeout» — they need «timeout».
func humaniseNetErr(s string) string {
	if s == "" {
		return "ошибка"
	}
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "i/o timeout"), strings.Contains(low, "timeout"):
		return "таймаут"
	case strings.Contains(low, "connection refused"):
		return "отказ соединения"
	case strings.Contains(low, "connection reset"):
		return "соединение сброшено"
	case strings.Contains(low, "broken pipe"), strings.Contains(low, "epipe"):
		return "канал порван"
	case strings.Contains(low, "no route to host"):
		return "нет маршрута"
	case strings.Contains(low, "network is unreachable"):
		return "сеть недоступна"
	case strings.Contains(low, "no such host"), strings.Contains(low, "nxdomain"):
		return "имя не резолвится"
	case strings.Contains(low, "tls"):
		return "TLS-ошибка"
	case strings.Contains(low, "context deadline"):
		return "таймаут"
	case strings.Contains(low, "context canceled"):
		return "отменено"
	}
	const max = 60
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// shortDur renders a duration as "6h" / "30m" / "1h30m" — no fractional units.
func shortDur(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
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

// pluralServers returns "1 сервер" / "2 сервера" / "5 серверов" — Russian
// plural form so headlines read naturally instead of "5 серверов" for one.
// Russian one/few/many: 1, 21, 31… → "сервер"; 2-4, 22-24… → "сервера";
// 5-20, 25-30… → "серверов"; 11-14 → "серверов" (special case).
func pluralServers(n int) string {
	mod10 := n % 10
	mod100 := n % 100
	switch {
	case mod100 >= 11 && mod100 <= 14:
		return fmt.Sprintf("%d серверов", n)
	case mod10 == 1:
		return fmt.Sprintf("%d сервер", n)
	case mod10 >= 2 && mod10 <= 4:
		return fmt.Sprintf("%d сервера", n)
	}
	return fmt.Sprintf("%d серверов", n)
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

// mscLoc возвращает один и тот же *time.Location, инициализированный лениво
// под sync.Once. До этого LoadLocation парсил tzdata на каждое форматирование
// HARD/STILL-DOWN/Smart-reply (~ms на VPS), сейчас один раз за процесс.
var (
	mscLocOnce sync.Once
	mscLocVal  *time.Location
)

func mscLoc() *time.Location {
	mscLocOnce.Do(func() {
		loc, err := time.LoadLocation("Europe/Moscow")
		if err != nil {
			mscLocVal = time.FixedZone("МСК", 3*3600)
			return
		}
		mscLocVal = loc
	})
	return mscLocVal
}
