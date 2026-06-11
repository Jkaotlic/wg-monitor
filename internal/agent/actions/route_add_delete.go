package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

func RouteAddPlanJSON(ctx context.Context, c *awgmgr.Client, req wire.RouteAddRequest) (string, error) {
	plan, err := buildRouteAddPlan(ctx, c, req)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func RouteAddJSON(ctx context.Context, c *awgmgr.Client, req wire.RouteAddRequest) (string, error) {
	plan, err := buildRouteAddPlan(ctx, c, req)
	if err != nil {
		return "", err
	}
	req = plan.Request
	if req.DraftHash != "" && req.DraftHash != plan.Hash {
		return "", fmt.Errorf("route_add: preview changed, refresh and try again")
	}
	if !plan.CanApply {
		return "", fmt.Errorf("route_add: blocking overlap found")
	}
	if err := c.RoutingRefresh(ctx); err != nil {
		return "", fmt.Errorf("route_add: refresh routing before create: %w", err)
	}
	t, err := resolveRouteEndpoint(ctx, c, req.TunnelID)
	if err != nil {
		return "", fmt.Errorf("resolve route target: %w", err)
	}
	hrTouched := false
	routeID := plan.Route.ID
	switch strings.ToLower(req.Kind) {
	case "dns":
		dnsBindID := routeDNSBindID(t)
		rule := awgmgr.DNSRoute{
			Name:          req.Name,
			Domains:       req.Targets,
			ManualDomains: req.Targets,
			Enabled:       true,
			Backend:       "ndms",
			Routes:        []awgmgr.DNSRouteEntry{{Interface: t.Iface, TunnelID: dnsBindID, Fallback: "auto"}},
		}
		if req.UseHRNeo {
			rule = hrNeoPolicyRoute(req.Name, req.Targets, t.Iface)
			hrTouched = true
		}
		if err := c.CreateDNSRoute(ctx, rule); err != nil {
			return "", err
		}
	case "static":
		rule := awgmgr.StaticRoute{Name: req.Name, TunnelID: t.Iface, Subnets: req.Targets, Fallback: "auto", Enabled: true}
		if err := c.CreateStaticRoute(ctx, rule); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("route_add: unknown kind %q", req.Kind)
	}
	hrRestarted, refreshErr := refreshRoutingAndMaybeHR(ctx, c, hrTouched)
	if refreshErr != nil {
		return "", refreshErr
	}
	res := wire.RouteApplyResult{Action: "add", Kind: req.Kind, RouteID: routeID, RouteName: req.Name, HRNeoRestarted: hrRestarted}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func RouteTemplatesJSON(ctx context.Context, c *awgmgr.Client) (string, error) {
	if c == nil {
		return "", fmt.Errorf("route_templates: awg-manager client is required")
	}
	presets, err := c.Presets(ctx)
	if err != nil {
		return "", fmt.Errorf("route_templates: presets: %w", err)
	}
	templates := wire.RouteTemplates{Templates: make([]wire.RouteTemplate, 0, len(presets))}
	for _, preset := range presets {
		id := strings.TrimSpace(preset.ID)
		if id == "" {
			continue
		}
		dns := cleanRouteTargets(preset.Engines.DNS.Domains)
		hr := cleanRouteTargets(preset.Engines.HydraRoute.GeoTags)
		if len(dns) == 0 && len(hr) == 0 {
			continue
		}
		name := strings.TrimSpace(preset.Name)
		if name == "" {
			name = id
		}
		templates.Templates = append(templates.Templates, wire.RouteTemplate{
			ID:       id,
			Name:     name,
			Category: strings.TrimSpace(preset.Category),
			DNS:      dns,
			HRNeo:    hr,
		})
	}
	b, err := json.Marshal(templates)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func RouteDeletePlanJSON(ctx context.Context, c *awgmgr.Client, req wire.RouteDeleteRequest) (string, error) {
	plan, err := buildRouteDeletePlan(ctx, c, req)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func RouteDeleteJSON(ctx context.Context, c *awgmgr.Client, req wire.RouteDeleteRequest) (string, error) {
	plan, err := buildRouteDeletePlan(ctx, c, req)
	if err != nil {
		return "", err
	}
	if req.PreviewHash != "" && req.PreviewHash != plan.Hash {
		return "", fmt.Errorf("route_delete: preview changed, refresh and try again")
	}
	hrTouched := strings.EqualFold(plan.Route.Backend, "hydraroute")
	switch strings.ToLower(req.Kind) {
	case "dns":
		if err := c.DeleteDNSRoute(ctx, req.RouteID); err != nil {
			return "", err
		}
	case "static":
		if err := c.DeleteStaticRoute(ctx, req.RouteID); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("route_delete: unknown kind %q", req.Kind)
	}
	hrRestarted, refreshErr := refreshRoutingAndMaybeHR(ctx, c, hrTouched)
	if refreshErr != nil {
		return "", refreshErr
	}
	res := wire.RouteApplyResult{Action: "delete", Kind: req.Kind, RouteID: plan.Route.ID, RouteName: plan.Route.Name, HRNeoRestarted: hrRestarted}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildRouteAddPlan(ctx context.Context, c *awgmgr.Client, req wire.RouteAddRequest) (wire.RouteAddPlan, error) {
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.Name = strings.TrimSpace(req.Name)
	req.TunnelID = strings.TrimSpace(req.TunnelID)
	req.TemplateID = strings.TrimSpace(req.TemplateID)
	req.Targets = cleanRouteTargets(req.Targets)
	if req.TemplateID != "" {
		var err error
		req, err = materializeRouteTemplate(ctx, c, req)
		if err != nil {
			return wire.RouteAddPlan{}, err
		}
	}
	if req.Kind != "dns" && req.Kind != "static" {
		return wire.RouteAddPlan{}, fmt.Errorf("route_add_plan: kind must be dns or static")
	}
	if req.Name == "" || req.TunnelID == "" || len(req.Targets) == 0 {
		return wire.RouteAddPlan{}, fmt.Errorf("route_add_plan: name, tunnel_id and targets are required")
	}
	if req.Kind == "static" {
		for _, target := range req.Targets {
			if normalizeRouteTarget(target).Type != "cidr" {
				return wire.RouteAddPlan{}, fmt.Errorf("route_add_plan: static target %q must be CIDR or IP", target)
			}
		}
	}
	t, err := resolveRouteEndpoint(ctx, c, req.TunnelID)
	if err != nil {
		return wire.RouteAddPlan{}, fmt.Errorf("resolve route target: %w", err)
	}
	existing, err := liveRouteSummaries(ctx, c)
	if err != nil {
		return wire.RouteAddPlan{}, err
	}
	route := wire.RouteRuleSummary{
		ID:      "new:" + req.Kind + ":" + req.Name,
		Name:    req.Name,
		Kind:    req.Kind,
		Enabled: true,
		Bind:    t.Iface,
		Targets: rawTargets(req.Targets),
	}
	if req.Kind == "dns" {
		route.Backend = "ndms"
		if req.UseHRNeo {
			route.Backend = "hydraroute"
		}
	}
	overlaps := classifyRouteOverlaps(route, existing)
	canApply := true
	for _, o := range overlaps {
		if o.Severity == "block" {
			canApply = false
			break
		}
	}
	route = normalizeRouteRuleSummary(route)
	plan := wire.RouteAddPlan{Request: req, Route: route, Overlaps: overlaps, CanApply: canApply}
	plan.Hash = routeHash(plan.Route)
	return plan, nil
}

func materializeRouteTemplate(ctx context.Context, c *awgmgr.Client, req wire.RouteAddRequest) (wire.RouteAddRequest, error) {
	if c == nil {
		return req, fmt.Errorf("route_add_plan: awg-manager client is required for template %q", req.TemplateID)
	}
	presets, err := c.Presets(ctx)
	if err != nil {
		return req, fmt.Errorf("route_add_plan: presets: %w", err)
	}
	var preset *awgmgr.Preset
	for i := range presets {
		if strings.EqualFold(strings.TrimSpace(presets[i].ID), req.TemplateID) {
			preset = &presets[i]
			break
		}
	}
	if preset == nil {
		return req, fmt.Errorf("route_add_plan: template %q not found", req.TemplateID)
	}
	if req.Kind == "" {
		req.Kind = "dns"
	}
	if req.Name == "" {
		req.Name = strings.TrimSpace(preset.Name)
		if req.Name == "" {
			req.Name = preset.ID
		}
	}
	targets := append([]string{}, req.Targets...)
	targets = append(targets, preset.Engines.DNS.Domains...)
	if req.UseHRNeo {
		targets = append(targets, preset.Engines.HydraRoute.GeoTags...)
	}
	req.Targets = cleanRouteTargets(targets)
	return req, nil
}

func buildRouteDeletePlan(ctx context.Context, c *awgmgr.Client, req wire.RouteDeleteRequest) (wire.RouteDeletePlan, error) {
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.RouteID = strings.TrimSpace(req.RouteID)
	if req.Kind != "dns" && req.Kind != "static" {
		return wire.RouteDeletePlan{}, fmt.Errorf("route_delete_plan: kind must be dns or static")
	}
	if req.RouteID == "" {
		return wire.RouteDeletePlan{}, fmt.Errorf("route_delete_plan: route_id is required")
	}
	rules, err := liveRouteSummaries(ctx, c)
	if err != nil {
		return wire.RouteDeletePlan{}, err
	}
	for _, rule := range rules {
		if rule.Kind == req.Kind && rule.ID == req.RouteID {
			rule = normalizeRouteRuleSummary(rule)
			plan := wire.RouteDeletePlan{Route: rule, Warnings: deleteWarnings(rule), CanApply: true}
			plan.Hash = routeHash(rule)
			return plan, nil
		}
	}
	return wire.RouteDeletePlan{}, fmt.Errorf("route_delete_plan: route %s/%s not found", req.Kind, req.RouteID)
}

func liveRouteSummaries(ctx context.Context, c *awgmgr.Client) ([]wire.RouteRuleSummary, error) {
	var (
		dns     []awgmgr.DNSRoute
		statics []awgmgr.StaticRoute
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { dns, err = c.ListDNSRoutes(gctx); return })
	g.Go(func() (err error) { statics, err = c.ListStaticRoutes(gctx); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := make([]wire.RouteRuleSummary, 0, len(dns)+len(statics))
	for _, r := range dns {
		bind := ""
		if len(r.Routes) > 0 {
			bind = firstNonEmptyRoute(r.Routes[0].Interface, r.Routes[0].TunnelID)
		}
		targets := append([]string{}, r.Domains...)
		targets = append(targets, r.ManualDomains...)
		out = append(out, normalizeRouteRuleSummary(wire.RouteRuleSummary{
			ID: r.ID, Name: r.Name, Kind: "dns", Backend: r.Backend, Enabled: r.Enabled, Bind: bind, Targets: rawTargets(targets),
		}))
	}
	for _, r := range statics {
		out = append(out, normalizeRouteRuleSummary(wire.RouteRuleSummary{
			ID: r.ID, Name: r.Name, Kind: "static", Enabled: r.Enabled, Bind: r.TunnelID, Targets: rawTargets(r.Subnets),
		}))
	}
	return out, nil
}

func deleteWarnings(rule wire.RouteRuleSummary) []wire.RouteOverlap {
	var out []wire.RouteOverlap
	for _, target := range rule.Targets {
		if target.Value == "0.0.0.0/0" || target.Value == "::/0" {
			out = append(out, wire.RouteOverlap{Severity: "warn", Reason: "default-route-like target", Existing: rule, Target: target})
		}
	}
	if strings.HasPrefix(rule.Bind, "wan") || strings.HasPrefix(rule.Bind, "eth") || strings.EqualFold(rule.Bind, "system") {
		out = append(out, wire.RouteOverlap{Severity: "warn", Reason: "WAN/system bind", Existing: rule})
	}
	if len(rule.Targets) >= 20 {
		out = append(out, wire.RouteOverlap{Severity: "warn", Reason: fmt.Sprintf("large route: %d targets", len(rule.Targets)), Existing: rule})
	}
	if strings.EqualFold(rule.Backend, "hydraroute") {
		out = append(out, wire.RouteOverlap{Severity: "warn", Reason: "HydraRoute-backed rule; HR-Neo will be restarted", Existing: rule})
	}
	return out
}

func refreshRoutingAndMaybeHR(ctx context.Context, c *awgmgr.Client, hrTouched bool) (bool, error) {
	if err := c.RoutingRefresh(ctx); err != nil {
		return false, err
	}
	if !hrTouched {
		return false, nil
	}
	hr, err := c.HydraRouteStatus(ctx)
	if err != nil || !hr.Installed {
		return false, nil
	}
	if err := c.HydraRouteControl(ctx, "restart"); err != nil {
		return false, err
	}
	return true, nil
}

func rawTargets(values []string) []wire.RouteTarget {
	out := make([]wire.RouteTarget, 0, len(values))
	for _, value := range values {
		out = append(out, wire.RouteTarget{Value: value})
	}
	return out
}

func cleanRouteTargets(values []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, value := range values {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == ',' || r == ';' || r == ' ' || r == '\t' }) {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

func routeHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func firstNonEmptyRoute(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
