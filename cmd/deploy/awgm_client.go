package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const awgmClientTimeout = 10 * time.Second

type AWGMClient struct {
	BaseURL  string
	LoginID  string
	Password string
	HTTP     *http.Client

	mu            sync.Mutex
	sessionCookie *http.Cookie
}

type awgmEnvelope[T any] struct {
	Success bool `json:"success"`
	Data    T    `json:"data"`
}

type AWGMSystemInfo struct {
	GoArch   string `json:"goArch"`
	GoOS     string `json:"goOS"`
	RouterIP string `json:"routerIP"`
	Version  string `json:"version"`
}

type AWGMTerminalStatus struct {
	Installed     bool `json:"installed"`
	Running       bool `json:"running"`
	SessionActive bool `json:"sessionActive"`
}

type TerminalRunResult struct {
	Output string
}

func NewAWGMClient(baseURL, login, password string) *AWGMClient {
	return &AWGMClient{
		BaseURL:  normalizeAWGMURL(baseURL),
		LoginID:  login,
		Password: password,
		HTTP:     &http.Client{Timeout: awgmClientTimeout},
	}
}

func normalizeAWGMURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return strings.TrimRight(raw, "/")
}

func (c *AWGMClient) Login(ctx context.Context) error {
	body, err := json.Marshal(struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}{Login: c.LoginID, Password: c.Password})
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
		return fmt.Errorf("awgm login: %w", err)
	}
	defer resp.Body.Close()
	rbody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("awgm login read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgm login: HTTP %d: %s", resp.StatusCode, awgmSnippet(rbody))
	}
	var env awgmEnvelope[json.RawMessage]
	if len(rbody) > 0 {
		if err := json.Unmarshal(rbody, &env); err == nil && !env.Success {
			return fmt.Errorf("awgm login: success=false")
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
	return fmt.Errorf("awgm login: missing awg_session cookie")
}

func (c *AWGMClient) Health(ctx context.Context) error {
	return c.get(ctx, "/health", nil)
}

func (c *AWGMClient) SystemInfo(ctx context.Context) (*AWGMSystemInfo, error) {
	var env awgmEnvelope[AWGMSystemInfo]
	if err := c.get(ctx, "/api/system/info", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgm system/info: success=false")
	}
	return &env.Data, nil
}

func (c *AWGMClient) TerminalInstall(ctx context.Context) error {
	return c.post(ctx, "/terminal/install", nil)
}

func (c *AWGMClient) TerminalStart(ctx context.Context) error {
	return c.post(ctx, "/terminal/start", nil)
}

func (c *AWGMClient) TerminalStatus(ctx context.Context) (*AWGMTerminalStatus, error) {
	var env awgmEnvelope[AWGMTerminalStatus]
	if err := c.get(ctx, "/terminal/status", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgm terminal/status: success=false")
	}
	return &env.Data, nil
}

func (c *AWGMClient) TerminalStop(ctx context.Context) error {
	return c.post(ctx, "/terminal/stop", nil)
}

func (c *AWGMClient) RunTerminalScript(ctx context.Context, script string) (TerminalRunResult, error) {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return TerminalRunResult{}, err
	}
	wsURL, err := c.wsURL("/terminal/ws")
	if err != nil {
		return TerminalRunResult{}, err
	}
	cfg, err := websocket.NewConfig(wsURL, c.BaseURL)
	if err != nil {
		return TerminalRunResult{}, err
	}
	cfg.Protocol = []string{"tty"}
	cfg.Header.Set("X-Requested-With", "XMLHttpRequest")
	if ck := c.cookie(); ck != nil {
		cfg.Header.Set("Cookie", ck.String())
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return TerminalRunResult{}, fmt.Errorf("awgm terminal ws: %w", err)
	}
	defer ws.Close()

	const marker = "__WG_MONITOR_DONE__"
	payload := "cat >/tmp/wg-monitor-bootstrap.sh <<'WG_MONITOR_BOOTSTRAP'\n" +
		script + "\nWG_MONITOR_BOOTSTRAP\n" +
		"sh /tmp/wg-monitor-bootstrap.sh\n" +
		"rc=$?\n" +
		"echo " + marker + "$rc\n"
	if err := websocket.Message.Send(ws, "0"+payload); err != nil {
		return TerminalRunResult{}, fmt.Errorf("awgm terminal send: %w", err)
	}

	var out strings.Builder
	for {
		select {
		case <-ctx.Done():
			return TerminalRunResult{Output: out.String()}, ctx.Err()
		default:
		}
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			return TerminalRunResult{Output: out.String()}, fmt.Errorf("awgm terminal receive: %w", err)
		}
		text := stripTerminalFramePrefix(msg)
		out.WriteString(text)
		if i := strings.Index(text, marker); i >= 0 {
			rcText := strings.TrimSpace(text[i+len(marker):])
			if rcText == "" || rcText[0] == '0' {
				return TerminalRunResult{Output: out.String()}, nil
			}
			return TerminalRunResult{Output: out.String()}, fmt.Errorf("bootstrap script failed: %s", rcText)
		}
	}
}

func (c *AWGMClient) get(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, out)
}

func (c *AWGMClient) post(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodPost, path, nil, out)
}

func (c *AWGMClient) doJSON(ctx context.Context, method, path string, body io.Reader, out any) error {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	if ck := c.cookie(); ck != nil {
		req.AddCookie(ck)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("awgm %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	rbody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("awgm %s %s read: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgm %s %s: HTTP %d: %s", method, path, resp.StatusCode, awgmSnippet(rbody))
	}
	if out != nil && len(rbody) > 0 {
		if err := json.Unmarshal(rbody, out); err != nil {
			return fmt.Errorf("awgm %s %s decode: %w", method, path, err)
		}
	}
	return nil
}

func (c *AWGMClient) ensureLoggedIn(ctx context.Context) error {
	if c.LoginID == "" && c.Password == "" {
		return nil
	}
	if c.cookie() != nil {
		return nil
	}
	return c.Login(ctx)
}

func (c *AWGMClient) cookie() *http.Cookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionCookie == nil {
		return nil
	}
	cp := *c.sessionCookie
	return &cp
}

func (c *AWGMClient) wsURL(path string) (string, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported AWG Manager URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func stripTerminalFramePrefix(msg string) string {
	if len(msg) > 0 && msg[0] >= '0' && msg[0] <= '9' {
		return msg[1:]
	}
	return msg
}

func awgmSnippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
