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

// DNS is the high-level check that probes every configured/discovered DNS
// endpoint and reports a FAIL if at least FailThreshold of them are
// unreachable or return a bad answer.
type DNS struct {
	Endpoints       []keenetic.DNSEndpoint
	TestDomain      string                         // e.g. "example.com"
	FailThreshold   int                            // FAIL if failed >= threshold; 0 → 1
	IfaceDialFn     func(iface string) *net.Dialer // for plain DNS iface-bound; required if any endpoint has NDMSName mapped
	HTTPClient      *http.Client                   // for DoH; if nil, http.DefaultClient
	PerProbeTimeout time.Duration                  // default 3s
	IfaceMap        map[string]string              // NDMSName → linux iface; injected from agent main
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

	if len(c.Endpoints) == 0 {
		return OK(c.Name(), start, map[string]any{"endpoints": 0, "note": "no DNS endpoints discovered/configured"})
	}

	var failed []map[string]any
	for _, ep := range c.Endpoints {
		err := c.probeOne(ctx, ep, httpc)
		if err != nil {
			failed = append(failed, map[string]any{
				"type":      ep.Type,
				"target":    epTarget(ep),
				"ndms_name": ep.NDMSName,
				"err":       err.Error(),
			})
		}
	}

	details := map[string]any{
		"endpoints":    len(c.Endpoints),
		"failed":       failed,
		"failed_count": len(failed),
	}
	if len(failed) >= threshold {
		return Fail(c.Name(), start, fmt.Sprintf("%d/%d endpoints failed", len(failed), len(c.Endpoints)), details)
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

func (c DNS) probeOne(ctx context.Context, ep keenetic.DNSEndpoint, httpc *http.Client) error {
	switch ep.Type {
	case "plain":
		var dialer *net.Dialer
		if linuxIface := c.resolveIface(ep.NDMSName); linuxIface != "" && c.IfaceDialFn != nil {
			dialer = c.IfaceDialFn(linuxIface)
		}
		_, err := ProbePlainDNS(ctx, fmt.Sprintf("%s:%d", ep.Host, ep.Port), c.TestDomain+".", dialer, c.PerProbeTimeout)
		return err
	case "doh":
		_, err := ProbeDoH(ctx, ep.URL, c.TestDomain, httpc, c.PerProbeTimeout)
		return err
	case "dot":
		// DoT not implemented yet — count as fail-by-policy. Users with DoT
		// will see this in details and can either switch transport or wait.
		return fmt.Errorf("dot transport not implemented")
	default:
		return fmt.Errorf("unknown transport %q", ep.Type)
	}
}

// resolveIface translates an NDMSName (e.g. "Wireguard0") to a Linux iface
// name (e.g. "nwg0") via the precomputed map. Returns empty if not in the map
// (non-WG interface or unknown — fall back to system default routing).
func (c DNS) resolveIface(ndms string) string {
	if ndms == "" {
		return ""
	}
	return c.IfaceMap[ndms]
}
