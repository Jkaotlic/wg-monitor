package tg

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/anex/wg-monitor/pkg/wire"
)

// RouteAddPreviewText renders a route_add_plan payload. The payload is
// intentionally accepted structurally so this package can compile before the
// planned pkg/wire route add/delete structs land.
func RouteAddPreviewText(plan any) string {
	p := readRouteAddPlan(plan)
	var b strings.Builder
	b.WriteString("🛣 Превью маршрута\n\n")
	b.WriteString("Сводка:\n")
	fmt.Fprintf(&b, "  • Тип маршрута: %s\n", addTypeLabel(p))
	fmt.Fprintf(&b, "  • Название: %s\n", fallback(p.name, "-"))
	fmt.Fprintf(&b, "  • Куда вести: %s\n", fallback(p.bind, "-"))
	writeTargets(&b, p.targets)
	writeOverlapSection(&b, "Blocking overlaps:", p.overlaps, "block")
	writeOverlapSection(&b, "Warnings:", p.overlaps, "warn")
	writeOverlapSection(&b, "Notes:", p.overlaps, "info")
	if hasSeverity(p.overlaps, "block") || !p.canApply {
		b.WriteString("\nДальше:\n  • выбери другой туннель, измени цели или отмени действие\n")
	} else {
		b.WriteString("\nДальше:\n  • подтверди добавление или отмени действие\n")
	}
	return b.String()
}

// RouteAddPreviewKeyboard builds confirm/cancel controls for route_add_plan.
// Confirm is hidden when a blocking overlap is present or CanApply is false.
func RouteAddPreviewKeyboard(userID int64, draftToken, confirmToken string, plan any) InlineKeyboardMarkup {
	p := readRouteAddPlan(plan)
	rows := [][]InlineKeyboardButton{}
	if p.canApply && !hasSeverity(p.overlaps, "block") {
		rows = append(rows, []InlineKeyboardButton{{
			Text:         "✅ Подтвердить",
			CallbackData: fmt.Sprintf("routes_add_confirm:%d:_panel_:%s:%s", userID, draftToken, confirmToken),
		}})
	}
	rows = append(rows, []InlineKeyboardButton{{
		Text:         "↩ Отмена",
		CallbackData: fmt.Sprintf("routes_add_cancel:%d:_panel_:%s", userID, draftToken),
	}})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

// RouteDeletePreviewText renders a route_delete_plan payload.
func RouteDeletePreviewText(plan any) string {
	p := readRouteDeletePlan(plan)
	var b strings.Builder
	b.WriteString("🛣 Превью удаления\n\n")
	b.WriteString("Правило:\n")
	fmt.Fprintf(&b, "  • Название: %s\n", fallback(p.route.name, "-"))
	fmt.Fprintf(&b, "  • Тип: %s\n", fallback(p.route.kind, "-"))
	fmt.Fprintf(&b, "  • Движок: %s\n", fallback(p.route.backend, "-"))
	fmt.Fprintf(&b, "  • Привязка: %s\n", fallback(p.route.bind, "-"))
	enabled := "нет"
	if p.route.enabled {
		enabled = "да"
	}
	fmt.Fprintf(&b, "  • Включено: %s\n", enabled)
	writeTargets(&b, p.route.targets)
	writeOverlapSection(&b, "Warnings:", p.warnings, "warn")
	writeOverlapSection(&b, "Notes:", p.warnings, "info")
	b.WriteString("\nДальше:\n  • подтверди удаление только этого правила или отмени действие\n")
	return b.String()
}

// RouteDeletePreviewKeyboard builds confirm/cancel controls for route_delete_plan.
func RouteDeletePreviewKeyboard(userID int64, draftToken, confirmToken string) InlineKeyboardMarkup {
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{
			Text:         "✅ Удалить",
			CallbackData: fmt.Sprintf("routes_del_confirm:%d:_panel_:%s:%s", userID, draftToken, confirmToken),
		}},
		{{
			Text:         "↩ Отмена",
			CallbackData: fmt.Sprintf("routes_del_cancel:%d:_panel_:%s", userID, draftToken),
		}},
	}}
}

