package callbacks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// RoutesEditTG is the subset of tg.Client RoutesPanelNotifier uses.
type RoutesEditTG interface {
	EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error
}

// RoutesPanelNotifier handles route_status / route_rebind CommandResults
// by editing the originating panel message in place. It also keeps the
// in-memory snapshot cache fresh.
type RoutesPanelNotifier struct {
	TG    RoutesEditTG
	Cache *RoutesCache
	DB    *db.DB
	Store *RouteWizardStore
	Sink  CommandEnqueuer
}

const routeTemplatesPageSize = 8

func (n *RoutesPanelNotifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error {
	user, err := n.DB.Users().GetByID(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user lookup: %w", err)
	}
	switch ref.Action {
	case "route_status":
		return n.renderStatus(ctx, ref, res, user)
	case "tunnels_status":
		return n.renderTunnelsStatus(ctx, ref, res, user)
	case "route_rebind":
		return n.renderRebind(ctx, ref, res, user)
	case "route_templates":
		return n.renderTemplates(ctx, ref, res, user)
	case "route_add_plan":
		return n.renderAddPlan(ctx, ref, res, user)
	case "route_add", "route_delete":
		return n.renderApply(ctx, ref, res, user)
	case "route_delete_plan":
		return n.renderDeletePlan(ctx, ref, res, user)
	case "hrneo_inventory":
		return n.renderHRNeoInventory(ctx, ref, res, user)
	case "hrneo_doctor":
		return n.renderHRNeoDoctor(ctx, ref, res, user)
	default:
		return fmt.Errorf("RoutesPanelNotifier: unsupported action %q", ref.Action)
	}
}

func (n *RoutesPanelNotifier) renderTunnelsStatus(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		text := alerts.Card{
			Badge:   "❌",
			Label:   "🎛 Туннели",
			Summary: "не смог прочитать живой список с роутера",
			Meta:    []string{alerts.KV("роутер", user.Nickname), alerts.KV("команда", "tunnels_status")},
			Details: res.Output,
			Hint:    "Нажми «Обновить». Если повторяется — проверь awg-manager или запусти /check.",
		}.Render(alerts.CardOpts{MaxBytes: 3900})
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "🔄 Обновить", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", user.ID)}},
		}}
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		return fmt.Errorf("decode tunnels snapshot: %w", err)
	}
	if n.Cache != nil {
		n.Cache.Put(user.ID, snap)
	}
	text := tg.TunnelsPanelText(user.Nickname, tunnelEntriesFromRouteSnapshot(snap))
	kb := tg.TunnelsPanelKeyboard(user.ID, tunnelEntriesFromRouteSnapshot(snap))
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *RoutesPanelNotifier) renderStatus(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		text := alerts.Card{
			Badge:   "❌",
			Label:   "🛣 Маршруты",
			Summary: "awg-manager не отвечает",
			Meta:    []string{alerts.KV("роутер", user.Nickname), alerts.KV("команда", "route_status")},
			Details: res.Output,
			Hint:    "Нажми «Обновить». Если повторяется — открой HR-Neo проверку или проверку роутера.",
		}.Render(alerts.CardOpts{MaxBytes: 3900})
		kb := routeStatusErrorKeyboard(user.ID)
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	n.Cache.Put(user.ID, snap)
	text := tg.RoutesPanelText(user.Nickname, snap)
	kb := tg.RoutesPanelKeyboard(user.ID, snap)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *RoutesPanelNotifier) renderRebind(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" && res.Status != "partial" {
		text := alerts.Card{
			Badge:   "❌",
			Label:   "🛣 Маршруты",
			Summary: "ошибка переноса маршрутов",
			Meta:    []string{alerts.KV("роутер", user.Nickname), alerts.KV("команда", "route_rebind")},
			Sections: []alerts.CardSection{{
				Title: "Ответ агента",
				Lines: splitAgentOutput(res.Output),
			}},
			Hint: "Операция идемпотентна: можно вернуться к маршрутам, обновить снапшот и повторить.",
		}.Render(alerts.CardOpts{MaxBytes: 3900})
		kb := routeErrorRecoveryKeyboard(user.ID)
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	var rb wire.RouteRebindResult
	if err := json.Unmarshal([]byte(res.Output), &rb); err != nil {
		return fmt.Errorf("decode rebind result: %w", err)
	}
	// Resolve human names BEFORE invalidation (cache holds the pre-rebind snapshot).
	srcName, dstName := rb.SrcTunnelID, rb.DstTunnelID
	if rb.SrcTunnelID == wire.RouteOtherID {
		srcName = routeOtherRebindName(rb)
	}
	if snap, ok := n.Cache.Get(user.ID); ok {
		for _, t := range snap.Tunnels {
			if t.ID == rb.SrcTunnelID {
				srcName = t.Name
			}
			if t.ID == rb.DstTunnelID {
				dstName = t.Name
			}
		}
	}
	n.Cache.Invalidate(user.ID)
	totalFailed := rb.DNS.Failed + rb.Static.Failed + rb.HRNeo.Failed + len(rb.DNS.Errors) + len(rb.Static.Errors) + len(rb.HRNeo.Errors)
	text := tg.RebindResultText(srcName, dstName, rb)
	kb := tg.RebindResultKeyboard(user.ID, rb.SrcTunnelID, rb.DstTunnelID, totalFailed)
	if err := n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb); err != nil {
		return err
	}
	n.enqueueFreshRouteStatus(user.ID, ref)
	return nil
}

