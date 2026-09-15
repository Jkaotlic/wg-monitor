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

// Исходы опроса, которые не являются состоянием панели (awgmstate.*) и
// нужны воркеру (Task 5+), чтобы не путать их с молчанием роутера.
const (
	// ProbeCancelled -- опрос прерван контекстом ВЫЗЫВАЮЩЕГО (остановка
	// бэкенда: ctx.Done()/дедлайн ctx), а не собственным таймаутом Prober и
	// не ответом панели. RecordProbe для такого исхода звать нельзя: иначе
	// выключение процесса обнуляло бы серию "панель отвечает" точно так же,
	// как настоящий сон роутера (fix round 1, Minor #2).
	ProbeCancelled = "probe_cancelled"
	// ProbeInvalidURL -- awgm_url не разбирается или без схемы. Это ошибка
	// конфигурации роутера, а не "роутер спит" -- воркер не обязан ждать
	// вечно (fix round 1, Minor #2).
	ProbeInvalidURL = "probe_invalid_url"
)

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
// облачным релеем (offline); любой другой HTTP-ответ -- reachable; неразобранный
// или бессхемный адрес -- ProbeInvalidURL; отмена/дедлайн ctx ВЫЗЫВАЮЩЕГО --
// ProbeCancelled (не offline: это решение бэкенда, а не роутера); остальные
// ошибки транспорта -- по типу, затем по awgmstate.Classify; неопознанное --
// offline.
func (p *Prober) Probe(ctx context.Context, awgmURL string) string {
	target := strings.TrimRight(strings.TrimSpace(awgmURL), "/") + "/api/system/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ProbeInvalidURL
	}
	// Пароль/логин в самом awgm_url (https://u:p@host) иначе ушёл бы Basic
	// auth-заголовком -- опрос обязан быть без учётных данных безусловно
	// (fix round 1, Minor #3), а не только пока в базе не завели URL с
	// userinfo. Прокси (HTTP(S)_PROXY) при этом не трогаем -- поведение
	// транспорта по умолчанию.
	req.URL.User = nil
	resp, err := p.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// Свой Client.Timeout Prober'а срабатывает через ВНУТРЕННИЙ,
			// производный контекст -- исходный ctx (переданный сюда
			// вызывающим) при этом остаётся неотменённым. Ненулевой
			// ctx.Err() здесь может значить только то, что ctx отменил или
			// довёл до дедлайна сам вызывающий (например, остановка
			// бэкенда), а не сам Probe.
			return ProbeCancelled
		}
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
	if isUnsupportedScheme(err) {
		return ProbeInvalidURL
	}
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

// isUnsupportedScheme -- awgm_url без http(s):// или с неизвестной схемой:
// транспорт отказывает раньше, чем успевает набрать номер, и это конфиг
// роутера, а не "спит" (fix round 1, Minor #2).
func isUnsupportedScheme(err error) bool {
	var ue *url.Error
	if !errors.As(err, &ue) || ue.Err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(ue.Err.Error()), "unsupported protocol scheme")
}
