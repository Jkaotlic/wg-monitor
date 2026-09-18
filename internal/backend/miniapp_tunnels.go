package backend

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// miniappTunnelPrefix mirrors checks.tunnelCheckPrefix ("tunnel_"): per-tunnel
// check names are "tunnel_<tunnelId>". Duplicated rather than imported because
// the backend does not depend on the agent's checks package.
const miniappTunnelPrefix = "tunnel_"

// miniappTunnelsInventoryCheck -- сводная проверка агента ("tunnels"): OK с
// ней приходит в каждом отчёте, где awg-manager отдал список туннелей
// (checks/tunnels.go). Без подчёркивания, то есть не строка туннеля.
const miniappTunnelsInventoryCheck = "tunnels"

// miniappTunnel is the per-tunnel projection the mini app is allowed to see.
//
// It is a WHITELIST, not a passthrough of events.details_json. The agent's
// details map carries router topology (endpoint, address, isp_interface,
// ndms_name, interface) that is fine for the admin dashboard but must not reach
// the mini app: owners and operators read this screen too. Same reasoning that
// makes miniappRouterSummaryFromAgent drop expected_exit_ip / awg_iface / kind
// (miniapp_handler.go:94-114).
//
// Pointer fields are the ones whose ABSENCE is meaningful: an old agent, or an
// awg-manager that wouldn't answer, omits them entirely, and "unknown" must
// stay distinguishable from "zero".
type miniappTunnel struct {
	TunnelID string `json:"tunnel_id"`
	Name     string `json:"name,omitempty"`
	// Status is the check verdict ("ok"|"fail") -- the FSM's opinion.
	Status string `json:"status"`
	// RunState is the router's own word for the tunnel ("running"|"stopped"|...).
	RunState string `json:"run_state,omitempty"`
	// Enabled is a pointer for the same reason as HandshakeAgeSec/PingLatencyMs:
	// an unparseable details blob, or an agent older than this key, must render
	// as "unknown", not as a guessed true/false.
	Enabled         *bool  `json:"enabled,omitempty"`
	HandshakeAgeSec *int   `json:"handshake_age_sec,omitempty"`
	PingCheckStatus string `json:"ping_check_status,omitempty"`
	PingLatencyMs   *int   `json:"ping_latency_ms,omitempty"`
	// MatrixLatencyMs -- задержка ЧЕРЕЗ туннель из матрицы awg-manager 2.18,
	// в отличие от PingLatencyMs, который меряет ping-check роутера и с целью
	// вроде 8.8.8.8 утекает мимо туннеля. Указатель, а не число: отсутствие
	// данных и «ноль миллисекунд» -- разные вещи, и вторая на экране читается
	// как «мгновенно».
	MatrixLatencyMs *int   `json:"matrix_latency_ms,omitempty"`
	MatrixUpdatedAt string `json:"matrix_updated_at,omitempty"`
	// DefaultRouteIntent: this tunnel claims to be the default route. Several
	// tunnels can each claim it -- it is NOT the answer to "where does traffic go".
	DefaultRouteIntent bool `json:"default_route_intent"`
	// IsActiveDefault: this tunnel IS the live egress (settings.download.routeTag).
	// Only trustworthy when ActiveDefaultKnown is true.
	IsActiveDefault bool `json:"is_active_default"`
	// ActiveDefaultKnown: whether the egress question has an answer at all.
	// False for agents older than the routeTag change, or when awg-manager's
	// /api/settings/get failed. False means "say unknown", never "guess".
	ActiveDefaultKnown bool   `json:"active_default_known"`
	Note               string `json:"note,omitempty"`
	TS                 string `json:"ts,omitempty"`
	// RoutesDNS / RoutesStatic -- сколько правил ведут в этот VPN-туннель.
	// Нужны только для вывода «обход идёт правилами» и в мини-апп не уходят.
	RoutesDNS    int `json:"-"`
	RoutesStatic int `json:"-"`
}

