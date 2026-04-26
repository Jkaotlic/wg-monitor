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
	Iface    string // informational; binding happens in the HTTPClient's dialer
	URL      string // e.g. https://api.ipify.org
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	got := strings.TrimSpace(string(body))
	if got != c.Expected {
		return Fail(c.Name(), start, "exit ip mismatch", map[string]any{"got_ip": got, "expected_ip": c.Expected})
	}
	return OK(c.Name(), start, map[string]any{"got_ip": got})
}
