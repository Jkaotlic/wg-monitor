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
	rows, err := d.DB.Events().LatestEventsByPrefixSince(routerID, miniappTunnelPrefix, time.Now().UTC().Add(-miniappEventsWindow))
	if err != nil {
		return ""
	}
	for _, row := range rows {
		if row.CheckName != check {
			continue
		}
		if tu, ok := miniappTunnelFromEvent(row); ok {
			return strings.TrimSpace(tu.Name)
		}
	}
	return ""
}
