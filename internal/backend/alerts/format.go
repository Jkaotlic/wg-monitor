package alerts

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// HardArgs feeds FormatHard. Check carries the raw probe payload from the
// agent (post-pivot: rich Details from awg-manager). IsMobile gates the
// 📱 badge in the title.
//
// Neighbors is optional context — short summaries of OTHER tunnels of the
// same user. Used both as a source of correlation hints in the diagnose
// helper and as a "других VPN-туннелей" hint in the advice line.
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
	if impact := impactFor(a.CheckName, a.Check.Details, a.Neighbors); impact != "" {
		sections = append(sections, CardSection{Title: tone.ImpactTitle, Lines: []string{impact}})
	}

	if h := diagnose(a.CheckName, a.Check.Details, a.Neighbors); h != "" {
		sections = append(sections, CardSection{Title: "Что я думаю", Lines: []string{h}})
	}

	if adv := suggestAction(a.CheckName, a.Check.Details, a.Neighbors); adv != "" {
		sections = append(sections, CardSection{Title: "Что делать", Lines: []string{adv}})
	}

	meta := []string{
		KV("проверка", checkHumanName(a.CheckName)),
	}
	// «Без ответа» -- неправда для запасных, не снявшихся рядом со своим: свой
	// DNS-сервер при этом может и отвечать.
	if a.ConsecFails > 0 && !(checkCategory(a.CheckName) == "resolver_guard" && resolverGuardForeignLeftover(a.Check.Details)) {
		meta = append(meta, fmt.Sprintf("проверок подряд без ответа: %d", a.ConsecFails))
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
	downtime := fmt.Sprintf("Простой: %s", durFmt(d))
	var lines []string
	if checkCategory(a.CheckName) == "resolver_guard" {
		// Простоя могло и не быть: на запасных сайты открывались, а владельцу
		// так и сказали. И свой DNS-сервер мог отвечать всё это время -- когда
		// не снялись запасные (foreign_leftover); причину здесь не знаем.
		downtime = fmt.Sprintf("Неполадка длилась: %s", durFmt(d))
		if note := resolverGuardRecoveryNote(a.Check.Details); len(note) > 0 {
			// Сторож перестал следить или ещё не прочитал роутер: кончилась ли
			// неполадка, бот не знает -- знает только, сколько о ней сообщали.
			lines = note
			downtime = fmt.Sprintf("Сторож DNS сообщал о неполадке: %s", durFmt(d))
		}
	}
	lines = append(lines, downtime)
	if strings.HasPrefix(a.CheckName, "tunnel_") {
		lines = append(lines, linesFromWriter(func(b *strings.Builder) {
			writeTunnelRecoveryFooter(b, a.Check.Details)
		})...)
	}
	meta := []string{KV("проверка", checkHumanName(a.CheckName))}
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
		parts = append(parts, fmt.Sprintf("%d по именам сайтов", rDNS))
	}
	if rStatic > 0 {
		parts = append(parts, fmt.Sprintf("%d по адресам", rStatic))
	}
	fmt.Fprintf(b, "\nСнова работают правила: %s", strings.Join(parts, ", "))
}

// Слова о молчащем роутере. Первая тревога (FormatRouterOffline), напоминание
// и восстановление по agent_heartbeat говорят об одном событии и обязаны
// говорить одинаково: раньше напоминание приходило как «Проверка
// agent_heartbeat падает».
const (
	routerOfflineHeadline  = "Роутер не на связи"
	routerOfflineRecovered = "Роутер снова на связи"
	routerOfflineAdvice    = "Проверьте, включён ли роутер и горят ли на нём лампочки. Если включён — проверьте, есть ли интернет у провайдера. Когда роутер вернётся, бот напишет сам."
)

