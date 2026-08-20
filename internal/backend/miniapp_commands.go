package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// miniappCommandAllowlist is what a mini-app session may dispatch to an agent.
//
// It is deliberately NOT dashboardCommandAllowlist. The two have different
// trust models: the dashboard is one admin holding one token, while a mini-app
// session is any Telegram user resolved to a per-router role (admin / owner /
// operator). So this list is scoped to "things the person who owns THIS router
// should be able to do to THIS router", which both subtracts from the dashboard's
// list (no dns_reset, no agent config editing, no opkg/entware maintenance) and
// adds to it (tunnel probes and restart -- see below).
//
// Three entries widen the browser-session boundary that wizard_handler.go draws.
// Each is justified against the trust precedent dns_reset established
// (wizard_handler.go:605-608: router-local, recoverable, and confirmed in the
// UI before dispatch):
//
//   - check_via_tunnel / check_direct: read-only HTTP probes. They change nothing;
//     they report an exit IP and whether a handful of sites answer. That is data
//     the same person already sees in the bot via 🌍 Через туннель?.
//   - tunnel_restart: mutating, and the only entry here that is. Its blast radius
//     is provably one tunnel on one router the caller already administers: the
//     client sends a tunnel_id, never an ndms_name, and miniappCommandHandler
//     resolves that id to the router's NDM interface name from THIS router's own
//     tunnel_* event rows (see miniappResolveTunnelRestartArgs below). An interface
//     the backend has no tunnel_<id> event for is unreachable, not merely
//     rejected -- sanitizeWizardCommandArgs's regex check on ndms_name is a
//     wizard-side belt-and-braces, not what makes this safe for a mini-app
//     session. It re-establishes in seconds, and it is the single action that
//     actually FIXES the common failure. Without it the screen can only tell an
//     owner to go ask the admin.
//
// Everything else stays out on purpose; TestMiniappCommandAllowlistContents pins
// the denied set. In particular update_backend_url is fleet-takeover blast radius
// (same reasoning as the dashboard's hidden-update-url rejection), tunnel_delete
// is irreversible, tunnel_enable/disable are configuration changes rather than
// repairs, and dns_reset is router-global -- it stays with the admin's dashboard.
var miniappCommandAllowlist = map[string]bool{
	// Read-only, already trusted to the dashboard.
	"force_recheck":  true,
	"diag_now":       true,
	"tunnels_status": true,
	"route_status":   true,
	// Read-only probes; new for a browser session (wizard-only until now).
	"check_via_tunnel": true,
	"check_direct":     true,
	// Mutating; new for a browser session. Router-local, reversible, UI-confirmed.
	"tunnel_restart": true,
	// Управление маршрутами. Каждое router-local: add/delete идут через план
	// с хешем черновика, rebind -- с превью и результатом по категориям,
	// promote переупорядочивает уже состоящие в цепочке интерфейсы и
	// обратим тем же действием.
	//
	// Аргументы всех семи проверяются ЯВНЫМИ ветками sanitizeWizardCommandArgs.
	// Это не перестраховка: его ветка default возвращает аргументы как есть,
	// и открыть здесь действие, которого там нет, значит отдать агенту
	// клиентский ввод без единой проверки.
	"route_templates":      true,
	"route_add_plan":       true,
	"route_add":            true,
	"route_delete_plan":    true,
	"route_delete":         true,
	"route_rebind":         true,
	"route_policy_promote": true,

	// Обслуживание (фаза D1). Читающие, аргументов не берут вовсе: версии,
	// два доктора и разовый прогон проверки связи. Раньше router_doctor был
	// закрыт как «простыня текста для админа» -- закрыт был не радиус
	// поражения, а вёрстка, и экран разбирает его вывод строками.
	//
	// pingcheck_status сюда НЕ входит намеренно: его JSON несёт ndms_name
	// каждого туннеля (агент кладёт его туда, чтобы бот нарисовал кнопки), а
	// это ровно та топология роутера, которую белый список miniapp_tunnels.go
	// клиенту не отдаёт. Состояние проверки связи экран и так знает: оно
	// приезжает в проекции туннеля (ping_check_status, ping_latency_ms).
	"version_audit": true,
	"router_doctor": true,
	"hrneo_doctor":  true,
	"pingcheck_now": true,

	// Три мутирующих. Радиус тот же, что у tunnel_restart, и ограничен так
	// же: клиент присылает tunnel_id, ndms_name сервер достаёт из событий
	// ЭТОГО роутера (miniappResolveTunnelArgs). Выключение туннеля обратимо
	// включением -- это переключатель, а не удаление.
	"tunnel_enable":    true,
	"tunnel_disable":   true,
	"pingcheck_toggle": true,

	// Обмен по туннелю (фаза F). Читающее: ряд ведёт сам роутер, агент его
	// только забирает.
	"tunnel_traffic": true,

	// Включение и выключение туннеля по идентификатору, через awg-manager.
	// Обратимо своей же парой и работает там, где ndmc бессилен: у
	// opkg-туннеля имени в NDMS нет вовсе.
	"tunnel_power": true,

	// Прошивка (фаза D2). Чтение состояния -- всем, у кого есть доступ;
	// установка -- только владельцу (miniappOwnerOnlyActions), потому что
	// она необратима и перезагружает роутер. Оператору дали смотреть и
	// чинить, а не менять прошивку на чужом устройстве.
	"firmware_status":  true,
	"firmware_install": true,
}