func routeOtherRebindName(rb wire.RouteRebindResult) string {
	dns := rb.DNS.OK + rb.DNS.Failed
	static := rb.Static.OK + rb.Static.Failed
	switch {
	case dns > 0 && static == 0:
		return "DNS routes (WAN/system)"
	case static > 0 && dns == 0:
		return "Static IP routes (WAN/system)"
	case dns+static > 0:
		return "DNS/Static routes (WAN/system)"
	default:
		return "WAN/system"
	}
}

func (n *RoutesPanelNotifier) renderTemplates(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		return n.renderRouteError(ctx, ref, user, "Route templates failed", res.Output)
	}
	var catalog wire.RouteTemplates
	if err := json.Unmarshal([]byte(res.Output), &catalog); err != nil {
		return fmt.Errorf("decode route templates: %w", err)
	}
	draftToken := tokenFromCommandID(res.ID)
	if n.Store == nil || draftToken == "" {
		return n.renderStaleTemplates(ctx, ref, user, "Превью устарело: начни добавление маршрута заново.")
	}
	draft, ok := n.Store.GetAddDraft(user.ID, ref.ThreadID, user.ID, draftToken)
	if !ok || draft.TunnelID == "" {
		return n.renderStaleTemplates(ctx, ref, user, "Черновик маршрута устарел: начни добавление заново.")
	}
	if len(routeTemplateOptionsForDraft(catalog.Templates, draft)) == 0 {
		return n.renderStaleTemplates(ctx, ref, user, "В AWG Manager нет подходящих шаблонов для выбранного типа маршрута.")
	}
	n.Store.PutTemplateCatalog(RouteTemplateCatalog{
		UserID: user.ID, ActorTGID: draft.ActorTGID, ThreadID: ref.ThreadID, RouterID: user.ID,
		DraftToken: draftToken, Templates: catalog.Templates,
	})
	return renderRouteTemplatesPage(ctx, n.TG, n.Store, ref, user, draft, draftToken, catalog.Templates, 0)
}

func (n *RoutesPanelNotifier) renderStaleTemplates(ctx context.Context, ref cmdpkg.MessageRef, user *db.User, text string) error {
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", user.ID)},
		{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", user.ID)},
	}}}
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

type routeTemplateOption struct {
	Template wire.RouteTemplate
	Targets  []string
}

