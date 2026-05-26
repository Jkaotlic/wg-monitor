package callbacks

import (
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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

func tunnelViewsFromRouteSnapshot(snap wire.RouteSnapshot) []alerts.TunnelView {
	entries := make([]alerts.TunnelView, 0, len(snap.Tunnels))
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
		entries = append(entries, alerts.TunnelView{
			Name:         t.Name,
			CheckName:    checkName,
			NDMSName:     t.NDMSName,
			Interface:    t.Iface,
			Enabled:      t.Enabled,
			HasEnabled:   true,
			Status:       t.Status,
			HasHandshake: t.HasHandshake,
			HandshakeAge: t.HandshakeAge,
			PingStatus:   t.PingStatus,
			FailCount:    t.PingFails,
			FailThresh:   t.PingFailMax,
		})
	}
	return entries
}

func tunnelCheckNamesFromRouteSnapshot(snap wire.RouteSnapshot) map[string]bool {
	out := make(map[string]bool, len(snap.Tunnels))
	for _, t := range snap.Tunnels {
		if t.Type != "" && t.Type != "managed" {
			continue
		}
		id := strings.TrimSpace(t.ID)
		if id == "" {
			continue
		}
		if !strings.HasPrefix(id, "tunnel_") {
			id = "tunnel_" + id
		}
		out[id] = true
	}
	return out
}

func tunnelPanelEntryFromRouteSnapshot(snap wire.RouteSnapshot, checkName string) (tg.TunnelPanelEntry, bool) {
	entries := tunnelEntriesFromRouteSnapshot(snap)
	for _, e := range entries {
		if e.CheckName == checkName {
			return e, true
		}
	}
	return tg.TunnelPanelEntry{}, false
}
