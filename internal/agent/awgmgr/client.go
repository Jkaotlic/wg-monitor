package awgmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "http://127.0.0.1:2222"
	DefaultTimeout = 5 * time.Second
)

type Client struct {
	BaseURL  string
	HTTP     *http.Client
	Login    string
	Password string

	mu            sync.Mutex
	sessionCookie *http.Cookie
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

func (c *Client) SetCredentials(login, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Login = login
	c.Password = password
	c.sessionCookie = nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if ck := c.cookie(); ck != nil {
		req.AddCookie(ck)
	}
	return c.HTTP.Do(req)
}

func (c *Client) cookie() *http.Cookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionCookie == nil {
		return nil
	}
	cp := *c.sessionCookie
	return &cp
}

func (c *Client) ensureSession(ctx context.Context) error {
	c.mu.Lock()
	needLogin := (c.Login != "" || c.Password != "") && c.sessionCookie == nil
	login := c.Login
	password := c.Password
	c.mu.Unlock()
	if !needLogin {
		return nil
	}
	body, err := json.Marshal(struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}{Login: login, Password: password})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("awgmgr auth login: %w", err)
	}
	defer resp.Body.Close()
	rbody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("awgmgr auth login read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr auth login: HTTP %d: %s", resp.StatusCode, snippet(rbody))
	}
	var env Envelope[json.RawMessage]
	if len(rbody) > 0 {
		if err := json.Unmarshal(rbody, &env); err == nil && !env.Success {
			return fmt.Errorf("awgmgr auth login: success=false")
		}
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "awg_session" && ck.Value != "" {
			c.mu.Lock()
			cp := *ck
			c.sessionCookie = &cp
			c.mu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("awgmgr auth login: missing awg_session cookie")
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	start := time.Now()
	resp, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "GET", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return fmt.Errorf("awgmgr GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "GET", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
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
	start := time.Now()
	resp, err := c.do(ctx, http.MethodPost, path, body, "")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "POST", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return fmt.Errorf("awgmgr POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "POST", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	rbody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("awgmgr read %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rbody))
	}
	if err := rejectFailureEnvelope(path, rbody); err != nil {
		return err
	}
	if out != nil && len(rbody) > 0 {
		if err := json.Unmarshal(rbody, out); err != nil {
			return fmt.Errorf("awgmgr %s: decode: %w", path, err)
		}
	}
	return nil
}