// FormatRouterOffline renders a router-offline message (heartbeat watcher).
// Includes a short hint at what to check first.
func FormatRouterOffline(nickname string, since time.Duration) string {
	return Card{
		Badge:   "🔴",
		Label:   fmt.Sprintf("[%s]", nickname),
		Summary: routerOfflineHeadline,
		Meta:    []string{KV("молчит", durFmt(since.Round(time.Minute)))},
		Sections: []CardSection{
			{Title: "Что не работает", Lines: []string{"Роутер молчит " + durFmt(since.Round(time.Minute)) + " — за это время он не прислал ни одного отчёта."}},
			{Title: "Что я думаю", Lines: []string{"Либо роутер выключен или перезагружается, либо у него пропал интернет."}},
			{Title: "Что делать", Lines: []string{routerOfflineAdvice}},
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
		if impact := impactFor(args.CheckName, args.Check.Details, args.Neighbors); impact != "" {
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
			KV("проверка", checkHumanName(args.CheckName)),
			"с " + args.HardSince.In(mscLoc()).Format("02.01 15:04 МСК"),
			durFmt(age) + " назад",
			"напомню снова через " + durFmt(cadence),
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
	case "resolver_guard":
		// Запасные DNS-серверы работают -- сайты открываются, это «обратить
		// внимание», а не пожар. Запасных нет -- тревога. Запасные не снялись
		// рядом со своим -- сайты тоже открываются.
		if resolverGuardOnFallback(d) || resolverGuardForeignLeftover(d) {
			return "🟡"
		}
	}
	return "🔴"
}

// hydraRouteExplained -- первое упоминание HydraRoute в тексте для владельца.
// Само имя ему ничего не говорит; дальше по тексту -- просто «HydraRoute».
const hydraRouteExplained = "HydraRoute (движок умной раздельной маршрутизации)"

// categoryHeadline returns the human-readable problem statement for the
// header — e.g. "Часть сайтов может не открываться по имени" /
// "VPN-туннель «Франкфурт» не отвечает". Replaces the old
// "<check name> — DOWN" pattern.
func categoryHeadline(checkName string, d map[string]any, ns []NeighborSummary) string {
	switch checkCategory(checkName) {
	case "tunnel":
		name := quotedTunnelName(d)
		// Упавший VPN-туннель -- ещё не потерянный обход. Пока жив соседний,
		// трафик идёт через него, и «не на связи» тревожным тоном гонит
		// человека чинить то, что у него работает. Приложение в этот же момент
		// говорит, что работает запасной VPN-туннель, -- два голоса одной
		// системы обязаны говорить одно и то же.
		if spare, ok := liveSpare(ns); ok {
			return fmt.Sprintf("VPN-туннель %s упал, обход идёт через «%s»", name, spareHumanName(spare))
		}
		return fmt.Sprintf("VPN-туннель %s не отвечает", name)
	case "dns":
		total, _ := intOrZero(d, "endpoints")
		failed, _ := intOrZero(d, "failed_count")
		if total > 0 && failed > 0 && failed < total {
			return "Часть сайтов может не открываться по имени"
		}
		if total > 0 && failed == total && len(ns) > 0 && neighborsAlive(ns) {
			return "Роутер стал хуже находить сайты по имени"
		}
		return "Роутер не находит сайты по имени"
	case "hydraroute":
		installed, _ := boolOrFalse(d, "installed")
		running, _ := boolOrFalse(d, "running")
		switch {
		case !installed:
			return hydraRouteExplained + " не установлен"
		case !running:
			return hydraRouteExplained + " остановлен"
		}
		return hydraRouteExplained + " даёт сбой"
	case "awg_manager":
		return "Панель управления роутера не отвечает"
	case "awgmgr_api":
		return "Бот не видит список VPN-туннелей роутера"
	case "external_reach":
		total, _ := intOrZero(d, "targets_total")
		failed := mapsSlice(d, "targets_failed")
		if total > 0 && len(failed) > 0 && len(failed) < total {
			return "Часть внешних сервисов недоступна"
		}
		return "Сервисы не открываются через обход"
	case "resolver_guard":
		return resolverGuardHeadline(d)
	}
	if checkName == "agent_heartbeat" {
		return routerOfflineHeadline
	}
	return "Проверка " + checkName + " падает"
}

func recoveryHeadline(checkName string, d map[string]any) string {
	switch checkCategory(checkName) {
	case "tunnel":
		if d != nil {
			if tname, _ := d["tunnel_name"].(string); strings.TrimSpace(tname) != "" {
				return "VPN-туннель «" + strings.TrimSpace(tname) + "» снова работает"
			}
		}
		return "VPN-туннель снова работает"
	case "dns":
		return "Роутер снова находит сайты по имени"
	case "hydraroute":
		return hydraRouteExplained + " снова работает"
	case "awg_manager":
		return "Панель управления роутера снова отвечает"
	case "awgmgr_api":
		return "Бот снова видит список VPN-туннелей"
	case "external_reach":
		return "Внешние сервисы снова доступны"
	case "resolver_guard":
		// При восстановлении у диспетчера есть только свежий ok-отчёт, причина
		// поломки ему неизвестна. Роутер мог уходить на запасные (fallback),
		// оставаться на молчащем своём (no_live_fallback) или держать запасные
		// рядом со своим (foreign_leftover) -- верно во всех трёх случаях
		// только «снова через свой». Но ok бывает и без своего сервера: сторож
		// перестал следить (idle) или ещё не прочитал роутер (ready:false) --
		// тогда и заголовок другой.
		switch {
		case resolverGuardIdle(d):
			return resolverGuardIdleHeadline
		case resolverGuardUnread(d):
			return resolverGuardUnreadHeadline
		}
		return resolverGuardRecoveredHeadline
	}
	if checkName == "agent_heartbeat" {
		return routerOfflineRecovered
	}
	return "Проверка " + checkName + " снова в норме"
}

// writeWhatBroke writes the "что не работает" body for a check category.
// Translates raw Go errors into human labels (timeout/refused/no-route/etc.)
// and drops internal IPs/socket pairs that operators flagged as noise.
func writeWhatBroke(b *strings.Builder, checkName string, d map[string]any, ns []NeighborSummary) {
	// Молчащий роутер подробностей не присылает по определению: общая
	// строка «агент не прислал подробностей» тут неправда, он не прислал
	// ничего.
	if checkName == "agent_heartbeat" {
		b.WriteString("  Роутер перестал присылать отчёты — что с ним сейчас, бот не знает.\n")
		return
	}
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
	case "resolver_guard":
		writeResolverGuardWhatBroke(b, d)
	default:
		writeGenericWhatBroke(b, d)
	}
}

func writeTunnelWhatBroke(b *strings.Builder, d map[string]any) {
	if ep := strOrEmpty(d, "endpoint"); ep != "" {
		if isp := strOrEmpty(d, "isp_interface"); isp != "" {
			fmt.Fprintf(b, "  Сервер VPN-туннеля: %s (выход провайдера: %s)\n", ep, isp)
		} else {
			fmt.Fprintf(b, "  Сервер VPN-туннеля: %s\n", ep)
		}
	}
	if age, ok := intOrZero(d, "handshake_age_sec"); ok {
		fmt.Fprintf(b, "  Последний обмен ключами: %s назад\n", humanAgeSec(age))
	} else {
		b.WriteString("  VPN-туннель ни разу не установил связь\n")
	}
	if pc := strOrEmpty(d, "ping_check_status"); pc != "" {
		fc, _ := intOrZero(d, "ping_check_fail_count")
		ft, _ := intOrZero(d, "ping_check_fail_threshold")
		var extras []string
		if rc, _ := intOrZero(d, "ping_check_restart_count"); rc > 0 {
			extras = append(extras, fmt.Sprintf("автоперезапусков: %d", rc))
		}
		if lat, ok := intOrZero(d, "ping_check_last_latency_ms"); ok && lat > 0 {
			extras = append(extras, fmt.Sprintf("последний ответ за %d мс", lat))
		}
		// Выключенная проверка ничего не пробовала: «0 из 0» -- счётчик, а не
		// ответ. Попытки называются, только когда есть порог, до которого
		// проверка их считает.
		if pc == "disabled" || ft == 0 {
			fmt.Fprintf(b, "  Проверка связи: %s", humanPingStatus(pc))
		} else {
			fmt.Fprintf(b, "  Проверка связи: %s — неудачных попыток %d из %d", humanPingStatus(pc), fc, ft)
		}
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
		parts = append(parts, fmt.Sprintf("%d по именам сайтов", rDNS))
	}
	if rStatic > 0 {
		parts = append(parts, fmt.Sprintf("%d по адресам", rStatic))
	}
	_ = rHR // разбивка по механизму -- инженерная деталь, она живёт в приложении
	fmt.Fprintf(b, "  Через этот VPN-туннель идут правила: %s\n", strings.Join(parts, ", "))
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
		fmt.Fprintf(b, "  VPN-туннелей видно: %d\n", cnt)
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

// Сторож своего DNS-сервера (проверка resolver_guard, спека dns-watchdog).
// Агент присылает fail в трёх случаях: роутер уже переведён на живой
// запасной (reason=fallback, mode=fallback); живых запасных нет и настройки
// роутера не тронуты (reason=no_live_fallback); роутер вернулся на свой, а
// запасные рядом с ним не снялись и перехватывают часть запросов
// (reason=foreign_leftover, mode=primary). Заголовки -- из спеки и решений
// v0.30 дословно: без «DoH», «апстрима» и «резолвинга».
const (
	resolverGuardFallbackHeadline        = "Свой DNS-сервер не отвечает — роутер временно перешёл на запасные, сайты открываются"
	resolverGuardNoFallbackHeadline      = "Свой DNS-сервер не отвечает, а запасные недоступны — сайты по имени могут не открываться"
	resolverGuardForeignLeftoverHeadline = "Запасные DNS-серверы не снялись — сайты открываются, но часть запросов идёт мимо фильтров"
	resolverGuardRecoveredHeadline       = "Роутер снова работает через свой DNS-сервер"
	resolverGuardIdleHeadline            = "Сторож DNS больше не следит: своего DNS-сервера нет в настройках роутера"
	resolverGuardUnreadHeadline          = "Сторож DNS пока не прочитал настройки роутера"
	resolverGuardIdleWatchAgain          = "Сторож DNS ничего не возвращает в настройки сам и снова начнёт следить, когда свой DNS-сервер там появится."
)

func resolverGuardNoFallback(d map[string]any) bool {
	return strOrEmpty(d, "reason") == "no_live_fallback"
}

// resolverGuardForeignLeftover -- роутер на своём DNS-сервере, но запасные
// остались рядом: сайты открываются, фильтры своего обходятся. Отвечает ли сам
// свой сервер, отсюда не видно: в окне удержания запасные нарочно не снимают,
// пока он молчит.
func resolverGuardForeignLeftover(d map[string]any) bool {
	return strOrEmpty(d, "reason") == "foreign_leftover"
}

// resolverGuardIdle -- ok-отчёт сторожа, который перестал следить: своего
// DNS-сервера нет в настройках роутера (details.idle, спека dns-watchdog).
func resolverGuardIdle(d map[string]any) bool {
	idle, _ := boolOrFalse(d, "idle")
	return idle
}

// resolverGuardUnread -- ok-отчёт сторожа, который после запуска агента ещё
// не прочитал настройки роутера (details.ready=false): что там, неизвестно.
func resolverGuardUnread(d map[string]any) bool {
	ready, ok := d["ready"].(bool)
	return ok && !ready
}

// resolverGuardRecoveryNote -- что сказать в итоге восстановления, когда ok
// не значит «снова через свой». Пусто -- обычное восстановление.
func resolverGuardRecoveryNote(d map[string]any) []string {
	switch {
	case resolverGuardIdle(d):
		var out []string
		switch strOrEmpty(d, "idle_reason") {
		case "own_line_removed_by_hand":
			out = append(out, "Свой DNS-сервер убрали из настроек роутера вручную. "+resolverGuardIdleWatchAgain)
		case "record_dropped_manual_cleanup":
			// Свою строку тут мог убрать и сам сторож при переходе на запасные --
			// «свой убрали вручную» было бы неправдой. Вручную точно правили
			// настройки DNS: ни своего, ни поставленных сторожем там нет.
			out = append(out, "Настройки DNS на роутере поменяли вручную. "+resolverGuardIdleWatchAgain)
		default:
			out = append(out, resolverGuardIdleWatchAgain)
		}
		if hasNonEmptyList(d, "leftover") {
			out = append(out, "В настройках остались запасные DNS-серверы, которые сторож ставил на время: пока своего DNS-сервера там нет, он их не убирает.")
		}
		return out
	case resolverGuardUnread(d):
		return []string{"После перезапуска агента сторож DNS ещё не прочитал настройки роутера и о неполадке не сообщает. Работает ли роутер через свой DNS-сервер, бот сейчас не знает."}
	}
	return nil
}

// resolverGuardOnFallback -- роутер уже работает через запасной. Без reason,
// но с mode=fallback -- тоже переход: режим агент знает точно.
func resolverGuardOnFallback(d map[string]any) bool {
	if resolverGuardNoFallback(d) {
		return false
	}
	return strOrEmpty(d, "reason") == "fallback" || strOrEmpty(d, "mode") == "fallback"
}

// resolverGuardHeadline не обещает «сайты открываются», пока агент этого не
// сказал: незнакомая причина получает нейтральную фразу.
func resolverGuardHeadline(d map[string]any) string {
	switch {
	case resolverGuardNoFallback(d):
		return resolverGuardNoFallbackHeadline
	case resolverGuardForeignLeftover(d):
		return resolverGuardForeignLeftoverHeadline
	case resolverGuardOnFallback(d):
		return resolverGuardFallbackHeadline
	}
	return "Свой DNS-сервер не отвечает"
}

// writeResolverGuardWhatBroke -- что уже случилось с роутером. Адрес
// запасного (details.candidate) владельцу ничего не говорит и не печатается.
func writeResolverGuardWhatBroke(b *strings.Builder, d map[string]any) {
	switch {
	case resolverGuardNoFallback(d):
		b.WriteString("  Роутер не дождался ответа от своего DNS-сервера, а запасные тоже не ответили — переключаться было некуда, настройки роутера не тронуты.\n")
	case resolverGuardForeignLeftover(d):
		// Не «свой отвечает»: в окне удержания запасные остаются, пока свой как
		// раз молчит. Точно известно одно -- они стоят рядом со своим.
		b.WriteString("  Запасные DNS-серверы остались в настройках роутера рядом со своим DNS-сервером — часть адресов сайтов роутер узнаёт у них.\n")
		if since, err := time.Parse(time.RFC3339, strOrEmpty(d, "since")); err == nil {
			fmt.Fprintf(b, "  Так с %s.\n", since.In(mscLoc()).Format("02.01 15:04 МСК"))
		}
	case resolverGuardOnFallback(d):
		b.WriteString("  Роутер не дождался ответа от своего DNS-сервера и сам переключился на запасные — адреса сайтов сейчас находят они.\n")
		if since, err := time.Parse(time.RFC3339, strOrEmpty(d, "since")); err == nil {
			fmt.Fprintf(b, "  На запасных с %s.\n", since.In(mscLoc()).Format("02.01 15:04 МСК"))
		}
	default:
		b.WriteString("  Роутер не дождался ответа от своего DNS-сервера.\n")
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

func impactFor(checkName string, d map[string]any, ns []NeighborSummary) string {
	switch checkCategory(checkName) {
	case "dns":
		return "Домены могут не открываться или уходить не туда: сайты, приложения и правила HR-Neo зависят от DNS."
	case "tunnel":
		// Пока жив запасной VPN-туннель, обход работает -- и человеку надо
		// сказать именно это, а не пугать его тем, чего он не увидит. Но и
		// молчать нельзя: запасной остался один, и вторая поломка оставит его
		// без обхода вовсе.
		if spare, ok := liveSpare(ns); ok {
			return fmt.Sprintf("Пока ничего: обход идёт через запасной VPN-туннель «%s», сайты открываются как обычно. "+
				"Починить упавший VPN-туннель всё равно стоит — запасной остался один.", spareHumanName(spare))
		}
		var parts []string
		if rDNS, _ := intOrZero(d, "routes_dns"); rDNS > 0 {
			parts = append(parts, fmt.Sprintf("%d по именам сайтов", rDNS))
		}
		if rStatic, _ := intOrZero(d, "routes_static"); rStatic > 0 {
			parts = append(parts, fmt.Sprintf("%d по адресам", rStatic))
		}
		// «Правила: 1 по именам сайтов» -- та же форма, что в «На что обратить
		// внимание»: с ней любое число звучит правильно, а «%d правил» давало
		// «3 правил» и «идут 1 правило».
		if len(parts) > 0 {
			return "Через этот VPN-туннель идут правила: " + strings.Join(parts, " и ") + " — они не работают, пока VPN-туннель не поднимется. Обычные сайты, банки и госуслуги открываются как всегда."
		}
		return "То, что должно ходить через этот VPN-туннель, сейчас туда не доходит. Обычные сайты, банки и госуслуги открываются как всегда."
	case "hydraroute":
		return "Правила по именам сайтов перестают направлять их в нужные VPN-туннели: часть сайтов пойдёт напрямую или не откроется."
	case "awg_manager", "awgmgr_api":
		return "Интернет от этого не пропадает: VPN-туннели работают сами по себе. Но кнопки в приложении — «Починить», перезапуск VPN-туннеля, правка маршрутов — могут не сработать, пока связь с роутером не вернётся."
	case "external_reach":
		return "Через этот VPN-туннель сервисы не открываются: дело либо в самом VPN-туннеле, либо в правилах, которые через него ведут."
	case "resolver_guard":
		switch {
		case resolverGuardNoFallback(d):
			return "Сайты и приложения могут не открываться: чтобы найти сайт по имени, роутеру сейчас не у кого спросить."
		case resolverGuardForeignLeftover(d):
			return "Сайты открываются. Но то, что настроено на вашем DNS-сервере, действует не для всех запросов: часть из них идёт через запасные, мимо него."
		case resolverGuardOnFallback(d):
			return "Пока ничего заметного: сайты открываются через запасные DNS-серверы. Но то, что настроено на вашем DNS-сервере, пока не действует."
		}
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
		return "Панель роутера не ответила на запрос состояния. Обычно это значит, что она перезапускается или роутер сильно загружен."
	case "awgmgr_api":
		return "Список VPN-туннелей бот читает у панели роутера. Если она не отвечает — либо перезапускается, либо на роутере поменяли доступ к ней."
	case "external_reach":
		return diagnoseExternalReach(d, ns)
	case "resolver_guard":
		switch {
		case resolverGuardNoFallback(d):
			return "Не отвечает ни свой DNS-сервер, ни запасные. Так бывает, когда у роутера пропал интернет целиком или провайдер мешает защищённым запросам имён."
		case resolverGuardForeignLeftover(d):
			// Не «каждую минуту»: интервал задаётся в конфиге, а пока свой
			// DNS-сервер не отвечает на проверку, уборка нарочно ждёт.
			return "Обычно так бывает, когда роутер не выполнил команду убрать запасные DNS-серверы после возврата на свой. Роутер повторяет попытку при каждой проверке, когда свой DNS-сервер отвечает."
		case resolverGuardOnFallback(d):
			return "Обычно так бывает, когда недоступен сервер, на котором работает ваш DNS-сервер, или дорога до него. Роутер продолжает его проверять и вернётся сам, как только тот ответит."
		}
	}
	return ""
}

func diagnoseDNS(d map[string]any, ns []NeighborSummary) string {
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")
	if rknProbed > 0 && rknSus == rknProbed {
		return "На всех проверенных серверах ответы похожи на блокировку: серверы имён живы, но ответ подменяют по дороге. Помогает шифрованный поиск имён или другой сервер."
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
			return fmt.Sprintf("%s идут через %s. Остальные VPN-туннели выглядят живыми, значит интернет на месте. Скорее всего испортился именно VPN-туннель «%s» — поиск имён просто заметил это первым.", prefix, label, name)
		}
		if !neighborsAlive(ns) && len(ns) > 0 {
			return fmt.Sprintf("%s идут через %s, и соседние VPN-туннели тоже молчат. Похоже, дело не в VPN-туннеле, а выше: провайдер или сам роутер.", prefix, label)
		}
		return fmt.Sprintf("%s идут через %s. Похоже на сбой самого VPN-туннеля или правила, а не поиска имён.", prefix, label)
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
				"Все молчащие серверы имён идут через один VPN-туннель — %s. Соседние VPN-туннели живы, значит интернет на месте. Скорее всего испортился именно он.",
				iface)
		}
		if !neighborsAlive(ns) && len(ns) > 0 {
			return fmt.Sprintf(
				"Молчащие серверы имён идут через %s, и соседние VPN-туннели тоже не отвечают. Похоже, дело выше — в провайдере или самом роутере.",
				iface)
		}
		return fmt.Sprintf(
			"Все молчащие серверы имён идут через один VPN-туннель — %s. Похоже на сбой именно этого VPN-туннеля, а не поиска имён.",
			iface)
	}

	if failedCount > 0 && failedCount < total {
		return "Лежит часть серверов, остальные отвечают. Резолв в целом работает — это деградация, не полный отказ."
	}
	if failedCount == total && total > 0 {
		if len(ns) > 0 && neighborsAlive(ns) {
			return "Серверы имён не ответили, но соседние VPN-туннели живы. Похоже на проблему самого сервера имён или устаревшего правила, а не на пропавший интернет."
		}
		return "Не отвечает ни один сервер имён. Либо у роутера пропал интернет, либо все эти серверы легли разом — что бывает редко."
	}
	return ""
}

func diagnoseTunnel(d map[string]any, ns []NeighborSummary) string {
	age, hasAge := intOrZero(d, "handshake_age_sec")
	pc := strOrEmpty(d, "ping_check_status")
	conflict, hasConflict := boolOrFalse(d, "address_conflict")

	var parts []string
	if hasConflict && conflict {
		parts = append(parts, "У VPN-туннеля конфликт адресов: он пытается подняться с тем же адресом, что и другой VPN-туннель. Сам по себе такой VPN-туннель не поднимется.")
	}
	switch {
	case !hasAge:
		parts = append(parts, "Обмена ключами не было ни разу с момента запуска — VPN-туннель так и не поднялся. Чаще всего дело в неверных настройках сервера или в том, что провайдер закрыл нужный порт.")
	case age > 600:
		parts = append(parts, fmt.Sprintf("Обмена ключами нет уже %s — VPN-туннель точно лежит, а не моргнул.", humanAgeSec(age)))
	case age > 180:
		parts = append(parts, "Обмен ключами устарел, но не катастрофически. Возможно, провайдер режет такой трафик, либо сервер VPN-туннеля временно недоступен.")
	}
	if pc == "dead" {
		parts = append(parts, "Проверка связи показывает сбой — пакеты не доходят даже после авто-рестартов.")
	}
	if len(parts) == 0 && len(ns) > 0 && neighborsAlive(ns) {
		parts = append(parts, "Соседние VPN-туннели живы, так что интернет и роутер в порядке. Проблема локальная — сервер этого VPN-туннеля или его настройки.")
	}
	if len(parts) == 0 && len(ns) > 0 && !neighborsAlive(ns) {
		parts = append(parts, "Соседние VPN-туннели тоже не на связи. Похоже на проблему уровнем выше — у провайдера или в самом роутере.")
	}
	return strings.Join(parts, " ")
}

func diagnoseHydraRoute(d map[string]any) string {
	installed, _ := boolOrFalse(d, "installed")
	running, _ := boolOrFalse(d, "running")
	switch {
	case !installed:
		return "HydraRoute не установлен — без него правила по именам сайтов не работают."
	case !running:
		return "HydraRoute установлен, но не запущен: скорее всего, он упал или его остановили вручную."
	}
	return "HydraRoute запущен, но проверка возвращает ошибку. Скорее всего сбой в конфиге — какое-то правило ссылается на несуществующий VPN-туннель."
}

func diagnoseExternalReach(d map[string]any, ns []NeighborSummary) string {
	iface, _ := d["via_interface"].(string)
	failed := mapsSlice(d, "targets_failed")
	total, _ := intOrZero(d, "targets_total")
	switch {
	case total > 0 && len(failed) == total && iface != "":
		return fmt.Sprintf("Через VPN-туннель %s не открылся ни один сервис — он не пропускает трафик наружу. Дело либо в самом VPN-туннеле, либо в его маршрутизации.", iface)
	case len(failed) > 0 && len(failed) < total:
		return "Часть целей живы, часть нет — это похоже на блокировку конкретных сервисов, а не общий сбой связи."
	}
	if len(ns) > 0 && !neighborsAlive(ns) {
		return "Соседние VPN-туннели тоже не на связи — похоже, дело в провайдере."
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
		return "Само по себе это не мешает интернету. Откройте приложение — там на экране «Проверки» видно, вернулась ли связь с панелью роутера. Если не вернулась за полчаса, перезагрузите роутер."
	case "external_reach":
		return adviseExternalReach(d, ns)
	case "resolver_guard":
		switch {
		case resolverGuardNoFallback(d):
			return "Откройте приложение, экран «Проверки»: там видно, есть ли у роутера интернет. Если интернета нет — дело у провайдера. Если есть — напишите тому, кто настраивал DNS-сервер."
		case resolverGuardForeignLeftover(d):
			// Сторож меняет только текущие настройки и никогда их не сохраняет --
			// это всё, что агент обещает. Если человек сохранил настройки в
			// веб-панели, запасные переживут и перезагрузку.
			return "Ничего делать не нужно: роутер сам убирает запасные и пришлёт сообщение, когда закончит. Если за пару часов этого не случится — перезагрузите роутер: сторож DNS сам никогда не сохраняет настройки роутера, и после перезагрузки поставленных им запасных не будет, если только настройки не сохраняли вручную в веб-панели роутера."
		case resolverGuardOnFallback(d):
			return "Ничего делать не нужно: когда свой DNS-сервер снова ответит, роутер сам вернётся на него и пришлёт сообщение. Если этого не случится за пару часов — напишите тому, кто настраивал DNS-сервер."
		}
	}
	if checkName == "agent_heartbeat" {
		return routerOfflineAdvice
	}
	return "Откройте приложение — на экране «Сейчас» видно, что с роутером происходит."
}

func adviseDNS(d map[string]any, ns []NeighborSummary) string {
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")
	if rknProbed > 0 && rknSus == rknProbed {
		return "Похоже, провайдер подменяет ответы на запросы имён. Откройте приложение, экран «VPN-туннели» — там видно, через какой VPN-туннель уходят эти запросы; их стоит увести в обход."
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
			return fmt.Sprintf("Откройте приложение, экран «VPN-туннели», и найдите VPN-туннель %s — начните с его перезапуска.", label)
		}
		return fmt.Sprintf("Откройте приложение, экран «Проверки»: там видно, работает ли интернет напрямую. Если напрямую работает, а через обход нет — дело в VPN-туннеле «%s».", name)
	}
	return "Подождите минуту — сервер имён мог не ответить разово. Если не вернётся, откройте приложение: на экране «Сейчас» видно общую картину."
}

