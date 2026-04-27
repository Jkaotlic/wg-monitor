package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProbeDoH issues an A-query for `domain` against the DoH endpoint at `url`
// using the application/dns-json query syntax (?name=...&type=A) and returns
// the answer IPs. Errors when the server returns non-2xx, NXDOMAIN/SERVFAIL,
// or zero answers.
//
// The HTTP client should be pre-configured with any iface-bound dialer.
// Default timeout from caller's context, capped by `timeout`.
func ProbeDoH(ctx context.Context, url, domain string, client *http.Client, timeout time.Duration) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	q := strings.Builder{}
	q.WriteString(url)
	if strings.Contains(url, "?") {
		q.WriteString("&")
	} else {
		q.WriteString("?")
	}
	q.WriteString("name=")
	q.WriteString(domain)
	q.WriteString("&type=A")

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, q.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("doh request: %w", err)
	}
	req.Header.Set("Accept", "application/dns-json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doh: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("doh: status %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var jr struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, fmt.Errorf("doh: decode: %w", err)
	}
	if jr.Status != 0 {
		return nil, fmt.Errorf("doh: dns rcode %d", jr.Status)
	}
	var out []string
	for _, a := range jr.Answer {
		if a.Type == 1 { // A
			out = append(out, a.Data)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("doh: no A answers")
	}
	return out, nil
}
