package actions

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/exitprobe"
)

// connectivityTarget — one HTTP probe target with a friendly Name.
type connectivityTarget struct {
	Name string
	URL  string
}

// targetsViaTunnel — services blocked-in-RU that should be reachable through
// the WG tunnel. These ARE the reason the user set up WG in the first place.
var targetsViaTunnel = []connectivityTarget{
	{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	{Name: "Telegram", URL: "https://web.telegram.org/"},
	{Name: "Instagram", URL: "https://www.instagram.com/favicon.ico"},
}

// targetsDirect — RU/CIS services that should NOT be tunneled (Yandex
// hits geofencing if it sees a foreign exit IP, VK is the canary that
// the local route still works). Probed via the system default route.
var targetsDirect = []connectivityTarget{
	{Name: "Яндекс", URL: "https://ya.ru/"},
	{Name: "VK", URL: "https://vk.com/"},
	{Name: "Mail.ru", URL: "https://mail.ru/"},
}

// CheckViaTunnel runs the "🌍 Через туннель?" probe: HTTP HEAD to each
// blocked-in-RU service through the iface that actually carries the matching
// HR-Neo route, falling back to defaultRoute=true when no explicit route
// matches. It also does a cdn-cgi/trace lookup to surface the egress IP.
func CheckViaTunnel(ctx context.Context, c *awgmgr.Client) (status, output string) {
	iface, ifaceLabel, pickErr := pickConnectivityTunnelIfaceDetailed(ctx, c, targetsViaTunnel)
	if iface == "" {
		if pickErr != "" {
			return "err", pickErr
		}
		return "err", "не нашёл подходящий HR-Neo/defaultRoute туннель в awg-manager — нечего проверять"
	}
	httpc := ifaceBoundClient(iface, 6*time.Second)

	exitIP, traceErr := exitprobe.FetchExitIP(ctx, httpc, 5*time.Second)
	results := probeAll(ctx, httpc, targetsViaTunnel, 5*time.Second)

	var b strings.Builder
	fmt.Fprintf(&b, "🌍 Через туннель (%s):\n", ifaceLabel)
	if traceErr != nil {
		fmt.Fprintf(&b, "Exit IP: ❓ не удалось определить (%s)\n", traceErr.Error())
	} else {
		fmt.Fprintf(&b, "Exit IP: %s\n", exitIP)
	}
	b.WriteString("\n")
	for _, r := range results {
		if r.ok {
			fmt.Fprintf(&b, "✅ %s\n", r.name)
		} else {
			fmt.Fprintf(&b, "❌ %s — %s\n", r.name, r.err)
		}
	}
	return classifyConnectivityStatus(results, traceErr == nil), b.String()
}

// CheckDirect runs the "🇷🇺 Напрямую?" probe: HTTP HEAD to each Russian
// service through the SYSTEM default route (no iface binding). Report
// includes the local exit IP — the user should see their ISP IP here,
// not the WG one.
func CheckDirect(ctx context.Context) (status, output string) {
	httpc := &http.Client{Timeout: 6 * time.Second}

	exitIP, traceErr := exitprobe.FetchExitIP(ctx, httpc, 5*time.Second)
	results := probeAll(ctx, httpc, targetsDirect, 5*time.Second)

	var b strings.Builder
	b.WriteString("🇷🇺 Напрямую (через системный маршрут):\n")
	if traceErr != nil {
		fmt.Fprintf(&b, "Exit IP: ❓ не удалось определить (%s)\n", traceErr.Error())
	} else {
		fmt.Fprintf(&b, "Exit IP: %s\n", exitIP)
	}
	b.WriteString("\n")
	for _, r := range results {
		if r.ok {
			fmt.Fprintf(&b, "✅ %s\n", r.name)
		} else {
			fmt.Fprintf(&b, "❌ %s — %s\n", r.name, r.err)
		}
	}
	return classifyConnectivityStatus(results, traceErr == nil), b.String()
}

type connectivityResult struct {
	name string
	ok   bool
	err  string
}

// probeAll fires all HEAD requests in parallel and collects results.
func probeAll(ctx context.Context, httpc *http.Client, targets []connectivityTarget, perTimeout time.Duration) []connectivityResult {
	out := make([]connectivityResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t connectivityTarget) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, perTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(cctx, http.MethodGet, t.URL, nil)
			if err != nil {
				out[i] = connectivityResult{name: t.Name, ok: false, err: err.Error()}
				return
			}
			req.Header.Set("User-Agent", "wg-monitor/connectivity")
			resp, err := httpc.Do(req)
			if err != nil {
				out[i] = connectivityResult{name: t.Name, ok: false, err: err.Error()}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 && resp.StatusCode/100 != 3 {
				out[i] = connectivityResult{name: t.Name, ok: false, err: fmt.Sprintf("HTTP %d", resp.StatusCode)}
				return
			}
			out[i] = connectivityResult{name: t.Name, ok: true}
		}(i, t)
	}
	wg.Wait()
	return out
}

