package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Фаза F: ряд обмена ведёт сам роутер, агент его забирает и отдаёт как есть,
// суммируя только то, что экрану нужно числом.
func TestRunner_TunnelTraffic_OK(t *testing.T) {
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tunnels/traffic" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.URL.Query().Get("id") != "awg11" || r.URL.Query().Get("period") != "24h" {
			t.Errorf("query: %q", r.URL.RawQuery)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":{"points":[{"t":"2026-08-20T09:00:00Z","rx":1000,"tx":200},{"t":"2026-08-20T10:00:00Z","rx":2000,"tx":300}]}}`))
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "t1",
		Action: "tunnel_traffic",
		Args:   map[string]any{"tunnel_id": "awg11", "period": "24h"},
	})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	var out wire.TunnelTraffic
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("output is not wire.TunnelTraffic: %v (%s)", err, res.Output)
	}
	if out.TunnelID != "awg11" || out.Period != "24h" {
		t.Errorf("ответ обязан помнить, о чём его спросили: %+v", out)
	}
	// Суммы считает агент: экран рисует плитки-счётчики, и складывать ряд на
	// клиенте значило бы держать вторую формулу того же числа.
	if out.RXTotal != 3000 || out.TXTotal != 500 {
		t.Errorf("totals = %d/%d, want 3000/500", out.RXTotal, out.TXTotal)
	}
	if len(out.Points) != 2 {
		t.Errorf("ряд обязан доехать целиком: %+v", out.Points)
	}
}

// Пустой ряд -- это «за период обмена не было», и он обязан отличаться от
// ошибки: экран напишет «0», а не «неизвестно».
func TestRunner_TunnelTraffic_EmptySeriesIsAnAnswer(t *testing.T) {
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":{"points":[]}}`))
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "t2", Action: "tunnel_traffic", Args: map[string]any{"tunnel_id": "awg11"}})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	var out wire.TunnelTraffic
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatal(err)
	}
	if out.RXTotal != 0 || len(out.Points) != 0 {
		t.Errorf("out = %+v", out)
	}
}

func TestRunner_TunnelTraffic_RequiresTunnelID(t *testing.T) {
	r := Runner{AwgClient: awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})), Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "t3", Action: "tunnel_traffic"})
	if res.Status != "err" || !strings.Contains(res.Output, "tunnel_id") {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
}
