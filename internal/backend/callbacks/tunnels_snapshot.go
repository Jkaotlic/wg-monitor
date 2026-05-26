package callbacks

import (
	"strings"

	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

func tunnelEntriesFromRouteSnapshot(snap wire.RouteSnapshot) []tg.TunnelPanelEntry {
	entries := make([]tg.TunnelPanelEntry, 0, len(snap.Tunnels))
	for _, t := range snap.Tunnels {
		if t.Type != "" && t.Type != "managed" {
			continue
		}
		id := strings.TrimSpace(t.ID)
		if id == "" {
			continue
		}
		checkName := id
		if !strings.HasPrefix(checkName, "tunnel_") {
			checkName = "tunnel_" + checkName
		}
		entries = append(entries, tg.TunnelPanelEntry{
			Name:         t.Name,
			CheckName:    checkName,
			Interface:    t.Iface,
			NDMSName:     t.NDMSName,
			Enabled:      t.Enabled,
			Status:       t.Status,
			HandshakeAge: t.HandshakeAge,
		})
	}
	return entries
}