// pickConnectivityTunnelIface returns (linuxIface, prettyLabel) for the
// tunnel that should carry the connectivity probe. HR-Neo route bindings are
// authoritative: when a target domain is explicitly routed through nwg2, we
// must test nwg2 even if another tunnel is marked defaultRoute=true.
func pickConnectivityTunnelIface(ctx context.Context, c *awgmgr.Client, targets []connectivityTarget) (string, string) {
	iface, label, _ := pickConnectivityTunnelIfaceDetailed(ctx, c, targets)
	return iface, label
}

func pickConnectivityTunnelIfaceDetailed(ctx context.Context, c *awgmgr.Client, targets []connectivityTarget) (string, string, string) {
	if c == nil {
		return "", "", ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ta, err := c.TunnelsAll(cctx)
	if err != nil {
		return "", "", ""
	}
	labels := map[string]string{}
	ifaceByAlias := map[string]string{}
	defaultIface := ""
	policyDefaultIface := ""
	for _, t := range ta.Tunnels {
		if t.Enabled && strings.TrimSpace(t.InterfaceName) != "" && t.DefaultRoute && policyDefaultIface == "" {
			policyDefaultIface = strings.TrimSpace(t.InterfaceName)
		}
		if !connectivityTunnelUsable(t) {
			continue
		}
		iface := strings.TrimSpace(t.InterfaceName)
		labels[iface] = nonEmptyString(t.Name, iface)
		for _, alias := range routeAliases(t.InterfaceName, t.NDMSName, t.ID) {
			ifaceByAlias[alias] = iface
		}
		if t.DefaultRoute && defaultIface == "" {
			defaultIface = iface
		}
	}
	if iface, blockReason := pickHydraRouteIface(ctx, c, targets, labels, ifaceByAlias, policyDefaultIface); iface != "" {
		return iface, nonEmptyString(labels[iface], iface), ""
	} else if blockReason != "" {
		return "", "", blockReason
	}
	if defaultIface != "" {
		return defaultIface, nonEmptyString(labels[defaultIface], defaultIface), ""
	}
	return "", "", ""
}

func connectivityTunnelUsable(t awgmgr.Tunnel) bool {
	if !t.Enabled || strings.TrimSpace(t.InterfaceName) == "" {
		return false
	}
	status := strings.TrimSpace(t.Status)
	return status == "" || routingStatusEnabled(status)
}

func pickHydraRouteIface(ctx context.Context, c *awgmgr.Client, targets []connectivityTarget, labels map[string]string, ifaceByAlias map[string]string, policyDefaultIface string) (string, string) {
	if len(targets) == 0 {
		return "", ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	routes, err := c.ListDNSRoutes(cctx)
	if err != nil {
		return "", ""
	}
	var (
		links       map[string]string
		linksOK     bool
		linksLoaded bool
	)
	for _, target := range targets {
		host := connectivityTargetHost(target.URL)
		if host == "" {
			continue
		}
		for _, route := range routes {
			if !strings.EqualFold(route.Backend, "hydraroute") || !route.Enabled {
				continue
			}
			if !hydraRouteMatchesConnectivityTarget(route, target, host) {
				continue
			}
			iface := ""
			if len(route.Routes) > 0 {
				bind := firstNonEmptyConnectivityRoute(route.Routes[0].Interface, route.Routes[0].TunnelID)
				iface = connectivityIfaceForBind(bind, ifaceByAlias)
			} else if hydraRouteUsesPolicyDefault(route) {
				// AGENT-15: у политики своя цепочка -- проба идёт через её
				// активное звено (первое доступное по порядку), как и трафик.
				if !linksLoaded {
					links, linksOK = connectivityPolicyActiveLinks(ctx, c)
					linksLoaded = true
				}
				policy := strings.ToLower(strings.TrimSpace(nonEmptyString(route.HRPolicyName, defaultHydraRoutePolicyName)))
				if bind, has := links[policy]; linksOK && has {
					routeName := nonEmptyString(route.Name, route.ID)
					if bind == "" {
						return "", fmt.Sprintf("HR-Neo правило %q для %s идёт через политику %q, но ни одно её звено сейчас не доступно; проверять через другой туннель не будем, чтобы не показать ложный OK", routeName, target.Name, route.HRPolicyName)
					}
					iface = connectivityIfaceForBindFold(bind, ifaceByAlias)
					if iface == "" {
						return "", fmt.Sprintf("HR-Neo правило %q для %s: политика %q сейчас ведёт через %s — это не VPN-туннель, проверка через туннель не про неё", routeName, target.Name, route.HRPolicyName, bind)
					}
				}
				if iface == "" {
					iface = pickHydraRoutePolicyIface(route, ifaceByAlias)
				}
				if iface == "" {
					iface = strings.TrimSpace(policyDefaultIface)
				}
			}
			if iface == "" {
				continue
			}
			if labels[iface] != "" {
				return iface, ""
			}
			routeName := nonEmptyString(route.Name, route.ID)
			return "", fmt.Sprintf("HR-Neo правило %q для %s идёт через %s, но этот туннель сейчас не запущен/не пригоден; fallback на другой туннель отключён, чтобы не показать ложный OK", routeName, target.Name, iface)
		}
	}
	return "", ""
}

// connectivityPolicyActiveLinks -- активное звено каждой политики со своей
// цепочкой: первое доступное по порядку (как buildPolicySummary в
// route_status). "" -- доступных звеньев нет. ok=false -- политики или их
// интерфейсы не прочитались (старая сборка, сбой): тогда прежняя логика.
func connectivityPolicyActiveLinks(ctx context.Context, c *awgmgr.Client) (map[string]string, bool) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	policies, err := c.AccessPolicies(cctx)
	if err != nil {
		return nil, false
	}
	ifaces, err := c.PolicyInterfaces(cctx)
	if err != nil {
		return nil, false
	}
	up := map[string]bool{}
	for _, pi := range ifaces {
		up[strings.ToLower(strings.TrimSpace(pi.Name))] = pi.Up
	}
	out := map[string]string{}
	for _, p := range policies {
		if len(p.Interfaces) == 0 {
			continue
		}
		chain := append([]awgmgr.AccessPolicyInterface(nil), p.Interfaces...)
		sort.SliceStable(chain, func(i, j int) bool { return chain[i].Order < chain[j].Order })
		active := ""
		for _, e := range chain {
			if up[strings.ToLower(strings.TrimSpace(e.Name))] {
				active = strings.TrimSpace(e.Name)
				break
			}
		}
		out[strings.ToLower(strings.TrimSpace(p.Name))] = active
	}
	return out, true
}

// connectivityIfaceForBindFold -- как connectivityIfaceForBind, но звено
// политики (OpkgTun10) сверяется с псевдонимами туннелей без учёта регистра,
// и незнакомое имя -- не туннель ("").
func connectivityIfaceForBindFold(bind string, ifaceByAlias map[string]string) string {
	bind = strings.TrimSpace(bind)
	if iface, ok := ifaceByAlias[bind]; ok {
		return iface
	}
	for alias, iface := range ifaceByAlias {
		if strings.EqualFold(alias, bind) {
			return iface
		}
	}
	return ""
}

func pickHydraRoutePolicyIface(route awgmgr.DNSRoute, ifaceByAlias map[string]string) string {
	for _, bind := range route.HRPolicyInterfaces {
		if iface := connectivityIfaceForBind(bind, ifaceByAlias); iface != "" {
			return iface
		}
	}
	return ""
}

func connectivityIfaceForBind(bind string, ifaceByAlias map[string]string) string {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return ""
	}
	if iface, ok := ifaceByAlias[bind]; ok {
		return iface
	}
	return bind
}