// miniappTunnelDetails is the subset of the agent's details map we decode.
// Unknown keys are ignored by encoding/json -- that is the whitelist.
type miniappTunnelDetails struct {
	TunnelID           string `json:"tunnel_id"`
	TunnelName         string `json:"tunnel_name"`
	Status             string `json:"status"`
	Enabled            *bool  `json:"enabled"`
	HandshakeAgeSec    *int   `json:"handshake_age_sec"`
	PingCheckStatus    string `json:"ping_check_status"`
	PingLastLatencyMs  *int   `json:"ping_check_last_latency_ms"`
	MatrixLatencyMs    *int   `json:"matrix_latency_ms"`
	MatrixUpdatedAt    string `json:"matrix_updated_at"`
	DefaultRouteIntent bool   `json:"default_route_intent"`
	IsActiveDefault    bool   `json:"is_active_default"`
	ActiveDefaultKnown bool   `json:"active_default_known"`
	Note               string `json:"note"`
	RoutesDNS          int    `json:"routes_dns"`
	RoutesStatic       int    `json:"routes_static"`
}

// miniappTunnelFromEvent projects one latest-event row into the mini app's
// tunnel view. Returns false for rows that are not per-tunnel checks (dns,
// external_reach, hydraroute, awg_manager, and the synthetic "tunnels").
//
// Details are best-effort: the agent/backend detail contract is an unversioned
// map, so a malformed or empty blob degrades to identity from the check name
// rather than dropping the tunnel off the screen.
func miniappTunnelFromEvent(row db.EventRow) (miniappTunnel, bool) {
	if !strings.HasPrefix(row.CheckName, miniappTunnelPrefix) {
		return miniappTunnel{}, false
	}
	id := strings.TrimPrefix(row.CheckName, miniappTunnelPrefix)
	if id == "" {
		return miniappTunnel{}, false
	}

	out := miniappTunnel{TunnelID: id, Status: row.Status}
	if !row.TS.IsZero() {
		out.TS = row.TS.UTC().Format(time.RFC3339)
	}

	var d miniappTunnelDetails
	if err := json.Unmarshal([]byte(row.DetailsJSON), &d); err != nil {
		return out, true
	}
	if d.TunnelID != "" {
		out.TunnelID = d.TunnelID
	}
	out.Name = d.TunnelName
	out.RunState = d.Status
	out.Enabled = d.Enabled
	out.HandshakeAgeSec = d.HandshakeAgeSec
	out.PingCheckStatus = d.PingCheckStatus
	out.PingLatencyMs = d.PingLastLatencyMs
	out.MatrixLatencyMs = d.MatrixLatencyMs
	out.MatrixUpdatedAt = d.MatrixUpdatedAt
	out.DefaultRouteIntent = d.DefaultRouteIntent
	out.ActiveDefaultKnown = d.ActiveDefaultKnown
	out.IsActiveDefault = d.ActiveDefaultKnown && d.IsActiveDefault
	out.Note = d.Note
	out.RoutesDNS = d.RoutesDNS
	out.RoutesStatic = d.RoutesStatic
	return out, true
}

// Traffic modes. These answer the operator's daily question -- "does traffic go
// direct or through the VPN" -- and the honest fourth answer, "we cannot tell".
const (
	miniappTrafficVPN     = "vpn"
	miniappTrafficDirect  = "direct"
	miniappTrafficSingbox = "singbox"
	miniappTrafficSplit   = "split"
	miniappTrafficUnknown = "unknown"
)

// miniappTraffic is the screen's headline answer.
type miniappTraffic struct {
	Mode             string `json:"mode"`
	EgressTunnelID   string `json:"egress_tunnel_id,omitempty"`
	EgressTunnelName string `json:"egress_tunnel_name,omitempty"`
	// ContestedDefault: more than one tunnel claims defaultRoute=true. Real and
	// common (the operator's own router does it). Worth surfacing either way: when
	// we know the egress it explains why the other tunnel looks idle; when we
	// don't, it is precisely why.
	ContestedDefault bool `json:"contested_default"`
	// Reason -- почему ответ «неизвестно», когда экрану есть что сказать
	// точнее общего «роутер не сообщил». Пусто у остальных режимов.
	Reason string `json:"reason,omitempty"`
	// ReserveTunnelIDs -- запасные звенья набора, несущего обход, которые
	// живы по своей проверке tunnel_* (status ok): кто подхватит обход, если
	// несущий ляжет. Заполняется только когда несущий назван по сводке
	// политик агента; пусто -- резерва нет или агент о нём не сообщил
	// (отличает их наличие EgressTunnelID при режиме split).
	ReserveTunnelIDs []string `json:"reserve_tunnel_ids,omitempty"`
}

