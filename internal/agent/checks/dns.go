package checks

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// DefaultRKNTestDomains: known to be filtered by RU TSPU/ISPs in 2026
// but having unambiguous correct A-records when resolved honestly.
// A spoofed answer (0.0.0.0 / 127.0.0.* / NXDOMAIN / single suspicious IP)
// across all three is a strong signal that the resolver path is RKN-tampered.
var DefaultRKNTestDomains = []string{"rutracker.org", "lostfilm.tv", "linkedin.com"}

// spoofIPs is the set of A-records ISPs commonly return for blocked domains.
var spoofIPs = map[string]struct{}{
	"0.0.0.0":      {},
	"127.0.0.1":    {},
	"127.0.0.10":   {},
	"255.255.255.255": {},
}

// DNS probes every configured/discovered DNS endpoint for two things:
//   1. Reachability — does the resolver answer a basic A-query for TestDomain?
//   2. RKN-cleanliness — for endpoints that ARE reachable, do the RKN-test
//      domains return plausible (non-spoofed) IPs?
//
// Check FAILs when:
//   - >= FailThreshold endpoints are unreachable, OR
//   - all reachable endpoints are RKN-suspect (every one returns spoofed
//     answers for >=2 of the 3 RKN-test domains).
type DNS struct {
	Endpoints       []keenetic.DNSEndpoint
	TestDomain      string
	FailThreshold   int
	IfaceDialFn     func(iface string) *net.Dialer
	HTTPClient      *http.Client
	PerProbeTimeout time.Duration
	IfaceMap        map[string]string
	RKNTestDomains  []string // empty → DefaultRKNTestDomains
}

func (DNS) Name() string { return "dns" }

func (c DNS) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	if c.PerProbeTimeout <= 0 {
		c.PerProbeTimeout = 3 * time.Second
	}
	threshold := c.FailThreshold
	if threshold <= 0 {
		threshold = 1
	}
	httpc := c.HTTPClient
	if httpc == nil {
		httpc = http.DefaultClient
	}
	// RKN-probe is opt-in: empty list disables it. The agent main wires in
	// DefaultRKNTestDomains when auto-discovery is enabled so production
	// configs get RKN-awareness for free; tests that don't set the field
	// stay focused on basic reachability.
	rknDomains := c.RKNTestDomains

	if len(c.Endpoints) == 0 {
		return OK(c.Name(), start, map[string]any{
			"endpoints": 0, "note": "no DNS endpoints discovered/configured",
		})
	}

	type epResult struct {
		Type      string         `json:"type"`
		Target    string         `json:"target"`
		NDMSName  string         `json:"ndms_name,omitempty"`
		Reachable bool           `json:"reachable"`
		Err       string         `json:"err,omitempty"`
		RKN       map[string]any `json:"rkn,omitempty"`
		RKNStatus string         `json:"rkn_status,omitempty"` // clean | suspect | n/a
	}

	var results []epResult
	failedCount := 0
	rknBlockedCount := 0
	rknProbedCount := 0
	for _, ep := range c.Endpoints {
		r := epResult{Type: ep.Type, Target: epTarget(ep), NDMSName: ep.NDMSName}
		if err := c.probeOne(ctx, ep, c.TestDomain, httpc); err != nil {
			r.Reachable = false
			r.Err = err.Error()
			r.RKNStatus = "n/a"
			failedCount++
			results = append(results, r)
			continue
		}
		r.Reachable = true
		blocked, perDomain := c.rknProbe(ctx, ep, rknDomains, httpc)
		r.RKN = perDomain
		rknProbedCount++
		if blocked {
			r.RKNStatus = "suspect"
			rknBlockedCount++
		} else {
			r.RKNStatus = "clean"
		}
		results = append(results, r)
	}

	details := map[string]any{
		"endpoints":         len(c.Endpoints),
		"failed_count":      failedCount,
		"rkn_probed":        rknProbedCount,
		"rkn_suspect":       rknBlockedCount,
		"rkn_test_domains":  rknDomains,
		"endpoints_detail":  results,
	}
	if failedCount >= threshold {
		return Fail(c.Name(), start,
			fmt.Sprintf("%d/%d endpoints unreachable", failedCount, len(c.Endpoints)),
			details)
	}
	if rknProbedCount > 0 && rknBlockedCount == rknProbedCount {
		return Fail(c.Name(), start,
			fmt.Sprintf("RKN block suspected — all %d reachable endpoints return spoofed answers", rknProbedCount),
			details)
	}
	return OK(c.Name(), start, details)
}

