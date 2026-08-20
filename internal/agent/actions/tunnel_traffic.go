package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// defaultTrafficPeriod -- период по умолчанию. Роутер требует period
// обязательным параметром (его OpenAPI: 5m, 10m, 30m, 1h, 3h, 6h, 12h, 24h),
// и пустая строка отвечала бы 400 вместо ряда.
const defaultTrafficPeriod = "24h"

// TunnelTrafficJSON забирает у роутера обмен по туннелю.
//
// Объём за период считает РОУТЕР (stats.volumeRx/volumeTx) -- агент его
// переносит, а не пересчитывает. Точки ряда несут скорости, и сумма скоростей
// объёмом не является; своя формула здесь разошлась бы с показаниями самого
// роутера. Проверено на живом роутере 20.08.2026.
func TunnelTrafficJSON(ctx context.Context, c *awgmgr.Client, tunnelID, period string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("tunnel_traffic: awg-manager client is required")
	}
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		return "", fmt.Errorf("tunnel_traffic: tunnel_id is required")
	}
	if period == "" {
		period = defaultTrafficPeriod
	}
	series, err := c.TunnelTraffic(ctx, tunnelID, period)
	if err != nil {
		return "", fmt.Errorf("tunnel_traffic: %w", err)
	}
	out := wire.TunnelTraffic{
		TunnelID:  tunnelID,
		Period:    period,
		RXTotal:   series.Stats.VolumeRx,
		TXTotal:   series.Stats.VolumeTx,
		CurrentRx: series.Stats.CurrentRx,
		CurrentTx: series.Stats.CurrentTx,
		Points:    make([]wire.TrafficPoint, 0, len(series.Points)),
	}
	for _, p := range series.Points {
		out.Points = append(out.Points, wire.TrafficPoint{T: p.T, RX: p.RX, TX: p.TX})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
