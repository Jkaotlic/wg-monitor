package awgmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ListDNSRoutes returns /api/dns-routes/list .data.
func (c *Client) ListDNSRoutes(ctx context.Context) ([]DNSRoute, error) {
	var env Envelope[[]DNSRoute]
	if err := c.get(ctx, "/api/dns-routes/list", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr dns-routes/list: success=false")
	}
	return env.Data, nil
}

// UpdateDNSRoute calls POST /api/dns-routes/update?id=<id> with the full
// rule object as the body. awg-manager treats the call as full-replace —
// the rule must be sent verbatim with only the desired fields modified.
func (c *Client) UpdateDNSRoute(ctx context.Context, rule DNSRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/dns-routes/update?id="+rule.ID, body, nil)
}

// postJSON is a helper that POSTs JSON with the right headers. The existing
// (lowercase) post helper accepts a body io.Reader but doesn't set
// Content-Type; awg-manager's update endpoints require it. Inline here to
// avoid disturbing the existing helper.
func (c *Client) postJSON(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("awgmgr POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rb))
	}
	if out != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("awgmgr %s: decode: %w", path, err)
		}
	}
	return nil
}