func epTarget(ep keenetic.DNSEndpoint) string {
	switch ep.Type {
	case "plain", "dot":
		return fmt.Sprintf("%s:%d", ep.Host, ep.Port)
	case "doh":
		return ep.URL
	}
	return "?"
}

// probeOne runs the basic reachability A-query for `domain` over `ep`.
func (c DNS) probeOne(ctx context.Context, ep keenetic.DNSEndpoint, domain string, httpc *http.Client) error {
	switch ep.Type {
	case "plain":
		var dialer *net.Dialer
		if linuxIface := c.resolveIface(ep.NDMSName); linuxIface != "" && c.IfaceDialFn != nil {
			dialer = c.IfaceDialFn(linuxIface)
		}
		_, err := ProbePlainDNS(ctx, fmt.Sprintf("%s:%d", ep.Host, ep.Port), domain+".", dialer, c.PerProbeTimeout)
		return err
	case "doh":
		_, err := ProbeDoH(ctx, ep.URL, domain, httpc, c.PerProbeTimeout)
		return err
	case "dot":
		return fmt.Errorf("dot transport not implemented")
	default:
		return fmt.Errorf("unknown transport %q", ep.Type)
	}
}

// resolveIPs returns A-record IPs (as strings) from `ep` for `domain`,
// or an error. Used by RKN probe — uniform return type across transports.
func (c DNS) resolveIPs(ctx context.Context, ep keenetic.DNSEndpoint, domain string, httpc *http.Client) ([]string, error) {
	switch ep.Type {
	case "plain":
		var dialer *net.Dialer
		if linuxIface := c.resolveIface(ep.NDMSName); linuxIface != "" && c.IfaceDialFn != nil {
			dialer = c.IfaceDialFn(linuxIface)
		}
		ips, err := ProbePlainDNS(ctx, fmt.Sprintf("%s:%d", ep.Host, ep.Port), domain+".", dialer, c.PerProbeTimeout)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(ips))
		for _, ip := range ips {
			out = append(out, ip.String())
		}
		return out, nil
	case "doh":
		return ProbeDoH(ctx, ep.URL, domain, httpc, c.PerProbeTimeout)
	default:
		return nil, fmt.Errorf("transport %q not supported for RKN probe", ep.Type)
	}
}

// rknProbe queries `ep` for each RKN-test domain and reports per-domain
// results. The endpoint is considered "blocked" if a strict majority of the
// configured domains return a suspect answer (no IPs, error, or spoof IP).
// Threshold scales with len(domains) so 1-domain probes still work and
// 5-domain probes don't false-positive on a single transient flap.
func (c DNS) rknProbe(ctx context.Context, ep keenetic.DNSEndpoint, domains []string, httpc *http.Client) (blocked bool, perDomain map[string]any) {
	perDomain = map[string]any{}
	if len(domains) == 0 {
		return false, perDomain
	}
	susCount := 0
	for _, dom := range domains {
		info := map[string]any{}
		ips, err := c.resolveIPs(ctx, ep, dom, httpc)
		switch {
		case err != nil:
			info["err"] = err.Error()
			info["sus"] = true
			susCount++
		case len(ips) == 0:
			info["ips"] = []string{}
			info["sus"] = true
			susCount++
		case hasSpoofIP(ips):
			info["ips"] = ips
			info["sus"] = true
			susCount++
		default:
			info["ips"] = ips
			info["sus"] = false
		}
		perDomain[dom] = info
	}
	// Strict majority: 2*sus > len(domains). For 1 domain → 1 sus blocks; for
	// 3 → 2 of 3; for 5 → 3 of 5.
	return susCount*2 > len(domains), perDomain
}

func hasSpoofIP(ips []string) bool {
	for _, ip := range ips {
		if _, ok := spoofIPs[ip]; ok {
			return true
		}
	}
	return false
}

func (c DNS) resolveIface(ndms string) string {
	if ndms == "" {
		return ""
	}
	return c.IfaceMap[ndms]
}
