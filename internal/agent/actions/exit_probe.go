package actions

import (
	"context"
	"encoding/json"
	"strings"
)

// runExitIPProbe -- команда exit_ip_probe: адрес выхода одного VPN-туннеля по
// кнопке «Проверить сейчас». Замерщик общий с периодическим (exitprobe.Prober),
// поэтому ответ кнопки сразу попадает и в факты следующего отчёта.
func (r *Runner) runExitIPProbe(ctx context.Context, args map[string]any) (string, string) {
	if r.ExitProbeNow == nil {
		return "err", "exit_ip_probe: этот агент не умеет мерить адрес выхода"
	}
	tunnelID, _ := args["tunnel_id"].(string)
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		return "err", "exit_ip_probe: tunnel_id is required"
	}
	res, err := r.ExitProbeNow(ctx, tunnelID)
	if err != nil {
		return "err", err.Error()
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "err", err.Error()
	}
	return "ok", string(b)
}