// miniappOwnerOnlyActions -- действия, которых оператору не положено. Список
// маленький намеренно: сюда попадает только то, что необратимо и меняет само
// устройство, а не его настройку.
var miniappOwnerOnlyActions = map[string]bool{
	"firmware_install": true,
}

// miniappTunnelArgActions -- действия, чей туннель адресуется идентификатором,
// а имя NDMS-интерфейса подставляет сервер. Список общий, чтобы новое такое
// действие нельзя было завести мимо резолвера: попав в allowlist без записи
// здесь, оно ушло бы к агенту с клиентскими аргументами.
var miniappTunnelArgActions = map[string]bool{
	"tunnel_restart":   true,
	"tunnel_enable":    true,
	"tunnel_disable":   true,
	"pingcheck_toggle": true,
	"tunnel_traffic":   true,
	"tunnel_power":     true,
}

// miniappNDMSRequiredActions -- те из них, которые без имени NDMS-интерфейса
// выполнить нельзя: агент делает их через ndmc. Opkg-туннеля в NDMS нет
// вовсе, и 202 на такую команду означал бы «принято» о том, что молча ничего
// не сделает.
var miniappNDMSRequiredActions = map[string]bool{
	"tunnel_enable":    true,
	"tunnel_disable":   true,
	"pingcheck_toggle": true,
}

type miniappCommandReq struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
}

// miniappCommandHandler dispatches an allowlisted agent command on behalf of a
// mini-app user. Authorization is the same per-router ACL that gates viewing, and
// it is checked BEFORE the router is looked up so a stranger cannot probe which
// ids exist (same ordering as the Phase 3 access endpoints).
func miniappCommandHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.CommandSink == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "command sink not configured")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req miniappCommandReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.Action = strings.TrimSpace(req.Action)
		if !miniappCommandAllowlist[req.Action] || !wire.IsValidCommandAction(req.Action) {
			writeJSONError(w, http.StatusBadRequest, "unsupported_command", "action is not allowed from the mini app")
			return
		}
		if miniappOwnerOnlyActions[req.Action] && !miniappIsOwner(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusForbidden, "owner_only",
				"this action changes the device itself and is available to the router's owner only")
			return
		}
		commandArgs := req.Args
		if miniappTunnelArgActions[req.Action] {
			// The client sends tunnel_id, never ndms_name -- see the allowlist
			// comment above. Any ndms_name in req.Args is ignored outright (not
			// merely validated): building commandArgs fresh here, rather than
			// patching req.Args, is what makes the old {"ndms_name":"..."} shape
			// inert instead of just rejected.
			tunnelID, _ := req.Args["tunnel_id"].(string)
			resolved, ok := miniappResolveTunnelArgs(d, routerID, tunnelID)
			if !ok {
				writeJSONError(w, http.StatusBadRequest, "unknown_tunnel", "tunnel_id does not match a known tunnel on this router")
				return
			}
			if _, hasNDMS := resolved["ndms_name"]; !hasNDMS && miniappNDMSRequiredActions[req.Action] {
				writeJSONError(w, http.StatusBadRequest, "no_ndms_name",
					"this tunnel has no NDMS interface: the router cannot switch it this way")
				return
			}
			// enable -- единственный аргумент, который клиенту разрешено
			// прислать: это его выбор, а не топология роутера.
			if req.Action == "pingcheck_toggle" {
				enable, _ := req.Args["enable"].(bool)
				resolved["enable"] = enable
			}
			// period -- тоже выбор человека, а не топология: он говорит, за
			// какой срок показать обмен. Форму проверит санитайзер.
			if req.Action == "tunnel_traffic" {
				period, _ := req.Args["period"].(string)
				resolved["period"] = period
			}
			// on -- выбор человека: включить или выключить. Топология тут ни
			// при чём, и имя NDMS-интерфейса действию не нужно вовсе.
			if req.Action == "tunnel_power" {
				on, _ := req.Args["on"].(bool)
				delete(resolved, "ndms_name")
				resolved["on"] = on
			}
			commandArgs = resolved
		}
		args, ok := sanitizeWizardCommandArgs(w, req.Action, commandArgs)
		if !ok {
			return
		}
		// miniappRouterAllowed's admin branch grants access without checking that
		// routerID actually exists (miniappIsAdmin short-circuits before
		// RouterAccessRole), so an admin hitting a stale/typo'd id can still reach
		// here. GetByID returns db.ErrUserNotFound (never a nil, nil-error User) in
		// that case; map it to 404 same as the Phase 3 access endpoints, not the
		// generic 500.
		u, err := d.DB.Users().GetByID(routerID)
		if errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
			return
		}
		enqueueAgentCommandForUser(w, d, u, req.Action, args)
	}
}

