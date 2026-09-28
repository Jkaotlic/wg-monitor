package checks

import (
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestAnnotateAwgmDown(t *testing.T) {
	since := time.Date(2026, 9, 28, 3, 12, 0, 0, time.UTC)
	out := []wire.Check{
		{Name: "tunnels", Status: "ok"},
		{Name: "tunnel_awg11", Status: "fail"},
		{Name: "tunnel_awg10", Status: "ok", Details: map[string]any{"x": 1}},
	}
	annotateAwgmDown(out, func(id string) (time.Time, bool) {
		if id == "awg11" {
			return since, true
		}
		return time.Time{}, false
	})
	if out[1].Details["awgm_down_since"] != "2026-09-28T03:12:00Z" {
		t.Fatalf("awg11 details = %+v", out[1].Details)
	}
	if _, ok := out[2].Details["awgm_down_since"]; ok {
		t.Fatal("живому VPN-туннелю пометка не положена")
	}
	if out[0].Details != nil {
		t.Fatal("сводная проверка tunnels -- не VPN-туннель")
	}
	annotateAwgmDown(out, nil) // старый агент без трекера: ничего не делает, не падает
}