// miniappTrafficReasonRulesUnreadable: агент не смог прочитать правила
// маршрутизации (mechanism_probe_error в проверке hydraroute), и «правил нет»
// из его нулей не следует.
const miniappTrafficReasonRulesUnreadable = "rules_unreadable"

// miniappHydraDetails decodes the sing-box flag and whether HydraRoute is
// executing any rules out of the hydraroute check.
type miniappHydraDetails struct {
	SingboxRouterActive bool `json:"singbox_router_active"`
	Running             bool `json:"running"`
	RoutesHRNeo         int  `json:"routes_hrneo"`
	// MechanismProbeError: агент не дочитал списки правил, и счётчики выше --
	// нули от незнания. 07.09.2026 на snekhaev список DNS-маршрутов перерос
	// потолок чтения, и экран неделю писал «трафик идёт напрямую».
	MechanismProbeError string `json:"mechanism_probe_error"`
	// Policies -- сводка политик доступа (агент v0.41+): кто несёт каждую
	// политику сейчас и роли звеньев. nil у старых агентов и сборок
	// awg-manager без политик -- тогда несущий по-прежнему выводится из
	// числа живых туннелей.
	Policies []wire.PolicyBrief `json:"policies"`
}

// miniappDeriveTraffic answers "direct or via VPN" from stored state alone.
//
// Order matters. sing-box wins first: when its tproxy router is active it picks a
// route per destination, so "the default route" is not the question anymore and
// any single answer would be a lie (the external_reach probe doesn't even run on
// those routers -- cmd/agent/main.go:304-318).
//
// Otherwise the answer is only as good as the agent: is_active_default comes from
// settings.download.routeTag and is the sole authority. Without it (agent older
// than the routeTag change, or /api/settings/get down) we return "unknown" rather
// than falling back to default_route_intent -- several tunnels can each claim it,
// so that fallback is a coin flip, and the operator's own router is exactly that
// case.
func miniappDeriveTraffic(tunnels []miniappTunnel, byCheck map[string]db.EventRow) miniappTraffic {
	out := miniappTraffic{Mode: miniappTrafficUnknown}

	claimed := 0
	for _, t := range tunnels {
		if t.DefaultRouteIntent {
			claimed++
		}
	}
	out.ContestedDefault = claimed > 1

	var hd miniappHydraDetails
	if row, ok := byCheck["hydraroute"]; ok && json.Unmarshal([]byte(row.DetailsJSON), &hd) == nil && hd.SingboxRouterActive {
		out.Mode = miniappTrafficSingbox
		return out
	}

	if len(tunnels) == 0 {
		return out
	}

	allKnown := true
	for _, t := range tunnels {
		if !t.ActiveDefaultKnown {
			allKnown = false
			continue
		}
		if t.IsActiveDefault {
			out.Mode = miniappTrafficVPN
			out.EgressTunnelID = t.TunnelID
			out.EgressTunnelName = t.Name
			return out
		}
	}
	if allKnown {
		// Every tunnel answered and none is the egress: traffic leaves through
		// the WAN. But LatestEventsByPrefixSince picks the latest row per
		// check_name independently (db/events.go), so this slice can mix a
		// tunnel's fresh state with a sibling's stale row from before it
		// dropped out of awg-manager -- that sibling's ActiveDefaultKnown=false
		// means "never asked this cycle", not "confirmed not the egress". One
		// unread tunnel is enough doubt to withhold "direct" and say "unknown"
		// instead.
		out.Mode = miniappTrafficDirect
		// Главный выход напрямую -- ещё не «обход не работает». Это обычная
		// раздельная маршрутизация: всё, что не названо правилами, идёт мимо
		// VPN, а заблокированное уводят правила. Так настроены рабочий роутер
		// и testkeen, и жёлтое «обход не работает» на них было неправдой.
		if carrier, pol := miniappPolicyCarrier(tunnels, hd); carrier != nil {
			// Несущего назвал сам роутер: активное звено набора с правилами.
			// Без этого при двух живых экран брал первый running и красил
			// живой обход тревогой запасного (workrouter, 18.09.2026).
			out.Mode = miniappTrafficSplit
			out.EgressTunnelID = carrier.TunnelID
			out.EgressTunnelName = carrier.Name
			out.ReserveTunnelIDs = miniappPolicyReserve(tunnels, pol)
		} else if bypass, carrier := miniappBypassByRules(tunnels, hd); bypass {
			out.Mode = miniappTrafficSplit
			if carrier != nil {
				out.EgressTunnelID = carrier.TunnelID
				out.EgressTunnelName = carrier.Name
			}
		} else if hd.MechanismProbeError != "" {
			// «Ни одно правило не ведёт в VPN» из непрочитанных правил не
			// следует: говорим, чего не узнали, а не выдумываем «напрямую».
			out.Mode = miniappTrafficUnknown
			out.Reason = miniappTrafficReasonRulesUnreadable
		}
	}
	return out
}