func adviseTunnel(d map[string]any, ns []NeighborSummary) string {
	age, hasAge := intOrZero(d, "handshake_age_sec")
	conflict, hasConflict := boolOrFalse(d, "address_conflict")
	if hasConflict && conflict {
		return "У этого VPN-туннеля адрес совпал с адресом другого VPN-туннеля — сам он не поднимется. Нажмите «Починить» в приложении: оно перевыпустит настройки VPN-туннеля заново."
	}
	if !hasAge {
		// Про увод трафика говорим только когда уводить есть куда: обещать
		// обход, которого нет, -- то же враньё, что и молчать о нём.
		if _, ok := liveSpare(ns); ok {
			return "Нажмите «Починить» в приложении — оно уведёт трафик на запасной VPN-туннель и перевыпустит настройки упавшего VPN-туннеля. Если и после этого связи нет, провайдер может резать такой трафик."
		}
		return "Нажмите «Починить» в приложении — оно перевыпустит настройки VPN-туннеля заново. Если и после этого связи нет, провайдер может резать такой трафик."
	}
	var base string
	if age > 600 {
		base = "Нажмите «Починить» в приложении — обычно помогает."
		if _, ok := liveSpare(ns); ok {
			base += " Пока чинит, трафик пойдёт через запасной VPN-туннель."
		}
	} else {
		base = "Подождите пару минут — связь могла моргнуть. Если не вернётся, нажмите «Починить» в приложении."
	}
	// pingCheck выключен → бот судит о связи только по возрасту handshake, а он
	// у idle-туннеля устаревает сам по себе. Подсказываем включить активную
	// проверку, чтобы отличать простой от настоящего обрыва.
	if pingCheckIsDisabled(d) {
		base += " Заодно включите проверку связи для этого VPN-туннеля на экране «Настройки»: сейчас она выключена, и бот судит о VPN-туннеле по косвенным признакам."
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
		return "Умная маршрутизация на роутере не установлена — без неё правила по именам сайтов не работают. Это ставится один раз; напишите тому, кто настраивал роутер."
	case !running:
		return "Умная маршрутизация остановлена. Обычно помогает перезагрузка роутера; если не помогла — напишите тому, кто его настраивал."
	}
	return "Откройте приложение, экран «VPN-туннели» — возможно, одно из правил ссылается на VPN-туннель, которого больше нет."
}

