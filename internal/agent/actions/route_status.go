package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// routeInputs is everything the router tells about its routing, read in one
// go. route_status and route_lookup answer from the very same reading, so the
// two screens cannot disagree about where a rule leads.
type routeInputs struct {
	hr              *awgmgr.HydraRouteStatus
	tunnels         *awgmgr.TunnelsAll
	routing         []awgmgr.RoutingTunnel
	routingErr      error
	dns             []awgmgr.DNSRoute
	statics         []awgmgr.StaticRoute
	settings        *awgmgr.Settings
	policies        []awgmgr.AccessPolicy
	policiesErr     error
	polIfaces       []awgmgr.PolicyInterface
	polIfacesErr    error
	policiesUnknown bool
}

// fetchRouteInputs reads the routing inputs in parallel. HydraRoute status,
// tunnels and both rule lists are fatal; the rest degrade into errors kept for
// the caller (RouteStatus turns them into Warnings).
func fetchRouteInputs(ctx context.Context, c *awgmgr.Client) (routeInputs, error) {
	var (
		hr           *awgmgr.HydraRouteStatus
		tunnels      *awgmgr.TunnelsAll
		routing      []awgmgr.RoutingTunnel
		routingErr   error
		dns          []awgmgr.DNSRoute
		statics      []awgmgr.StaticRoute
		settings     *awgmgr.Settings
		policies     []awgmgr.AccessPolicy
		policiesErr  error
		polIfaces    []awgmgr.PolicyInterface
		polIfacesErr error
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { hr, err = c.HydraRouteStatus(gctx); return })
	g.Go(func() (err error) { tunnels, err = c.TunnelsAll(gctx); return })
	g.Go(func() error {
		routing, routingErr = c.RoutingTunnels(gctx)
		return nil
	})
	g.Go(func() (err error) { dns, err = c.ListDNSRoutes(gctx); return })
	g.Go(func() (err error) { statics, err = c.ListStaticRoutes(gctx); return })
	g.Go(func() error {
		// Non-fatal: older awg-manager builds serve the SPA shell at this
		// endpoint. On any failure settings stays nil and buildRouteSnapshot
		// falls back to the first defaultRoute=true tunnel.
		settings, _ = c.Settings(gctx)
		return nil
	})
	g.Go(func() error {
		// Не фатально: роутер без политик — это старая модель, а не поломка.
		policies, policiesErr = c.AccessPolicies(gctx)
		return nil
	})
	g.Go(func() error {
		// Тоже не фатально: без up/down цепочка всё ещё читается, деградируют
		// только роли звеньев. Но деградируют молча и целиком: резолвер считает
		// НЕизвестный интерфейс лежачим, поэтому активного звена не остаётся ни
		// у одной политики, ActiveTunnelID пустеет, а бот печатает "нет
		// доступного интерфейса" на всё подряд. Без строки в Warnings обе морды
		// покажут это как полный снимок -- поэтому ошибка обязана дойти.
		polIfaces, polIfacesErr = c.PolicyInterfaces(gctx)
		return nil
	})
	if err := g.Wait(); err != nil {
		return routeInputs{}, err
	}
	// "Эндпоинта нет" и "чтение упало" -- разные факты, и путать их нельзя.
	// 404 означает сборку без политик: там привязка действительно живёт в
	// правиле, и старая модель для неё верна. Любой другой отказ (500,
	// таймаут) оставляет привязку НЕизвестной -- уйти в старую модель значит
	// приписать правила default-туннелю по выдумке, которую фаза и удаляет.
	// Путь записи (addTunnelToHydraRoutePolicies) различает эти два случая
	// ровно так же; чтение обязано быть с ним согласовано.
	return routeInputs{
		hr: hr, tunnels: tunnels, routing: routing, routingErr: routingErr,
		dns: dns, statics: statics, settings: settings,
		policies: policies, policiesErr: policiesErr,
		polIfaces: polIfaces, polIfacesErr: polIfacesErr,
		policiesUnknown: policiesErr != nil && !awgmgr.IsEndpointMissing(policiesErr),
	}, nil
}

