package tg

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

const routesMaxPerRow = 2

// RoutesPanelText renders Screen 2.
//
// "Visible total" per tunnel = DNS + Static. HRNeo is a sub-count of DNS
// (rules with engine=hydraroute), shown separately in the upper status block
// but NOT added to per-tunnel totals (would double-count).
func RoutesPanelText(nickname string, snap wire.RouteSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 Маршруты — %s\n", nickname)
	fmt.Fprintf(&b, "обновлено: %s\n\n", time.Now().Format("15:04:05"))
	b.WriteString("Что видно:\n")
	if snap.HRNeo.Installed {
		state := "✅ установлен, работает"
		if !snap.HRNeo.Running {
			state = "⚠ установлен, остановлен"
		}
		fmt.Fprintf(&b, "  • HydraRoute Neo: %s\n", state)
	}
	totalDNS := snap.Other.DNS
	totalStatic := snap.Other.Static
	totalHR := snap.Other.HRNeo
	for _, c := range snap.Counts {
		totalDNS += c.DNS
		totalStatic += c.Static
		totalHR += c.HRNeo
	}
	fmt.Fprintf(&b, "  • DNS routes: %d правил\n", totalDNS)
	fmt.Fprintf(&b, "  • Static IP routes: %d правил\n", totalStatic)
	if snap.HRNeo.Installed {
		fmt.Fprintf(&b, "  • из них HR-Neo: %d\n", totalHR)
	}
	b.WriteString("\nПо туннелям:\n")
	for _, t := range snap.Tunnels {
		c := snap.Counts[t.ID]
		visible := c.DNS + c.Static
		fmt.Fprintf(&b, "  • %s (%s): %d правил\n", t.Name, t.Iface, visible)
	}
	if snap.Other.DNS+snap.Other.Static > 0 {
		b.WriteString("\nБез привязки к туннелю:\n")
		if snap.Other.DNS > 0 {
			fmt.Fprintf(&b, "  • DNS routes: %d правил ← WAN/system\n", snap.Other.DNS)
		}
		if snap.Other.Static > 0 {
			fmt.Fprintf(&b, "  • Static IP routes: %d правил ← WAN/system\n", snap.Other.Static)
		}
	}
	return b.String()
}

