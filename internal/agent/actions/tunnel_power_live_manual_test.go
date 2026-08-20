//go:build manual

package actions

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Живая проверка включения и выключения туннеля по идентификатору. Мутация
// обратимая: состояние снимается ДО, туннель включается, проверяется и
// возвращается ровно в то состояние, в котором был.
//
//	AWGM_URL=http://192.168.0.1:2222 POWER_TUNNEL=awg10 \
//	  go test -tags manual ./internal/agent/actions/ -run LiveTunnelPower -v
func TestLiveTunnelPower(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	tunnel := os.Getenv("POWER_TUNNEL")
	if base == "" || tunnel == "" {
		t.Skip("нужны AWGM_URL и POWER_TUNNEL")
	}
	c := awgmgr.New(base)
	ctx := context.Background()
	r := Runner{AwgClient: c, Now: time.Now}

	state := func(stage string) awgmgr.Tunnel {
		all, err := c.TunnelsAll(ctx)
		if err != nil {
			t.Fatalf("%s: список туннелей: %v", stage, err)
		}
		for _, tu := range all.Tunnels {
			if tu.ID == tunnel {
				t.Logf("%s: enabled=%v status=%q", stage, tu.Enabled, tu.Status)
				return tu
			}
		}
		t.Fatalf("%s: туннель %s не найден", stage, tunnel)
		return awgmgr.Tunnel{}
	}

	before := state("ДО")

	res := r.Execute(ctx, wire.Command{ID: "live-on", Action: "tunnel_power", Args: map[string]any{"tunnel_id": tunnel, "on": true}})
	if res.Status != "ok" {
		t.Fatalf("включение: %s", res.Output)
	}
	t.Logf("включили: %s", res.Output)
	time.Sleep(2 * time.Second)
	state("ПОСЛЕ ВКЛЮЧЕНИЯ")

	// Откат в исходное состояние -- каким бы оно ни было.
	res = r.Execute(ctx, wire.Command{ID: "live-off", Action: "tunnel_power", Args: map[string]any{"tunnel_id": tunnel, "on": before.Enabled}})
	if res.Status != "ok" {
		t.Fatalf("ОТКАТ НЕ УДАЛСЯ: %s", res.Output)
	}
	time.Sleep(2 * time.Second)
	after := state("ПОСЛЕ ОТКАТА")
	if after.Enabled != before.Enabled {
		t.Fatalf("постусловие не совпало: было enabled=%v, стало %v", before.Enabled, after.Enabled)
	}
}