// RouteStatus fetches the routing snapshot and returns it as a JSON-encoded
// string suitable for wire.CommandResult.Output.
func RouteStatus(ctx context.Context, c *awgmgr.Client) (string, error) {
	in, err := fetchRouteInputs(ctx, c)
	if err != nil {
		return "", err
	}
	settings, policies := in.settings, in.policies
	routingErr, policiesErr, polIfacesErr := in.routingErr, in.policiesErr, in.polIfacesErr
	snap := buildRouteSnapshot(in.hr, in.tunnels, in.routing, in.dns, in.statics, activeDefaultTunnelID(settings), policies, in.polIfaces, in.policiesUnknown)
	if settings != nil && settings.SingboxRouterActive() {
		snap.SingboxRouter = &wire.SingboxRouterStatus{
			Enabled:    true,
			DeviceMode: settings.SingboxRouter.DeviceMode,
			PolicyName: settings.SingboxRouter.PolicyName,
		}
	}
	if routingErr != nil {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("/api/routing/tunnels failed: %v", routingErr))
	}
	// 404 на любой из двух выборок политик -- это не отказ, а ответ роутера
	// "я старая сборка": сигнал модели, а не потеря данных. Warnings ниже по
	// потоку означает "снапшот неполный" (панель бота, баннер мини-аппа), и
	// клеймить им каждый опрос легаси-половины парка значило бы врать о
	// состоянии роутера, у которого всё в порядке. Предупреждаем о том, что
	// классифицировать не смогли: 500, таймаут, ошибка разбора.
	if policiesErr != nil && !awgmgr.IsEndpointMissing(policiesErr) {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("/api/routing/access-policies failed: %v", policiesErr))
	}
	// Для интерфейсов политик 404 означает "старая сборка" ТОЛЬКО пока старая
	// модель и правда в силе, то есть пока политик у нас нет. Если политики
	// прочитались, сборка заведомо новая, снимок живёт ими -- и отсутствие
	// up/down это не сигнал модели, а недостающие данные: резолвер сочтёт
	// лежачим каждый интерфейс, активного звена не останется ни у одной
	// политики, и обе морды покажут "нет доступного интерфейса" как полный
	// снимок.
	legacyModel := len(policies) == 0
	if polIfacesErr != nil && !(legacyModel && awgmgr.IsEndpointMissing(polIfacesErr)) {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("/api/routing/policy-interfaces failed: %v", polIfacesErr))
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// activeDefaultTunnelID extracts the authoritative active default-route tunnel
// id from awg-manager settings. download.routeTag has the form
// "<routeKind>-<tunnelID>" (e.g. "awg-awg12"); strip the kind prefix to recover
// the tunnel id used in /api/tunnels/all. Returns "" when settings are
// unavailable so callers keep the legacy first-defaultRoute heuristic.
func activeDefaultTunnelID(s *awgmgr.Settings) string {
	return s.ActiveDefaultTunnelID()
}

