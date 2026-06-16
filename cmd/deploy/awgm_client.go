package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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
	APIKey   string
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

type AWGMHTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *AWGMHTTPError) Error() string {
	return fmt.Sprintf("awgm %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func NewAWGMClient(baseURL, login, password string) *AWGMClient {
	httpClient := &http.Client{Timeout: awgmClientTimeout}
	if awgmInsecureTLS() {
		httpClient.Transport = &http.Transport{
			// #nosec G402 -- explicit AWGM_INSECURE_TLS=1 break-glass mode for self-signed router web UI.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	return &AWGMClient{
		BaseURL:  normalizeAWGMURL(baseURL),
		LoginID:  login,
		Password: password,
		HTTP:     httpClient,
	}
}

func (c *AWGMClient) WithAPIKey(apiKey string) *AWGMClient {
	c.APIKey = strings.TrimSpace(apiKey)
	return c
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
	if strings.TrimSpace(c.APIKey) != "" {
		return nil
	}
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
	c.setAuth(req)
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
	return c.post(ctx, "/api/terminal/install", nil)
}

func (c *AWGMClient) TerminalStart(ctx context.Context) error {
	return c.post(ctx, "/api/terminal/start", nil)
}

func (c *AWGMClient) TerminalStatus(ctx context.Context) (*AWGMTerminalStatus, error) {
	var env awgmEnvelope[AWGMTerminalStatus]
	if err := c.get(ctx, "/api/terminal/status", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgm terminal/status: success=false")
	}
	return &env.Data, nil
}

func (c *AWGMClient) TerminalStop(ctx context.Context) error {
	return c.post(ctx, "/api/terminal/stop", nil)
}

func (c *AWGMClient) RunTerminalScript(ctx context.Context, script string) (TerminalRunResult, error) {
	return c.RunTerminalScriptWithLogin(ctx, script, "", "")
}

func (c *AWGMClient) RunTerminalScriptWithLogin(ctx context.Context, script, loginUser, loginPassword string) (TerminalRunResult, error) {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return TerminalRunResult{}, err
	}
	wsURL, err := c.wsURL("/api/terminal/ws")
	if err != nil {
		return TerminalRunResult{}, err
	}
	cfg, err := websocket.NewConfig(wsURL, c.BaseURL)
	if err != nil {
		return TerminalRunResult{}, err
	}
	cfg.Header.Set("X-Requested-With", "XMLHttpRequest")
	applyAWGMWebsocketTLSConfig(cfg)
	if hdr := c.authHeader(); hdr != "" {
		cfg.Header.Set("Authorization", hdr)
	}
	if ck := c.cookie(); ck != nil {
		cfg.Header.Set("Cookie", ck.String())
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return TerminalRunResult{}, fmt.Errorf("awgm terminal ws: %w", err)
	}
	defer ws.Close()
	if err := websocket.Message.Send(ws, `{"AuthToken":""}`); err != nil {
		return TerminalRunResult{}, fmt.Errorf("awgm terminal auth token: %w", err)
	}
	if err := awgmTerminalSendResize(ws, 120, 40); err != nil {
		return TerminalRunResult{}, err
	}

	var out strings.Builder
	if err := awgmLoginTerminalIfPrompt(ctx, ws, loginUser, loginPassword, &out); err != nil {
		return TerminalRunResult{Output: out.String()}, err
	}

	const marker = "__WG_MONITOR_DONE__"
	payload := "cat >/tmp/wg-monitor-bootstrap.sh <<'WG_MONITOR_BOOTSTRAP'\n" +
		script + "\nWG_MONITOR_BOOTSTRAP\n" +
		"sh /tmp/wg-monitor-bootstrap.sh\n" +
		"rc=$?\n" +
		"echo " + marker + "$rc\n"
	if err := awgmTerminalSendInput(ws, payload); err != nil {
		return TerminalRunResult{}, fmt.Errorf("awgm terminal send: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return TerminalRunResult{Output: out.String()}, ctx.Err()
		default:
		}
		text, err := awgmTerminalReceiveText(ws)
		if err != nil {
			return TerminalRunResult{Output: out.String()}, fmt.Errorf("awgm terminal receive: %w", err)
		}
		out.WriteString(text)
		done, err := awgmTerminalDoneFromChunk(text, marker)
		if done {
			return TerminalRunResult{Output: out.String()}, err
		}
	}
}

func awgmInsecureTLS() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("AWGM_INSECURE_TLS")), "1")
}

func applyAWGMWebsocketTLSConfig(cfg *websocket.Config) {
	if cfg == nil || !awgmInsecureTLS() {
		return
	}
	// #nosec G402 -- explicit AWGM_INSECURE_TLS=1 break-glass mode for self-signed router web UI.
	cfg.TlsConfig = &tls.Config{InsecureSkipVerify: true}
}