// miniappBypassByRules: уводят ли правила заблокированное в работающий
// VPN-туннель. Правило в лежащий VPN-туннель ничего не обходит, остановленный
// HydraRoute правил не исполняет.
//
// carrier -- VPN-туннель, который несёт обход, и только когда он единственный
// живой. При нескольких живых выбирает набор HydraRoute, а в проверках этого
// нет: правила без явного маршрута агент приписывает первому заявившему
// основной маршрут, и назвать любой -- угадать.
func miniappBypassByRules(tunnels []miniappTunnel, hd miniappHydraDetails) (bool, *miniappTunnel) {
	var live []*miniappTunnel
	bypass := false
	for i := range tunnels {
		t := &tunnels[i]
		if t.RunState != "running" {
			continue
		}
		live = append(live, t)
		if t.RoutesDNS+t.RoutesStatic > 0 {
			bypass = true
		}
	}
	if len(live) > 0 && hd.Running && hd.RoutesHRNeo > 0 {
		bypass = true
	}
	if !bypass {
		return false, nil
	}
	if len(live) == 1 {
		return true, live[0]
	}
	return true, nil
}

// miniappPolicyCarrier -- VPN-туннель, который несёт обход по сводке политик
// агента: активное звено политики, у которой есть исполняемые правила.
// Правила HydraRoute Neo исполняются только запущенным HydraRoute, остальные
// правила политики -- самим роутером. Несущий обязан быть среди туннелей
// экрана и работать: ссылка на линию, которой экран не показывает, -- не
// ответ. Политик с правилами несколько -- несущим считается та, что
// исполняет больше правил (первая при равенстве).
//
// nil -- сводки нет (старый агент) или ни одна политика с правилами не идёт
// через живой VPN-туннель; тогда отвечает miniappBypassByRules.
func miniappPolicyCarrier(tunnels []miniappTunnel, hd miniappHydraDetails) (*miniappTunnel, *wire.PolicyBrief) {
	var (
		best    *wire.PolicyBrief
		carrier *miniappTunnel
	)
	bestExecuted := 0
	for i := range hd.Policies {
		p := &hd.Policies[i]
		executed := miniappPolicyExecuted(p, hd)
		if executed <= 0 || !p.ViaVPN || p.ActiveTunnelID == "" {
			continue
		}
		t := miniappTunnelByID(tunnels, p.ActiveTunnelID)
		if t == nil || t.RunState != "running" {
			continue
		}
		if best == nil || executed > bestExecuted {
			best, carrier, bestExecuted = p, t, executed
		}
	}
	return carrier, best
}

// miniappPolicyExecuted -- сколько правил политики роутер исполняет сейчас:
// правила HydraRoute Neo -- только при запущенном HydraRoute, остальные --
// всегда.
func miniappPolicyExecuted(p *wire.PolicyBrief, hd miniappHydraDetails) int {
	if hd.Running {
		return p.DNS
	}
	return p.DNS - p.HRNeo
}

// miniappPolicyReserve -- запасные звенья набора, живые по своей проверке:
// роль fallback (интерфейс поднят) ещё не значит, что туннель что-то везёт --
// на workrouter запасной был поднят с обменом ключами 24-минутной давности.
func miniappPolicyReserve(tunnels []miniappTunnel, p *wire.PolicyBrief) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, l := range p.Links {
		if l.Role != "fallback" || l.TunnelID == "" || l.TunnelID == p.ActiveTunnelID {
			continue
		}
		if t := miniappTunnelByID(tunnels, l.TunnelID); t != nil && t.Status == "ok" {
			out = append(out, l.TunnelID)
		}
	}
	return out
}

func miniappTunnelByID(tunnels []miniappTunnel, id string) *miniappTunnel {
	for i := range tunnels {
		if tunnels[i].TunnelID == id {
			return &tunnels[i]
		}
	}
	return nil
}