// routeSnapshotBase builds what every rule's binding is read against: the
// tunnels, the interface map, the main exit and the access policies. It is the
// half of buildRouteSnapshot that route_lookup shares.
//
// activeDefaultID is the authoritative active default-route tunnel id (from
// awg-manager settings.download.routeTag, "" when unknown). Several tunnels can
// each report defaultRoute=true in /api/tunnels/all; HR-Neo policy rules with
// no explicit hrPolicyInterfaces fall through to the live default egress, so we
// must credit/label them against the tunnel awg-manager actually routes
// through — not just the first defaultRoute=true tunnel encountered.
//
// policiesUnknown says the router HAS the access-policies endpoint and reading
// it failed anyway (anything but a 404). Then the binding of every
// policy-bound rule is unknown, and unknown is not the default route: such
// rules go to snap.Other rather than being credited to a tunnel by guesswork.
// An empty policies slice with policiesUnknown=false is the honest old-build
// case and keeps the legacy per-rule behaviour.
func routeSnapshotBase(hr *awgmgr.HydraRouteStatus, tunnels *awgmgr.TunnelsAll, routing []awgmgr.RoutingTunnel, activeDefaultID string, policies []awgmgr.AccessPolicy, polIfaces []awgmgr.PolicyInterface, policiesUnknown bool) (wire.RouteSnapshot, *routeEgressEnv) {
	snap := wire.RouteSnapshot{Counts: make(map[string]wire.TunnelCounts)}
	if hr != nil {
		snap.HRNeo = wire.HRStatus{Installed: hr.Installed, Running: hr.Running}
	}
	byIface := make(map[string]string)
	defaultIface := ""
	authoritativeDefaultIface := ""
	if tunnels != nil {
		for _, t := range tunnels.Tunnels {
			ep := managedRouteEndpoint(t)
			if ep.Iface == "" {
				continue
			}
			snap.Tunnels = append(snap.Tunnels, wire.TunnelMeta{
				ID: ep.ID, Name: ep.Name, Iface: ep.Iface, Type: ep.Type,
				NDMSName: t.NDMSName, Enabled: ep.Enabled, Available: ep.Available,
				Status: t.Status, DefaultRoute: ep.DefaultRoute,
				HasHandshake: t.LastHandshake.Time() != nil,
				HandshakeAge: handshakeAgeSeconds(t.LastHandshake.Time()),
				PingStatus:   t.PingCheck.Status, PingFails: t.PingCheck.FailCount, PingFailMax: t.PingCheck.FailThreshold,
				RestartMethod: "control",
			})
			for _, alias := range ep.Aliases {
				byIface[alias] = ep.ID
			}
			if ep.DefaultRoute && ep.Enabled && defaultIface == "" {
				defaultIface = ep.Iface
			}
			if activeDefaultID != "" && ep.Enabled && ep.ID == activeDefaultID {
				authoritativeDefaultIface = ep.Iface
			}
		}
	}
	// awg-manager's recorded default-route tunnel wins over the first-listed
	// defaultRoute=true heuristic: when multiple tunnels carry defaultRoute=true
	// only one is the live egress, and routeTag names it.
	if authoritativeDefaultIface != "" {
		defaultIface = authoritativeDefaultIface
	}

	// Куда уходит трафик, не заявленный ни одним правилом. Это отдельный от
	// defaultIface факт: тот служит привязке fall-through правил HR-Neo, а
	// этот отвечает экрану на вопрос "а всё остальное куда". Вывести его из
	// флагов defaultRoute нельзя -- на живом роутере они стоят у всех
	// туннелей сразу, а трафик идёт мимо них.
	switch {
	case activeDefaultID == wire.DefaultEgressDirect:
		snap.DefaultEgress = wire.DefaultEgressDirect
	case activeDefaultID != "" && byIface[activeDefaultID] != "":
		snap.DefaultEgress = activeDefaultID
	default:
		// Роутер промолчал либо назвал туннель, которого в снимке нет.
		// Сослаться на линию, которую экран не показывает, нельзя, а
		// подставить первую попавшуюся -- значит выдать догадку за факт.
		snap.DefaultEgress = ""
	}
	for _, t := range routing {
		ep := ndmsRouteEndpoint(t)
		if ep.Iface == "" {
			continue
		}
		if id, ok := routeMappedID(ep, byIface); ok {
			for _, alias := range ep.Aliases {
				byIface[alias] = id
			}
			for i := range snap.Tunnels {
				if snap.Tunnels[i].ID == id {
					snap.Tunnels[i].Iface = ep.Iface
					snap.Tunnels[i].Available = snap.Tunnels[i].Available || ep.Available
					snap.Tunnels[i].Enabled = snap.Tunnels[i].Enabled || ep.Enabled
					if snap.Tunnels[i].Status == "" {
						snap.Tunnels[i].Status = t.Status
					}
					break
				}
			}
			continue
		}
		snap.Tunnels = append(snap.Tunnels, wire.TunnelMeta{
			ID: ep.ID, Name: ep.Name, Iface: ep.Iface, Type: ep.Type,
			Enabled: ep.Enabled, Available: ep.Available,
			RestartMethod: "none",
		})
		for _, alias := range ep.Aliases {
			byIface[alias] = ep.ID
		}
	}
	// Модель политик (awg-manager 2.16+): привязка правила с
	// hrRouteMode=="policy" лежит на политике, а не на правиле. Старые сборки
	// политик не отдают вовсе -- там продолжает работать ветка
	// hrPolicyInterfaces ниже. Выбор идёт по наличию данных, не по версии:
	// версия бывает кастомной сборкой.
	policyModel := len(policies) > 0
	policyIndex := map[string]int{}
	var policyResolver *policyIfaceResolver
	if policyModel {
		snap.PolicyModel = true
		// Only Type=="managed" entries are our VPN tunnels. snap.Tunnels also
		// carries WAN/system routing targets (added above from the NDMS
		// routing catalogue) purely for the rebind picker; their display
		// names can coincidentally collide with a policy interface's label
		// (e.g. both called "Подключение Ethernet"), and letting them into
		// the resolver would misclassify a WAN egress as one of our tunnels.
		var vpnTunnels []wire.TunnelMeta
		for _, t := range snap.Tunnels {
			if strings.EqualFold(t.Type, "managed") {
				vpnTunnels = append(vpnTunnels, t)
			}
		}
		policyResolver = newPolicyIfaceResolver(vpnTunnels, polIfaces)
		for _, p := range policies {
			snap.Policies = append(snap.Policies, buildPolicySummary(p, policyResolver))
		}
		for i, p := range snap.Policies {
			policyIndex[strings.ToLower(strings.TrimSpace(p.Name))] = i
		}
	}
	return snap, &routeEgressEnv{
		byIface: byIface, defaultIface: defaultIface, tunnels: snap.Tunnels,
		policyIndex: policyIndex, policies: snap.Policies, resolver: policyResolver,
		policiesUnknown: policiesUnknown,
	}
}

