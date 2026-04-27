package checks

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type AwgRouting struct {
	Iface    string // informational; binding happens in HTTPClient's dialer
	URL      string // e.g. https://1.1.1.1/cdn-cgi/trace
	Expected string // expected egress IPv4
}

func (AwgRouting) Name() string { return "awg_routing" }

func (c AwgRouting) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, c.URL, nil)
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return Fail(c.Name(), start, "http error", map[string]any{"err": err.Error()})
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Fail(c.Name(), start, "non-2xx", map[string]any{"http_code": resp.StatusCode})
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	got := parseCdnCgiTraceIP(string(body))
	if got == "" {
		return Fail(c.Name(), start, "no ip= line in trace body", nil)
	}
	if got != c.Expected {
		return Fail(c.Name(), start, "exit ip mismatch", map[string]any{"got_ip": got, "expected_ip": c.Expected})
	}
	return OK(c.Name(), start, map[string]any{"got_ip": got})
}

// parseCdnCgiTraceIP extracts the value of the "ip=" line from a Cloudflare
// cdn-cgi/trace response. Returns "" if no such line is present.
func parseCdnCgiTraceIP(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "ip="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
