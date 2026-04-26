package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type Client struct {
	baseURL string
	token   string
	version string
	http    *http.Client
}

func NewClient(baseURL, token, version string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		version: version,
		http:    &http.Client{Timeout: timeout},
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
