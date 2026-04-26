package checks

import (
	"context"
	"net/http"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type AwgMarker struct {
	Iface       string
	URL         string
	MaxRetries  int           // total attempts (not retries on top of 1); spec says 3
	BaseBackoff time.Duration // first backoff; doubles each retry
}

func (AwgMarker) Name() string { return "awg_marker" }

func (c AwgMarker) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	maxRetries := c.MaxRetries
	if maxRetries < 1 {
		maxRetries = 3
	}
	backoff := c.BaseBackoff
	if backoff <= 0 {
		backoff = 250 * time.Millisecond
	}
	var lastCode int
	var lastErr string
	for attempt := 1; attempt <= maxRetries; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, c.URL, nil)
		resp, err := d.HTTPClient.Do(req)
		cancel()
		if err == nil && resp.StatusCode/100 == 2 {
			resp.Body.Close()
			return OK(c.Name(), start, map[string]any{"attempts": attempt, "http_code": resp.StatusCode})
		}
		if resp != nil {
			lastCode = resp.StatusCode
			resp.Body.Close()
		}
		if err != nil {
			lastErr = err.Error()
		}
		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return Fail(c.Name(), start, "ctx cancelled", map[string]any{"attempts": attempt})
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	return Fail(c.Name(), start, "all retries failed", map[string]any{
		"attempts": maxRetries, "last_http_code": lastCode, "last_err": lastErr,
	})
}