// adviseExternalReach -- совет владельцу, а не оператору старой панели бота:
// кнопок «🎛 Туннели» и «🇷🇺 Напрямую?» у него нет, есть приложение.
func adviseExternalReach(d map[string]any, ns []NeighborSummary) string {
	iface, _ := d["via_interface"].(string)
	failed := mapsSlice(d, "targets_failed")
	total, _ := intOrZero(d, "targets_total")
	if total > 0 && len(failed) == total && iface != "" {
		return fmt.Sprintf("VPN-туннель %s не пропускает трафик наружу. Откройте приложение, экран «VPN-туннели», и нажмите «Починить» у этого VPN-туннеля.", iface)
	}
	if len(ns) > 0 && !neighborsAlive(ns) {
		return "Откройте приложение, экран «Проверки»: там видно, работает ли интернет напрямую. Если и напрямую наружу не выходит — дело у провайдера."
	}
	return "Откройте приложение, экран «VPN-туннели» — там видно, через какой VPN-туннель идёт трафик до этих сервисов."
}

// neighborsAlive returns true when at least one neighbor is in alive/ok status.
// Used to distinguish "местная проблема туннеля" from "WAN-сбой".
// checkHumanName -- как проверка называется для человека. Техническое имя
// («tunnel_awg12», «awgmgr_api») он нигде не видел, а в подписи тревоги оно
// стояло первым.
func checkHumanName(check string) string {
	switch checkCategory(check) {
	case "tunnel":
		return "VPN-туннель"
	case "dns":
		return "поиск сайтов по имени"
	case "hydraroute":
		return "умная маршрутизация"
	case "awg_manager", "awgmgr_api":
		return "связь с панелью роутера"
	case "external_reach":
		return "доступность сервисов через обход"
	case "resolver_guard":
		return "свой DNS-сервер"
	}
	if check == "agent_heartbeat" {
		return "отчёты роутера"
	}
	return check
}

