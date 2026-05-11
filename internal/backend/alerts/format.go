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
// helper and as a "других тоннелей" hint in the advice line.
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
	Interface    string // "nwg1"
	Status       string // "alive" / "dead" / "ok" / "fail"
	HandshakeAge int    // seconds, 0 if unknown
}

type RecoveryArgs struct {
	Nickname    string
	CheckName   string
	HardSince   time.Time
	RecoveredAt time.Time
}

// FormatHard renders a HARD alert as three sections — «что не работает /
// что я думаю / что делать». Returns plain text (no MarkdownV2 — too many
// escaping landmines in dynamic strings like endpoint hostnames).
func FormatHard(a HardArgs) string {
	var b strings.Builder
	mobileBadge := ""
	if a.IsMobile {
		mobileBadge = "📱 "
	}
	severity := categorySeverity(a.CheckName, a.Check.Details, a.Neighbors)
	headline := categoryHeadline(a.CheckName, a.Check.Details)
	fmt.Fprintf(&b, "%s %s[%s] — %s\n", severity, mobileBadge, a.Nickname, headline)

	b.WriteString("\nЧто не работает:\n")
	writeWhatBroke(&b, a.CheckName, a.Check.Details)

	if h := diagnose(a.CheckName, a.Check.Details, a.Neighbors); h != "" {
		b.WriteString("\nЧто я думаю:\n")
		writeWrapped(&b, h)
	}

	if adv := suggestAction(a.CheckName, a.Check.Details, a.Neighbors); adv != "" {
		b.WriteString("\nЧто делать:\n")
		writeWrapped(&b, adv)
	}

	fmt.Fprintf(&b, "\n%d fails · с %s",
		a.ConsecFails, a.HardSince.In(mscLoc()).Format("02.01 15:04 МСК"))
	return b.String()
}

// FormatRecovery renders a recovery message in the same human tone as
// FormatHard — single short line, no AI-style sections.
func FormatRecovery(a RecoveryArgs) string {
	d := a.RecoveredAt.Sub(a.HardSince).Round(time.Minute)
	headline := recoveryHeadline(a.CheckName, nil)
	return fmt.Sprintf(
		"🟢 [%s] — %s\nПростой: %s",
		a.Nickname, headline, durFmt(d),
	)
}

// FormatRouterOffline renders a router-offline message (heartbeat watcher).
// Includes a short hint at what to check first.
func FormatRouterOffline(nickname string, since time.Duration) string {
	return fmt.Sprintf(
		"🔴 [%s] — Роутер не на связи\n\n"+
			"Что не работает:\n  Нет heartbeat'ов %s.\n\n"+
			"Что я думаю:\n  Либо роутер выключен/перезагружается, либо у него отвалился WAN, либо упал агент wg-monitor.\n\n"+
			"Что делать:\n  Проверь питание роутера, потом WAN/4G. Если железо живо — зайди по SSH и глянь /opt/etc/init.d/S99wg-monitor status.",
		nickname, durFmt(since.Round(time.Minute)),
	)
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
	var b strings.Builder
	mobileBadge := ""
	if args.IsMobile {
		mobileBadge = "📱 "
	}
	headline := categoryHeadline(args.CheckName, args.Check.Details)
	fmt.Fprintf(&b, "🔁 %s[%s] — Всё ещё: %s\n", mobileBadge, args.Nickname, headline)

	if args.Check.Name != "" {
		b.WriteString("\nЧто не работает:\n")
		writeWhatBroke(&b, args.CheckName, args.Check.Details)
	}

	age := time.Since(args.HardSince).Round(time.Minute)
	cadence := args.RealertEvery
	if cadence <= 0 {
		cadence = 6 * time.Hour
	}
	fmt.Fprintf(&b, "\nс %s (%s назад) · напомню снова через %s · #%d",
		args.HardSince.In(mscLoc()).Format("02.01 15:04 МСК"),
		durFmt(age),
		shortDur(cadence),
		args.RealertCount,
	)
	return b.String()
}

