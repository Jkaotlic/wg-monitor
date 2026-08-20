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
		_, _ = w.Write([]byte(`{"success":true,"data":{"points":[{"t":1787219289,"rx":1000.5,"tx":200.5},{"t":1787219299,"rx":2000,"tx":300}],"stats":{"points":2,"currentRx":2000,"currentTx":300,"volumeRx":3000,"volumeTx":500}}}`))
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
	// Объём за период считает РОУТЕР (stats.volumeRx/volumeTx). Точки несут
	// скорости, и сумма скоростей объёмом не является -- своя формула здесь
	// разошлась бы с показаниями самого роутера.
	if out.RXTotal != 3000 || out.TXTotal != 500 {
		t.Errorf("totals = %d/%d, want объём из stats 3000/500", out.RXTotal, out.TXTotal)
	}
	if out.CurrentRx != 2000 {
		t.Errorf("current_rx = %v, want мгновенную скорость из stats", out.CurrentRx)
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
		_, _ = w.Write([]byte(`{"success":true,"data":{"points":[],"stats":{"points":0}}}`))
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
	// Период роутер требует обязательным, поэтому пустой заменяется суточным,
	// а не уезжает пустым в query.
	if out.Period != "24h" {
		t.Errorf("period = %q, want 24h по умолчанию", out.Period)
	}
}

func TestRunner_TunnelTraffic_RequiresTunnelID(t *testing.T) {
	r := Runner{AwgClient: awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})), Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "t3", Action: "tunnel_traffic"})
	if res.Status != "err" || !strings.Contains(res.Output, "tunnel_id") {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
}