func renderRouteTemplatesPage(ctx context.Context, editor RoutesEditTG, store *RouteWizardStore, ref cmdpkg.MessageRef, user *db.User, draft RouteAddDraft, draftToken string, templates []wire.RouteTemplate, page int) error {
	options := routeTemplateOptionsForDraft(templates, draft)
	if len(options) == 0 {
		text := "В AWG Manager нет подходящих шаблонов для выбранного типа маршрута."
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", user.ID)},
			{Text: "Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", user.ID)},
		}}}
		return editor.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	pages := (len(options) + routeTemplatesPageSize - 1) / routeTemplatesPageSize
	if pages <= 0 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * routeTemplatesPageSize
	end := start + routeTemplatesPageSize
	if end > len(options) {
		end = len(options)
	}
	rows := make([][]tg.InlineKeyboardButton, 0, routeTemplatesPageSize+3)
	lines := []string{
		"AWG Manager templates",
		"",
		fmt.Sprintf("Всего: %d. Страница: %d/%d.", len(options), page+1, pages),
		"Выбери шаблон: дальше покажу preview и кнопку применения.",
		"",
	}
	for _, opt := range options[start:end] {
		tpl := opt.Template
		token := store.PutTemplateToken(RouteTemplateToken{
			UserID: user.ID, ActorTGID: draft.ActorTGID, ThreadID: ref.ThreadID, RouterID: user.ID,
			DraftToken: draftToken, TemplateID: tpl.ID, TemplateName: routeTemplateDisplayName(tpl),
		})
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         routeTemplateButtonText(tpl, len(opt.Targets)),
			CallbackData: fmt.Sprintf("routes_tpl_pick:%d:_panel_:%s:%s", user.ID, draftToken, token),
		}})
		lines = append(lines, fmt.Sprintf("- %s - %d targets", routeTemplateDisplayName(tpl), len(opt.Targets)))
	}
	if pages > 1 {
		nav := []tg.InlineKeyboardButton{}
		if page > 0 {
			nav = append(nav, tg.InlineKeyboardButton{Text: "Назад", CallbackData: fmt.Sprintf("routes_tpl_page:%d:_panel_:%s:%d", user.ID, draftToken, page-1)})
		}
		if page+1 < pages {
			nav = append(nav, tg.InlineKeyboardButton{Text: "Дальше", CallbackData: fmt.Sprintf("routes_tpl_page:%d:_panel_:%s:%d", user.ID, draftToken, page+1)})
		}
		if len(nav) > 0 {
			rows = append(rows, nav)
		}
	}
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         "Обновить шаблоны",
		CallbackData: fmt.Sprintf("routes_tpl_load:%d:_panel_:%s", user.ID, draftToken),
	}})
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         "Отмена",
		CallbackData: fmt.Sprintf("routes_add_cancel:%d:_panel_:%s", user.ID, draftToken),
	}})
	return editor.EditMessageText(ctx, ref.ChatID, ref.MessageID, strings.Join(lines, "\n"), "", &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func routeTemplateOptionsForDraft(templates []wire.RouteTemplate, draft RouteAddDraft) []routeTemplateOption {
	out := make([]routeTemplateOption, 0, len(templates))
	for _, tpl := range templates {
		targets := routeTemplateTargetsForDraft(tpl, draft)
		if len(targets) == 0 {
			continue
		}
		out = append(out, routeTemplateOption{Template: tpl, Targets: targets})
	}
	return out
}

func routeTemplateTargetsForDraft(tpl wire.RouteTemplate, draft RouteAddDraft) []string {
	targets := append([]string{}, tpl.DNS...)
	if draft.UseHRNeo {
		targets = append(targets, tpl.HRNeo...)
	}
	return cleanTemplateTargetList(targets)
}

func cleanTemplateTargetList(values []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func routeTemplateButtonText(tpl wire.RouteTemplate, targets int) string {
	name := routeTemplateDisplayName(tpl)
	if targets > 0 {
		name = fmt.Sprintf("%s (%d)", name, targets)
	}
	runes := []rune(name)
	if len(runes) <= 64 {
		return name
	}
	return string(runes[:61]) + "..."
}

func routeTemplateDisplayName(tpl wire.RouteTemplate) string {
	if strings.TrimSpace(tpl.Name) != "" {
		return strings.TrimSpace(tpl.Name)
	}
	return strings.TrimSpace(tpl.ID)
}

func (n *RoutesPanelNotifier) renderAddPlan(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		return n.renderRouteError(ctx, ref, user, "Add route preview failed", res.Output)
	}
	var plan wire.RouteAddPlan
	if err := json.Unmarshal([]byte(res.Output), &plan); err != nil {
		return fmt.Errorf("decode route add plan: %w", err)
	}
	draftToken := tokenFromCommandID(res.ID)
	confirmToken := ""
	if n.Store != nil && draftToken != "" {
		if d, ok := n.Store.SetAddConfirm(user.ID, ref.ThreadID, user.ID, draftToken, plan.Hash); ok {
			confirmToken = d.ConfirmToken
		}
	}
	text := tg.RouteAddPreviewText(plan)
	if confirmToken == "" && plan.CanApply {
		text += "\n\nПревью устарело: обнови маршруты и попробуй ещё раз.\n"
		plan.CanApply = false
	}
	kb := tg.RouteAddPreviewKeyboard(user.ID, draftToken, confirmToken, plan)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *RoutesPanelNotifier) renderDeletePlan(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		return n.renderRouteError(ctx, ref, user, "Delete route preview failed", res.Output)
	}
	var plan wire.RouteDeletePlan
	if err := json.Unmarshal([]byte(res.Output), &plan); err != nil {
		return fmt.Errorf("decode route delete plan: %w", err)
	}
	draftToken := tokenFromCommandID(res.ID)
	confirmToken := ""
	if n.Store != nil && draftToken != "" {
		if d, ok := n.Store.SetDeleteConfirm(user.ID, ref.ThreadID, user.ID, draftToken, plan.Hash); ok {
			confirmToken = d.ConfirmToken
		}
	}
	text := tg.RouteDeletePreviewText(plan)
	if confirmToken == "" {
		text += "\n\nПревью устарело: обнови маршруты и попробуй ещё раз.\n"
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", user.ID)},
			{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", user.ID)},
		}}}
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	kb := tg.RouteDeletePreviewKeyboard(user.ID, draftToken, confirmToken)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *RoutesPanelNotifier) renderApply(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		return n.renderRouteError(ctx, ref, user, "Route change failed", res.Output)
	}
	var result wire.RouteApplyResult
	if err := json.Unmarshal([]byte(res.Output), &result); err != nil {
		return fmt.Errorf("decode route apply result: %w", err)
	}
	if n.Cache != nil {
		n.Cache.Invalidate(user.ID)
	}
	text := tg.RouteApplyResultText(result)
	kb := routeApplyResultKeyboard(user.ID)
	if err := n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb); err != nil {
		return err
	}
	n.enqueueFreshRouteStatus(user.ID, ref)
	return nil
}

