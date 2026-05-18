package tg

import (
	"fmt"
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
	if snap.HRNeo.Installed {
		state := "✅ установлен, работает"
		if !snap.HRNeo.Running {
			state = "⚠ установлен, остановлен"
		}
		fmt.Fprintf(&b, "HydraRoute Neo: %s\n", state)
	}
	totalDNS := snap.Other.DNS
	totalStatic := snap.Other.Static
	totalHR := snap.Other.HRNeo
	for _, c := range snap.Counts {
		totalDNS += c.DNS
		totalStatic += c.Static
		totalHR += c.HRNeo
	}
	fmt.Fprintf(&b, "DNS routes: %d правил\n", totalDNS)
	fmt.Fprintf(&b, "Static IP routes: %d правил\n", totalStatic)
	if snap.HRNeo.Installed {
		fmt.Fprintf(&b, "из них HR-Neo: %d\n", totalHR)
	}
	b.WriteString("\nПо туннелям (направленные в туннели):\n")
	for _, t := range snap.Tunnels {
		c := snap.Counts[t.ID]
		visible := c.DNS + c.Static
		fmt.Fprintf(&b, "  • %s (%s): %d правил\n", t.Name, t.Iface, visible)
	}
	b.WriteString("\nНе входят в перенос (показано для контроля):\n")
	wanTotal := snap.Other.DNS + snap.Other.Static
	fmt.Fprintf(&b, "  • WAN/system: %d правил ← RU-сервисы\n", wanTotal)
	return b.String()
}

// RoutesPanelKeyboard builds Screen 2 inline keyboard.
//
// callback_data shape:
//
//	routes_rebind:<userID>:<src_tunnel_id>
//	routes_refresh:<userID>:_panel_
//	routes_close:0:_panel_
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
	rows = append(rows, []InlineKeyboardButton{
		{Text: "Add route", CallbackData: fmt.Sprintf("routes_add:%d:_panel_", userID)},
		{Text: "Delete route", CallbackData: fmt.Sprintf("routes_del:%d:_panel_:_list_", userID)},
	})
	if snap.HRNeo.Installed {
		rows = append(rows, []InlineKeyboardButton{{
			Text:         "HR-Neo rules",
			CallbackData: fmt.Sprintf("routes_hrneo:%d:_panel_", userID),
		}})
	}
	rows = append(rows, HelpRowFor("routes"))
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", userID)},
		{Text: "Закрыть", CallbackData: "routes_close:0:_panel_"},
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
	if src == nil {
		return "источник недоступен", InlineKeyboardMarkup{}
	}
	text := fmt.Sprintf("🛣 Перенос с %s (%s) → куда?\n\nДоступные:", src.Name, src.Iface)
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
	if src == nil || dst == nil {
		return "источник или назначение недоступны"
	}
	c := snap.Counts[srcID]
	visible := c.DNS + c.Static
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 Превью: %s → %s\n\n", src.Name, dst.Name)
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
	wanTotal := snap.Other.DNS + snap.Other.Static
	fmt.Fprintf(&b, "  • WAN/system: %d правил ← RU-сервисы\n", wanTotal)
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
		{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		{Text: "Закрыть", CallbackData: "routes_close:0:_panel_"},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}
