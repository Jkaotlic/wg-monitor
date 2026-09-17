package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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
// list and adds to it (tunnel probes and restart -- see below).
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
// repairs. dns_reset and agent config editing are router-global and came in
// only behind their own gates: admin-only, an agent version floor and a
// confirming screen (see their entries below).
var miniappCommandAllowlist = map[string]bool{
	// Read-only, already trusted to the dashboard.
	"force_recheck":  true,
	"diag_now":       true,
	"tunnels_status": true,
	"route_status":   true,
	// «Открывается ли сайт с этого роутера»: только чтение -- имя разрешается
	// через dns-proxy роутера и идёт TCP на 443. Смотреть вправе и оператор:
	// ничего не меняется.
	"dns_open": true,
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
	// «Куда пойдёт сайт»: только чтение, одинаково владельцу, оператору и
	// админу. До агента доезжает ровно имя сайта -- явная ветка санитайзера.
	"route_lookup": true,

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
	// Список правил HydraRoute Neo (цикл 4): только чтение, аргументов нет.
	// Видят все, у кого есть доступ к роутеру.
	"hrneo_inventory": true,
	"pingcheck_now":   true,

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

	// Прошивка (фаза D2). Чтение и установка -- всем, у кого есть доступ к
	// роутеру: с цикла 1 ставят и операторы (решение оператора 14.09).
	// Установка необратима и перезагружает роутер, поэтому её держат набор
	// имени роутера, который бэкенд сверяет сам (miniappConfirmRequired), и
	// отказ агента при allow_firmware_install=false.
	"firmware_status":  true,
	"firmware_install": true,

	// Обслуживание и обновления (цикл 1 «бот без слеш-команд»). Кнопки
	// переехали из панели обслуживания бота, круг у них тот же -- админ,
	// владелец и операторы. Каждое router-local:
	//
	//   - awgm_update / hrneo_update -- обновление по решению самого роутера,
	//     аргументов нет, пол версии агента v0.32.0;
	//   - opkg_upgrade -- обновление пакетов Entware, аргументов нет;
	//   - opkg_feed_disable -- один http(s)-адрес мёртвого фида
	//     (sanitizeOpkgFeedURL);
	//   - service_restart -- имя из miniappServiceRestartNames; перезагрузка
	//     роутера требует набора имени и держит кулдаун.
	"awgm_update":       true,
	"hrneo_update":      true,
	"opkg_upgrade":      true,
	"opkg_feed_disable": true,
	"service_restart":   true,

	// Правка конфига агента (решение оператора № 3, отменяет D4 программы
	// мини-аппа). Радиус router-global, поэтому границ у неё сразу три, и
	// каждая независима от остальных:
	//
	//   - только админ бота (miniappAdminOnlyActions), отказ 404 not_found;
	//   - пол версии агента с отказом по умолчанию
	//     (miniappActionMinAgentVersion): старый агент не знает про новые
	//     поля и сделает не то, что человек прочитал на экране;
	//   - подтверждение набором имени роутера на экране.
	//
	// Аргументы проверяет уже написанная ветка sanitizeAgentConfigArgs
	// (wizard_handler.go), а не ветка default: это закрытый whitelist полей,
	// повторяющий агентский. backend.url и токен в него не входят намеренно
	// и остаются на пути мастера и CLI -- перенаправить адрес бэкенда значит
	// захватить весь парк, и запрет живёт на стороне агента, где его не
	// обойти правкой сервера. update_backend_url сюда не переезжает вовсе.
	// Сброс DNS (решение оператора № 5, отменяет D3 программы мини-аппа).
	// Радиус router-global, поэтому границы те же, что у правки конфига, плюс
	// обязательный предпросмотр:
	//
	//   - только админ бота (miniappAdminOnlyActions), отказ 404 not_found --
	//     и на постановке, и на опросе результата;
	//   - пол версии агента с отказом по умолчанию
	//     (miniappActionMinAgentVersion): старый агент не знает dry_run и на
	//     «посмотреть» сделал бы настоящий сброс, поэтому пол стоит и на
	//     предпросмотре;
	//   - экран, который таким роутерам не рисуется, кнопка сброса только
	//     после предпросмотра и подтверждение набором имени роутера.
	//
	// Аргумент один -- dry_run, его проверяет ветка sanitizeWizardCommandArgs.
	"dns_reset": true,

	"agent_config_get":    true,
	"update_agent_config": true,

	// Пакеты по расписанию (спека цикла 2, п. 9): переехали из старого
	// дашборда, где они были всегда. Радиус router-global (cron и очистка
	// /opt), поэтому круг -- только админ (miniappAdminOnlyActions), отказ
	// 404 и на постановке, и на опросе результата. Аргументы -- явные ветки
	// sanitizeWizardCommandArgs: расписание HH:MM или пять полей cron, число
	// строк журнала 1..300; всё прочее клиентское до агента не доезжает.
	"opkg_cron_status":      true,
	"opkg_cron_install":     true,
	"opkg_cron_logs":        true,
	"opkg_cron_remove":      true,
	"entware_clean_status":  true,
	"entware_clean_install": true,
	"entware_clean_run":     true,
	"entware_clean_logs":    true,
	"entware_clean_remove":  true,
}

