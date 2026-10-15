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
