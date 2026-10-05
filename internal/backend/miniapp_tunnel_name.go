package backend

import (
	"strings"
	"time"
)

// miniappTunnelNameForCheck -- имя VPN-туннеля для проверки tunnel_<id> этого
// роутера. Кнопка «Починить» в приложении присылает только имя проверки, а
// починке имя нужно, когда снимок от роутера не придёт: иначе в личку уйдёт
// идентификатор.
//
// Имя берётся из последних событий ЭТОГО роутера -- тем же путём, каким его
// видит экран VPN-туннелей. Чужого роутера и незнакомой проверки здесь нет:
// подставить чужое имя хуже, чем идентификатор. Не нашлось -- пустая строка.
func miniappTunnelNameForCheck(d Deps, routerID int64, check string) string {
	if !strings.HasPrefix(check, miniappTunnelPrefix) {
		return ""
	}
	return miniappTunnelNames(d, routerID)[check]
}

// miniappTunnelNames -- имена VPN-туннелей роутера по имени проверки, одним
// запросом к событиям: таблица горячая, и список меток не должен ходить в
// неё на каждый туннель.
func miniappTunnelNames(d Deps, routerID int64) map[string]string {
	out := map[string]string{}
	rows, err := d.DB.Events().LatestEventsByPrefixSince(routerID, miniappTunnelPrefix, time.Now().UTC().Add(-miniappEventsWindow))
	if err != nil {
		return out
	}
	for _, row := range rows {
		if _, seen := out[row.CheckName]; seen {
			continue
		}
		if tu, ok := miniappTunnelFromEvent(row); ok {
			out[row.CheckName] = strings.TrimSpace(tu.Name)
		}
	}
	return out
}
