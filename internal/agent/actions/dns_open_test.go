package actions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeResolveExec отвечает на nslookup к dns-proxy роутера заранее заданным
// выводом. Именно ответ РОУТЕРА и важен: проверка отвечает на вопрос «что
// увидит человек за этим роутером», а не «что видит агент со своей машины».
type fakeResolveExec struct {
	out   string
	err   error
	calls []string
}

func (f *fakeResolveExec) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return []byte(f.out), f.err
}

const nslookupOK = `Server:    127.0.0.1
Address 1: 127.0.0.1 localhost

Name:      gosuslugi.example.com
Address 1: 198.51.100.7
`

// Имя разрешается ЧЕРЕЗ dns-proxy самого роутера, а не через резолвер агента.
func TestDNSOpenResolvesThroughRouterProxy(t *testing.T) {
	f := &fakeResolveExec{out: nslookupOK}
	swapDial(t, func(string, string, time.Duration) (net.Conn, error) { return nil, errors.New("no net") })

	_, out := DNSOpen(context.Background(), f.exec, "gosuslugi.example.com")

	if len(f.calls) == 0 {
		t.Fatal("роутер не спрошен вовсе")
	}
	if !strings.Contains(f.calls[0], "127.0.0.1") {
		t.Errorf("имя разрешалось не через dns-proxy роутера: %q", f.calls[0])
	}
	if !strings.Contains(out, "198.51.100.7") {
		t.Errorf("в ответе нет адреса, который отдал роутер:\n%s", out)
	}
}

// Разные исходы -- разные статусы. «Имя не разрешилось» и «разрешилось, но сайт
// не открылся» -- разные состояния: первое чинится настройкой DNS, второе
// маршрутом или блокировкой. Слить их в одно значило бы послать человека чинить
// не то.
func TestDNSOpenSeparatesResolveFailureFromConnectFailure(t *testing.T) {
	swapDial(t, func(string, string, time.Duration) (net.Conn, error) { return nil, errors.New("timeout") })

	f := &fakeResolveExec{err: fmt.Errorf("nslookup: not found")}
	statusResolve, outResolve := DNSOpen(context.Background(), f.exec, "nowhere.example.com")

	f2 := &fakeResolveExec{out: nslookupOK}
	statusConnect, outConnect := DNSOpen(context.Background(), f2.exec, "gosuslugi.example.com")

	if statusResolve == statusConnect {
		t.Fatalf("оба исхода дали %q — человека пошлют чинить не то:\nимя: %s\nсоединение: %s",
			statusResolve, outResolve, outConnect)
	}
	if !strings.Contains(outResolve, "не разрешилось") {
		t.Errorf("не сказано, что дело в имени:\n%s", outResolve)
	}
	if !strings.Contains(outConnect, "198.51.100.7") || !strings.Contains(outConnect, "не открыл") {
		t.Errorf("не сказано, что имя разрешилось, а соединение не встало:\n%s", outConnect)
	}
}

// Удачный исход: имя разрешилось и сайт открылся.
func TestDNSOpenSaysOKWhenSiteAnswers(t *testing.T) {
	f := &fakeResolveExec{out: nslookupOK}
	swapDial(t, func(string, string, time.Duration) (net.Conn, error) { return fakeConn{}, nil })

	status, out := DNSOpen(context.Background(), f.exec, "gosuslugi.example.com")
	if status != "ok" {
		t.Fatalf("status = %q, хотим ok\n%s", status, out)
	}
	if !strings.Contains(out, "198.51.100.7") {
		t.Errorf("в ответе нет адреса:\n%s", out)
	}
}

func swapDial(t *testing.T, fn func(network, addr string, d time.Duration) (net.Conn, error)) {
	t.Helper()
	saved := dnsOpenDial
	dnsOpenDial = fn
	t.Cleanup(func() { dnsOpenDial = saved })
}

type fakeConn struct{ net.Conn }

func (fakeConn) Close() error { return nil }
