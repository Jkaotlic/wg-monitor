package amnezia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultMirrorURL = "https://storage.googleapis.com/amnezia/cp?m-path=/ru"
	DefaultTimeout   = 30 * time.Second
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type AccountInfo struct {
	HTTPStatus             int            `json:"http_status"`
	AvailableCountries     []Country      `json:"available_countries"`
	IssuedConfigs          []IssuedConfig `json:"issued_configs"`
	SubscriptionStartDate  string         `json:"subscription_start_date"`
	SubscriptionEndDate    string         `json:"subscription_end_date"`
	SubscriptionPeriodDays int            `json:"subscription_period_days"`
	ActiveDeviceCount      int            `json:"active_device_count"`
	MaxDeviceCount         int            `json:"max_device_count"`
	VPNKey                 string         `json:"vpn_key"`
	RenewalLink            string         `json:"renewal_link"`
	RenewalLinkStatus      string         `json:"renewal_link_status"`
	SubscriptionStatus     string         `json:"subscription_status"`
}

type Country struct {
	Code string `json:"server_country_code"`
	Name string `json:"server_country_name"`
}

type IssuedConfig struct {
	InstallationUUID string `json:"installation_uuid"`
	WorkerLastUpdate string `json:"worker_last_updated"`
	LastDownloaded   string `json:"last_downloaded"`
	CountryCode      string `json:"server_country_code"`
	CountryName      string `json:"server_country_name"`
	SourceType       string `json:"source_type"`
	OSVersion        string `json:"os_version"`
}

func New(baseURL string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultMirrorURL
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: DefaultTimeout, Jar: jar},
	}
}

func (c *Client) Login(ctx context.Context, vpnKey string) error {
	if !strings.HasPrefix(strings.TrimSpace(vpnKey), "vpn://") {
		return fmt.Errorf("amnezia login: key must start with vpn://")
	}
	body, err := json.Marshal(map[string]any{"vpnKey": strings.TrimSpace(vpnKey), "remember": false})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/login", bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("amnezia login: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	return nil
}

func (c *Client) AccountInfo(ctx context.Context) (*AccountInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/account-info", nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("amnezia account-info read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("amnezia account-info: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	var env struct {
		Data AccountInfo `json:"data"`
	}
	if err := json.Unmarshal(rb, &env); err != nil {
		return nil, fmt.Errorf("amnezia account-info decode: %w", err)
	}
	return &env.Data, nil
}

func (c *Client) DownloadConfig(ctx context.Context, countryCode string) ([]byte, error) {
	body, err := json.Marshal(map[string]string{"countryCode": strings.ToLower(strings.TrimSpace(countryCode))})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/download-config", bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("amnezia download-config read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("amnezia download-config: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	return rb, nil
}

func (c *Client) RevokeCountryConfig(ctx context.Context, countryCode string) error {
	body, err := json.Marshal(map[string]string{"countryCode": strings.ToLower(strings.TrimSpace(countryCode))})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/revoke-country-config", bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("amnezia revoke-country-config: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	return nil
}

func (c *Client) RevokeGatewayConfig(ctx context.Context, installationUUID, countryCode string) error {
	body, err := json.Marshal(map[string]string{
		"installationUuid": strings.TrimSpace(installationUUID),
		"countryCode":      strings.ToLower(strings.TrimSpace(countryCode)),
	})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/revoke-gateway-config", bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("amnezia revoke-gateway-config: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if strings.HasPrefix(c.BaseURL, "https://storage.googleapis.com/") {
		base, err := ResolveMirrorTarget(ctx, c.HTTP, c.BaseURL)
		if err != nil {
			return nil, err
		}
		c.BaseURL = base
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("amnezia %s %s: %w", method, path, err)
	}
	return resp, nil
}

var mirrorToRe = regexp.MustCompile(`(?i)<meta\s+name=["']mirror-to["']\s+data-link=["']([^"']+)["']`)

func ResolveMirrorTarget(ctx context.Context, hc *http.Client, mirrorURL string) (string, error) {
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mirrorURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("amnezia mirror resolve: %w", err)
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	if err != nil {
		return "", fmt.Errorf("amnezia mirror resolve read: %w", err)
	}
	m := mirrorToRe.FindSubmatch(rb)
	if len(m) < 2 {
		return "", fmt.Errorf("amnezia mirror resolve: mirror-to meta not found")
	}
	return strings.TrimRight(string(m[1]), "/"), nil
}

func snippet(b []byte) string {
	const n = 240
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
