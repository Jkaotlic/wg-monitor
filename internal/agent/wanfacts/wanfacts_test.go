package wanfacts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// Форма со сверки С1 (workrouter 28.09).
const liveWAN = `{"success":true,"data":{"interfaces":{
	"eth3":{"up":true,"label":"Подключение Ethernet","priority":58248},
	"cdc_br0":{"up":false,"label":"Huawei Mobile Broadband","priority":36405},
	"apcli0":{"up":false,"label":"Wi-Fi","priority":0},
	"apclii0":{"up":false,"label":"Wi-Fi 5","priority":0}},"anyWANUp":true}}`

func wanServer(t *testing.T, status int, body string) (*awgmgr.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return awgmgr.New(srv.URL), &calls
}

func TestLiveShapeRolesByPriority(t *testing.T) {
	cli, _ := wanServer(t, 200, liveWAN)
	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	c := &Collector{Client: cli, Now: func() time.Time { return now }}
	f := c.Fact(context.Background())
	if f == nil || len(f.Links) != 2 {
		t.Fatalf("хотим две участвующие линии (priority 0 не участвует): %+v", f)
	}
	p, b := f.Links[0], f.Links[1]
	if p.Name != "eth3" || p.Role != "primary" || !p.Up || p.Label != "Подключение Ethernet" {
		t.Fatalf("основное = %+v", p)
	}
	if b.Name != "cdc_br0" || b.Role != "backup" || b.Up {
		t.Fatalf("резервное = %+v", b)
	}
	if p.PingCheck != nil || b.PingCheck != nil {
		t.Fatal("до сверки С3 о Ping-Check молчим: nil, а не «не задан»")
	}
}

func TestCollectorCachesForEvery(t *testing.T) {
	cli, calls := wanServer(t, 200, liveWAN)
	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	c := &Collector{Client: cli, Now: func() time.Time { return now }}
	c.Fact(context.Background())
	c.Fact(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("вызовов %d за окно кэша", calls.Load())
	}
	now = now.Add(11 * time.Minute)
	c.Fact(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("после окна кэша вызовов %d", calls.Load())
	}
}

func TestUnsupportedWAN(t *testing.T) {
	cli, _ := wanServer(t, 404, "404 page not found")
	f := (&Collector{Client: cli}).Fact(context.Background())
	if f == nil || !f.Unsupported || len(f.Links) != 0 {
		t.Fatalf("f = %+v", f)
	}
}