// RoutesPanelKeyboard builds Screen 2 inline keyboard.
//
// callback_data shape:
//
//	routes_rebind:<userID>:<src_tunnel_id>
//	routes_refresh:<userID>:_panel_
//	routes_close:<userID>:_panel_
func RoutesPanelKeyboard(userID int64, snap wire.RouteSnapshot) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}
	var row []InlineKeyboardButton
	for _, t := range snap.Tunnels {
		c := snap.Counts[t.ID]
		if c.DNS+c.Static == 0 {
			continue
		}
		row = append(row, InlineKeyboardButton{
			Text:         fmt.Sprintf("🔄 %s", t.Name),
			CallbackData: fmt.Sprintf("routes_rebind:%d:%s", userID, t.ID),
		})
		if len(row) >= routesMaxPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if snap.Other.DNS+snap.Other.Static > 0 {
		rows = append(rows, []InlineKeyboardButton{{
			Text:         "🔄 " + routeOtherSourceLabel(snap.Other),
			CallbackData: fmt.Sprintf("routes_rebind:%d:%s", userID, wire.RouteOtherID),
		}})
	}
	rows = append(rows, []InlineKeyboardButton{
		{Text: "➕ Добавить маршрут", CallbackData: fmt.Sprintf("routes_add:%d:_panel_", userID)},
		{Text: "🗑 Удалить маршрут", CallbackData: fmt.Sprintf("routes_del:%d:_panel_:_list_", userID)},
	})
	if snap.HRNeo.Installed {
		rows = append(rows, []InlineKeyboardButton{{
			Text:         "HR-Neo правила",
			CallbackData: fmt.Sprintf("routes_hrneo:%d:_panel_", userID),
		}})
		rows = append(rows, []InlineKeyboardButton{{
			Text:         "HR-Neo проверка",
			CallbackData: fmt.Sprintf("routes_hrneo_doctor:%d:_panel_", userID),
		}})
	}
	rows = append(rows, []InlineKeyboardButton{{
		Text:         "Снапшот",
		CallbackData: fmt.Sprintf("routes_snapshot:%d:_panel_", userID),
	}})
	rows = append(rows, HelpRowFor("routes"))
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", userID)},
		{Text: "Закрыть", CallbackData: fmt.Sprintf("routes_close:%d:_panel_", userID)},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

// RebindPickKeyboard builds Screen 3 — destination picker.
func RebindPickKeyboard(userID int64, srcID string, snap wire.RouteSnapshot) (string, InlineKeyboardMarkup) {
	var src *wire.TunnelMeta
	for i, t := range snap.Tunnels {
		if t.ID == srcID {
			src = &snap.Tunnels[i]
			break
		}
	}
	srcName := ""
	srcIface := ""
	if srcID == wire.RouteOtherID {
		srcName = routeOtherSourceLabel(snap.Other)
	} else if src != nil {
		srcName = src.Name
		srcIface = src.Iface
	} else {
		return "источник недоступен", InlineKeyboardMarkup{}
	}
	if srcIface != "" {
		srcName = fmt.Sprintf("%s (%s)", srcName, srcIface)
	}
	text := fmt.Sprintf("🛣 Перенос с %s → куда?\n\nДоступные:", srcName)
	rows := [][]InlineKeyboardButton{}
	for _, t := range snap.Tunnels {
		if t.ID == srcID {
			continue
		}
		label := t.Name
		if !t.Enabled {
			label += " (off)"
		}
		rows = append(rows, []InlineKeyboardButton{{
			Text:         label,
			CallbackData: fmt.Sprintf("routes_pick:%d:%s:%s", userID, srcID, t.ID),
		}})
	}
	rows = append(rows, []InlineKeyboardButton{{
		Text: "← Отмена", CallbackData: fmt.Sprintf("routes_back:%d:_panel_", userID),
	}})
	return text, InlineKeyboardMarkup{InlineKeyboard: rows}
}

func RouteSnapshotText(nickname string, snap wire.RouteSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 Снапшот маршрутов — %s\n", nickname)
	hr := "not installed"
	if snap.HRNeo.Installed && snap.HRNeo.Running {
		hr = "installed/running"
	} else if snap.HRNeo.Installed {
		hr = "installed/stopped"
	}
	fmt.Fprintf(&b, "\nСводка:\n")
	fmt.Fprintf(&b, "  • HR-Neo: %s\n", humanHRState(hr))
	fmt.Fprintf(&b, "  • правил всего: %d\n", len(snap.Rules))
	fmt.Fprintf(&b, "  • туннелей всего: %d\n", len(snap.Tunnels))
	totalDNS, totalStatic, totalHR := snap.Other.DNS, snap.Other.Static, snap.Other.HRNeo
	for _, c := range snap.Counts {
		totalDNS += c.DNS
		totalStatic += c.Static
		totalHR += c.HRNeo
	}
	fmt.Fprintf(&b, "  • DNS: %d, static: %d, HR-Neo: %d\n", totalDNS, totalStatic, totalHR)
	b.WriteString("\nТуннели:\n")
	for _, t := range snap.Tunnels {
		c := snap.Counts[t.ID]
		fmt.Fprintf(&b, "  • %s (%s): dns=%d static=%d hr=%d\n", t.Name, t.Iface, c.DNS, c.Static, c.HRNeo)
	}
	if snap.Other.DNS+snap.Other.Static > 0 {
		fmt.Fprintf(&b, "  • WAN/system: dns=%d static=%d hr=%d\n", snap.Other.DNS, snap.Other.Static, snap.Other.HRNeo)
	}
	return b.String()
}

func humanHRState(state string) string {
	switch state {
	case "installed/running":
		return "установлен и работает"
	case "installed/stopped":
		return "установлен, но остановлен"
	case "not installed":
		return "не установлен"
	}
	return state
}

func RouteExplainText(nickname, target string, snap wire.RouteSnapshot) string {
	target = strings.TrimSpace(target)
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 Разбор маршрута — %s\n", nickname)
	fmt.Fprintf(&b, "цель: %s\n", fallback(target, "-"))
	if target == "" {
		b.WriteString("\nЧто отправить:\n  • домен\n  • IP\n  • CIDR\n")
		return b.String()
	}
	matches := explainMatches(target, snap)
	if len(matches) == 0 {
		b.WriteString("\nСовпадений нет:\n")
		b.WriteString("  • explicit DNS/HR-Neo/static route не найден\n")
		b.WriteString("  • вероятный путь: default routing или HR-Neo policy/default fall-through\n")
		return b.String()
	}
	b.WriteString("\nНайденные правила:\n")
	for _, m := range matches {
		fmt.Fprintf(&b, "  • %s [%s]", fallback(m.rule.Name, m.rule.ID), explainKindLabel(m.rule))
		if m.matched != "" {
			fmt.Fprintf(&b, " via %s", m.matched)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "  куда ведёт: %s\n", describeBind(m.rule.Bind, snap))
		if !m.rule.Enabled {
			b.WriteString("  статус: выключено\n")
		}
	}
	return b.String()
}

type routeExplainMatch struct {
	rule    wire.RouteRuleSummary
	matched string
}

func explainMatches(target string, snap wire.RouteSnapshot) []routeExplainMatch {
	var out []routeExplainMatch
	for _, rule := range snap.Rules {
		for _, rt := range rule.Targets {
			if routeTargetMatches(target, rt) {
				out = append(out, routeExplainMatch{rule: rule, matched: rt.Value})
				break
			}
		}
	}
	return out
}

func routeTargetMatches(target string, rt wire.RouteTarget) bool {
	tv := strings.ToLower(strings.TrimSpace(target))
	rv := strings.ToLower(strings.TrimSpace(rt.Value))
	if tv == "" || rv == "" {
		return false
	}
	if tv == rv {
		return true
	}
	if isDomainTarget(rt) {
		return strings.HasSuffix(tv, "."+rv)
	}
	addr, aerr := netip.ParseAddr(tv)
	prefix, perr := netip.ParsePrefix(rv)
	if aerr == nil && perr == nil {
		return prefix.Contains(addr)
	}
	return false
}

func isDomainTarget(rt wire.RouteTarget) bool {
	if strings.EqualFold(rt.Type, "domain") {
		return true
	}
	v := strings.TrimSpace(rt.Value)
	if _, err := netip.ParseAddr(v); err == nil {
		return false
	}
	if _, err := netip.ParsePrefix(v); err == nil {
		return false
	}
	return strings.Contains(v, ".")
}

func explainKindLabel(rule wire.RouteRuleSummary) string {
	if strings.EqualFold(rule.Backend, "hydraroute") {
		return "DNS / HR-Neo"
	}
	if strings.EqualFold(rule.Kind, "static") {
		return "Static"
	}
	return strings.ToUpper(rule.Kind)
}

func describeBind(bind string, snap wire.RouteSnapshot) string {
	if strings.TrimSpace(bind) == "" {
		return "policy/default"
	}
	for _, t := range snap.Tunnels {
		if bind == t.Iface || bind == t.ID {
			return fmt.Sprintf("%s (%s)", t.Name, t.Iface)
		}
	}
	return bind
}

// RebindPreviewText renders Screen 4 with the safety "untouched" block.
func RebindPreviewText(snap wire.RouteSnapshot, srcID, dstID, token string) string {
	var src, dst *wire.TunnelMeta
	for i, t := range snap.Tunnels {
		if t.ID == srcID {
			src = &snap.Tunnels[i]
		}
		if t.ID == dstID {
			dst = &snap.Tunnels[i]
		}
	}
	if (src == nil && srcID != wire.RouteOtherID) || dst == nil {
		return "источник или назначение недоступны"
	}
	srcName := ""
	if src != nil {
		srcName = src.Name
	}
	c := snap.Counts[srcID]
	if srcID == wire.RouteOtherID {
		srcName = routeOtherSourceLabel(snap.Other)
		c = snap.Other
	}
	visible := c.DNS + c.Static
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 Превью: %s → %s\n\n", srcName, dst.Name)
	fmt.Fprintf(&b, "Будет перенесено (%d):\n", visible)
	if c.DNS > 0 {
		fmt.Fprintf(&b, "  • DNS routes: %d", c.DNS)
		if c.HRNeo > 0 {
			fmt.Fprintf(&b, " (из них HR-Neo: %d)", c.HRNeo)
		}
		b.WriteString("\n")
	}
	if c.Static > 0 {
		fmt.Fprintf(&b, "  • Static IP: %d\n", c.Static)
	}
	b.WriteString("\nНЕ ТРОГАЕМ:\n")
	if srcID != wire.RouteOtherID {
		wanTotal := snap.Other.DNS + snap.Other.Static
		if wanTotal > 0 {
			fmt.Fprintf(&b, "  • %s: %d правил\n", routeOtherSourceLabel(snap.Other), wanTotal)
		}
	}
	for _, t := range snap.Tunnels {
		if t.ID == srcID {
			continue
		}
		oc := snap.Counts[t.ID]
		ot := oc.DNS + oc.Static
		fmt.Fprintf(&b, "  • %s: %d\n", t.Name, ot)
	}
	fmt.Fprintf(&b, "\ntoken:%s  истекает через 5 мин\n", token)
	return b.String()
}

// RebindPreviewKeyboard for Screen 4.
func RebindPreviewKeyboard(userID int64, srcID, dstID, token string) InlineKeyboardMarkup {
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "✅ Подтвердить", CallbackData: fmt.Sprintf("routes_confirm:%d:%s:%s:%s", userID, srcID, dstID, token)},
		{Text: "← Отмена", CallbackData: fmt.Sprintf("routes_back:%d:_panel_", userID)},
	}}}
}