// buildRouteSnapshot is the pure aggregation function: easy to test. See
// routeSnapshotBase for activeDefaultID and policiesUnknown; every enabled DNS
// rule is credited where ruleEgress says it leads.
func buildRouteSnapshot(hr *awgmgr.HydraRouteStatus, tunnels *awgmgr.TunnelsAll, routing []awgmgr.RoutingTunnel, dns []awgmgr.DNSRoute, statics []awgmgr.StaticRoute, activeDefaultID string, policies []awgmgr.AccessPolicy, polIfaces []awgmgr.PolicyInterface, policiesUnknown bool) wire.RouteSnapshot {
	snap, env := routeSnapshotBase(hr, tunnels, routing, activeDefaultID, policies, polIfaces, policiesUnknown)
	byIface := env.byIface

	creditDNS := func(tid string, isHRNeo bool) {
		c := snap.Counts[tid]
		c.DNS++
		if isHRNeo {
			c.HRNeo++
		}
		snap.Counts[tid] = c
	}
	creditOther := func(isHRNeo bool, isStatic bool) {
		if isStatic {
			snap.Other.Static++
		} else {
			snap.Other.DNS++
			if isHRNeo {
				snap.Other.HRNeo++
			}
		}
	}
	creditPolicy := func(r awgmgr.DNSRoute) {
		name := strings.TrimSpace(r.HRPolicyName)
		if name == "" {
			name = defaultHydraRoutePolicyName
		}
		ifaces := routePolicyInterfaces(r.HRPolicyInterfaces, byIface, snap.Tunnels)
		for i := range snap.Policies {
			if strings.EqualFold(snap.Policies[i].Name, name) {
				snap.Policies[i].DNS++
				snap.Policies[i].HRNeo++
				if len(ifaces) > len(snap.Policies[i].Interfaces) {
					snap.Policies[i].Interfaces = ifaces
				}
				return
			}
		}
		snap.Policies = append(snap.Policies, wire.RoutePolicySummary{
			Name:       name,
			Interfaces: ifaces,
			DNS:        1,
			HRNeo:      1,
		})
	}
	for _, r := range dns {
		isHR := isHydraRouteBackend(r)
		ruleBind := ""
		if r.Enabled {
			bind, tunnelID, _, credit := ruleEgress(r, env)
			ruleBind = bind
			switch {
			case credit.policy >= 0:
				snap.Policies[credit.policy].DNS++
				if isHR {
					snap.Policies[credit.policy].HRNeo++
				}
			case credit.legacyPolicy:
				creditPolicy(r)
			case credit.tunnel:
				creditDNS(tunnelID, isHR)
			default:
				creditOther(isHR, false)
			}
		} else if len(r.Routes) > 0 {
			ruleBind = firstNonEmptyRoute(r.Routes[0].Interface, r.Routes[0].TunnelID)
		}
		targets := append([]string{}, r.Domains...)
		targets = append(targets, r.ManualDomains...)
		snap.Rules = append(snap.Rules, normalizeRouteRuleSummary(wire.RouteRuleSummary{
			ID: r.ID, Name: r.Name, Kind: "dns", Backend: r.Backend, Enabled: r.Enabled, Bind: ruleBind, Targets: rawTargets(targets),
		}))
	}
	for _, r := range statics {
		if r.Enabled {
			if id, ok := byIface[r.TunnelID]; ok {
				c := snap.Counts[id]
				c.Static++
				snap.Counts[id] = c
			} else {
				snap.Other.Static++
			}
		}
		snap.Rules = append(snap.Rules, normalizeRouteRuleSummary(wire.RouteRuleSummary{
			ID: r.ID, Name: r.Name, Kind: "static", Enabled: r.Enabled, Bind: r.TunnelID, Targets: rawTargets(r.Subnets),
		}))
	}
	return snap
}

