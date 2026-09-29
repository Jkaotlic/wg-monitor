package awg3panel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// DefaultTimeout -- спека: «панель недоступна» после 10 с.
const DefaultTimeout = 10 * time.Second

// maxBody -- предел ответа. Самый крупный -- список пиров; QR в base64 --
// десятки КБ.
const maxBody = 1 << 20

// Credentials -- всё, с чем бот входит в панель. Печать -- «[скрыто]».
type Credentials struct {
	BaseURL  string
	User     string
	Password string
	CertPEM  []byte
	KeyPEM   []byte
}

func (Credentials) String() string       { return hiddenValue }
func (Credentials) GoString() string     { return hiddenValue }
func (Credentials) LogValue() slog.Value { return slog.StringValue(hiddenValue) }

type ClientOptions struct {
	// RootCAs -- корни для сертификата панели. nil -- системные: в проде
	// Caddy отдаёт публичный сертификат. Тесты и песочница кладут свой CA.
	RootCAs *x509.CertPool
	Timeout time.Duration
}

type Client struct {
	base string
	user string
	pass string
	hc   *http.Client
}

func NewClient(c Credentials, o ClientOptions) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("awg3-панель: адрес должен быть вида https://имя[:порт]")
	}
	pair, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		return nil, &Error{Kind: KindCert, Msg: "клиентский сертификат не читается"}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tr := &http.Transport{
		// Прокси из окружения не берётся: Basic и клиентский сертификат --
		// только напрямую к панели.
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			RootCAs:      o.RootCAs,
			Certificates: []tls.Certificate{pair},
		},
		TLSHandshakeTimeout: timeout,
		MaxIdleConns:        2,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Client{
		base: u.String(),
		user: c.User,
		pass: c.Password,
		hc: &http.Client{
			Transport: tr,
			Timeout:   timeout,
			// Редирект от прокси (логин, http→https) -- не ответ панели; идти
			// по нему с Basic незачем.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type reply struct {
	status int
	ctype  string
	body   []byte
}

func (r reply) isJSON() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.ctype)), "application/json")
}

// do -- один запрос. mutation -- маршрут есть только в полной сборке панели:
// 405 и 404 не-JSON на нём значат «readonly».
func (c *Client) do(ctx context.Context, method, path string, body []byte, mutation bool) (reply, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return reply{}, &Error{Kind: KindBadResponse, Msg: "запрос к панели не собрался", cause: err}
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return reply{}, classifyTransport(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return reply{}, &Error{Kind: KindUnreachable, Status: resp.StatusCode, Msg: "ответ панели оборвался", cause: err}
	}
	if len(raw) > maxBody {
		return reply{}, &Error{Kind: KindBadResponse, Status: resp.StatusCode, Msg: "ответ панели больше 1 МБ"}
	}
	r := reply{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: raw}
	if err := classifyStatus(r, mutation); err != nil {
		return reply{}, err
	}
	return r, nil
}

func classifyStatus(r reply, mutation bool) error {
	switch {
	case r.status >= 200 && r.status < 300:
		return nil
	case r.status == http.StatusUnauthorized:
		return &Error{Kind: KindBadPassword, Status: r.status, Msg: "панель не приняла логин или пароль"}
	case r.status == http.StatusTooManyRequests:
		return &Error{Kind: KindBanned, Status: r.status, Msg: "панель ограничила вход"}
	case mutation && (r.status == http.StatusMethodNotAllowed || (r.status == http.StatusNotFound && !r.isJSON())):
		return &Error{Kind: KindReadonly, Status: r.status, Msg: "панель только для просмотра"}
	case r.status == http.StatusNotFound && r.isJSON():
		return &Error{Kind: KindNotFound, Status: r.status, Msg: panelErrorText(r.body, "не найдено")}
	case r.status == http.StatusBadRequest && r.isJSON():
		return &Error{Kind: KindInvalid, Status: r.status, Msg: panelErrorText(r.body, "панель отвергла запрос")}
	case r.status >= 500:
		return &Error{Kind: KindUnreachable, Status: r.status, Msg: fmt.Sprintf("панель ответила %d", r.status)}
	default:
		return &Error{Kind: KindBadResponse, Status: r.status, Msg: fmt.Sprintf("панель ответила %d", r.status)}
	}
}

