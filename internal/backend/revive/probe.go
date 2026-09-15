package revive

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
)

// ProbeTimeout -- сколько ждём ответа панели за один опрос.
const ProbeTimeout = 10 * time.Second

// Prober опрашивает панель роутера без учётных данных. Учётные данные сюда
// не передаются вовсе: опрос отвечает только на «роутер появился?».
type Prober struct {
	Client *http.Client
}

func NewProber(timeout time.Duration) *Prober {
	if timeout <= 0 {
		timeout = ProbeTimeout
	}
	return &Prober{Client: &http.Client{
		Timeout: timeout,
		// Любой ответ панели -- уже «роутер жив»; переход по редиректу на
		// страницу входа только удлинил бы опрос.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Probe -- GET <awgm_url>/api/system/info. 502/503/504 -- роутер спит за
// облачным релеем (offline); любой другой HTTP-ответ -- reachable; ошибки
// транспорта -- по типу, затем по awgmstate.Classify; неопознанное -- offline.
func (p *Prober) Probe(ctx context.Context, awgmURL string) string {
	target := strings.TrimRight(strings.TrimSpace(awgmURL), "/") + "/api/system/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return awgmstate.Offline
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return classifyTransport(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return awgmstate.Offline
	}
	return awgmstate.Reachable
}

func classifyTransport(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTimeout {
			return awgmstate.Offline
		}
		return awgmstate.DNSError
	}
	var (
		verifyErr  *tls.CertificateVerificationError
		hostErr    x509.HostnameError
		authErr    x509.UnknownAuthorityError
		invalidErr x509.CertificateInvalidError
	)
	if errors.As(err, &verifyErr) || errors.As(err, &hostErr) || errors.As(err, &authErr) || errors.As(err, &invalidErr) {
		return awgmstate.TLSError
	}

	msg := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		msg = ue.Err.Error() // без адреса: имя хоста не должно влиять на вывод
		if ue.Timeout() {
			msg += " timeout"
		}
	}
	switch st := awgmstate.Classify(msg); st {
	case awgmstate.TLSError, awgmstate.DNSError, awgmstate.Offline:
		return st
	}
	return awgmstate.Offline
}