// miniappMaxCommandWaitSec caps the long-poll. Agents report on a ~68s median
// cadence (measured 2026-07-06), and the command channel is a separate long-poll,
// but a sleeping mobile router can take far longer -- so the client polls in
// bounded hops rather than holding one socket open forever.
const miniappMaxCommandWaitSec = 30

// miniappCommandResultHandler polls for the result of a command previously
// dispatched via miniappCommandHandler. It is gated by the same per-router
// ACL, checked before cmd_id is even looked at, for the same reason: a
// stranger must not be able to read another router's command output, nor
// tell an existing-but-forbidden router apart from a nonexistent one.
//
// A missing result is 404 result_not_ready, not an error -- the agent simply
// hasn't answered yet and the client is expected to poll again. That mirrors
// wizardCmdResultHandler's contract (wizard_handler.go:1284) exactly, so a
// later frontend task can rely on the same "keep polling" vs "something
// broke" distinction it already implements for the wizard.
func miniappCommandResultHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.CommandSink == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "command sink not configured")
			return
		}
		cmdID := strings.TrimSpace(r.PathValue("cmd_id"))
		if cmdID == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "cmd_id required")
			return
		}
		wait := miniappMaxCommandWaitSec
		if q := r.URL.Query().Get("wait_sec"); q != "" {
			if n, err := strconv.Atoi(q); err == nil {
				wait = n
			}
		}
		if wait < 0 {
			wait = 0
		}
		if wait > miniappMaxCommandWaitSec {
			wait = miniappMaxCommandWaitSec
		}
		// routerID IS users.id in the mini app (see miniappRouterAllowed above),
		// so it goes straight to AwaitResult -- unlike wizardCmdResultHandler,
		// there is no nickname to resolve first.
		res, ok := d.CommandSink.AwaitResult(r.Context(), routerID, cmdID, time.Duration(wait)*time.Second)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "result_not_ready", "no result yet — poll again")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(res)
	}
}

// miniappIsOwner -- владелец роутера или админ бота. Роль читается тем же
// запросом, что и доступ вообще (db.RouterAccessRole), чтобы «кто такой
// владелец» имело в приложении один ответ, а не два.
func miniappIsOwner(d Deps, telegramUserID, routerID int64) bool {
	if miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
		return true
	}
	role, err := d.DB.RouterAccessRole(routerID, telegramUserID)
	return err == nil && role == "owner"
}

// miniappTunnelNDMSNameDetails is the minimal decode of a tunnel_* event's
// details_json needed to resolve tunnel_restart's ndms_name. It is
// deliberately separate from miniappTunnelDetails/miniappTunnel: ndms_name is
// router topology that the mini-app whitelist (miniapp_tunnels.go) must never
// send to the client, so it is decoded here, server-side only, and never
// attached to any response type.
type miniappTunnelNDMSNameDetails struct {
	TunnelID string `json:"tunnel_id"`
	NDMSName string `json:"ndms_name"`
}

// miniappResolveTunnelRestartArgs maps a tunnel_id the mini-app client sent to
// the arguments the agent needs, using only routerID's own tunnel_* event rows —
// never the caller-supplied id itself, and never a row from another router.
// This is what makes tunnel_restart's blast radius provably one tunnel on one
// router: a tunnel the backend has no tunnel_<id> event for is unreachable,
// not merely rejected, and a real tunnel belonging to a different router does
// not resolve here either.
//
// tunnel_id is always returned — awg-manager restarts by id, which is the only
// path that works for opkg tunnels. ndms_name is added only when the router's
// own events carry one; the agent needs it solely as a fallback on builds that
// predate /api/control/restart. It is still server-derived: a client-supplied
// ndms_name never reaches the agent.
func miniappResolveTunnelArgs(d Deps, routerID int64, tunnelID string) (map[string]any, bool) {
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		return nil, false
	}
	rows, err := d.DB.Events().LatestEventsByPrefixSince(routerID, miniappTunnelPrefix, time.Now().UTC().Add(-miniappEventsWindow))
	if err != nil {
		return nil, false
	}
	for _, row := range rows {
		tu, ok := miniappTunnelFromEvent(row)
		if !ok || tu.TunnelID != tunnelID {
			continue
		}
		args := map[string]any{"tunnel_id": tunnelID}
		var det miniappTunnelNDMSNameDetails
		if err := json.Unmarshal([]byte(row.DetailsJSON), &det); err == nil {
			if ndmsName := strings.TrimSpace(det.NDMSName); ndmsName != "" {
				args["ndms_name"] = ndmsName
			}
		}
		return args, true
	}
	return nil, false
}
