package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// connStub -- awg-manager с заданными туннелями и ответом проверки связи на
// каждый. conn -- обработчик /api/test/connectivity; nil -- сборка без
// эндпоинта (404). asked считает, у кого спросили связь.
type connStub struct {
	tunnels string
	conn    func(w http.ResponseWriter, r *http.Request)

	mu    sync.Mutex
	asked map[string]int
}

func (s *connStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	s.asked = map[string]int{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[` + s.tunnels + `]}}`))
		case "/api/pingcheck/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
		case "/api/dns-routes/list", "/api/static-routes/list":
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		case "/api/settings/get":
			_, _ = w.Write([]byte(`{"success":true,"data":{"download":{"routeTag":""}}}`))
		case "/api/monitoring/matrix", "/api/routing/access-policies", "/api/routing/policy-interfaces":
			w.WriteHeader(http.StatusNotFound)
		case "/api/test/connectivity":
			id := r.URL.Query().Get("id")
			s.mu.Lock()
			s.asked[id]++
			s.mu.Unlock()
			if s.conn == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			s.conn(w, r)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
}

func connTunnel(id, status string) string {
	fresh := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	return `{"id":"` + id + `","name":"line-` + id + `","type":"awg","status":"` + status + `","enabled":true,"interfaceName":"nwg` + id + `","lastHandshake":"` + fresh + `"}`
}

func runConn(t *testing.T, s *connStub) map[string]wire.Check {
	t.Helper()
	srv := s.server(t)
	defer srv.Close()
	out := map[string]wire.Check{}
	for _, c := range (TunnelsCheck{Client: awgmgr.New(srv.URL)}).Run(context.Background(), Deps{}) {
		out[c.Name] = c
	}
	return out
}

func writeConn(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

// Ровно та форма, что на router4car4 02.11: awg-manager пишет «Нет связи», а
// статус туннеля -- running. Сырьё доезжает как есть, статус не трогается.
func TestTunnelsCheck_AwgmConnDisconnected(t *testing.T) {
	s := &connStub{
		tunnels: connTunnel("awg10", "running"),
		conn:    writeConn(`{"success":true,"data":{"connected":false,"latency":null,"reason":"no reply from 203.0.113.7"}}`),
	}
	c := runConn(t, s)["tunnel_awg10"]
	if c.Details["awgm_conn"] != "disconnected" {
		t.Fatalf("awgm_conn = %v", c.Details["awgm_conn"])
	}
	if c.Details["status"] != "running" {
		t.Fatalf("status переписан: %v", c.Details["status"])
	}
	if _, ok := c.Details["awgm_conn_latency_ms"]; ok {
		t.Fatal("без связи задержки нет -- и ключа нет")
	}
	reason, _ := c.Details["awgm_conn_reason"].(string)
	if reason == "" || strings.Contains(reason, "203.0.113.7") {
		t.Fatalf("причина без адреса обязана доехать: %q", reason)
	}
}

func TestTunnelsCheck_AwgmConnConnectedLatency(t *testing.T) {
	s := &connStub{
		tunnels: connTunnel("awg10", "running"),
		conn:    writeConn(`{"success":true,"data":{"connected":true,"latency":64,"reason":""}}`),
	}
	c := runConn(t, s)["tunnel_awg10"]
	if c.Details["awgm_conn"] != "connected" || c.Details["awgm_conn_latency_ms"] != 64 {
		t.Fatalf("details = %+v", c.Details)
	}
	if _, ok := c.Details["awgm_conn_reason"]; ok {
		t.Fatal("пустая причина -- ключа нет")
	}
}

// UI awg-manager спрашивает связь у running и broken -- и мы так же; у
// остановленных и выключенных вопроса нет вовсе.
func TestTunnelsCheck_AwgmConnAskedOnlyRunningAndBroken(t *testing.T) {
	broken := `{"id":"awg11","name":"b","type":"awg","status":"broken","statusDetails":"Не запустился на vpn.example.com — перезапустите туннель","enabled":true,"interfaceName":"nwg11"}`
	disabled := `{"id":"awg14","name":"d","type":"awg","status":"disabled","enabled":false,"interfaceName":"nwg14"}`
	s := &connStub{
		tunnels: strings.Join([]string{connTunnel("awg10", "running"), broken, connTunnel("awg12", "stopped"), connTunnel("awg13", "needs_start"), disabled}, ","),
		conn:    writeConn(`{"success":true,"data":{"connected":false,"latency":null,"reason":"timeout"}}`),
	}
	out := runConn(t, s)
	if s.asked["awg10"] != 1 || s.asked["awg11"] != 1 {
		t.Fatalf("running и broken спрашиваются ровно раз за обход: %+v", s.asked)
	}
	for _, id := range []string{"awg12", "awg13", "awg14"} {
		if s.asked[id] != 0 {
			t.Fatalf("%s спрошен, хотя не running/broken: %+v", id, s.asked)
		}
		if _, ok := out["tunnel_"+id].Details["awgm_conn"]; ok {
			t.Fatalf("%s: ключ awgm_conn без вопроса", id)
		}
	}
	if out["tunnel_awg11"].Details["awgm_conn"] != "disconnected" {
		t.Fatalf("broken: %+v", out["tunnel_awg11"].Details)
	}
	sd, _ := out["tunnel_awg11"].Details["awgm_status_details"].(string)
	if !strings.Contains(sd, "перезапустите") || strings.Contains(sd, "vpn.example.com") {
		t.Fatalf("пояснение awg-manager без адреса обязано доехать: %q", sd)
	}
	if _, ok := out["tunnel_awg10"].Details["awgm_status_details"]; ok {
		t.Fatal("пустое пояснение -- ключа нет")
	}
}

func TestTunnelsCheck_AwgmConnMissingEndpointNoKey(t *testing.T) {
	s := &connStub{tunnels: connTunnel("awg10", "running")}
	c := runConn(t, s)["tunnel_awg10"]
	if _, ok := c.Details["awgm_conn"]; ok {
		t.Fatalf("сборка без эндпоинта: ключа нет, а не «нет связи»: %v", c.Details["awgm_conn"])
	}
}

func TestTunnelsCheck_AwgmConnFailureIsError(t *testing.T) {
	s := &connStub{
		tunnels: connTunnel("awg10", "running"),
		conn: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		},
	}
	c := runConn(t, s)["tunnel_awg10"]
	if c.Details["awgm_conn"] != "error" {
		t.Fatalf("awgm_conn = %v, хотим error", c.Details["awgm_conn"])
	}
}

// Новое поле только показывается: вердикт проверки при «Нет связи» тот же,
// что и без эндпоинта. Иначе это новый источник тревог без замера на парке.
func TestTunnelsCheck_AwgmConnDoesNotChangeVerdict(t *testing.T) {
	tunnels := connTunnel("awg10", "running") + "," + connTunnel("awg11", "broken")
	without := runConn(t, &connStub{tunnels: tunnels})
	with := runConn(t, &connStub{tunnels: tunnels, conn: writeConn(`{"success":true,"data":{"connected":false,"latency":null,"reason":"timeout"}}`)})
	for name, a := range without {
		b := with[name]
		for _, k := range []string{"error", "unverified", "unverified_reason", "note"} {
			if a.Details[k] != b.Details[k] {
				t.Fatalf("%s: %s поменялся: %v -> %v", name, k, a.Details[k], b.Details[k])
			}
		}
		if a.Status != b.Status {
			t.Fatalf("%s: вердикт поменялся: %s -> %s", name, a.Status, b.Status)
		}
	}
}

// Мёртвый туннель отвечает на проверку долго: обход не должен растягиваться на
// число туннелей, умноженное на таймаут. Не больше awgmConnParallel запросов
// разом, все вместе -- не дольше awgmConnTimeout.
func TestTunnelsCheck_AwgmConnBoundedParallelAndTimeout(t *testing.T) {
	old := awgmConnTimeout
	awgmConnTimeout = 200 * time.Millisecond
	defer func() { awgmConnTimeout = old }()

	var cur, peak atomic.Int32
	release := make(chan struct{})
	ids := []string{"awg1", "awg2", "awg3", "awg4", "awg5", "awg6", "awg7", "awg8"}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, connTunnel(id, "running"))
	}
	s := &connStub{
		tunnels: strings.Join(parts, ","),
		conn: func(w http.ResponseWriter, r *http.Request) {
			n := cur.Add(1)
			defer cur.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			// Мёртвый туннель: awg-manager молчит, пока агент не бросит ждать.
			select {
			case <-release:
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		},
	}
	start := time.Now()
	out := runConn(t, s)
	close(release)
	took := time.Since(start)
	if p := peak.Load(); p > int32(awgmConnParallel) || p < 2 {
		t.Fatalf("одновременно %d запросов, хотим 2..%d", p, awgmConnParallel)
	}
	// Срок общий на все вопросы: ~200 мс, а не 8/4*200; запас на медленный CI.
	if took > 1500*time.Millisecond {
		t.Fatalf("обход занял %v: таймаут на запрос не работает", took)
	}
	for _, id := range ids {
		if out["tunnel_"+id].Details["awgm_conn"] != "error" {
			t.Fatalf("%s: таймаут -- это error: %v", id, out["tunnel_"+id].Details["awgm_conn"])
		}
	}
}

// Срок всей проверки задаёт репортёр (perCheckTimeout). Вопросы о связи
// обязаны оставить ей запас: съешь они весь срок -- упали бы и списки правил,
// и матрица, и проверка целиком.
func TestTunnelsCheck_AwgmConnLeavesMarginToParentDeadline(t *testing.T) {
	s := &connStub{
		tunnels: connTunnel("awg10", "running"),
		conn: func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		},
	}
	srv := s.server(t)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), awgmConnMargin+300*time.Millisecond)
	defer cancel()
	start := time.Now()
	out := (TunnelsCheck{Client: awgmgr.New(srv.URL)}).Run(ctx, Deps{})
	if took := time.Since(start); took > awgmConnMargin {
		t.Fatalf("вопросы о связи съели срок проверки: %v", took)
	}
	for _, c := range out {
		if c.Name == "tunnel_awg10" {
			if c.Details["awgm_conn"] != "error" {
				t.Fatalf("awgm_conn = %v", c.Details["awgm_conn"])
			}
			return
		}
	}
	t.Fatal("проверки tunnel_awg10 нет")
}
