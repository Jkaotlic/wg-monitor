package exitprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

const threeTunnels = `[{"id":"awg10","name":"A","enabled":true,"status":"running","interfaceName":"nwg0"},
{"id":"awg11","name":"B","enabled":true,"status":"running","interfaceName":"opkgtun0"},
{"id":"awg12","name":"C","enabled":false,"status":"stopped","interfaceName":"nwg2"}]`

type fakeRouter struct {
	mu      sync.Mutex
	tunnels string
	ipCalls atomic.Int32
	testIP  http.HandlerFunc
}

func (f *fakeRouter) setTunnels(s string) { f.mu.Lock(); f.tunnels = s; f.mu.Unlock() }

func newFakeRouter(t *testing.T, testIP http.HandlerFunc) (*fakeRouter, *awgmgr.Client) {
	t.Helper()
	f := &fakeRouter{tunnels: threeTunnels, testIP: testIP}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tunnels/all":
			f.mu.Lock()
			body := `{"success":true,"data":{"tunnels":` + f.tunnels + `}}`
			f.mu.Unlock()
			_, _ = w.Write([]byte(body))
		case "/api/test/ip":
			f.ipCalls.Add(1)
			f.testIP(w, r)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return f, awgmgr.New(srv.URL)
}

// ipOK отвечает адресом, собранным из номера туннеля: awg10 -> 203.0.113.10.
func ipOK(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	_, _ = w.Write([]byte(`{"success":true,"data":{"directIp":"198.51.100.4","vpnIp":"203.0.113.` + id[3:] + `","endpointIp":"","ipChanged":true}}`))
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{t: time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)} }
func ownSame(_ context.Context, _ string) (string, string, error) {
	return "198.51.100.4", "198.51.100.4", nil
}

func TestStepRotatesRunningTunnelsAndRespectsPerTunnel(t *testing.T) {
	_, cli := newFakeRouter(t, ipOK)
	c := newClock()
	p := &Prober{Client: cli, Now: c.now, PerTunnel: 20 * time.Minute, Own: ownSame}
	ctx := context.Background()
	if got := p.Step(ctx); got != "awg10" {
		t.Fatalf("первый замер %q, хотим awg10", got)
	}
	if got := p.Step(ctx); got != "awg11" {
		t.Fatalf("второй замер %q, хотим awg11 (остановленный awg12 не мерим)", got)
	}
	if got := p.Step(ctx); got != "" {
		t.Fatalf("срок не подошёл, а мерим %q", got)
	}
	c.add(21 * time.Minute)
	if got := p.Step(ctx); got != "awg10" {
		t.Fatalf("после срока %q, хотим самый давний awg10", got)
	}
	snap := p.Snapshot()
	if snap == nil || snap.Tunnels["awg10"].VPNIP != "203.0.113.10" || snap.Tunnels["awg10"].Source != "awgm" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Tunnels["awg10"].Changed == nil || !*snap.Tunnels["awg10"].Changed {
		t.Fatal("changed потерян")
	}
}

func TestFallsBackToOwnMeasureOn404(t *testing.T) {
	_, cli := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	var ifaces []string
	p := &Prober{Client: cli, Now: newClock().now, Own: func(_ context.Context, iface string) (string, string, error) {
		ifaces = append(ifaces, iface)
		return "198.51.100.4", "198.51.100.4", nil
	}}
	p.Step(context.Background())
	got := p.Snapshot().Tunnels["awg10"]
	if got.Source != "agent" || len(ifaces) != 1 || ifaces[0] != "nwg0" {
		t.Fatalf("probe=%+v ifaces=%v", got, ifaces)
	}
	if got.Changed == nil || *got.Changed {
		t.Fatalf("одинаковые адреса обязаны дать changed=false: %+v", got)
	}
}

func TestAwgmFailuresPauseAwgmForAnHour(t *testing.T) {
	f, cli := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	c := newClock()
	p := &Prober{Client: cli, Now: c.now, PerTunnel: 20 * time.Minute, Own: ownSame}
	ctx := context.Background()
	p.Step(ctx)
	p.Step(ctx)
	if n := f.ipCalls.Load(); n != 2 {
		t.Fatalf("вызовов test/ip %d, хотим 2", n)
	}
	c.add(21 * time.Minute)
	p.Step(ctx)
	if n := f.ipCalls.Load(); n != 2 {
		t.Fatalf("после двух отказов awg-manager не спрашиваем час, а вызовов %d", n)
	}
	c.add(61 * time.Minute)
	p.Step(ctx)
	if n := f.ipCalls.Load(); n != 3 {
		t.Fatalf("через час снова спрашиваем awg-manager, вызовов %d", n)
	}
}

func TestSnapshotDropsTunnelsMissingFromInventory(t *testing.T) {
	f, cli := newFakeRouter(t, ipOK)
	p := &Prober{Client: cli, Now: newClock().now, Own: ownSame}
	ctx := context.Background()
	p.Step(ctx)
	p.Step(ctx)
	f.setTunnels(`[{"id":"awg11","name":"B","enabled":true,"status":"running","interfaceName":"opkgtun0"}]`)
	p.Step(ctx)
	snap := p.Snapshot()
	if _, ok := snap.Tunnels["awg10"]; ok || len(snap.Tunnels) != 1 {
		t.Fatalf("удалённый VPN-туннель остался в фактах: %+v", snap.Tunnels)
	}
}

func TestProbeNowRefusesStoppedTunnel(t *testing.T) {
	_, cli := newFakeRouter(t, ipOK)
	p := &Prober{Client: cli, Now: newClock().now, Own: ownSame}
	if _, err := p.ProbeNow(context.Background(), "awg12"); err == nil {
		t.Fatal("остановленный VPN-туннель мерить нечем")
	}
	got, err := p.ProbeNow(context.Background(), "awg11")
	if err != nil || got.VPNIP != "203.0.113.11" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

// Неполный ответ awg-manager -- не вердикт: без обоих адресов «совпало» и
// «разошлось» одинаково выдуманы.
func TestAwgmPartialAnswerHasNoVerdict(t *testing.T) {
	_, cli := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"directIp":"198.51.100.4","vpnIp":"","endpointIp":"","ipChanged":false}}`))
	})
	p := &Prober{Client: cli, Now: newClock().now, Own: ownSame}
	p.Step(context.Background())
	got := p.Snapshot().Tunnels["awg10"]
	if got.Changed != nil || got.Err == "" {
		t.Fatalf("got %+v", got)
	}
}

// После старта агента адреса выхода есть по всем работающим VPN-туннелям
// сразу, а не по одному за Every (с тремя туннелями -- через 15 минут).
func TestRunMeasuresAllRunningTunnelsRightAfterStart(t *testing.T) {
	_, cli := newFakeRouter(t, ipOK)
	p := &Prober{Client: cli, Every: time.Hour, Own: ownSame}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if snap := p.Snapshot(); snap != nil && len(snap.Tunnels) == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("через 3 с после старта замеры: %+v, хотим awg10 и awg11", p.Snapshot())
}