// categorySeverity returns 🔴 / 🟡 based on how broken things actually are.
// FSM already decided this is HARD; severity is a visual hint about scale,
// not a gate on alerting.
func categorySeverity(checkName string, d map[string]any, _ []NeighborSummary) string {
	switch checkCategory(checkName) {
	case "dns":
		total, _ := intOrZero(d, "endpoints")
		failed, _ := intOrZero(d, "failed_count")
		rknSus, _ := intOrZero(d, "rkn_suspect")
		rknProbed, _ := intOrZero(d, "rkn_probed")
		if rknProbed > 0 && rknSus == rknProbed {
			return "🔴"
		}
		if total >= 3 && failed*2 < total {
			return "🟡" // меньше половины серверов упало — частичный сбой
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
func categoryHeadline(checkName string, d map[string]any) string {
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
		return "Состояние awg-manager не читается"
	case "awgmgr_api":
		return "Реестр туннелей awg-manager недоступен"
	case "external_reach":
		total, _ := intOrZero(d, "targets_total")
		failed := mapsSlice(d, "targets_failed")
		if total > 0 && len(failed) > 0 && len(failed) < total {
			return "Часть внешних сервисов недоступна"
		}
		return "Внешние сервисы недоступны через тоннель"
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
func writeWhatBroke(b *strings.Builder, checkName string, d map[string]any) {
	switch checkCategory(checkName) {
	case "tunnel":
		writeTunnelWhatBroke(b, d)
	case "dns":
		writeDNSWhatBroke(b, d)
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
			fmt.Fprintf(b, "  Endpoint %s (через %s)\n", ep, isp)
		} else {
			fmt.Fprintf(b, "  Endpoint %s\n", ep)
		}
	}
	if age, ok := intOrZero(d, "handshake_age_sec"); ok {
		fmt.Fprintf(b, "  Последний handshake: %s назад\n", humanAgeSec(age))
	} else {
		b.WriteString("  Handshake — ни разу не было\n")
	}
	if pc := strOrEmpty(d, "ping_check_status"); pc != "" {
		fc, _ := intOrZero(d, "ping_check_fail_count")
		ft, _ := intOrZero(d, "ping_check_fail_threshold")
		var extras []string
		if rc, _ := intOrZero(d, "ping_check_restart_count"); rc > 0 {
			extras = append(extras, fmt.Sprintf("auto-рестартов: %d", rc))
		}
		if lat, ok := intOrZero(d, "ping_check_last_latency_ms"); ok && lat > 0 {
			extras = append(extras, fmt.Sprintf("last %dms", lat))
		}
		fmt.Fprintf(b, "  pingCheck: %s — %d/%d fails", pc, fc, ft)
		if len(extras) > 0 {
			fmt.Fprintf(b, " (%s)", strings.Join(extras, " · "))
		}
		b.WriteString("\n")
	}
	if conflict, ok := boolOrFalse(d, "address_conflict"); ok && conflict {
		b.WriteString("  ⚠ конфликт адресов на интерфейсе\n")
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
		fmt.Fprintf(b, "  Параметры: %s\n", strings.Join(parts, " · "))
	}
}

func writeDNSWhatBroke(b *strings.Builder, d map[string]any) {
	total, _ := intOrZero(d, "endpoints")
	failed, _ := intOrZero(d, "failed_count")
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")

	if total > 0 {
		switch {
		case failed == 0 && rknProbed > 0 && rknSus == rknProbed:
			fmt.Fprintf(b, "  Серверы отвечают (%d), но похоже трафик подменяется\n", total)
		case failed == 0:
			fmt.Fprintf(b, "  Серверы отвечают, но проверка вернула fail (всего %d)\n", total)
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
				label += " (" + ndms + ")"
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
	fmt.Fprintf(b, "  HydraRoute · installed=%v running=%v\n", installed, running)
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
	if iface, _ := d["via_interface"].(string); iface != "" {
		fmt.Fprintf(b, "  Через интерфейс: %s\n", iface)
	}
}

func writeGenericWhatBroke(b *strings.Builder, d map[string]any) {
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		fmt.Fprintf(b, "  %s\n", errStr)
	} else {
		b.WriteString("  Проверка вернула fail без подробностей.\n")
	}
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
		return "awg-manager не отвечает на запрос статуса. Обычно это значит что демон упал или сильно загружен — версии и firmware не считываются."
	case "awgmgr_api":
		return "Реестр туннелей читается через REST awg-manager. Если он недоступен — либо awg-manager умер, либо HTTP-эндпоинт сменил порт/auth."
	case "external_reach":
		return diagnoseExternalReach(d, ns)
	}
	return ""
}

func diagnoseDNS(d map[string]any, ns []NeighborSummary) string {
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")
	if rknProbed > 0 && rknSus == rknProbed {
		return "На всех проверенных endpoint'ах ответы похожи на RKN-блокировку. DNS-серверы живы, но трафик подменяется по дороге — нужен DoH или другой апстрим."
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

	// Если все упавшие endpoint'ы прибиты к одному ndms_name (= один тоннель/интерфейс),
	// а соседи живы — диагноз не про DNS, а про этот туннель.
	if len(failedIfaces) == 1 && failedCount > 0 {
		var iface string
		for k := range failedIfaces {
			iface = k
		}
		if neighborsAlive(ns) && len(ns) > 0 {
			return fmt.Sprintf(
				"Все упавшие DNS-серверы идут через один интерфейс — %s. Соседние тоннели живы, так что это не WAN. Скорее всего деградировал именно %s — DNS просто первый это заметил.",
				iface, iface)
		}
		if !neighborsAlive(ns) && len(ns) > 0 {
			return fmt.Sprintf(
				"Упавшие DNS-серверы идут через %s, и соседние тоннели тоже не на связи. Похоже на проблему уровнем выше — WAN или провайдер.",
				iface)
		}
		return fmt.Sprintf(
			"Все упавшие DNS-серверы идут через один интерфейс — %s. Похоже на сбой именно этого тоннеля, не самого DNS.",
			iface)
	}

	if failedCount > 0 && failedCount < total {
		return "Лежит часть серверов, остальные отвечают. Резолв в целом работает — это деградация, не полный отказ."
	}
	if failedCount == total && total > 0 {
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
		parts = append(parts, "Handshake'а не было ни разу с момента старта — туннель так и не поднялся. Чаще всего это неправильный endpoint, AWG-параметры или закрытый порт у провайдера.")
	case age > 600:
		parts = append(parts, fmt.Sprintf("Handshake'а нет уже %s — туннель явно лежит, не просто моргнул.", humanAgeSec(age)))
	case age > 180:
		parts = append(parts, "Handshake устарел, но не катастрофически. Возможно провайдер режет UDP, либо peer временно недоступен.")
	}
	if pc == "dead" {
		parts = append(parts, "pingCheck показывает dead — пакеты не доходят даже после auto-рестартов.")
	}
	if len(parts) == 0 && len(ns) > 0 && neighborsAlive(ns) {
		parts = append(parts, "Соседние тоннели живы, так что WAN/роутер целы. Проблема локальная — этот peer/endpoint.")
	}
	if len(parts) == 0 && len(ns) > 0 && !neighborsAlive(ns) {
		parts = append(parts, "Соседние тоннели тоже не на связи. Похоже на проблему уровнем выше — WAN, провайдер или сам роутер.")
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
	return "HydraRoute запущен, но проверка возвращает ошибку. Скорее всего сбой в конфиге — какое-то правило ссылается на несуществующий тоннель."
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
		return "Соседние тоннели тоже не на связи — похоже WAN или провайдер."
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
		if neighborsAlive(ns) {
			return fmt.Sprintf(
				"Открой alert по %s (или 🎛 Туннели) и нажми «Перезапуск туннеля». Если %s не висит в общем списке — возможно его удалили из awg-manager.",
				iface, iface)
		}
		return fmt.Sprintf(
			"Сначала проверь WAN: 🌍 Через тоннель? и 🇷🇺 Напрямую?. Если связь есть только напрямую — туннели лежат, начни с %s.",
			iface)
	}
	return "Подожди минуту — иногда апстримы временно отвечают timeout. Если не вернётся — открой 📊 Что происходит? и глянь общую картину по роутеру."
}

func adviseTunnel(d map[string]any, _ []NeighborSummary) string {
	age, hasAge := intOrZero(d, "handshake_age_sec")
	conflict, hasConflict := boolOrFalse(d, "address_conflict")
	if hasConflict && conflict {
		return "Открой 🎛 Туннели, найди этот тоннель и проверь его адрес. Скорее всего он совпадает с другим интерфейсом — поменяй на свободную /24."
	}
	if !hasAge {
		return "Тыкни «📊 Диагностика» — она покажет endpoint, AWG-параметры и попытается поднять. Если endpoint правильный, но handshake не идёт — провайдер режет UDP."
	}
	if age > 600 {
		return "Жми «🔁 Перезапуск туннеля» — обычно помогает. Если нет — «📊 Диагностика», там увидишь конкретный сбой."
	}
	return "Подожди ещё минуту — handshake мог моргнуть. Если не вернётся за 2-3 минуты, тыкни «🔁 Перезапуск туннеля»."
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
	return "Проверь правила hrneo (🛣 Маршруты) — возможно одно из них ссылается на удалённый туннель."
}

func adviseExternalReach(d map[string]any, ns []NeighborSummary) string {
	iface, _ := d["via_interface"].(string)
	failed := mapsSlice(d, "targets_failed")
	total, _ := intOrZero(d, "targets_total")
	if total > 0 && len(failed) == total && iface != "" {
		return fmt.Sprintf("Туннель %s не пропускает наружу. Тыкни «🔁 Перезапуск туннеля» в его alert'е, либо открой 🎛 Туннели и сделай force handshake.", iface)
	}
	if len(ns) > 0 && !neighborsAlive(ns) {
		return "Сначала проверь WAN: 🇷🇺 Напрямую? — если и без тоннеля наружу не выходит, проблема у провайдера."
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
		return "timeout"
	case strings.Contains(low, "connection refused"):
		return "refused"
	case strings.Contains(low, "no route to host"):
		return "нет маршрута"
	case strings.Contains(low, "network is unreachable"):
		return "сеть недоступна"
	case strings.Contains(low, "no such host"):
		return "имя не резолвится"
	case strings.Contains(low, "tls"):
		return "TLS-ошибка"
	case strings.Contains(low, "context deadline"):
		return "таймаут"
	}
	const max = 60
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// writeWrapped writes a paragraph as a single indented line. Telegram already
// handles soft-wrap; we just want the leading two spaces for visual grouping.
func writeWrapped(b *strings.Builder, s string) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
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
func pluralServers(n int) string {
	mod10 := n % 10
	mod100 := n % 100
	switch {
	case mod100 >= 11 && mod100 <= 14:
		return fmt.Sprintf("%d серверов", n)
	case mod10 == 1:
		return fmt.Sprintf("%d сервера", n) // "не отвечает 1 из 1 сервера"
	case mod10 >= 2 && mod10 <= 4:
		return fmt.Sprintf("%d серверов", n)
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
