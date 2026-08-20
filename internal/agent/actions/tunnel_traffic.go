package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// TunnelTrafficJSON забирает у роутера ряд обмена по туннелю и складывает его
// суммы. Считать байты агенту не нужно -- роутер ведёт ряд сам
// (docs/operations/2026-08-19-awgm-r15-audit.md); задача агента здесь --
// принести ряд и посчитать по нему то единственное число, которое печатает
// экран.
func TunnelTrafficJSON(ctx context.Context, c *awgmgr.Client, tunnelID, period string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("tunnel_traffic: awg-manager client is required")
	}
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		return "", fmt.Errorf("tunnel_traffic: tunnel_id is required")
	}
	series, err := c.TunnelTraffic(ctx, tunnelID, period)
	if err != nil {
		return "", fmt.Errorf("tunnel_traffic: %w", err)
	}
	out := wire.TunnelTraffic{TunnelID: tunnelID, Period: period, Points: make([]wire.TrafficPoint, 0, len(series.Points))}
	for _, p := range series.Points {
		out.RXTotal += p.RX
		out.TXTotal += p.TX
		out.Points = append(out.Points, wire.TrafficPoint{T: p.T, RX: p.RX, TX: p.TX})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
