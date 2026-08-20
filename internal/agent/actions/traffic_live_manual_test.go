//go:build manual

package actions

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Живая проверка обмена по туннелю. Только чтение: /api/tunnels/traffic ничего
// на роутере не меняет.
//
//	AWGM_URL=http://192.168.0.1:2222 TRAFFIC_TUNNEL=awg12 \
//	  go test -tags manual ./internal/agent/actions/ -run LiveTunnelTraffic -v
func TestLiveTunnelTraffic(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	tunnel := os.Getenv("TRAFFIC_TUNNEL")
	if base == "" || tunnel == "" {
		t.Skip("нужны AWGM_URL и TRAFFIC_TUNNEL")
	}
	c := awgmgr.New(base)
	ctx := context.Background()

	for _, period := range []string{"5m", "1h", "24h"} {
		out, err := TunnelTrafficJSON(ctx, c, tunnel, period)
		if err != nil {
			t.Fatalf("period %s: %v", period, err)
		}
		var got wire.TunnelTraffic
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("period %s: ответ не разбирается: %v", period, err)
		}
		if got.Period != period || got.TunnelID != tunnel {
			t.Fatalf("ответ обязан помнить, о чём спросили: %+v", got)
		}
		t.Logf("%s: объём %d/%d байт, сейчас %.0f/%.0f Б/с, точек %d",
			period, got.RXTotal, got.TXTotal, got.CurrentRx, got.CurrentTx, len(got.Points))
		if len(got.Points) > 0 && got.Points[0].T == 0 {
			t.Fatalf("время точки не разобралось: %+v", got.Points[0])
		}
	}

	// Период роутер требует обязательным: пустой заменяется суточным здесь, а
	// не отвечает 400 с роутера.
	out, err := TunnelTrafficJSON(ctx, c, tunnel, "")
	if err != nil {
		t.Fatalf("пустой период: %v", err)
	}
	var got wire.TunnelTraffic
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Period != "24h" {
		t.Fatalf("пустой период обязан стать суточным, got %q", got.Period)
	}
}
