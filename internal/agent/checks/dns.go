package checks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/agent/keenetic"
	"github.com/anex/wg-monitor/pkg/wire"
)

// DefaultRKNTestDomains: known to be filtered by RU TSPU/ISPs in 2026
// but having unambiguous correct A-records when resolved honestly.
// A spoofed answer (0.0.0.0 / 127.0.0.* / NXDOMAIN / single suspicious IP)
// across all three is a strong signal that the resolver path is RKN-tampered.
var DefaultRKNTestDomains = []string{"rutracker.org", "lostfilm.tv", "linkedin.com"}

// spoofIPs is the set of A-records ISPs commonly return for blocked domains.
var spoofIPs = map[string]struct{}{
	"0.0.0.0":         {},
	"127.0.0.1":       {},
	"127.0.0.10":      {},
	"255.255.255.255": {},
}

// DNS probes every configured/discovered DNS endpoint for two things:
//  1. Reachability — does the resolver answer a basic A-query for TestDomain?
//  2. RKN-cleanliness — for endpoints that ARE reachable, do the RKN-test
//     domains return plausible (non-spoofed) IPs?
//
// Endpoints, and (per reachable endpoint) its RKN-test domains, are probed
// concurrently — see probeEndpoint/rknProbe — so total wall-clock time stays
// close to a single PerProbeTimeout round-trip regardless of how many
// endpoints/domains are configured, instead of their sum. This matters
// because reporter.go bounds every Check.Run to a fixed per-check budget:
// probed sequentially, N endpoints x M RKN domains can blow that budget long
// before finishing, and the parent ctx being canceled/deadline-exceeded
// mid-probe must NOT be scored the same as a genuine spoofed/unreachable
// answer — see probeInconclusive.
//
// Check FAILs when:
//   - >= FailThreshold endpoints are unreachable, OR
//   - all reachable endpoints are RKN-suspect (every one returns spoofed
//     answers for >=2 of the 3 RKN-test domains).
type DNS struct {
	Endpoints        []keenetic.DNSEndpoint
	EndpointProvider func(context.Context) ([]keenetic.DNSEndpoint, error)
	TestDomain       string
	FailThreshold    int
	IfaceDialFn      func(iface string) *net.Dialer
	HTTPClient       *http.Client
	PerProbeTimeout  time.Duration
	IfaceMap         map[string]string
	IfaceMapProvider func(context.Context) (map[string]string, error)
	RKNTestDomains   []string // empty → DefaultRKNTestDomains
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

	endpoints := append([]keenetic.DNSEndpoint(nil), c.Endpoints...)
	var endpointProviderErr error
	if c.EndpointProvider != nil {
		discovered, err := c.EndpointProvider(ctx)
		if err != nil {
			endpointProviderErr = err
		} else {
			endpoints = append(endpoints, discovered...)
		}
	}
	ifaceMapFresh := false
	if c.IfaceMapProvider != nil {
		if ifaceMap, err := c.IfaceMapProvider(ctx); err == nil {
			c.IfaceMap = ifaceMap
			ifaceMapFresh = true
		}
	}

	if len(endpoints) == 0 {
		details := map[string]any{
			"endpoints": 0, "note": "no DNS endpoints discovered/configured",
		}
		if endpointProviderErr != nil {
			details["discovery_error"] = endpointProviderErr.Error()
		}
		return OK(c.Name(), start, details)
	}

	type epResult struct {
		Type       string         `json:"type"`
		Target     string         `json:"target"`
		NDMSName   string         `json:"ndms_name,omitempty"`
		Skipped    bool           `json:"skipped,omitempty"`
		SkipReason string         `json:"skip_reason,omitempty"`
		Reachable  bool           `json:"reachable"`
		Err        string         `json:"err,omitempty"`
		RKN        map[string]any `json:"rkn,omitempty"`
		RKNStatus  string         `json:"rkn_status,omitempty"` // clean | suspect | n/a
	}

	// probeEndpoint runs the full per-endpoint sequence (NDMS-skip check,
	// reachability, then — if reachable — the RKN probe). Called
	// concurrently, once per endpoint, below; it only ever touches its own
	// local epResult, so there is nothing shared to race on.
	probeEndpoint := func(ep keenetic.DNSEndpoint) epResult {
		r := epResult{Type: ep.Type, Target: epTarget(ep), NDMSName: ep.NDMSName}
		if c.shouldSkipNDMSEndpoint(ep, ifaceMapFresh) {
			r.Skipped = true
			r.SkipReason = "ndms interface is not present in current awg-manager tunnel map"
			r.RKNStatus = "n/a"
			return r
		}

		err := c.probeOne(ctx, ep, c.TestDomain, httpc)
		switch {
		case err == nil:
			r.Reachable = true
		case probeInconclusive(ctx, err):
			// The check ran out of its time budget (or was canceled) before
			// this probe got a fair shot — that is NOT evidence the resolver
			// is down or tampered with, so it must not count as a failure.
			r.Skipped = true
			r.SkipReason = "check canceled/timed out before probe completed: " + err.Error()
			r.RKNStatus = "n/a"
			return r
		default:
			r.Err = err.Error()
			r.RKNStatus = "n/a"
			return r
		}

		blocked, conclusive, perDomain := c.rknProbe(ctx, ep, rknDomains, httpc)
		r.RKN = perDomain
		switch {
		case !conclusive:
			// Every configured RKN domain was inconclusive (ctx canceled or
			// deadline-exceeded) — no verdict possible for this endpoint.
			r.RKNStatus = "n/a"
		case blocked:
			r.RKNStatus = "suspect"
		default:
			r.RKNStatus = "clean"
		}
		return r
	}

	// Probe every endpoint concurrently: each goroutine owns exactly one
	// index of `results`, so there is no shared-map/slice race, and
	// endpoints_detail keeps the same order as `endpoints`.
	results := make([]epResult, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func(i int, ep keenetic.DNSEndpoint) {
			defer wg.Done()
			results[i] = probeEndpoint(ep)
		}(i, ep)
	}
	wg.Wait()

	failedCount := 0
	skippedCount := 0
	rknBlockedCount := 0
	rknProbedCount := 0
	for _, r := range results {
		switch {
		case r.Skipped:
			skippedCount++
		case !r.Reachable:
			failedCount++
		default:
			switch r.RKNStatus {
			case "suspect":
				rknProbedCount++
				rknBlockedCount++
			case "clean":
				rknProbedCount++
			}
			// RKNStatus == "n/a" here means the endpoint was reachable but
			// every RKN domain probe on it was inconclusive: no verdict,
			// excluded from both the numerator and denominator below.
		}
	}

	details := map[string]any{
		"endpoints":        len(endpoints),
		"failed_count":     failedCount,
		"skipped_count":    skippedCount,
		"rkn_probed":       rknProbedCount,
		"rkn_suspect":      rknBlockedCount,
		"rkn_test_domains": rknDomains,
		"endpoints_detail": results,
	}
	if endpointProviderErr != nil {
		details["discovery_error"] = endpointProviderErr.Error()
	}
	if failedCount >= threshold {
		return Fail(c.Name(), start,
			fmt.Sprintf("%d/%d endpoints unreachable", failedCount, len(endpoints)),
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

// rknProbe queries `ep` for each RKN-test domain concurrently and reports
// per-domain results. The endpoint is considered "blocked" if a strict
// majority of the CONCLUSIVE domains (i.e. excluding any whose probe was
// canceled/timed-out at the check level — see probeInconclusive) come back
// suspect (no IPs, genuine error, or spoof IP). The threshold scales with the
// conclusive count, not the configured count, so a run cut short by the
// check's time budget can't manufacture a false majority in either
// direction; each per-domain goroutine owns exactly one index of `results`,
// and perDomain is only written to after wg.Wait(), so there is no
// shared-map race.
//
// conclusive is false only when >=1 domain was configured and every single
// one came back inconclusive — i.e. we have zero signal either way. When RKN
// probing is disabled (no domains configured), conclusive is true and
// blocked is false, matching the pre-existing "n/a -> clean" trivial
// behavior for that case.
func (c DNS) rknProbe(ctx context.Context, ep keenetic.DNSEndpoint, domains []string, httpc *http.Client) (blocked, conclusive bool, perDomain map[string]any) {
	perDomain = map[string]any{}
	if len(domains) == 0 {
		return false, true, perDomain
	}

	type domainResult struct {
		domain       string
		info         map[string]any
		sus          bool
		inconclusive bool
	}
	results := make([]domainResult, len(domains))
	var wg sync.WaitGroup
	for i, dom := range domains {
		wg.Add(1)
		go func(i int, dom string) {
			defer wg.Done()
			info := map[string]any{}
			ips, err := c.resolveIPs(ctx, ep, dom, httpc)
			switch {
			case err != nil && probeInconclusive(ctx, err):
				info["err"] = err.Error()
				info["inconclusive"] = true
				results[i] = domainResult{domain: dom, info: info, inconclusive: true}
			case err != nil:
				info["err"] = err.Error()
				info["sus"] = true
				results[i] = domainResult{domain: dom, info: info, sus: true}
			case len(ips) == 0:
				info["ips"] = []string{}
				info["sus"] = true
				results[i] = domainResult{domain: dom, info: info, sus: true}
			case hasSpoofIP(ips):
				info["ips"] = ips
				info["sus"] = true
				results[i] = domainResult{domain: dom, info: info, sus: true}
			default:
				info["ips"] = ips
				info["sus"] = false
				results[i] = domainResult{domain: dom, info: info}
			}
		}(i, dom)
	}
	wg.Wait()

	susCount := 0
	conclusiveDomains := 0
	for _, res := range results {
		perDomain[res.domain] = res.info
		if res.inconclusive {
			continue
		}
		conclusiveDomains++
		if res.sus {
			susCount++
		}
	}
	if conclusiveDomains == 0 {
		return false, false, perDomain
	}
	// Strict majority: 2*sus > conclusiveDomains. For 1 domain → 1 sus
	// blocks; for 3 → 2 of 3; for 5 → 3 of 5.
	return susCount*2 > conclusiveDomains, true, perDomain
}

// probeInconclusive reports whether err reflects the check itself running
// out of its time budget (parent ctx canceled or deadline-exceeded) rather
// than a genuine reachability/spoofing verdict about the target.
//
// This can't be decided by inspecting err alone: ProbePlainDNS/ProbeDoH each
// enforce PerProbeTimeout via their OWN internally-derived child context, so
// an ordinary "this resolver never answered" failure produces the exact same
// context.DeadlineExceeded shape as a parent-level cancellation would. The
// one signal that reliably tells them apart is the state of the ctx WE
// handed the probe: if it is still healthy, whatever error came back is the
// probe's own finding about the target and must count toward the verdict; if
// it is already Done, the probe never got a fair shot, so its result carries
// no signal either way and must not be scored as a failure.
func probeInconclusive(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	cerr := ctx.Err()
	return errors.Is(cerr, context.Canceled) || errors.Is(cerr, context.DeadlineExceeded)
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

func (c DNS) shouldSkipNDMSEndpoint(ep keenetic.DNSEndpoint, ifaceMapFresh bool) bool {
	if !ifaceMapFresh || ep.NDMSName == "" {
		return false
	}
	return c.resolveIface(ep.NDMSName) == ""
}