// miniappOwnerOnlyActions -- действия, которых оператору не положено. С цикла 1
// список пуст: прошивку ставят и операторы (решение оператора 14.09). Карта и
// её гейты на постановке и опросе остаются -- новое такое действие заводится
// сюда, а не отдельной веткой.
var miniappOwnerOnlyActions = map[string]bool{}

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
	// Confirm -- набранное человеком имя роутера. Сверяется для необратимых
	// действий (miniappConfirmRequired); остальным не нужно.
	Confirm string `json:"confirm"`
}

// miniappCommandHandler dispatches an allowlisted agent command on behalf of a
// mini-app user. Authorization is the same per-router ACL that gates viewing, and
// it is checked BEFORE the router is looked up so a stranger cannot probe which
// ids exist (same ordering as the Phase 3 access endpoints).
func miniappCommandHandler(d Deps) http.HandlerFunc {
	// Окно перезагрузки живёт вместе с обработчиком: один мукс -- одно окно,
	// и тесты с разными муксами не мешают друг другу.
	rebootCooldown := newRouterCooldown(miniappRebootCooldown, time.Now)
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
		// Радиус router-global -- круг только админ бота, и отказ приходит
		// как 404 not_found, а не 403: владельцу роутера незачем узнавать по
		// коду ответа, что действие вообще существует. Тот же порядок, что у
		// остальных админских срезов мини-аппа, и до поиска роутера.
		if miniappAdminOnlyActions[req.Action] && !miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
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
		// Узкий круг по аргументам, а не по имени действия: запуск и остановка
		// HydraRoute Neo -- только админ и владелец (цикл 4, решение 1).
		if miniappOwnerOnlyCommand(req.Action, args) && !miniappIsOwner(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusForbidden, "owner_only",
				"this action changes the device itself and is available to the router's owner only")
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
		// Гейт по версии агента -- ПЕРВАЯ из двух независимых преград
		// (решение оператора п. 10). Версия та, о которой роутер сообщил сам
		// в последнем отчёте; отказ по умолчанию (agentAtLeast), то есть
		// пустая и нечитаемая версия ЗАПРЕЩАЮТ действие.
		//
		// Отказ стоит до очереди, а не в ответе агента: старый агент не
		// знает про новые поля, перепишет config.yaml по своим правилам и
		// перезапустит себя, а «принято» о таком было бы обещанием того, что
		// не случится. Вторая преграда -- экран, который для таких роутеров
		// не рисуется вовсе; ни одна не заменяет другую.
		if floor := miniappActionMinAgentVersion[req.Action]; floor != "" {
			agentVersion := ""
			if u.LastDeployedVersion != nil {
				agentVersion = *u.LastDeployedVersion
			}
			if !agentAtLeast(agentVersion, floor) {
				writeJSONError(w, http.StatusConflict, "agent_too_old",
					"на роутере агент "+agentVersion+", этому действию нужен "+floor+" или новее")
				return
			}
		}
		// Необратимое подтверждается набором имени роутера, и сверяет его
		// сервер: проверка на экране -- пауза для человека, а не граница.
		if miniappConfirmRequired(req.Action, args) && !confirmPhraseMatches(req.Confirm, u.Nickname) {
			writeJSONError(w, http.StatusBadRequest, "confirm_mismatch", "имя роутера набрано неверно")
			return
		}
		reboot := miniappIsRouterReboot(req.Action, args)
		if reboot && !rebootCooldown.tryStart(u.ID) {
			writeJSONError(w, http.StatusTooManyRequests, "reboot_cooldown", "роутер уже перезагружается")
			return
		}
		resp := wizardDeployResp{}
		resp.RouterAsleep, resp.RouterStatus, resp.WakeWindowMin = miniappWakeWindow(d, u, req.Action, time.Now().UTC())
		if !enqueueAgentCommandForUserResp(w, d, u, req.Action, args, resp) && reboot {
			rebootCooldown.release(u.ID)
		}
	}
}

