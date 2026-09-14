package actions

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Действие должно быть доступно через диспетчер: без этой проводки кнопка
// «проверить сайт» в мини-аппе упрётся в «неизвестное действие».
func TestDispatchDNSOpenPassesDomain(t *testing.T) {
	f := &fakeResolveExec{out: nslookupOK}
	swapDial(t, func(string, string, time.Duration) (net.Conn, error) { return fakeConn{}, nil })
	r := &Runner{Exec: f.exec}

	res := r.Execute(context.Background(), wire.Command{
		ID: "c1", Action: "dns_open", Args: map[string]any{"domain": "gosuslugi.example.com"},
	})

	if res.Status != "ok" {
		t.Fatalf("status = %q\n%s", res.Status, res.Output)
	}
	if len(f.calls) == 0 || !strings.Contains(f.calls[0], "gosuslugi.example.com") {
		t.Errorf("имя не доехало до действия: %v", f.calls)
	}
}

// Пустое имя -- отказ, а не запрос в никуда: команда без имени сайта бессмысленна.
func TestDispatchDNSOpenRejectsEmptyDomain(t *testing.T) {
	f := &fakeResolveExec{out: nslookupOK}
	r := &Runner{Exec: f.exec}

	res := r.Execute(context.Background(), wire.Command{ID: "c2", Action: "dns_open"})

	if res.Status == "ok" {
		t.Fatalf("пустое имя принято: %s", res.Output)
	}
	if len(f.calls) != 0 {
		t.Errorf("роутер спрошен впустую: %v", f.calls)
	}
}
