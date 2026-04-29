package awgmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	DefaultBaseURL = "http://127.0.0.1:2222"
	DefaultTimeout = 5 * time.Second
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: DefaultTimeout},
	}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("awgmgr GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("awgmgr read %s: %w", path, err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("awgmgr %s: decode: %w (body=%s)", path, err, snippet(body))
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("awgmgr POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	rbody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("awgmgr read %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rbody))
	}
	if out != nil && len(rbody) > 0 {
		if err := json.Unmarshal(rbody, out); err != nil {
			return fmt.Errorf("awgmgr %s: decode: %w", path, err)
		}
	}
	return nil
}

func snippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// TunnelsAll returns /api/tunnels/all data.
func (c *Client) TunnelsAll(ctx context.Context) (*TunnelsAll, error) {
	var env Envelope[TunnelsAll]
	if err := c.get(ctx, "/api/tunnels/all", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr tunnels/all: success=false")
	}
	return &env.Data, nil
}

// PingCheckStatus returns /api/pingcheck/status data.
func (c *Client) PingCheckStatus(ctx context.Context) (*PingCheckStatus, error) {
	var env Envelope[PingCheckStatus]
	if err := c.get(ctx, "/api/pingcheck/status", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr pingcheck/status: success=false")
	}
	return &env.Data, nil
}

// SystemInfo returns /api/system/info data.
func (c *Client) SystemInfo(ctx context.Context) (*SystemInfo, error) {
	var env Envelope[SystemInfo]
	if err := c.get(ctx, "/api/system/info", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr system/info: success=false")
	}
	return &env.Data, nil
}

// HydraRouteStatus returns /api/system/hydraroute-status data.
func (c *Client) HydraRouteStatus(ctx context.Context) (*HydraRouteStatus, error) {
	var env Envelope[HydraRouteStatus]
	if err := c.get(ctx, "/api/system/hydraroute-status", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr hydraroute-status: success=false")
	}
	return &env.Data, nil
}

// RestartAll triggers /api/control/restart-all.
func (c *Client) RestartAll(ctx context.Context) error {
	return c.post(ctx, "/api/control/restart-all", nil, nil)
}

// PingCheckNow triggers /api/pingcheck/check-now.
func (c *Client) PingCheckNow(ctx context.Context) error {
	return c.post(ctx, "/api/pingcheck/check-now", nil, nil)
}