// RouteApplyResultText renders route_add / route_delete apply results.
func RouteApplyResultText(result any) string {
	v := valueOf(result)
	action := fallback(stringField(v, "Action"), "route")
	var b strings.Builder
	fmt.Fprintf(&b, "✅ %s\n\n", humanRouteActionDone(action))
	b.WriteString("Итог:\n")
	fmt.Fprintf(&b, "  • Тип: %s\n", fallback(stringField(v, "Kind"), "-"))
	fmt.Fprintf(&b, "  • Название: %s\n", fallback(stringField(v, "RouteName"), "-"))
	fmt.Fprintf(&b, "  • ID: %s\n", fallback(stringField(v, "RouteID"), "-"))
	if boolField(v, "HRNeoRestarted") {
		b.WriteString("  • HR-Neo перезапущен: да\n")
	}
	return b.String()
}

func humanRouteActionDone(action string) string {
	switch action {
	case "add":
		return "Маршрут добавлен"
	case "delete":
		return "Маршрут удалён"
	}
	return "Маршрут обновлён"
}

func HRNeoInventoryText(nickname string, inv wire.HRNeoInventory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 HR-Neo правила — %s\n", nickname)
	state := "не установлен"
	if inv.Status.Installed && inv.Status.Running {
		state = "установлен и работает"
	} else if inv.Status.Installed {
		state = "установлен, но остановлен"
	}
	fmt.Fprintf(&b, "\nСводка:\n")
	fmt.Fprintf(&b, "  • State: %s\n", state)
	fmt.Fprintf(&b, "  • Rules: %d\n", len(inv.Rules))
	if len(inv.Rules) == 0 {
		b.WriteString("\nHR-Neo маршрутов не найдено.\n")
		return b.String()
	}
	b.WriteString("\nПравила:\n")
	for _, rule := range inv.Rules {
		fmt.Fprintf(&b, "  • %s\n", fallback(rule.Name, rule.ID))
		fmt.Fprintf(&b, "  ID: %s\n", rule.ID)
		fmt.Fprintf(&b, "  Привязка: %s\n", fallback(rule.Bind, "policy/default"))
		if rule.PolicyName != "" {
			fmt.Fprintf(&b, "  Политика: %s\n", rule.PolicyName)
		}
		if len(rule.Domains) > 0 {
			fmt.Fprintf(&b, "  Домены: %s\n", strings.Join(rule.Domains, ", "))
		}
		if len(rule.ManualDomains) > 0 {
			fmt.Fprintf(&b, "  Вручную: %s\n", strings.Join(rule.ManualDomains, ", "))
		}
		if len(rule.Routes) > 0 {
			fmt.Fprintf(&b, "  CIDR/IP: %s\n", strings.Join(rule.Routes, ", "))
		}
	}
	return b.String()
}

type routeAddPlanView struct {
	kind     string
	name     string
	bind     string
	useHRNeo bool
	targets  []string
	overlaps []routeOverlapView
	canApply bool
}

type routeDeletePlanView struct {
	route    routeRuleView
	warnings []routeOverlapView
	canApply bool
}

type routeRuleView struct {
	id      string
	name    string
	kind    string
	backend string
	enabled bool
	bind    string
	targets []string
}

type routeOverlapView struct {
	severity string
	reason   string
	target   string
	existing routeRuleView
}

func readRouteAddPlan(plan any) routeAddPlanView {
	v := valueOf(plan)
	req := field(v, "Request")
	route := readRouteRule(field(v, "Route"))
	p := routeAddPlanView{
		kind:     firstNonEmpty(stringField(req, "Kind"), route.kind),
		name:     firstNonEmpty(stringField(req, "Name"), route.name),
		bind:     firstNonEmpty(stringField(req, "TunnelID"), route.bind),
		useHRNeo: boolField(req, "UseHRNeo") || strings.EqualFold(route.backend, "hydraroute"),
		targets:  stringSliceField(req, "Targets"),
		overlaps: readOverlaps(field(v, "Overlaps")),
		canApply: boolField(v, "CanApply"),
	}
	if len(p.targets) == 0 {
		p.targets = route.targets
	}
	return p
}

func readRouteDeletePlan(plan any) routeDeletePlanView {
	v := valueOf(plan)
	return routeDeletePlanView{
		route:    readRouteRule(field(v, "Route")),
		warnings: readOverlaps(field(v, "Warnings")),
		canApply: boolField(v, "CanApply"),
	}
}

