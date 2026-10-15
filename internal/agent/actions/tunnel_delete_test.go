package actions

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// AG-08: остановка в awg-manager сама ставит enabled=false. Переключатель
// enabled по снимку ДО остановки включил бы туннель обратно, а при упавшем
// удалении на роутере остался бы включённый остановленный туннель, который
// сторож зависаний потом сам запустит.
func TestRunner_TunnelDelete_NoReEnableAfterStop(t *testing.T) {
	var (
		mu      sync.Mutex
		enabled = true
		status  = "running"
		calls   []string
	)
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/tunnels/get":
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"id":"awg10","name":"old","status":%q,"enabled":%v,"interfaceName":"opkgtun10"}}`, status, enabled)
			return
		case "/api/control/stop":
			enabled, status = false, "stopped"
		case "/api/control/toggle-enabled":
			enabled = !enabled
		case "/api/tunnels/delete":
			calls = append(calls, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"error":"delete failed"}`))
			return
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		calls = append(calls, r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true}`))
	})

	r := Runner{AwgClient: awgmgrFake(t, mux), Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID: "del1", Action: "tunnel_delete", Args: map[string]any{"tunnel_id": "awg10"},
	})

	if res.Status != "err" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if enabled {
		t.Fatalf("tunnel re-enabled before a failed delete; calls:\n%s", strings.Join(calls, "\n"))
	}
	if got := strings.Join(calls, "\n"); got != "/api/control/stop\n/api/tunnels/delete" {
		t.Fatalf("calls:\n%s", got)
	}
}

// AG-05: действия с туннелем просят внеплановый отчёт (ReportNow), а не
// «вернулся после отвала» (ForceRecheck → Resumed=true): у мобильного роутера
// бэкенд выбросил бы из такого отчёта упавшие проверки туннелей.
func TestRunner_TunnelActions_ReportNowNotForceResumed(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tunnels/get" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg11","status":"running","enabled":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	for _, cmd := range []wire.Command{
		{ID: "a", Action: "tunnel_restart", Args: map[string]any{"tunnel_id": "awg11"}},
		{ID: "b", Action: "tunnel_power", Args: map[string]any{"tunnel_id": "awg11", "on": true}},
		{ID: "c", Action: "tunnel_enable", Args: map[string]any{"ndms_name": "Wireguard3"}},
	} {
		var now, resumed int
		r := Runner{
			AwgClient:    awgmgrFake(t, ok),
			Exec:         func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, nil },
			Now:          mockNow(),
			ForceRecheck: func(ctx context.Context) { resumed++ },
			ReportNow:    func(ctx context.Context) { now++ },
		}
		res := r.Execute(context.Background(), cmd)
		if res.Status != "ok" {
			t.Fatalf("%s: status=%q output=%q", cmd.Action, res.Status, res.Output)
		}
		if now != 1 || resumed != 0 {
			t.Fatalf("%s: ReportNow=%d ForceRecheck=%d, want 1/0", cmd.Action, now, resumed)
		}
	}
}