// panelErrorText -- {"error": "..."} панели, не длиннее 200 рун, без
// управляющих символов.
func panelErrorText(body []byte, fallback string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || strings.TrimSpace(e.Error) == "" {
		return fallback
	}
	out := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, e.Error))
	if len(out) > 200 {
		out = out[:200]
	}
	return string(out)
}

func classifyTransport(err error) error {
	if isTLSError(err) {
		return &Error{Kind: KindCert, Msg: "сертификат не принят", cause: err}
	}
	var ue *url.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ue) && ue.Timeout()) {
		return &Error{Kind: KindUnreachable, Msg: "панель не ответила за 10 секунд", cause: err}
	}
	return &Error{Kind: KindUnreachable, Msg: "панель недоступна", cause: err}
}

// isTLSError -- отказ на TLS: сервер не принял наш сертификат (алерт
// «remote error: tls: …» -- certificate required / bad certificate / unknown
// authority, сверено на Go 1.27 29.09) или сертификат сервера не прошёл
// проверку.
func isTLSError(err error) bool {
	var cv *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var he x509.HostnameError
	var ci x509.CertificateInvalidError
	if errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &he) || errors.As(err, &ci) {
		return true
	}
	return strings.Contains(err.Error(), "remote error: tls:")
}

func decodeJSON(r reply, dst any) error {
	if !r.isJSON() {
		return &Error{Kind: KindBadResponse, Status: r.status, Msg: "панель ответила не JSON — проверьте адрес"}
	}
	if err := json.Unmarshal(r.body, dst); err != nil {
		return &Error{Kind: KindBadResponse, Status: r.status, Msg: "ответ панели не разобрался", cause: err}
	}
	return nil
}

func ifacePath(iface string) string { return "/api/ifaces/" + url.PathEscape(iface) }

func (c *Client) Ifaces(ctx context.Context) ([]Iface, error) {
	r, err := c.do(ctx, http.MethodGet, "/api/ifaces", nil, false)
	if err != nil {
		return nil, err
	}
	var out []Iface
	if err := decodeJSON(r, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Peers(ctx context.Context, iface string) ([]Peer, error) {
	r, err := c.do(ctx, http.MethodGet, ifacePath(iface)+"/peers", nil, false)
	if err != nil {
		return nil, err
	}
	out := []Peer{}
	if err := decodeJSON(r, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Summary(ctx context.Context, iface string) (Summary, error) {
	r, err := c.do(ctx, http.MethodGet, ifacePath(iface)+"/summary", nil, false)
	if err != nil {
		return Summary{}, err
	}
	var out Summary
	return out, decodeJSON(r, &out)
}

func (c *Client) AddPeer(ctx context.Context, iface, name string) (Issued, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	r, err := c.do(ctx, http.MethodPost, ifacePath(iface)+"/peers", body, true)
	if err != nil {
		return Issued{}, err
	}
	var out Issued
	if err := decodeJSON(r, &out); err != nil {
		return Issued{}, err
	}
	if out.ID == "" || !strings.Contains(out.Config, "[Interface]") {
		return Issued{}, &Error{Kind: KindBadResponse, Status: r.status, Msg: "панель не вернула конфиг"}
	}
	return out, nil
}

// PeerConfig -- тот же .conf уже выпущенного пира: панель собирает его заново
// из сохранённого ключа, слот не тратится.
func (c *Client) PeerConfig(ctx context.Context, iface, peerID string) ([]byte, error) {
	r, err := c.do(ctx, http.MethodGet, ifacePath(iface)+"/peers/"+url.PathEscape(peerID)+"/config", nil, true)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(r.body, []byte("[Interface]")) {
		return nil, &Error{Kind: KindBadResponse, Status: r.status, Msg: "панель вернула не конфиг"}
	}
	return r.body, nil
}