// RebindResultText renders Screen 5.
func RebindResultText(srcName, dstName string, res wire.RouteRebindResult) string {
	totalFailed := res.DNS.Failed + res.Static.Failed
	var b strings.Builder
	if totalFailed == 0 {
		fmt.Fprintf(&b, "🛣 ✅ %s → %s готово\n\n", srcName, dstName)
	} else {
		fmt.Fprintf(&b, "🛣 ⚠ %s → %s — частично\n\n", srcName, dstName)
	}
	fmt.Fprintf(&b, "  • DNS routes: %d ok", res.DNS.OK)
	if res.DNS.Failed > 0 {
		fmt.Fprintf(&b, ", %d FAIL", res.DNS.Failed)
	}
	if res.HRNeo.OK > 0 || res.HRNeo.Failed > 0 {
		fmt.Fprintf(&b, " (из них HR-Neo: %d ok", res.HRNeo.OK)
		if res.HRNeo.Failed > 0 {
			fmt.Fprintf(&b, ", %d FAIL", res.HRNeo.Failed)
		}
		b.WriteString(")")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  • Static IP: %d ok", res.Static.OK)
	if res.Static.Failed > 0 {
		fmt.Fprintf(&b, ", %d FAIL", res.Static.Failed)
	}
	b.WriteString("\n")
	if totalFailed > 0 {
		b.WriteString("\nОперация идемпотентна — можно повторить.\n")
		for _, e := range append(append([]string{}, res.DNS.Errors...), res.Static.Errors...) {
			fmt.Fprintf(&b, "  • %s\n", e)
		}
	}
	return b.String()
}

// RebindResultKeyboard for Screen 5. Shows [Repeat] only on partial fail.
func RebindResultKeyboard(userID int64, srcID, dstID string, totalFailed int) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}
	if totalFailed > 0 {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔁 Повторить", CallbackData: fmt.Sprintf("routes_pick:%d:%s:%s", userID, srcID, dstID)},
		})
	}
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🎛 Проверить тоннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
	})
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
	})
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		{Text: "Закрыть", CallbackData: fmt.Sprintf("routes_close:%d:_panel_", userID)},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

func routeOtherSourceLabel(c wire.TunnelCounts) string {
	switch {
	case c.DNS > 0 && c.Static == 0:
		return "DNS routes (WAN/system)"
	case c.Static > 0 && c.DNS == 0:
		return "Static IP routes (WAN/system)"
	case c.DNS+c.Static > 0:
		return "DNS/Static routes (WAN/system)"
	default:
		return "WAN/system"
	}
}