func (n *RoutesPanelNotifier) enqueueFreshRouteStatus(userID int64, ref cmdpkg.MessageRef) {
	if n.Sink == nil {
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "route_status", IssuedAt: time.Now().UTC()}
	nextRef := cmdpkg.MessageRef{ChatID: ref.ChatID, MessageID: ref.MessageID, ThreadID: ref.ThreadID}
	if err := n.Sink.EnqueueWithRef(userID, cmd, nextRef); err != nil {
		slog.Warn("routes notifier refresh enqueue failed", "err", err, "user_id", userID)
	}
}

func routeApplyResultKeyboard(userID int64) tg.InlineKeyboardMarkup {
	return tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", userID)},
	}, {
		{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		{Text: "🌍 Проверить выход", CallbackData: fmt.Sprintf("check_via_tunnel:%d:_panel_", userID)},
	}, {
		{Text: "🛡 PingCheck", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
		{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
	}, {
		{Text: "📄 Снапшот маршрутов", CallbackData: fmt.Sprintf("routes_snapshot:%d:_panel_", userID)},
	}}}
}

func (n *RoutesPanelNotifier) renderHRNeoInventory(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		return n.renderRouteError(ctx, ref, user, "HR-Neo inventory failed", res.Output)
	}
	var inv wire.HRNeoInventory
	if err := json.Unmarshal([]byte(res.Output), &inv); err != nil {
		return fmt.Errorf("decode hrneo inventory: %w", err)
	}
	text := tg.HRNeoInventoryText(user.Nickname, inv)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{
			{Text: "▶ Запустить HR-Neo", CallbackData: fmt.Sprintf("maint_restart:%d:hrneo_start", user.ID)},
			{Text: "⏹ Остановить HR-Neo", CallbackData: fmt.Sprintf("maint_restart:%d:hrneo_stop", user.ID)},
		},
		{
			{Text: "🔁 Перезапустить HR-Neo", CallbackData: fmt.Sprintf("maint_restart:%d:hrneo", user.ID)},
		},
		{
			{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", user.ID)},
			{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_hrneo:%d:_panel_", user.ID)},
		},
	}}
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *RoutesPanelNotifier) renderHRNeoDoctor(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	text := strings.TrimSpace(res.Output)
	if text == "" {
		text = "HR-Neo проверка вернула пустой ответ."
	}
	status := "готово"
	badge := "🩺"
	if res.Status != "ok" {
		status = "ошибка проверки"
		badge = "❌"
	}
	text = alerts.Card{
		Badge:   badge,
		Label:   "HR-Neo проверка",
		Summary: status,
		Meta:    []string{alerts.KV("роутер", user.Nickname), alerts.KV("статус", res.Status)},
		Details: text,
	}.Render(alerts.CardOpts{MaxBytes: 3900})
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{
			{Text: "HR-Neo правила", CallbackData: fmt.Sprintf("routes_hrneo:%d:_panel_", user.ID)},
			{Text: "🔁 Повторить", CallbackData: fmt.Sprintf("routes_hrneo_doctor:%d:_panel_", user.ID)},
		},
		{
			{Text: "🔁 Перезапустить HR-Neo", CallbackData: fmt.Sprintf("maint_restart:%d:hrneo", user.ID)},
		},
		{
			{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", user.ID)},
		},
	}}
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *RoutesPanelNotifier) renderRouteError(ctx context.Context, ref cmdpkg.MessageRef, user *db.User, title, body string) error {
	text := alerts.Card{
		Badge:   "❌",
		Label:   "🛣 Маршруты",
		Summary: humanRouteErrorTitle(title),
		Meta:    []string{alerts.KV("роутер", user.Nickname)},
		Sections: []alerts.CardSection{{
			Title: "Ответ агента",
			Lines: splitAgentOutput(body),
		}},
		Hint: "Вернись к маршрутам, обнови снапшот и повтори действие.",
	}.Render(alerts.CardOpts{MaxBytes: 3900})
	kb := routeErrorRecoveryKeyboard(user.ID)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func routeStatusErrorKeyboard(userID int64) tg.InlineKeyboardMarkup {
	return tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", userID)},
	}, {
		{Text: "HR-Neo проверка", CallbackData: fmt.Sprintf("routes_hrneo_doctor:%d:_panel_", userID)},
		{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
	}, {
		{Text: "🛠 Обслуживание", CallbackData: fmt.Sprintf("maint_open:%d:_panel_", userID)},
	}}}
}

func routeErrorRecoveryKeyboard(userID int64) tg.InlineKeyboardMarkup {
	return tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", userID)},
	}, {
		{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
		{Text: "🛠 Обслуживание", CallbackData: fmt.Sprintf("maint_open:%d:_panel_", userID)},
	}}}
}

func humanRouteErrorTitle(title string) string {
	switch title {
	case "Add route preview failed":
		return "не удалось подготовить превью добавления"
	case "Delete route preview failed":
		return "не удалось подготовить превью удаления"
	case "Route change failed":
		return "не удалось применить изменение маршрута"
	case "HR-Neo inventory failed":
		return "не удалось прочитать HR-Neo правила"
	}
	return title
}

func splitAgentOutput(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return []string{"пустой ответ"}
	}
	return out
}

func tokenFromCommandID(id string) string {
	parts := strings.Split(id, ":")
	if len(parts) >= 3 {
		return parts[1]
	}
	return ""
}
