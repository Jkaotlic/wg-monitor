package hidemy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://hide-my-name.cloud"
	DefaultTimeout = 30 * time.Second
)

var accessCodeRe = regexp.MustCompile(`^\d{10,20}$`)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type Server struct {
	ID   string
	Name string
	IP   string
}

func New(baseURL string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: DefaultTimeout},
	}
}

func ValidAccessCode(code string) bool {
	return accessCodeRe.MatchString(strings.TrimSpace(code))
}

func (c *Client) ServerList(ctx context.Context, code string) ([]Server, error) {
	code = strings.TrimSpace(code)
	if !ValidAccessCode(code) {
		return nil, fmt.Errorf("hidemy access code must be 10-20 digits")
	}
	resp, err := c.postForm(ctx, "/api/serverlist.php?out=js&wg", url.Values{"code": {code}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("hidemy serverlist read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hidemy serverlist: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	servers, err := parseServerList(rb)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("hidemy serverlist: no WireGuard servers returned")
	}
	return servers, nil
}

func (c *Client) DownloadAmneziaWG20Config(ctx context.Context, code, serverIP string) ([]byte, error) {
	code = strings.TrimSpace(code)
	serverIP = strings.TrimSpace(serverIP)
	if !ValidAccessCode(code) {
		return nil, fmt.Errorf("hidemy access code must be 10-20 digits")
	}
	if serverIP == "" {
		return nil, fmt.Errorf("hidemy server is required")
	}
	resp, err := c.postForm(ctx, "/api/vpn_get_config_wg.php", url.Values{
		"awg":    {"4"},
		"code":   {code},
		"server": {serverIP},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("hidemy config read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hidemy config: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	return rb, nil
}

func (c *Client) postForm(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", c.BaseURL)
	req.Header.Set("Referer", c.BaseURL+"/faq/vpn/vpn-installation-and-configuration/third-party-applications/wireguard-for-windows/")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hidemy POST %s: %w", path, err)
	}
	return resp, nil
}

type rawServer struct {
	Name     string `json:"name"`
	NameEN   string `json:"name_en"`
	Services struct {
		WG *struct {
			IP string `json:"ip"`
		} `json:"wg"`
	} `json:"services"`
}

func parseServerList(body []byte) ([]Server, error) {
	var raw []rawServer
	if err := jsonUnmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("hidemy serverlist decode: %w", err)
	}
	out := make([]Server, 0, len(raw))
	for _, s := range raw {
		if s.Services.WG == nil || strings.TrimSpace(s.Services.WG.IP) == "" {
			continue
		}
		name := strings.TrimSpace(s.NameEN)
		if name == "" {
			name = strings.TrimSpace(s.Name)
		}
		if name == "" {
			name = strings.TrimSpace(s.Services.WG.IP)
		}
		ip := strings.TrimSpace(s.Services.WG.IP)
		out = append(out, Server{ID: ServerID(ip), Name: name, IP: ip})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func FindServer(servers []Server, id string) (Server, bool) {
	for _, s := range servers {
		if s.ID == id {
			return s, true
		}
	}
	return Server{}, false
}

func snippet(b []byte) string {
	const n = 240
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
