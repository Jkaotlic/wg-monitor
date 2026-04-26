package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// TODO(stage-2+): rename "dns_doh" to "dns_dot" — the implementation uses DoT
// (port 853 via dig +tls). Keeping the spec name for now to avoid migration noise.

type DNSProvider struct {
	Name string
	Host string
}

type DNSDoH struct {
	Providers     []DNSProvider
	TestDomain    string
	FailThreshold int
}

func (DNSDoH) Name() string { return "dns_doh" }

func (c DNSDoH) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	var failed []string
	for _, p := range c.Providers {
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		out, err := d.Runner.Run(cctx, "dig", "+tls", "+short", "+timeout=3", "@"+p.Host, c.TestDomain)
		cancel()
		if err != nil || !looksLikeAnAnswer(out) {
			failed = append(failed, p.Name)
		}
	}
	if len(failed) >= c.FailThreshold {
		return Fail(c.Name(), start,
			fmt.Sprintf("%d providers failed", len(failed)),
			map[string]any{"failed_providers": failed, "checked": len(c.Providers)})
	}
	return OK(c.Name(), start, map[string]any{"failed_providers": failed, "checked": len(c.Providers)})
}

func looksLikeAnAnswer(out string) bool {
	o := strings.TrimSpace(out)
	if o == "" {
		return false
	}
	// dig +short prints just the IPs (which contain digits); dig without +short prints
	// a section header. We accept either: a non-empty trimmed output containing any
	// digit (matches IP literals like "93.184.216.34") OR the literal "ANSWER SECTION"
	// header. Subprocess errors (dig exit != 0) are caught earlier via err != nil and
	// never reach this function.
	return strings.Contains(o, "ANSWER SECTION") || strings.ContainsAny(o, "0123456789")
}
