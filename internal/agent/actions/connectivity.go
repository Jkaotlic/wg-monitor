package actions

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/internal/agent/checks"
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
	iface, ifaceLabel := pickConnectivityTunnelIface(ctx, c, targetsViaTunnel)
	if iface == "" {
		return "err", "не нашёл подходящий HR-Neo/defaultRoute туннель в awg-manager — нечего проверять"
	}
	httpc := ifaceBoundClient(iface, 6*time.Second)

	exitIP, traceErr := fetchExitIP(ctx, httpc, 5*time.Second)
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

	exitIP, traceErr := fetchExitIP(ctx, httpc, 5*time.Second)
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

// fetchExitIP queries Cloudflare's cdn-cgi/trace to discover what egress
// IP the connection appears as. Works via any HTTP client (iface-bound or
// not) — that's exactly the contract: ask through this client, see what
// IP the world sees us as.
func fetchExitIP(ctx context.Context, httpc *http.Client, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "https://1.1.1.1/cdn-cgi/trace", nil)
	if err != nil {
		return "", err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(line, "ip="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("no ip= line in trace response")
}

// pickConnectivityTunnelIface returns (linuxIface, prettyLabel) for the
// tunnel that should carry the connectivity probe. HR-Neo route bindings are
// authoritative: when a target domain is explicitly routed through nwg2, we
// must test nwg2 even if another tunnel is marked defaultRoute=true.
func pickConnectivityTunnelIface(ctx context.Context, c *awgmgr.Client, targets []connectivityTarget) (string, string) {
	if c == nil {
		return "", ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ta, err := c.TunnelsAll(cctx)
	if err != nil {
		return "", ""
	}
	labels := map[string]string{}
	defaultIface := ""
	for _, t := range ta.Tunnels {
		if !t.Enabled || t.InterfaceName == "" {
			continue
		}
		labels[t.InterfaceName] = nonEmptyString(t.Name, t.InterfaceName)
		if t.DefaultRoute && defaultIface == "" {
			defaultIface = t.InterfaceName
		}
	}
	if iface := pickHydraRouteIface(ctx, c, targets, labels); iface != "" {
		return iface, nonEmptyString(labels[iface], iface)
	}
	if defaultIface != "" {
		return defaultIface, nonEmptyString(labels[defaultIface], defaultIface)
	}
	return "", ""
}

// pickDefaultTunnelIface is kept for older tests/callers; new connectivity
// checks should use pickConnectivityTunnelIface.
func pickDefaultTunnelIface(ctx context.Context, c *awgmgr.Client) (string, string) {
	return pickConnectivityTunnelIface(ctx, c, nil)
}

func pickHydraRouteIface(ctx context.Context, c *awgmgr.Client, targets []connectivityTarget, knownIfaces map[string]string) string {
	if len(targets) == 0 {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	routes, err := c.ListDNSRoutes(cctx)
	if err != nil {
		return ""
	}
	for _, target := range targets {
		host := connectivityTargetHost(target.URL)
		if host == "" {
			continue
		}
		for _, route := range routes {
			if route.Backend != "hydraroute" || !route.Enabled || len(route.Routes) == 0 {
				continue
			}
			if !hydraRouteMatchesHost(route, host) {
				continue
			}
			iface := strings.TrimSpace(route.Routes[0].Interface)
			if iface == "" {
				iface = strings.TrimSpace(route.Routes[0].TunnelID)
			}
			if iface == "" {
				continue
			}
			if len(knownIfaces) == 0 || knownIfaces[iface] != "" {
				return iface
			}
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
// given linux iface (e.g. "nwg1"). Reuses the existing checks.IfaceDialer
// so the binding logic is exactly the same as the periodic external_reach
// check.
func ifaceBoundClient(iface string, timeout time.Duration) *http.Client {
	d := checks.IfaceDialer(iface)
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
		},
	}
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