// liveSpare -- первый живой соседний VPN-туннель. Это и есть тот обход,
// который сейчас работает вместо упавшего: у роутера их обычно два, и второй
// молчит ровно до того момента, когда понадобится.
func liveSpare(ns []NeighborSummary) (NeighborSummary, bool) {
	for _, n := range ns {
		if isLiveStatus(n.Status) {
			return n, true
		}
	}
	return NeighborSummary{}, false
}

// noTunnelName -- подпись VPN-туннеля, у которого нет ни имени, ни интерфейса.
const noTunnelName = "без имени"

// tunnelHumanName -- как VPN-туннель называет человек. Идентификатор
// («awg12») он нигде не видел; имя даёт ему то, что он узнает в приложении.
func tunnelHumanName(d map[string]any) string {
	if tname, _ := d["tunnel_name"].(string); strings.TrimSpace(tname) != "" {
		return strings.TrimSpace(tname)
	}
	if iface, _ := d["interface"].(string); strings.TrimSpace(iface) != "" {
		return strings.TrimSpace(iface)
	}
	return noTunnelName
}

// quotedTunnelName -- имя VPN-туннеля для текста: в ёлочках, как в
// приложении. Безымянному ёлочки не нужны: «без имени» -- не имя.
func quotedTunnelName(d map[string]any) string {
	name := tunnelHumanName(d)
	if name == noTunnelName {
		return name
	}
	return "«" + name + "»"
}