func readRouteRule(v reflect.Value) routeRuleView {
	return routeRuleView{
		id:      stringField(v, "ID"),
		name:    stringField(v, "Name"),
		kind:    stringField(v, "Kind"),
		backend: stringField(v, "Backend"),
		enabled: boolField(v, "Enabled"),
		bind:    stringField(v, "Bind"),
		targets: targetValues(field(v, "Targets")),
	}
}

func readOverlaps(v reflect.Value) []routeOverlapView {
	v = deref(v)
	if !v.IsValid() || v.Kind() != reflect.Slice {
		return nil
	}
	out := make([]routeOverlapView, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		item := valueOf(v.Index(i).Interface())
		out = append(out, routeOverlapView{
			severity: stringField(item, "Severity"),
			reason:   stringField(item, "Reason"),
			target:   firstNonEmpty(stringField(field(item, "Target"), "Value"), stringField(field(item, "Target"), "value")),
			existing: readRouteRule(field(item, "Existing")),
		})
	}
	return out
}

func addTypeLabel(p routeAddPlanView) string {
	switch strings.ToLower(p.kind) {
	case "dns":
		if p.useHRNeo {
			return "DNS / HR-Neo"
		}
		return "DNS"
	case "static":
		return "Static CIDR"
	default:
		return fallback(p.kind, "-")
	}
}

func writeTargets(b *strings.Builder, targets []string) {
	b.WriteString("\nЧто маршрутизируется:\n")
	if len(targets) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, target := range targets {
		fmt.Fprintf(b, "- %s\n", target)
	}
}

func writeOverlapSection(b *strings.Builder, title string, overlaps []routeOverlapView, severity string) {
	var lines []string
	for _, overlap := range overlaps {
		if !strings.EqualFold(overlap.severity, severity) {
			continue
		}
		line := overlap.reason
		if line == "" {
			line = overlap.target
			if overlap.existing.bind != "" {
				line += " already goes to " + overlap.existing.bind
			}
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", humanOverlapTitle(title))
	for _, line := range lines {
		fmt.Fprintf(b, "- %s\n", line)
	}
}

func humanOverlapTitle(title string) string {
	switch title {
	case "Blocking overlaps:":
		return "Блокирует добавление:"
	case "Warnings:":
		return "Предупреждения:"
	case "Notes:":
		return "Заметки:"
	}
	return title
}

func hasSeverity(overlaps []routeOverlapView, severity string) bool {
	for _, overlap := range overlaps {
		if strings.EqualFold(overlap.severity, severity) {
			return true
		}
	}
	return false
}

func targetValues(v reflect.Value) []string {
	v = deref(v)
	if !v.IsValid() || v.Kind() != reflect.Slice {
		return nil
	}
	out := make([]string, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		item := valueOf(v.Index(i).Interface())
		if item.Kind() == reflect.String {
			out = append(out, item.String())
			continue
		}
		if value := stringField(item, "Value"); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func stringSliceField(v reflect.Value, name string) []string {
	f := field(v, name)
	f = deref(f)
	if !f.IsValid() || f.Kind() != reflect.Slice {
		return nil
	}
	out := make([]string, 0, f.Len())
	for i := 0; i < f.Len(); i++ {
		item := valueOf(f.Index(i).Interface())
		if item.Kind() == reflect.String {
			out = append(out, item.String())
		}
	}
	return out
}

func stringField(v reflect.Value, name string) string {
	f := field(v, name)
	f = deref(f)
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}

func boolField(v reflect.Value, name string) bool {
	f := field(v, name)
	f = deref(f)
	if !f.IsValid() || f.Kind() != reflect.Bool {
		return false
	}
	return f.Bool()
}

func field(v reflect.Value, name string) reflect.Value {
	v = deref(v)
	if !v.IsValid() {
		return reflect.Value{}
	}
	switch v.Kind() {
	case reflect.Struct:
		f := v.FieldByName(name)
		if f.IsValid() {
			return f
		}
	case reflect.Map:
		if v.Type().Key().Kind() == reflect.String {
			for _, key := range []string{name, strings.ToLower(name)} {
				f := v.MapIndex(reflect.ValueOf(key))
				if f.IsValid() {
					return f
				}
			}
		}
	}
	return reflect.Value{}
}

func valueOf(x any) reflect.Value {
	if x == nil {
		return reflect.Value{}
	}
	if v, ok := x.(reflect.Value); ok {
		return deref(v)
	}
	return deref(reflect.ValueOf(x))
}

func deref(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func fallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
