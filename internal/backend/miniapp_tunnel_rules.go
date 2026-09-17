package backend

import (
	"strings"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// miniappTunnelRules -- сколько правил ведёт в один VPN-туннель по снимку
// route_status. Формула та же, что у экрана (miniapp/src/routes.js,
// tunnelRows): собственные правила туннеля (counts) плюс правила политики,
// которую он несёт прямо сейчас (policies[].active_tunnel_id). Разойтись
// им нельзя: экран скажет «правил нет», а сервер откажет в удалении -- или
// наоборот, что хуже.
type miniappTunnelRules struct {
	Total     int `json:"total"`
	DNS       int `json:"dns"`
	Static    int `json:"static"`
	HRNeo     int `json:"hr_neo"`
	ViaPolicy int `json:"via_policy"`
}

func miniappTunnelRuleCount(snap wire.RouteSnapshot, tunnelID string) miniappTunnelRules {
	c := snap.Counts[tunnelID]
	out := miniappTunnelRules{DNS: c.DNS, Static: c.Static, HRNeo: c.HRNeo}
	dns, hrNeo := miniappPolicyRulesFor(snap, tunnelID)
	out.ViaPolicy = dns
	out.HRNeo += hrNeo
	out.Total = out.DNS + out.Static + out.ViaPolicy
	return out
}

// miniappSnapshotHasPolicyIdentity -- читал ли агент политики роутера
// (hasPolicyIdentity в routes.js): флаг авторитетен, перебор -- для снимков
// до его появления.
func miniappSnapshotHasPolicyIdentity(snap wire.RouteSnapshot) bool {
	if snap.PolicyModel {
		return true
	}
	for _, p := range snap.Policies {
		if p.ActiveTunnelID != "" {
			return true
		}
		for _, i := range p.Interfaces {
			if i.TunnelID != "" {
				return true
			}
		}
	}
	return false
}

// miniappPolicyRulesFor -- правила политик, приписанные туннелю. У нового
// агента -- только активному звену; у старого -- каждому звену, найденному
// по iface или имени (неточно, но ровно как на экране).
func miniappPolicyRulesFor(snap wire.RouteSnapshot, tunnelID string) (dns, hrNeo int) {
	if miniappSnapshotHasPolicyIdentity(snap) {
		for _, p := range snap.Policies {
			if p.DNS == 0 || p.ActiveTunnelID == "" || p.ActiveTunnelID != tunnelID {
				continue
			}
			dns += p.DNS
			hrNeo += p.HRNeo
		}
		return dns, hrNeo
	}
	byBind := map[string]string{}
	byName := map[string]string{}
	for _, t := range snap.Tunnels {
		if b := strings.TrimSpace(t.Iface); b != "" {
			byBind[strings.ToLower(b)] = t.ID
		}
		if n := strings.TrimSpace(t.Name); n != "" {
			byName[strings.ToLower(n)] = t.ID
		}
	}
	for _, p := range snap.Policies {
		if p.DNS == 0 {
			continue
		}
		for _, iface := range p.Interfaces {
			id := ""
			if b := strings.TrimSpace(iface.Bind); b != "" {
				id = byBind[strings.ToLower(b)]
			}
			if id == "" {
				if n := strings.TrimSpace(iface.Name); n != "" {
					id = byName[strings.ToLower(n)]
				}
			}
			if id != "" && id == tunnelID {
				dns += p.DNS
				hrNeo += p.HRNeo
				break
			}
		}
	}
	return dns, hrNeo
}

func miniappSnapshotTunnel(snap wire.RouteSnapshot, id string) (wire.TunnelMeta, bool) {
	for _, t := range snap.Tunnels {
		if t.ID == id {
			return t, true
		}
	}
	return wire.TunnelMeta{}, false
}

// miniappLegacyAWGTunnelID -- старый идентификатор awgN: такие туннели
// awg-manager удаляет не всегда, и агент дочищает их сам по
// force_legacy_cleanup (как делал бот, callbacks/actions.go до цикла 4).
func miniappLegacyAWGTunnelID(id string) bool {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, "awg") || len(id) == len("awg") {
		return false
	}
	for _, r := range id[len("awg"):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// miniappTunnelPolicyChainRules -- правила политик, в цепочке которых
// VPN-туннель стоит РАНЬШЕ активного звена или активного звена нет вовсе.
//
// По формуле экрана (miniappTunnelRuleCount) такой туннель правил не несёт:
// политика сейчас идёт через другое звено или никуда. Но он -- первый
// претендент: поднимется -- правила вернутся на него. Удалить его значит
// оставить политику навсегда на WAN или на запасном звене (ревью цикла 4).
// Резервное звено ПОСЛЕ активного удаление не блокирует: оно правил не
// получит, пока активное живо (спорное решение 4 плана).
//
// Звенья сопоставляются только по tunnel_id: у старого агента его нет, но
// там формула экрана и так приписывает правила каждому звену.
func miniappTunnelPolicyChainRules(snap wire.RouteSnapshot, tunnelID string) miniappTunnelRules {
	var out miniappTunnelRules
	for _, p := range snap.Policies {
		if p.DNS == 0 && p.HRNeo == 0 {
			continue
		}
		mine, active := -1, -1
		for i, iface := range p.Interfaces {
			if mine < 0 && strings.TrimSpace(iface.TunnelID) == tunnelID {
				mine = i
			}
			if active < 0 && strings.EqualFold(strings.TrimSpace(iface.Role), "active") {
				active = i
			}
		}
		if mine < 0 || mine == active {
			continue
		}
		if active >= 0 && !miniappChainLinkAhead(p.Interfaces[mine], p.Interfaces[active], mine, active) {
			continue
		}
		out.ViaPolicy += p.DNS
		out.HRNeo += p.HRNeo
	}
	out.Total = out.ViaPolicy
	return out
}

// miniappChainLinkAhead -- стоит ли звено a раньше звена b. Порядок -- номер
// приоритета политики (меньше -- раньше); без номеров -- место в срезе (агент
// отдаёт его уже отсортированным).
func miniappChainLinkAhead(a, b wire.RoutePolicyInterface, ai, bi int) bool {
	if a.Order > 0 && b.Order > 0 && a.Order != b.Order {
		return a.Order < b.Order
	}
	return ai < bi
}

// miniappSoleDefaultClaimant -- главный выход, когда роутер его не назвал
// (default_egress пуст): единственный включённый VPN-туннель с default_route,
// про который не известно, что он лежит. Претендентов нет или больше одного
// -- выход не определён. Правило то же, что у экрана (routingVerdict в
// miniapp/src/routes.js), плюс «включён»: выключенный туннель трафик не несёт.
func miniappSoleDefaultClaimant(snap wire.RouteSnapshot) (string, bool) {
	id, n := "", 0
	for _, t := range snap.Tunnels {
		if !t.DefaultRoute || !t.Enabled || miniappTunnelStatusDown(t.Status) {
			continue
		}
		id, n = t.ID, n+1
	}
	return id, n == 1
}

// miniappTunnelStatusDown -- tunnelLive(t) === 'down' из routes.js: пустой
// статус -- «неизвестно», а не «лежит».
func miniappTunnelStatusDown(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "running", "up", "started", "active":
		return false
	}
	return true
}