func rejectFailureEnvelope(path string, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	var env struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
		Error   any    `json:"error"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Success == nil || *env.Success {
		return nil
	}
	detail := strings.TrimSpace(env.Message)
	if detail == "" && env.Code != "" {
		detail = strings.TrimSpace(env.Code)
	}
	if detail == "" && env.Error != nil {
		detail = fmt.Sprintf("%v", env.Error)
	}
	if detail == "" {
		detail = snippet(body)
	}
	return fmt.Errorf("awgmgr %s: success=false: %s", path, detail)
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

// DiagResult returns the raw JSON body of /api/diagnostics/result. The exact
// shape is owned by awg-manager and may change between versions, so we pass
// it through unchanged for the TG side to render.
func (c *Client) DiagResult(ctx context.Context) (string, error) {
	start := time.Now()
	const path = "/api/diagnostics/result"
	resp, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "GET", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return "", fmt.Errorf("awgmgr GET diagnostics/result: %w", err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "GET", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", fmt.Errorf("awgmgr read diagnostics/result: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP_%d: awgmgr diagnostics/result: %s", resp.StatusCode, snippet(body))
	}
	return string(body), nil
}

// DiagRun POSTs /api/diagnostics/run to trigger a fresh diagnostic
// pass. awg-manager 2.8.2 returns {success:true,data:{status:"running"}}
// on accept; the actual report arrives later via DiagResult. The call
// is idempotent — posting again during an in-flight run returns the
// same body without re-starting.
func (c *Client) DiagRun(ctx context.Context) error {
	start := time.Now()
	const path = "/api/diagnostics/run"
	resp, err := c.do(ctx, http.MethodPost, path, nil, "")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "POST", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return fmt.Errorf("HTTP_REFUSED: awgmgr POST diagnostics/run: %w", err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "POST", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if rerr != nil && resp.StatusCode != 200 {
		return fmt.Errorf("HTTP_%d: awgmgr diagnostics/run: read body: %w", resp.StatusCode, rerr)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP_%d: awgmgr diagnostics/run: %s", resp.StatusCode, snippet(body))
	}
	return nil
}

// ImportConf calls POST /api/import/conf — passes raw .conf text, awg-manager
// does its own parsing. Returns the created Tunnel (enabled=false by default).
// backend may be "" to let awg-manager use its active backend.
func (c *Client) ImportConf(ctx context.Context, rawConf, name, backend string) (*Tunnel, error) {
	start := time.Now()
	const path = "/api/import/conf"
	body, err := json.Marshal(struct {
		Content string `json:"content"`
		Name    string `json:"name"`
		Backend string `json:"backend,omitempty"`
	}{Content: rawConf, Name: name, Backend: backend})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(body), "application/json")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "POST", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return nil, fmt.Errorf("awgmgr POST /api/import/conf: %w", err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "POST", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("awgmgr read import/conf: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("awgmgr import/conf: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	var env Envelope[Tunnel]
	if err := json.Unmarshal(rb, &env); err != nil {
		return nil, fmt.Errorf("awgmgr import/conf: decode: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr import/conf: success=false")
	}
	return &env.Data, nil
}

// StartTunnel calls POST /api/control/start?id=<tunnelID>.
func (c *Client) StartTunnel(ctx context.Context, tunnelID string) error {
	return c.post(ctx, "/api/control/start?id="+url.QueryEscape(tunnelID), nil, nil)
}

// StopTunnel calls POST /api/control/stop?id=<tunnelID>.
func (c *Client) StopTunnel(ctx context.Context, tunnelID string) error {
	return c.post(ctx, "/api/control/stop?id="+url.QueryEscape(tunnelID), nil, nil)
}

// ToggleEnabled calls POST /api/control/toggle-enabled?id=<tunnelID>.
func (c *Client) ToggleEnabled(ctx context.Context, tunnelID string) error {
	return c.post(ctx, "/api/control/toggle-enabled?id="+url.QueryEscape(tunnelID), nil, nil)
}

// ToggleDefaultRoute calls POST /api/control/toggle-default-route?id=<tunnelID>.
func (c *Client) ToggleDefaultRoute(ctx context.Context, tunnelID string) error {
	return c.post(ctx, "/api/control/toggle-default-route?id="+url.QueryEscape(tunnelID), nil, nil)
}

// ReplaceConf calls POST /api/tunnels/replace?id=<tunnelID> — replaces an
// existing tunnel's config in-place. Returns the updated Tunnel.
func (c *Client) ReplaceConf(ctx context.Context, tunnelID, rawConf, name, backend string) (*Tunnel, error) {
	return c.confPost(ctx, "/api/tunnels/replace?id="+url.QueryEscape(tunnelID), rawConf, name, backend)
}

// DeleteTunnel calls POST /api/tunnels/delete?id=<tunnelID>.
func (c *Client) DeleteTunnel(ctx context.Context, tunnelID string) error {
	return c.post(ctx, "/api/tunnels/delete?id="+url.QueryEscape(tunnelID), nil, nil)
}

// GetEnv is a public version of the lowercase get helper. Used by callers in
// other packages (internal/agent/actions) that need to issue typed GETs
// against awg-manager without duplicating the HTTP plumbing.
func (c *Client) GetEnv(ctx context.Context, path string, out any) error {
	return c.get(ctx, path, out)
}

func (c *Client) confPost(ctx context.Context, path, rawConf, name, backend string) (*Tunnel, error) {
	start := time.Now()
	body, err := json.Marshal(struct {
		Content string `json:"content"`
		Name    string `json:"name"`
		Backend string `json:"backend,omitempty"`
	}{Content: rawConf, Name: name, Backend: backend})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(body), "application/json")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "POST", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return nil, fmt.Errorf("awgmgr POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "POST", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("awgmgr read %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rb))
	}
	var env Envelope[Tunnel]
	if err := json.Unmarshal(rb, &env); err != nil {
		return nil, fmt.Errorf("awgmgr %s: decode: %w", path, err)
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr %s: success=false", path)
	}
	return &env.Data, nil
}
