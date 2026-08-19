package actions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// awg-manager 2.17.2 отдаёт 404 на /api/pingcheck/toggle: эндпоинта больше
// нет, вместо него появилась пара /api/tunnels/pingcheck и .../remove
// (проверено на живом роутере r15). Пока код бил в старый адрес, включение
// сторожа держалось на одном лишь запасном пути через ndmc -- а у opkg-
// туннелей нет ndms_name, и для них функция была сломана целиком.
func TestPingCheckToggle_UsesPerTunnelEndpoint(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if strings.HasPrefix(r.URL.Path, "/api/tunnels/pingcheck") {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	failExec := func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ndmc не должен зваться, когда штатный эндпоинт ответил")
		return nil, nil
	}

	if err := PingCheckToggle(context.Background(), awgmgr.New(srv.URL), failExec, "awg11", "", true); err != nil {
		t.Fatalf("включение: %v", err)
	}
	if len(hits) != 1 || !strings.HasPrefix(hits[0], "POST /api/tunnels/pingcheck?") || !strings.Contains(hits[0], "id=awg11") {
		t.Fatalf("включение пошло не туда: %v", hits)
	}

	hits = nil
	if err := PingCheckToggle(context.Background(), awgmgr.New(srv.URL), failExec, "awg11", "", false); err != nil {
		t.Fatalf("выключение: %v", err)
	}
	if len(hits) != 1 || !strings.HasPrefix(hits[0], "POST /api/tunnels/pingcheck/remove?") {
		t.Fatalf("выключение пошло не туда: %v", hits)
	}
}

// На сборке, где новой пары ещё нет, остаётся запасной путь через ndmc --
// но только он, и только когда имя интерфейса известно.
func TestPingCheckToggle_FallsBackOnOldBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var ran string
	exec := func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = name + " " + strings.Join(args, " ")
		return nil, nil
	}
	if err := PingCheckToggle(context.Background(), awgmgr.New(srv.URL), exec, "awg20", "Wireguard0", true); err != nil {
		t.Fatalf("запасной путь: %v", err)
	}
	if !strings.Contains(ran, "interface Wireguard0 ping-check") {
		t.Fatalf("ndmc позвали не так: %q", ran)
	}
}
