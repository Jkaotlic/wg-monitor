package backend

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Удаление VPN-туннеля (цикл 4, решение 2). Необратимо, поэтому границ три,
// и все на сервере:
//
//   - только админ и владелец (404 остальным, до чтения тела);
//   - набор имени VPN-туннеля, сверенный с именем из снимка роутера;
//   - отказ, если на туннеле есть правила или он главный выход роутера, --
//     по СВЕЖЕМУ снимку route_status, который сервер спрашивает сам.
//     Клиентскому «правил нет» не верим: экран мог открыться час назад.
//
// Снимок с warnings -- неполный: привязка правил политик в нём могла не
// прочитаться, и «правил нет» по нему было бы догадкой.

type miniappTunnelDeleteReq struct {
	Confirm string `json:"confirm"`
}

// miniappTunnelStateResp -- успешный ответ удаления и подтверждения импорта.
// state: "checking" -- роутер ещё не ответил, повторите тот же запрос;
// "queued" -- команда в очереди, итог -- опросом /commands/{cmd_id}.
type miniappTunnelStateResp struct {
	State         string `json:"state"`
	CmdID         string `json:"cmd_id,omitempty"`
	TunnelName    string `json:"tunnel_name,omitempty"`
	RouterAsleep  bool   `json:"router_asleep,omitempty"`
	RouterStatus  string `json:"router_status,omitempty"`
	WakeWindowMin int    `json:"wake_window_min,omitempty"`
}

func miniappTunnelDeleteHandler(d Deps, questions *miniappAgentQuestions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetOwner)
		if !ok {
			return
		}
		if d.CommandSink == nil {
			writeMiniappTunnelError(w, http.StatusServiceUnavailable, "commands_not_configured")
			return
		}
		tunnelID := strings.TrimSpace(r.PathValue("tunnel_id"))
		if tunnelID == wire.RouteOtherID || !wizardRouteTargetIDLooksSafe(tunnelID) {
			writeMiniappTunnelError(w, http.StatusBadRequest, "invalid_tunnel_id")
			return
		}
		var req miniappTunnelDeleteReq
		if !decodeMiniappCabinetBody(w, r, &req) {
			return
		}
		log := miniappCabinetLogger(d)
		res, answered, err := questions.ask(r.Context(), d.CommandSink, u.ID, "route_status", "route_status", nil)
		if err != nil {
			log.Warn("удаление VPN-туннеля: снимок не запрошен", "router_id", u.ID, "err", err)
			writeMiniappTunnelError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		if !answered {
			writeMiniappCabinetJSON(w, http.StatusAccepted, miniappTunnelStateResp{State: "checking"})
			return
		}
		if res.Status != "ok" {
			log.Warn("удаление VPN-туннеля: роутер не отдал снимок", "router_id", u.ID, "status", res.Status)
			writeMiniappTunnelError(w, http.StatusBadGateway, "router_failed")
			return
		}
		var snap wire.RouteSnapshot
		if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
			log.Warn("удаление VPN-туннеля: снимок не разобран", "router_id", u.ID, "err", err)
			writeMiniappTunnelError(w, http.StatusBadGateway, "router_garbled")
			return
		}
		tunnel, found := miniappSnapshotTunnel(snap, tunnelID)
		if !found {
			writeMiniappTunnelError(w, http.StatusNotFound, "tunnel_not_found")
			return
		}
		if !strings.EqualFold(strings.TrimSpace(tunnel.Type), "managed") {
			writeMiniappTunnelError(w, http.StatusConflict, "tunnel_not_managed")
			return
		}
		name := strings.TrimSpace(tunnel.Name)
		if name == "" {
			name = tunnel.ID
		}
		if !confirmPhraseMatches(req.Confirm, name) {
			writeMiniappTunnelError(w, http.StatusBadRequest, "confirm_mismatch")
			return
		}
		if len(snap.Warnings) > 0 {
			writeMiniappTunnelError(w, http.StatusConflict, "snapshot_partial")
			return
		}
		if snap.DefaultEgress == tunnel.ID {
			writeMiniappTunnelError(w, http.StatusConflict, "tunnel_is_default")
			return
		}
		// Роутер главный выход не назвал -- решает единственный претендент
		// с default_route (ревью цикла 4).
		if strings.TrimSpace(snap.DefaultEgress) == "" {
			if id, sole := miniappSoleDefaultClaimant(snap); sole && id == tunnel.ID {
				writeMiniappTunnelError(w, http.StatusConflict, "tunnel_is_default")
				return
			}
		}
		if rules := miniappTunnelRuleCount(snap, tunnel.ID); rules.Total > 0 {
			writeMiniappTunnelHasRules(w, rules)
			return
		}
		if rules := miniappTunnelPolicyChainRules(snap, tunnel.ID); rules.Total > 0 {
			writeMiniappTunnelInPolicyChain(w, name, rules)
			return
		}
		args := map[string]any{"tunnel_id": tunnel.ID}
		if miniappLegacyAWGTunnelID(tunnel.ID) {
			args["force_legacy_cleanup"] = true
		}
		cmdID, err := newCmdID()
		if err != nil {
			writeMiniappTunnelError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		cmd := wire.Command{ID: cmdID, Action: "tunnel_delete", Args: args, IssuedAt: time.Now().UTC()}
		if err := d.CommandSink.Enqueue(u.ID, cmd); err != nil {
			log.Warn("удаление VPN-туннеля: команда не встала в очередь", "router_id", u.ID, "err", err)
			writeMiniappTunnelError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		resp := miniappTunnelStateResp{State: "queued", CmdID: cmdID}
		resp.RouterAsleep, resp.RouterStatus, resp.WakeWindowMin = miniappWakeWindow(d, u, "tunnel_delete", time.Now().UTC())
		log.Info("miniapp tunnel delete queued", "router_id", u.ID, "nickname", u.Nickname, "tunnel_id", tunnel.ID, "cmd_id", cmdID)
		writeMiniappCabinetJSON(w, http.StatusAccepted, resp)
	}
}