func awgmTerminalDoneFromChunk(text, marker string) (bool, error) {
	if i := strings.LastIndex(text, marker); i >= 0 {
		rcText := strings.TrimSpace(text[i+len(marker):])
		rcFields := strings.Fields(rcText)
		if rcText == "" || len(rcFields) > 0 && rcFields[0] == "0" {
			return true, nil
		}
		return true, fmt.Errorf("bootstrap script failed: %s", rcText)
	}
	return false, nil
}

func awgmLoginTerminalIfPrompt(ctx context.Context, ws *websocket.Conn, loginUser, loginPassword string, out *strings.Builder) error {
	deadline := time.Now().Add(6 * time.Second)
	sentUser := false
	sentPassword := false
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_ = ws.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
		text, err := awgmTerminalReceiveText(ws)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if !sentUser && !sentPassword {
					_ = ws.SetReadDeadline(time.Time{})
					return nil
				}
				continue
			}
			return fmt.Errorf("awgm terminal auth receive: %w", err)
		}
		if out != nil {
			out.WriteString(text)
		}
		action := awgmTerminalPromptAction(text)
		if action == "" && out != nil {
			action = awgmTerminalPromptAction(out.String())
		}
		switch action {
		case "login":
			if sentUser {
				continue
			}
			if strings.TrimSpace(loginUser) == "" {
				return fmt.Errorf("awgm terminal asks for login but terminal user is empty")
			}
			if err := awgmTerminalSendInput(ws, loginUser+"\n"); err != nil {
				return fmt.Errorf("awgm terminal auth user: %w", err)
			}
			sentUser = true
		case "password":
			if sentPassword {
				continue
			}
			if loginPassword == "" {
				return fmt.Errorf("awgm terminal asks for password but router root password is empty")
			}
			if err := awgmTerminalSendInput(ws, loginPassword+"\n"); err != nil {
				return fmt.Errorf("awgm terminal auth password: %w", err)
			}
			sentPassword = true
		case "shell":
			_ = ws.SetReadDeadline(time.Time{})
			return nil
		}
	}
	_ = ws.SetReadDeadline(time.Time{})
	return nil
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
	c.setAuth(req)
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
		return &AWGMHTTPError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       awgmSnippet(rbody),
		}
	}
	if out != nil && len(rbody) > 0 {
		if err := json.Unmarshal(rbody, out); err != nil {
			return fmt.Errorf("awgm %s %s decode: %w", method, path, err)
		}
	}
	return nil
}

func (c *AWGMClient) ensureLoggedIn(ctx context.Context) error {
	if strings.TrimSpace(c.APIKey) != "" {
		return nil
	}
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

func (c *AWGMClient) setAuth(req *http.Request) {
	if hdr := c.authHeader(); hdr != "" {
		req.Header.Set("Authorization", hdr)
	}
}

func (c *AWGMClient) authHeader() string {
	if key := strings.TrimSpace(c.APIKey); key != "" {
		return "Bearer " + key
	}
	if c.LoginID == "" && c.Password == "" {
		return ""
	}
	token := base64.StdEncoding.EncodeToString([]byte(c.LoginID + ":" + c.Password))
	return "Basic " + token
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

func awgmTerminalSendInput(ws *websocket.Conn, text string) error {
	payload := append([]byte{'0'}, []byte(text)...)
	return websocket.Message.Send(ws, payload)
}

func awgmTerminalSendResize(ws *websocket.Conn, cols, rows int) error {
	payload := append([]byte{'1'}, []byte(fmt.Sprintf(`{"columns":%d,"rows":%d}`, cols, rows))...)
	if err := websocket.Message.Send(ws, payload); err != nil {
		return fmt.Errorf("awgm terminal resize: %w", err)
	}
	return nil
}

func awgmTerminalReceiveText(ws *websocket.Conn) (string, error) {
	var msg []byte
	if err := websocket.Message.Receive(ws, &msg); err != nil {
		return "", err
	}
	return stripTerminalFramePrefix(string(msg)), nil
}

func stripTerminalFramePrefix(msg string) string {
	if len(msg) > 0 && msg[0] >= '0' && msg[0] <= '9' {
		return msg[1:]
	}
	return msg
}

func awgmTerminalPromptAction(text string) string {
	s := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.Contains(s, "password:"):
		return "password"
	case strings.Contains(s, "login:"):
		return "login"
	case strings.HasSuffix(s, "#") || strings.HasSuffix(s, "$"):
		return "shell"
	default:
		return ""
	}
}

func awgmSnippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