// routeEgressEnv is what a rule's binding is resolved against. Built once by
// routeSnapshotBase; read by ruleEgress for route_status and route_lookup alike.
type routeEgressEnv struct {
	byIface         map[string]string // alias → snapshot tunnel id
	defaultIface    string            // egress of HR-Neo fall-through rules
	tunnels         []wire.TunnelMeta
	policyIndex     map[string]int // lowercased policy name → index in policies (policy model only)
	policies        []wire.RoutePolicySummary
	resolver        *policyIfaceResolver
	policiesUnknown bool
}

// ruleCredit is which counter of the snapshot a rule belongs to. It is
// route_status's business only; route_lookup needs just the egress.
type ruleCredit struct {
	policy       int  // index into env.policies (policy model), -1 otherwise
	legacyPolicy bool // hrPolicyInterfaces fall-through: counted on its own policy row
	tunnel       bool // counted on tunnelID; otherwise it is Other
}

// ruleEgress says where one ENABLED DNS rule sends its traffic.
//
// bind is the label the rule summary carries. tunnelID is the snapshot id the
// traffic leaves through: one of our tunnels or another router exit (WAN), and
// "" when it is not one of ours. known=false means nobody can say where the
// rule leads -- not "the default tunnel", not "the provider".
func ruleEgress(r awgmgr.DNSRoute, env *routeEgressEnv) (bind, tunnelID string, known bool, credit ruleCredit) {
	credit.policy = -1
	if len(r.Routes) > 0 {
		iface := firstNonEmptyRoute(r.Routes[0].Interface, r.Routes[0].TunnelID)
		id, ok := env.byIface[iface]
		credit.tunnel = ok
		return iface, id, ok, credit
	}
	if idx, ok := env.policyIndex[strings.ToLower(strings.TrimSpace(r.HRPolicyName))]; ok {
		// Правило привязано политикой: чей это трафик, знает цепочка -- её
		// первое живое звено. Живого звена нет -- не знает никто.
		p := env.policies[idx]
		credit.policy = idx
		return "policy:" + p.Name, p.ActiveTunnelID, policyHasActiveLink(p), credit
	}
	if bind, id, ok := hrNeoInterfaceBind(r, env.resolver); ok {
		// Правило приколочено к интерфейсу, а не к политике. Интерфейс, не
		// являющийся нашим туннелем, -- выход мимо VPN.
		credit.tunnel = id != ""
		return bind, id, true, credit
	}
	if env.policiesUnknown && isMovableHRNeoFallthrough(r) {
		// Привязка такого правила лежит на политике, а политики не
		// прочитались. Имя политики -- всё, что мы честно знаем; куда она
		// ведёт, не знает никто, поэтому правило идёт в Other, а не туннелю по
		// умолчанию.
		return routePolicyLabel(r), "", false, credit
	}
	if isMovableHRNeoFallthrough(r) && len(r.HRPolicyInterfaces) > 0 {
		credit.legacyPolicy = true
		for _, it := range routePolicyInterfaces(r.HRPolicyInterfaces, env.byIface, env.tunnels) {
			if it.Role == "active" {
				id := env.byIface[it.Bind]
				return routePolicyLabel(r), id, id != "", credit
			}
		}
		return routePolicyLabel(r), "", false, credit
	}
	if isMovableHRNeoFallthrough(r) && env.defaultIface != "" {
		bind := routePolicyBind(r, env.defaultIface, env.byIface)
		id, ok := env.byIface[bind]
		credit.tunnel = ok
		return bind, id, ok, credit
	}
	// Привязки нет. Правило HydraRoute Neo с набором «напрямую» ведёт к
	// провайдеру; про остальное роутер не сказал ничего.
	return "", "", isHydraRouteBackend(r) && isDirectProviderHRNeoPolicy(r), credit
}

// policyHasActiveLink reports whether some link of the policy chain is up and
// carrying its traffic now.
func policyHasActiveLink(p wire.RoutePolicySummary) bool {
	for _, it := range p.Interfaces {
		if it.Role == "active" {
			return true
		}
	}
	return false
}

