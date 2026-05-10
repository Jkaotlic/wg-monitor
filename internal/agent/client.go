package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Client разделяет два HTTP-клиента: short для report/result (быстрый
// timeout — если backend жив, ответ должен прийти моментально) и longPoll
// для GET /v1/cmd?wait=N (timeout > 60s, чтобы перекрыть backend'овский
// `maxCmdWait`). Прежде был один общий клиент с Timeout=10s, что рвало
// каждый long-poll до того, как backend успевал вернуть команду — кнопки
// из TG (restart_tunnel/diag_now/firmware_install/...) фактически НИКОГДА
// не доходили до агента в проде.
type Client struct {
	baseURL  string
	token    string
	version  string
	http     *http.Client
	longPoll *http.Client
}

// NewClient: timeout — для коротких report/result. long-poll-клиент
// автоматически получает timeout = max(timeout, 90s). Один общий
// http.Transport переиспользуется обоими — keep-alive не дублируется.
func NewClient(baseURL, token, version string, timeout time.Duration) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	longTimeout := timeout
	if longTimeout < 90*time.Second {
		longTimeout = 90 * time.Second
	}
	return &Client{
		baseURL:  baseURL,
		token:    token,
		version:  version,
		http:     &http.Client{Transport: tr, Timeout: timeout},
		longPoll: &http.Client{Transport: tr, Timeout: longTimeout},
	}
}

func (c *Client) SendReport(ctx context.Context, report wire.Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/report", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wg-monitor/"+c.version)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("backend returned %d: %s", resp.StatusCode, string(preview))
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain for keep-alive
	return nil
}

// PollCommand long-polls /v1/cmd?wait=N. Returns (nil, nil) on 204 (no command
// before the hold expired) — that's the normal idle path; the cmdloop simply
// retries. Returns (cmd, nil) on 200 with body. Other statuses → error.
//
// The agent passes its own waitSec; the backend caps it at 60s.
func (c *Client) PollCommand(ctx context.Context, waitSec int) (*wire.Command, error) {
	url := c.baseURL + "/v1/cmd?wait=" + strconv.Itoa(waitSec)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "wg-monitor/"+c.version)
	resp, err := c.longPoll.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil
	}
	if resp.StatusCode/100 != 2 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("backend returned %d: %s", resp.StatusCode, string(preview))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	var cmd wire.Command
	if err := json.Unmarshal(body, &cmd); err != nil {
		return nil, fmt.Errorf("decode command: %w", err)
	}
	return &cmd, nil
}

// PostResult uploads a CommandResult to /v1/cmd/result. Caller is responsible
// for filling DurationMs and a valid Status.
func (c *Client) PostResult(ctx context.Context, result wire.CommandResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/cmd/result", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wg-monitor/"+c.version)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("backend returned %d: %s", resp.StatusCode, string(preview))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
