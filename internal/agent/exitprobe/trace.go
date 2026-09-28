// Package exitprobe меряет, каким адресом видно трафик через каждый
// VPN-туннель и напрямую. Один замерщик на агента: периодический замер,
// кнопка в мини-аппе и старые команды check_via_tunnel/check_direct ходят
// через одну функцию trace, а не через три копии.
package exitprobe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
)

// TraceURL -- куда спрашивать «каким адресом меня видно». Переменная, чтобы
// тесты подставляли свой сервер.
var TraceURL = "https://1.1.1.1/cdn-cgi/trace"

// FetchExitIP спрашивает Cloudflare trace через данный клиент (привязанный к
// интерфейсу или нет) и возвращает строку ip=.
func FetchExitIP(ctx context.Context, httpc *http.Client, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, TraceURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(line, "ip="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("no ip= line in trace response")
}

// IfaceClient -- HTTP-клиент, чей трафик прибит к интерфейсу (SO_BINDTODEVICE
// через checks.IfaceDialer, как у проверки external_reach).
func IfaceClient(iface string, timeout time.Duration) *http.Client {
	d := checks.IfaceDialer(iface)
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

// OwnMeasure -- свой замер, когда awg-manager /api/test/ip не умеет.
func OwnMeasure(ctx context.Context, iface string) (string, string, error) {
	vpn, vErr := FetchExitIP(ctx, IfaceClient(iface, 6*time.Second), 5*time.Second)
	direct, dErr := FetchExitIP(ctx, &http.Client{Timeout: 6 * time.Second}, 5*time.Second)
	if vErr != nil {
		vErr = fmt.Errorf("через VPN-туннель: %w", vErr)
	}
	if dErr != nil {
		dErr = fmt.Errorf("напрямую: %w", dErr)
	}
	return vpn, direct, errors.Join(vErr, dErr)
}