// buildPolicySummary turns one access policy into the wire shape: the chain
// sorted by the router's own priority, one active link (the first that is up),
// and the tunnel that link resolves to.
func buildPolicySummary(p awgmgr.AccessPolicy, r *policyIfaceResolver) wire.RoutePolicySummary {
	entries := append([]awgmgr.AccessPolicyInterface(nil), p.Interfaces...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Order < entries[j].Order })
	out := wire.RoutePolicySummary{Name: p.Name, Description: p.Description}
	activeAssigned := false
	for _, e := range entries {
		tunnelID, isTunnel := r.tunnelForNames(e.Name, e.Label)
		item := wire.RoutePolicyInterface{
			Bind:      e.Name,
			Name:      firstNonEmptyRoute(e.Label, e.Name),
			Order:     e.Order,
			Available: r.up(e.Name),
			TunnelID:  tunnelID,
			ViaVPN:    isTunnel,
		}
		switch {
		case item.Available && !activeAssigned:
			item.Role = "active"
			activeAssigned = true
			// Активное звено и определяет, куда политика ведёт СЕЙЧАС. Пустой
			// ActiveTunnelID при непустой цепочке -- это выход мимо VPN.
			out.ActiveTunnelID = tunnelID
			out.ViaVPN = isTunnel
		case item.Available:
			item.Role = "fallback"
		default:
			item.Role = "unavailable"
		}
		out.Interfaces = append(out.Interfaces, item)
	}
	return out
}

// hrNeoInterfaceBind resolves a rule pinned to an interface rather than to a
// policy. hrRouteMode=="interface" binds through hrPolicyInterfaces, and
// hrPolicyName may itself name an interface instead of a policy — the router's
// own UI resolves it that way, and tunnel_import writes exactly such rules.
//
// A name the router does not offer to policies is NOT claimed here: it falls
// through to the legacy branches, which is what a router without the policy
// model needs.
func hrNeoInterfaceBind(r awgmgr.DNSRoute, resolver *policyIfaceResolver) (bind string, tunnelID string, ok bool) {
	if resolver == nil {
		return "", "", false
	}
	candidates := append(append([]string{}, r.HRPolicyInterfaces...), r.HRPolicyName)
	for _, name := range candidates {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if id, isTunnel := resolver.tunnelForNames(name); isTunnel {
			return name, id, true
		}
		if resolver.knownInterface(name) {
			return name, "", true
		}
	}
	return "", "", false
}

func routePolicyBind(r awgmgr.DNSRoute, fallbackIface string, byIface map[string]string) string {
	for _, bind := range r.HRPolicyInterfaces {
		bind = firstNonEmptyRoute(bind)
		if bind == "" {
			continue
		}
		if _, ok := byIface[bind]; ok {
			return bind
		}
	}
	return fallbackIface
}

func routePolicyLabel(r awgmgr.DNSRoute) string {
	name := strings.TrimSpace(r.HRPolicyName)
	if name == "" {
		name = defaultHydraRoutePolicyName
	}
	return "policy:" + name
}

func routePolicyInterfaces(binds []string, byIface map[string]string, tunnels []wire.TunnelMeta) []wire.RoutePolicyInterface {
	out := make([]wire.RoutePolicyInterface, 0, len(binds))
	seen := map[string]bool{}
	for _, bind := range binds {
		bind = strings.TrimSpace(bind)
		if bind == "" {
			continue
		}
		item := wire.RoutePolicyInterface{Bind: bind}
		if id, ok := byIface[bind]; ok {
			if t, ok := routeTunnelByID(tunnels, id); ok {
				item.Bind = t.Iface
				item.Name = t.Name
				item.Available = routeTunnelUsable(t)
			}
		}
		key := strings.ToLower(firstNonEmptyRoute(item.Name, item.Bind))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	activeAssigned := false
	for i := range out {
		if out[i].Available && !activeAssigned {
			out[i].Role = "active"
			activeAssigned = true
			continue
		}
		if out[i].Available {
			out[i].Role = "fallback"
		} else {
			out[i].Role = "unavailable"
		}
	}
	return out
}

func routeTunnelByID(tunnels []wire.TunnelMeta, id string) (wire.TunnelMeta, bool) {
	for _, t := range tunnels {
		if t.ID == id {
			return t, true
		}
	}
	return wire.TunnelMeta{}, false
}

func routeTunnelUsable(t wire.TunnelMeta) bool {
	if !t.Enabled {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(t.Status))
	return t.Available || status == "" || status == "running" || status == "up"
}

func handshakeAgeSeconds(t *time.Time) int {
	if t == nil {
		return 0
	}
	return int(time.Since(*t).Seconds())
}
