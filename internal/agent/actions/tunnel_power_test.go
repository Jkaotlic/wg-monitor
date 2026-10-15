package actions

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Включение и выключение туннеля ПО ИДЕНТИФИКАТОРУ, через awg-manager.
//
// Прежний путь (tunnel_enable/tunnel_disable) умеет только ndmc, то есть
// только NDMS-интерфейсы. Половина туннелей живого роутера -- opkg, у них
// имени в NDMS нет вовсе, и выключить их было нечем: ни из бота, ни из
// приложения, ни мастеру замены конфига, которому это нужно на шаге отката.
// powerFake -- awg-manager с состоянием одного туннеля, как его ведёт живой
// роутер (заметка mcp-stop-disables-tunnel, 11.09): stop выключает туннель
// (enabled=false), start выключенный не поднимает -- нужен toggle-enabled.
type powerFake struct {
	mu      sync.Mutex
	enabled bool
	status  string
	// startTo -- куда start переводит включённый туннель ("" → running).
	startTo string
	// stopTo -- куда stop переводит туннель ("" → stopped).
	stopTo string
	calls  []string
	gets   int
}

func (f *powerFake) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if id := r.URL.Query().Get("id"); id != "awg11" {
			t.Errorf("id = %q", id)
		}
		switch r.URL.Path {
		case "/api/tunnels/get":
			f.gets++
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"id":"awg11","name":"nl","status":%q,"enabled":%v}}`, f.status, f.enabled)
			return
		case "/api/control/start":
			if f.enabled {
				f.status = "running"
				if f.startTo != "" {
					f.status = f.startTo
				}
			}
		case "/api/control/stop":
			f.enabled = false
			f.status = "stopped"
			if f.stopTo != "" {
				f.status = f.stopTo
			}
		case "/api/control/toggle-enabled":
			f.enabled = !f.enabled
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		f.calls = append(f.calls, r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
}

func powerRunner(t *testing.T, f *powerFake) *Runner {
	return &Runner{
		AwgClient: awgmgrFake(t, f.handler(t)),
		Now:       mockNow(),
		Sleep:     func(ctx context.Context, d time.Duration) error { return ctx.Err() },
	}
}

func runPower(r *Runner, on bool) wire.CommandResult {
	return r.Execute(context.Background(), wire.Command{
		ID: "p1", Action: "tunnel_power", Args: map[string]any{"tunnel_id": "awg11", "on": on},
	})
}

// AG-02: туннель, выключенный этой же кнопкой (stop ставит enabled=false),
// включается: сначала enable, потом start; «запущен» -- только увидев running.
func TestRunner_TunnelPower_OnEnablesDisabledTunnel(t *testing.T) {
	f := &powerFake{enabled: false, status: "stopped"}
	res := runPower(powerRunner(t, f), true)
	if res.Status != "ok" || !strings.Contains(res.Output, "запущен") {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	want := "/api/control/toggle-enabled\n/api/control/start"
	if got := strings.Join(f.calls, "\n"); got != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
	}
	if !f.enabled || f.status != "running" {
		t.Fatalf("tunnel left enabled=%v status=%q", f.enabled, f.status)
	}
}

// Включённый туннель повторно не переключается: toggle выключил бы его.
func TestRunner_TunnelPower_OnEnabledTunnelOnlyStarts(t *testing.T) {
	f := &powerFake{enabled: true, status: "stopped"}
	res := runPower(powerRunner(t, f), true)
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if got := strings.Join(f.calls, "\n"); got != "/api/control/start" {
		t.Fatalf("calls = %q, want only start", got)
	}
}

// AG-02: start ответил success, но туннель за срок не дошёл до running --
// ответ не ok и называет настоящее состояние, «запущен» не говорит.
func TestRunner_TunnelPower_OnNotRunningIsNotOK(t *testing.T) {
	f := &powerFake{enabled: true, status: "stopped", startTo: "starting"}
	r := powerRunner(t, f)
	var slept time.Duration
	r.Sleep = func(ctx context.Context, d time.Duration) error { slept += d; return nil }
	res := runPower(r, true)
	if res.Status == "ok" || strings.Contains(res.Output, "запущен") {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "запускается") {
		t.Fatalf("output does not name the real state: %q", res.Output)
	}
	if slept < 10*time.Second || slept > 15*time.Second {
		t.Fatalf("waited %v, want 10..15s", slept)
	}
}

func TestRunner_TunnelPower_OffChecksStopped(t *testing.T) {
	f := &powerFake{enabled: true, status: "running"}
	res := runPower(powerRunner(t, f), false)
	if res.Status != "ok" || !strings.Contains(res.Output, "остановлен") {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if got := strings.Join(f.calls, "\n"); got != "/api/control/stop" {
		t.Fatalf("calls = %q", got)
	}

	f = &powerFake{enabled: true, status: "running", stopTo: "stopping"}
	res = runPower(powerRunner(t, f), false)
	if res.Status == "ok" || !strings.Contains(res.Output, "останавливается") {
		t.Fatalf("stuck stop: status=%q output=%q", res.Status, res.Output)
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