// spareHumanName -- имя запасного VPN-туннеля для строки про обход.
func spareHumanName(n NeighborSummary) string {
	if strings.TrimSpace(n.TunnelName) != "" {
		return strings.TrimSpace(n.TunnelName)
	}
	if strings.TrimSpace(n.Interface) != "" {
		return strings.TrimSpace(n.Interface)
	}
	return strings.TrimPrefix(n.CheckName, "tunnel_")
}

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
	case name == "resolver_guard":
		return "resolver_guard"
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

// hasNonEmptyList: details[key] is a non-empty list -- []any after JSON, or
// []string when a Go caller builds the details directly.
func hasNonEmptyList(d map[string]any, key string) bool {
	switch x := d[key].(type) {
	case []any:
		return len(x) > 0
	case []string:
		return len(x) > 0
	}
	return false
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

// durFmt -- длительность по-русски. Английские «12m» и «2h30m» в тексте,
// который читает владелец роутера, ничем не лучше остального жаргона.
func durFmt(d time.Duration) string {
	if d < time.Minute {
		return "меньше минуты"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		if m == 0 {
			return fmt.Sprintf("%d ч", h)
		}
		return fmt.Sprintf("%d ч %d мин", h, m)
	}
	return fmt.Sprintf("%d мин", m)
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