// miniappMaxCommandWaitSec caps the long-poll. Agents report on a ~68s median
// cadence (measured 2026-07-06), and the command channel is a separate long-poll,
// but a sleeping mobile router can take far longer -- so the client polls in
// bounded hops rather than holding one socket open forever. Хоп обязан
// укладываться под обрыв релея KeenDNS на 15-й секунде (см. maxCmdWait):
// прежние 30 превращали каждую команду дольше 15 секунд в ошибку у владельца.
const miniappMaxCommandWaitSec = int(maxCmdWait / time.Second)

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
		// Граница по роли стоит и здесь, а не только на постановке команды.
		// Ответ агента на agent_config_get несёт адрес панели роутера -- а
		// это ровно то, чего владельцу в мини-аппе не показывают (ему
		// «панель известна» и кнопка «Открыть»). Без этой проверки владелец,
		// у которого есть идентификатор команды, дочитал бы адрес из чужого
		// ответа, хотя саму команду поставить не может.
		//
		// Действие берётся у очереди и НЕ зависит от записи о выдаче:
		// RecordResult кладёт action рядом с результатом, поэтому оно живёт
		// ровно столько же, сколько сам результат.
		//
		// Раньше здесь стоял расчёт «действие забыто -- значит и результата
		// нет», и он был НЕВЕРЕН: Sweep чистит issued по issuedAt, а results
		// по recordedAt одним cutoff, и issuedAt всегда раньше. Запись о
		// выдаче уходила первой, и гейт открывался ровно на время задержки
		// ответа агента -- у спящего мобильного роутера это минуты. Сторожит
		// TestMiniappResultRoleGateSurvivesSweep.
		//
		// Отказ повторяет постановку для каждого действия: admin-only -- 404
		// not_found (по коду ответа владельцу незачем узнавать, что действие
		// существует), owner-only -- 403 owner_only.
		if cmd, known := d.CommandSink.CommandByID(routerID, cmdID); known {
			if miniappAdminOnlyActions[cmd.Action] && !miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
				writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
				return
			}
			// Второй гейт (miniappOwnerOnlyActions) проверяется тоже, а не
			// только admin-only -- хотя сегодня карта пуста: обслуживание,
			// включая firmware_install, открыто админу, владельцу и
			// оператору (решение оператора 14.09), а необратимость держит
			// набор имени роутера, а не роль. Код остаётся написанным на
			// будущее: owner-only действие с чувствительным выводом заведётся
			// в эту карту, а не отдельной веткой, и гейт прикроет его сразу и
			// на постановке, и здесь, на опросе.
			if (miniappOwnerOnlyActions[cmd.Action] || miniappOwnerOnlyCommand(cmd.Action, cmd.Args)) && !miniappIsOwner(d, telegramUserID, routerID) {
				writeJSONError(w, http.StatusForbidden, "owner_only",
					"this action changes the device itself and is available to the router's owner only")
				return
			}
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

// miniappServiceRestartNames -- что мини-апп вправе перезапустить, запустить
// или остановить. Список -- граница: имя вне него до агента не доезжает.
// hrneo_start и hrneo_stop добавлены циклом 4 (блок HydraRoute Neo во
// вкладке «Маршруты») и уже круга остальных (miniappOwnerOnlyCommand).
var miniappServiceRestartNames = map[string]bool{
	"hrneo":       true,
	"hrneo_start": true,
	"hrneo_stop":  true,
	"awgmgr":      true,
	"router":      true,
}

// miniappOwnerOnlyCommand -- действие, которое оператору не положено из-за
// аргументов: запуск и остановка HydraRoute Neo. Остановка выключает правила
// по имени сайта на весь роутер, поэтому круг -- админ и владелец, как у
// удаления VPN-туннеля (цикл 4, решение 1). Проверяется и на постановке, и на
// опросе результата.
func miniappOwnerOnlyCommand(action string, args map[string]any) bool {
	if action != "service_restart" {
		return false
	}
	name, _ := args["name"].(string)
	return name == "hrneo_start" || name == "hrneo_stop"
}

// sanitizeOpkgFeedURL -- адрес мёртвого фида для opkg_feed_disable. Агент только
// закомментирует строку конфига с этим адресом, но чужое до очереди не доезжает:
// один http(s)-адрес без логина, запроса и пробелов.
func sanitizeOpkgFeedURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 512 || strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	return s, true
}
