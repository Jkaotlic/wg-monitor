// Package premiumfiles downloads country files using the Premium website's
// session API. It never installs tunnels or runs unattended provisioning.
package premiumfiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

var (
	ErrRejected    = errors.New("кабинет Amnezia не принял ключ")
	ErrBusy        = errors.New("выпуск Amnezia уже выполняется")
	ErrSlotsFull   = errors.New("свободных мест в подписке Amnezia нет")
	ErrUnavailable = errors.New("кабинет Amnezia не ответил, проверьте подписку и повторите")
)

const maxBody = 1 << 20

type Country struct {
	Code string `json:"server_country_code"`
	Name string `json:"server_country_name"`
}
type Issued struct {
	Country string `json:"server_country_code"`
	Source  string `json:"source_type"`
}
type Account struct {
	Countries []Country `json:"available_countries"`
	Issued    []Issued  `json:"issued_configs"`
	Used      int       `json:"active_device_count"`
	Limit     int       `json:"max_device_count"`
	Expires   string    `json:"subscription_end_date"`
	Status    string    `json:"subscription_status"`
}

type Client struct {
	base string
	http *http.Client
}

func New(base string) *Client {
	if base == "" {
		base = "https://cp.amnezia.org"
	}
	jar, _ := cookiejar.New(nil)
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 20 * time.Second, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.base)
	req.Header.Set("Referer", c.base+"/ru")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity:
		return nil, ErrRejected
	case http.StatusConflict:
		return nil, ErrSlotsFull
	default:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrUnavailable, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		return nil, ErrUnavailable
	}
	return b, nil
}

func (c *Client) Login(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "vpn://") || len(key) <= 6 || len(key) > 8192 {
		return ErrRejected
	}
	_, err := c.request(ctx, http.MethodPost, "/api/login", map[string]any{"vpnKey": key, "remember": false})
	return err
}
func (c *Client) Account(ctx context.Context) (Account, error) {
	b, err := c.request(ctx, http.MethodGet, "/api/account-info", nil)
	if err != nil {
		return Account{}, err
	}
	var env struct {
		Data *Account `json:"data"`
	}
	if json.Unmarshal(b, &env) != nil || env.Data == nil {
		return Account{}, ErrUnavailable
	}
	return *env.Data, nil
}
func (c *Client) Download(ctx context.Context, country, declared string) ([]byte, error) {
	if len(country) != 2 || country[0] < 'a' || country[0] > 'z' || country[1] < 'a' || country[1] > 'z' || (declared != "ru" && declared != "ag") {
		return nil, errors.New("выберите страну и страну подключения")
	}
	b, err := c.request(ctx, http.MethodPost, "/api/download-config", map[string]string{"countryCode": country, "declaredCountryCode": declared})
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(b, []byte("[Interface]")) || !bytes.Contains(b, []byte("[Peer]")) {
		return nil, ErrUnavailable
	}
	return b, nil
}
