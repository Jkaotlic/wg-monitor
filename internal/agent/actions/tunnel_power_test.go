package actions

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Включение и выключение туннеля ПО ИДЕНТИФИКАТОРУ, через awg-manager.
//
// Прежний путь (tunnel_enable/tunnel_disable) умеет только ndmc, то есть
// только NDMS-интерфейсы. Половина туннелей живого роутера -- opkg, у них
// имени в NDMS нет вовсе, и выключить их было нечем: ни из бота, ни из
// приложения, ни мастеру замены конфига, которому это нужно на шаге отката.
func TestRunner_TunnelPower_StartsByID(t *testing.T) {
	var path string
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path + "?" + r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID: "p1", Action: "tunnel_power", Args: map[string]any{"tunnel_id": "awg11", "on": true},
	})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if path != "/api/control/start?id=awg11" {
		t.Fatalf("путь = %q", path)
	}
}

func TestRunner_TunnelPower_StopsByID(t *testing.T) {
	var path string
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path + "?" + r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID: "p2", Action: "tunnel_power", Args: map[string]any{"tunnel_id": "awg11", "on": false},
	})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if path != "/api/control/stop?id=awg11" {
		t.Fatalf("путь = %q", path)
	}
}

// Без идентификатора действие ничего не трогает: пустой id в query -- это
// запрос «останови неизвестно что».
func TestRunner_TunnelPower_RequiresTunnelID(t *testing.T) {
	called := false
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "p3", Action: "tunnel_power", Args: map[string]any{"on": true}})
	if res.Status != "err" || !strings.Contains(res.Output, "tunnel_id") {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if called {
		t.Fatal("роутер не должен был получить ни одного запроса")
	}
}
