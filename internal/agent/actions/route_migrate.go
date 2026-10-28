package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Перенос статического маршрута NDMS в правило HydraRoute Neo (оператор
// 28.10.2026: «если включен гидра роут -- все туда»). Новые правила агент и так
// заводит в Neo; это -- для уже заведённых раньше.
//
// Порядок -- сначала новое правило, потом удаление старого, и удаление только
// после того, как новое нашлось в живом списке роутера: сеть ни на миг не
// остаётся без маршрута, а неподтверждённая замена не стоит ей маршрута вовсе.

func RouteMigratePlanJSON(ctx context.Context, c *awgmgr.Client, req wire.RouteMigrateRequest) (string, error) {
	plan, _, _, err := prepareRouteMigrate(ctx, c, req)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildRouteMigratePlan(ctx context.Context, c *awgmgr.Client, req wire.RouteMigrateRequest) (wire.RouteMigratePlan, error) {
	plan, _, _, err := prepareRouteMigrate(ctx, c, req)
	return plan, err
}

func RouteMigrateJSON(ctx context.Context, c *awgmgr.Client, req wire.RouteMigrateRequest) (string, error) {
	plan, static, planEP, err := prepareRouteMigrate(ctx, c, req)
	if err != nil {
		return "", err
	}
	if req.PreviewHash != "" && req.PreviewHash != plan.Hash {
		return "", fmt.Errorf("route_migrate: preview changed, refresh and try again")
	}
	if !plan.CanApply {
		return "", fmt.Errorf("route_migrate: blocking overlap found")
	}
	if err := c.RoutingRefresh(ctx); err != nil {
		return "", fmt.Errorf("route_migrate: refresh routing before create: %w", err)
	}
	// Привязка перечитывается после refresh по УСТОЙЧИВОМУ id туннеля (awg12,
	// Wireguard0): интерфейс opkg-туннеля плавает, и правило обязано встать на
	// нынешний, а имя интерфейса в самой статике к этому моменту может уже
	// ничего не значить.
	ep, err := resolveRouteEndpoint(ctx, c, routeDNSBindID(planEP))
	if err != nil {
		ep, err = resolveStaticEndpoint(ctx, c, static.TunnelID)
	}
	if err != nil {
		return "", fmt.Errorf("route_migrate: resolve route target: %w", err)
	}
	rule := hrNeoPolicyRoute(static.Name, cleanRouteTargets(static.Subnets), ep.Iface, routeDNSBindID(ep))
	rule.Enabled = static.Enabled
	createErr := c.CreateDNSRoute(ctx, rule)
	// Сверка -- с тем правилом, что реально ушло на роутер: привязка взята
	// после refresh, и правило из превью могло стоять на прежнем интерфейсе.
	expected := plan.Route
	expected.Bind = ep.Iface
	created, found := reconcileAppliedRouteAdd(ctx, c, expected)
	if createErr != nil && !found {
		return "", fmt.Errorf("route_migrate: create HydraRoute Neo rule: %w", createErr)
	}
	res := wire.RouteApplyResult{Action: "migrate", Kind: "dns", RouteName: static.Name}
	if !found {
		// Создание ответило успехом, но правила в живом списке нет. Удалить
		// статику сейчас -- значит оставить сеть без маршрута.
		res.RouteID = plan.Route.ID
		res.NotApplied = true
		res.Warning = "HydraRoute Neo rule not confirmed in the live route list; static route kept"
	} else {
		res.RouteID = created.ID
		if createErr != nil {
			res.Warning = "create response failed; reconciled from live route list"
		}
		if err := c.DeleteStaticRoute(ctx, static.ID); err != nil && !reconcileAppliedRouteDelete(ctx, c, "static", static.ID) {
			res.Warning = appendRouteWarning(res.Warning, "HydraRoute Neo rule created, but the old static route could not be deleted: "+err.Error())
		}
	}
	hrRestarted, refreshErr := refreshRoutingAndMaybeHR(ctx, c, true)
	res.HRNeoRestarted = hrRestarted
	if refreshErr != nil {
		res.Warning = appendRouteWarning(res.Warning, "post-change refresh failed: "+refreshErr.Error())
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func prepareRouteMigrate(ctx context.Context, c *awgmgr.Client, req wire.RouteMigrateRequest) (wire.RouteMigratePlan, awgmgr.StaticRoute, routeEndpoint, error) {
	req.RouteID = strings.TrimSpace(req.RouteID)
	if req.RouteID == "" {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, fmt.Errorf("route_migrate_plan: route_id is required")
	}
	if c == nil {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, fmt.Errorf("route_migrate_plan: awg-manager client is required")
	}
	if s, serr := c.Settings(ctx); serr == nil && s.SingboxRouterActive() {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, fmt.Errorf("route_migrate: router routes via sing-box (deviceMode %q) — HR-Neo routes do not take effect here", s.SingboxRouter.DeviceMode)
	}
	if !hrNeoActive(ctx, c) {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, fmt.Errorf("route_migrate_plan: HydraRoute Neo is not running on this router — nothing to move the route into")
	}
	statics, err := c.ListStaticRoutes(ctx)
	if err != nil {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, err
	}
	var static *awgmgr.StaticRoute
	for i := range statics {
		if statics[i].ID == req.RouteID {
			static = &statics[i]
			break
		}
	}
	if static == nil {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, fmt.Errorf("route_migrate_plan: static route %s not found", req.RouteID)
	}
	targets := cleanRouteTargets(static.Subnets)
	if len(targets) == 0 {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, fmt.Errorf("route_migrate_plan: static route %s has no subnets", req.RouteID)
	}
	ep, err := resolveStaticEndpoint(ctx, c, static.TunnelID)
	if err != nil {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, fmt.Errorf("resolve route target: %w", err)
	}
	existing, err := liveRouteSummaries(ctx, c)
	if err != nil {
		return wire.RouteMigratePlan{}, awgmgr.StaticRoute{}, routeEndpoint{}, err
	}
	// Сам переносимый маршрут не спорит с собой: он уйдёт тем же действием.
	others := existing[:0:0]
	var from wire.RouteRuleSummary
	for _, rule := range existing {
		if rule.Kind == "static" && rule.ID == static.ID {
			from = rule
			continue
		}
		others = append(others, rule)
	}
	route := normalizeRouteRuleSummary(wire.RouteRuleSummary{
		ID:      "new:dns:" + static.Name,
		Name:    static.Name,
		Kind:    "dns",
		Backend: "hydraroute",
		Enabled: static.Enabled,
		Bind:    ep.Iface,
		Targets: rawTargets(targets),
	})
	overlaps := classifyRouteOverlaps(route, others)
	canApply := true
	for _, o := range overlaps {
		if o.Severity == "block" {
			canApply = false
			break
		}
	}
	plan := wire.RouteMigratePlan{From: from, Route: route, Overlaps: overlaps, CanApply: canApply}
	plan.Hash = routeHash(struct {
		From  wire.RouteRuleSummary
		Route wire.RouteRuleSummary
	}{plan.From, plan.Route})
	return plan, *static, ep, nil
}

// resolveStaticEndpoint -- туннель статического маршрута по его привязке.
// Статика хранит ndms-написание интерфейса («OpkgTun12»), а каталог
// маршрутизации -- ядерное («opkgtun12»): это одно имя, и точное сравнение
// resolveRouteEndpoint его бы не узнало.
func resolveStaticEndpoint(ctx context.Context, c *awgmgr.Client, bind string) (routeEndpoint, error) {
	ep, err := resolveRouteEndpoint(ctx, c, bind)
	if err == nil {
		return ep, nil
	}
	routing, rerr := c.RoutingTunnels(ctx)
	if rerr != nil {
		return routeEndpoint{}, err
	}
	for _, t := range routing {
		cand := ndmsRouteEndpoint(t)
		if strings.EqualFold(cand.Iface, bind) || strings.EqualFold(cand.ID, bind) || strings.EqualFold(t.ID, bind) {
			return resolveRouteEndpoint(ctx, c, t.ID)
		}
	}
	return routeEndpoint{}, err
}