func hydraRouteUsesPolicyDefault(route awgmgr.DNSRoute) bool {
	return strings.EqualFold(strings.TrimSpace(route.HRRouteMode), "policy") || strings.TrimSpace(route.HRPolicyName) != ""
}

func hydraRouteMatchesConnectivityTarget(route awgmgr.DNSRoute, target connectivityTarget, host string) bool {
	if hydraRouteMatchesHost(route, host) {
		return true
	}
	aliases := connectivityTargetRouteAliases(target)
	if len(aliases) == 0 {
		return false
	}
	values := []string{route.ID, route.Name}
	values = append(values, route.Domains...)
	values = append(values, route.ManualDomains...)
	for _, value := range values {
		if connectivityRouteTokenMatches(value, aliases) {
			return true
		}
	}
	return false
}

func connectivityTargetRouteAliases(target connectivityTarget) []string {
	name := normalizeConnectivityRouteToken(target.Name)
	switch name {
	case "youtube":
		return []string{"youtube"}
	case "telegram":
		return []string{"telegram"}
	case "instagram":
		return []string{"instagram", "meta"}
	}
	if name != "" {
		return []string{name}
	}
	host := connectivityTargetHost(target.URL)
	if host == "" {
		return nil
	}
	parts := strings.Split(strings.ToLower(host), ".")
	if len(parts) == 0 {
		return nil
	}
	return []string{normalizeConnectivityRouteToken(parts[0])}
}

func connectivityRouteTokenMatches(value string, aliases []string) bool {
	token := normalizeConnectivityRouteToken(value)
	if token == "" {
		return false
	}
	fields := strings.Fields(token)
	for _, alias := range aliases {
		alias = normalizeConnectivityRouteToken(alias)
		if alias == "" {
			continue
		}
		if token == alias {
			return true
		}
		for _, field := range fields {
			if field == alias {
				return true
			}
		}
	}
	return false
}

func normalizeConnectivityRouteToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if i := strings.LastIndex(value, ":"); i >= 0 {
		value = value[i+1:]
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func firstNonEmptyConnectivityRoute(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func hydraRouteMatchesHost(route awgmgr.DNSRoute, host string) bool {
	for _, target := range append(append([]string{}, route.Domains...), route.ManualDomains...) {
		if routeTargetMatchesHost(host, target) {
			return true
		}
	}
	return false
}

func routeTargetMatchesHost(host, target string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	target = strings.ToLower(strings.TrimSpace(target))
	if host == "" || target == "" {
		return false
	}
	if host == target || strings.HasSuffix(host, "."+target) {
		return true
	}
	addr, aerr := netip.ParseAddr(host)
	prefix, perr := netip.ParsePrefix(target)
	return aerr == nil && perr == nil && prefix.Contains(addr)
}

func connectivityTargetHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Hostname())
}

func nonEmptyString(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// ifaceBoundClient builds an HTTP client whose dialer pins traffic to the
// given linux iface (e.g. "nwg1"). Delegates to exitprobe.IfaceClient so the
// binding logic is exactly the same as the periodic external_reach check and
// the exit-address prober (v0.47).
func ifaceBoundClient(iface string, timeout time.Duration) *http.Client {
	return exitprobe.IfaceClient(iface, timeout)
}

// classifyConnectivityStatus picks the wire-protocol status code:
// "ok" if everything passed; "err" otherwise. The narrative diff is in
// `output`, so backend can render whatever icons it wants.
func classifyConnectivityStatus(results []connectivityResult, traceOK bool) string {
	if !traceOK {
		return "err"
	}
	for _, r := range results {
		if !r.ok {
			return "err"
		}
	}
	return "ok"
}
