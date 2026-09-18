package backend

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/timeline"
)

func registerMiniappRoutes(mux *http.ServeMux, d Deps, entrance *remoteRateLimiter) {
	reqID := requestIDMiddleware()
	auth := MiniAppAuthMiddleware(d.TelegramBotToken, d.DashboardToken, d.TelegramAdminUserID, d.Logger)
	entranceLimit := remoteRateLimitMiddleware(entrance, d.Logger)

	staticFS, err := fs.Sub(miniappStaticFS, "miniapp_static")
	if err != nil {
		panic(err)
	}
	// The app shell (HTML/JS/CSS) carries no sensitive data — only the JSON
	// API calls it makes are auth-gated, same principle as any SPA's public
	// login page. Telegram must be able to load it before a session exists.
	staticHandler := staticCacheHeaders(http.StripPrefix("/miniapp/", http.FileServer(http.FS(staticFS))))
	mux.Handle("GET /miniapp/", reqID(staticHandler))

	// The bare domain has no handler of its own, so Go's mux answers a plain
	// "404 page not found" — from the outside that is indistinguishable from
	// an outage. Send it to the app instead, same idiom as GET /dashboard.
	// "{$}" anchors the pattern to the exact root: a bare "/" would turn this
	// into a catch-all and swallow every genuine 404.
	mux.Handle("GET /{$}", reqID(http.RedirectHandler("/miniapp/", http.StatusFound)))

	mux.Handle("POST /v1/miniapp/session", reqID(entranceLimit(miniappSessionHandler(d))))
	// Кто я -- для браузерного входа: там нет initData, и сессия уже есть
	// (кука дашборда). Telegram-клиент этим маршрутом не пользуется.
	mux.Handle("GET /v1/miniapp/session", reqID(auth(miniappWhoAmIHandler(d))))
	// Ссылка на веб-управление -- только админу; гейт внутри хендлера, отказ
	// 404, как у остальных поверхностей мини-аппа.
	mux.Handle("POST /v1/miniapp/web-link", reqID(auth(webLinkIssueHandler(d))))
	// Сводка всего парка -- только админу: это проекция дашбордной сводки,
	// и радиус у неё парковый, а не роутерный.
	mux.Handle("GET /v1/miniapp/fleet", reqID(auth(miniappFleetHandler(d))))
	// Обновление агента -- только админу, гейт внутри (404 не-админу).
	mux.Handle("POST /v1/miniapp/routers/{id}/agent/update", reqID(auth(miniappAgentUpdateHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/agent/update/cancel", reqID(auth(miniappAgentUpdateCancelHandler(d))))
	mux.Handle("POST /v1/miniapp/fleet/agent/update", reqID(auth(miniappFleetAgentUpdateHandler(d))))
	// Оживление агента -- только админу, гейт внутри (404 не-админу).
	// Тело POST несёт пароль: middleware тела не читают, хендлер его не логирует.
	mux.Handle("POST /v1/miniapp/routers/{id}/agent/revive", reqID(auth(miniappAgentReviveHandler(d))))
	mux.Handle("DELETE /v1/miniapp/routers/{id}/agent/revive", reqID(auth(miniappAgentReviveCancelHandler(d))))
	// Админские операции цикла 2 (веб-управление = мини-апп): гейт админа
	// внутри, отказ 404. Тела с паролями middleware не читают, обработчики не
	// логируют.
	mux.Handle("POST /v1/miniapp/backend/deploy", reqID(auth(miniappBackendDeployHandler(d))))
	mux.Handle("GET /v1/miniapp/jobs/{job_id}", reqID(auth(miniappJobHandler(d))))
	mux.Handle("POST /v1/miniapp/provision", reqID(auth(miniappProvisionHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/agent/reinstall", reqID(auth(miniappAgentReinstallHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/agent/repoint", reqID(auth(miniappAgentRepointHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/agent/connection", reqID(auth(miniappAgentConnectionGetHandler(d))))
	mux.Handle("PUT /v1/miniapp/routers/{id}/agent/connection", reqID(auth(miniappAgentConnectionPutHandler(d))))
	mux.Handle("GET /v1/miniapp/routers", reqID(auth(miniappRoutersHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}", reqID(auth(miniappRouterDetailHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/events", reqID(auth(miniappRouterEventsHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/timeline", reqID(auth(miniappRouterTimelineHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/settings", reqID(auth(miniappRouterSettingsHandler(d))))
	mux.Handle("PUT /v1/miniapp/routers/{id}/notify", reqID(auth(miniappNotifyHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/vpn", reqID(auth(miniappVPNAccountsHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/vpn/issue", reqID(auth(miniappVPNIssueHandler(d))))
	// Кабинеты VPN (цикл 3): ключи Amnezia и коды HideMy. Гейт роли внутри
	// (404), тела с секретами middleware не читают, обработчики не логируют.
	mux.Handle("GET /v1/miniapp/routers/{id}/cabinets", reqID(auth(miniappCabinetsHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/cabinets/amnezia/keys", reqID(auth(miniappCabinetAddHandler(d, "amnezia"))))
	mux.Handle("PUT /v1/miniapp/routers/{id}/cabinets/amnezia/active", reqID(auth(miniappCabinetActiveHandler(d, "amnezia"))))
	mux.Handle("DELETE /v1/miniapp/routers/{id}/cabinets/amnezia/keys/{secret_id}", reqID(auth(miniappCabinetDeleteHandler(d, "amnezia"))))
	mux.Handle("POST /v1/miniapp/routers/{id}/cabinets/hidemy/codes", reqID(auth(miniappCabinetAddHandler(d, "hidemyname"))))
	mux.Handle("PUT /v1/miniapp/routers/{id}/cabinets/hidemy/active", reqID(auth(miniappCabinetActiveHandler(d, "hidemyname"))))
	mux.Handle("DELETE /v1/miniapp/routers/{id}/cabinets/hidemy/codes/{secret_id}", reqID(auth(miniappCabinetDeleteHandler(d, "hidemyname"))))
	mux.Handle("POST /v1/miniapp/routers/{id}/cabinets/amnezia/revoke", reqID(auth(miniappCabinetRevokeHandler(d))))
	// .conf документом в личку нажавшему: админ и владелец, свой сервер -- админ.
	mux.Handle("POST /v1/miniapp/routers/{id}/vpn/send-conf", reqID(auth(miniappSendConfHandler(d))))
	// Свои VPN-серверы (цикл 3): только админ, гейт внутри (404), тело формы
	// несёт пароль SSH -- middleware его не читают, обработчики не логируют.
	mux.Handle("GET /v1/miniapp/selfhosted", reqID(auth(miniappSelfHostedListHandler(d))))
	mux.Handle("POST /v1/miniapp/selfhosted", reqID(auth(miniappSelfHostedCreateHandler(d))))
	mux.Handle("PUT /v1/miniapp/selfhosted/{inst}", reqID(auth(miniappSelfHostedUpdateHandler(d))))
	mux.Handle("POST /v1/miniapp/selfhosted/{inst}/toggle", reqID(auth(miniappSelfHostedToggleHandler(d))))
	mux.Handle("DELETE /v1/miniapp/selfhosted/{inst}", reqID(auth(miniappSelfHostedDeleteHandler(d))))
	mux.Handle("POST /v1/miniapp/selfhosted/{inst}/check", reqID(auth(miniappSelfHostedCheckHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/replace", reqID(auth(miniappReplaceStartHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/replace", reqID(auth(miniappReplaceStatusHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/repair", reqID(auth(miniappRepairStartHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/repair", reqID(auth(miniappRepairStatusHandler(d))))
	mux.Handle("PUT /v1/miniapp/routers/{id}/repair/auto", reqID(auth(miniappRepairAutoHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/commands", reqID(auth(miniappCommandHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/commands/{cmd_id}", reqID(auth(miniappCommandResultHandler(d))))
	// VPN-туннели (цикл 4): удаление и импорт .conf. Вопросы агенту живут
	// вместе с муксом, как окно перезагрузки у /commands.
	tunnelQuestions := newMiniappAgentQuestions(miniappAgentAskWait, miniappAgentAskReuse, time.Now)
	mux.Handle("POST /v1/miniapp/routers/{id}/tunnels/{tunnel_id}/delete", reqID(auth(miniappTunnelDeleteHandler(d, tunnelQuestions))))
	importPreviews := newMiniappImportPreviews(miniappImportTTL, time.Now)
	mux.Handle("POST /v1/miniapp/routers/{id}/tunnels/import", reqID(auth(miniappTunnelImportHandler(d, importPreviews, tunnelQuestions))))
	mux.Handle("GET /v1/miniapp/routers/{id}/tunnels/import/{token}", reqID(auth(miniappTunnelImportPreviewHandler(d, importPreviews, tunnelQuestions))))
	mux.Handle("POST /v1/miniapp/routers/{id}/tunnels/import/confirm", reqID(auth(miniappTunnelImportConfirmHandler(d, importPreviews, tunnelQuestions))))
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/silence", reqID(auth(miniappSilenceHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/ack", reqID(auth(miniappAckHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/mute", reqID(auth(miniappMuteHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/incidents/{check}/history", reqID(auth(miniappHistoryHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/access", reqID(auth(miniappAccessHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/access/operators", reqID(auth(miniappAddOperatorHandler(d))))
	mux.Handle("DELETE /v1/miniapp/routers/{id}/access/operators/{tgid}", reqID(auth(miniappRemoveOperatorHandler(d))))
	mux.Handle("DELETE /v1/miniapp/routers/{id}/access/owner", reqID(auth(miniappUnbindOwnerHandler(d))))
	mux.Handle("PUT /v1/miniapp/routers/{id}/access/owner", reqID(auth(miniappSetOwnerHandler(d))))
	// Новости об обновлениях. Читают владелец, оператор и админ; новость о
	// прошивке -- только владелец и админ. Решение «отложить/скрыть» касается
	// всего роутера, поэтому его принимает владелец или админ -- гейт внутри
	// обработчика, отказ 404 до поиска роутера.
	mux.Handle("GET /v1/miniapp/routers/{id}/versions", reqID(auth(miniappRouterVersionsHandler(d))))
	mux.Handle("PUT /v1/miniapp/routers/{id}/updates/{component}", reqID(auth(miniappUpdateReminderHandler(d))))
}

type miniappSessionReq struct {
	InitData string `json:"init_data"`
}

type miniappSessionResp struct {
	OK             bool       `json:"ok"`
	TelegramUserID int64      `json:"telegram_user_id"`
	IsAdmin        bool       `json:"is_admin"`
	Via            miniappVia `json:"via"`
}

func miniappSessionHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req miniappSessionReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InitData == "" {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "init_data required")
			return
		}
		user, err := verifyInitData(req.InitData, d.TelegramBotToken, miniappNow())
		if err != nil {
			if d.Logger != nil {
				d.Logger.Warn("miniapp session: init_data rejected", "err", err)
			}
			writeJSONError(w, http.StatusUnauthorized, "invalid_init_data", "could not verify Telegram init data")
			return
		}
		http.SetCookie(w, miniappSessionCookie(r, d.TelegramBotToken, user.ID))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(miniappSessionResp{
			OK:             true,
			TelegramUserID: user.ID,
			IsAdmin:        miniappIsAdmin(user.ID, d.TelegramAdminUserID),
			Via:            miniappViaTelegram,
		})
	}
}

// miniappWhoAmIHandler -- кто я для уже открытой сессии (кука мини-аппа или
// кука веб-управления); новой куки не выпускает.
func miniappWhoAmIHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, _ := miniappUserFromContext(r.Context())
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(miniappSessionResp{
			OK:             true,
			TelegramUserID: uid,
			IsAdmin:        miniappIsAdmin(uid, d.TelegramAdminUserID),
			Via:            miniappViaFromContext(r.Context()),
		})
	}
}

// miniappIsAdmin enforces a fail-closed access gate for the mini app: only the
// explicitly configured TelegramAdminUserID is treated as admin. Unlike the
// bot's callbacks.Router.isAdminTG (which is fail-open for UI hints only),
// miniappIsAdmin is the actual authorization boundary: it controls fleet
// visibility and query access. Returning true grants complete router access,
// so it must reject any unset adminUserID (0) even though config-loading
// prevents that in practice.
func miniappIsAdmin(telegramUserID, adminUserID int64) bool {
	return adminUserID != 0 && telegramUserID == adminUserID
}

type miniappRouterSummary struct {
	ID             int64      `json:"id"`
	Nickname       string     `json:"nickname"`
	Status         string     `json:"status"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
	LastSeenAgeSec *int64     `json:"last_seen_age_sec,omitempty"`
	// AgentVersion и Kind -- для поиска и фильтров списка (спека цикла 2,
	// п. 11). Не доступы: видны всем, кто видит роутер. Без omitempty --
	// неизвестная версия приходит пустой строкой.
	AgentVersion    string              `json:"agent_version"`
	Kind            string              `json:"kind"`
	ActiveIncidents []dashboardIncident `json:"active_incidents,omitempty"`
	// Checks -- состояние пяти служб роутера, тех самых, что на его экране
	// нарисованы лампами. Экран флота показывает их точками, и без них он
	// либо красит строку наугад, либо ходит за каждым роутером отдельным
	// запросом уже после отрисовки.
	Checks []miniappCheckDot `json:"checks,omitempty"`
	// ReserveOnlyAlert -- все активные тревоги роутера только по VPN-туннелям,
	// которые обход сейчас не несут (запасным): обход работает, резерва нет.
	// Список рисует янтарную «резерв не работает» вместо красной «тревога».
	// Несущий известен только из сводки политик агента; без неё -- false.
	// Тревоги и уведомления от этого не меняются.
	ReserveOnlyAlert bool `json:"reserve_only_alert,omitempty"`
	// PanelURL -- ссылка на панель awg-manager роутера (прошедший
	// panelAddress). Только админу и владельцу; оператору роутера -- нет.
	PanelURL string `json:"panel_url,omitempty"`
}

// miniappCheckDot -- имя службы и её состояние, без деталей: точке на экране
// флота больше ничего не нужно, а факты проверок живут на своём экране.
type miniappCheckDot struct {
	CheckName string `json:"check_name"`
	Status    string `json:"status"`
}

// miniappLampChecks -- те же пять служб, что рисует прибор на экране роутера
// (LAMP_ORDER в RouterDevice.jsx). Список фиксирован намеренно: точек ровно
// пять, и лишняя строка событий не должна превращаться в шестую точку,
// которую человеку никто не объяснял.
var miniappLampChecks = map[string]bool{
	"dns":            true,
	"external_reach": true,
	"hydraroute":     true,
	"awg_manager":    true,
	"tunnels":        true,
}

// miniappServiceDots читает состояние пяти служб одного роутера и заодно
// решает, не одни ли запасные VPN-туннели у него в тревоге (ReserveOnlyAlert):
// обоим ответам нужна одна и та же выборка последних событий.
//
// Запрос на роутер: на флоте оператора их восемь, и восемь дешёвых выборок
// при открытии списка честнее одной общей, которой в репозитории нет. Если
// флот вырастет до сотен, здесь понадобится один запрос с группировкой.
func miniappServiceDots(d Deps, routerID int64, incidents []dashboardIncident) ([]miniappCheckDot, bool) {
	rows, err := d.DB.Events().LatestEventsByPrefixSince(routerID, "", time.Now().UTC().Add(-miniappEventsWindow))
	if err != nil {
		return nil, false
	}
	out := make([]miniappCheckDot, 0, len(miniappLampChecks))
	for _, row := range rows {
		if !miniappLampChecks[row.CheckName] {
			continue
		}
		out = append(out, miniappCheckDot{CheckName: row.CheckName, Status: row.Status})
	}
	return out, miniappReserveOnlyAlert(rows, incidents)
}

// miniappReserveOnlyAlert: тревоги есть, и каждая -- по туннелю, который
// обход сейчас не несёт, а только лежит в запасе у несущего набора. Несущий --
// тот же, что называет экран роутера (miniappPolicyCarrier по сводке политик
// агента); неизвестен (старый агент, sing-box) -- false: не угадываем.
//
// Тревога считается «резервной», только если её туннель одновременно:
// звено несущего набора не в роли active; не активное звено НИ ОДНОГО набора
// с исполняемыми правилами (второй набор мог идти именно через него); не
// ведёт своих правил (routes_dns/routes_static); не главный выход. Кроме
// того, ни один набор с исполняемыми правилами не должен остаться без живого
// звена -- его правила не идут никуда. Всё прочее -- настоящая тревога.
func miniappReserveOnlyAlert(rows []db.EventRow, incidents []dashboardIncident) bool {
	if len(incidents) == 0 {
		return false
	}
	var tunnels []miniappTunnel
	var hd miniappHydraDetails
	for _, row := range rows {
		if tu, ok := miniappTunnelFromEvent(row); ok {
			tunnels = append(tunnels, tu)
		}
		if row.CheckName == "hydraroute" {
			if json.Unmarshal([]byte(row.DetailsJSON), &hd) != nil {
				return false
			}
		}
	}
	if hd.SingboxRouterActive {
		return false
	}
	carrier, carrierPolicy := miniappPolicyCarrier(tunnels, hd)
	if carrier == nil {
		return false
	}
	carrying := map[string]bool{}
	for i := range hd.Policies {
		p := &hd.Policies[i]
		if miniappPolicyExecuted(p, hd) <= 0 {
			continue
		}
		hasActive := false
		for _, l := range p.Links {
			if l.Role == "active" {
				hasActive = true
			}
		}
		if !hasActive {
			return false
		}
		if p.ActiveTunnelID != "" {
			carrying[p.ActiveTunnelID] = true
		}
	}
	spare := map[string]bool{}
	for _, l := range carrierPolicy.Links {
		if l.TunnelID != "" && l.Role != "active" {
			spare[l.TunnelID] = true
		}
	}
	for _, inc := range incidents {
		id, ok := strings.CutPrefix(inc.CheckName, miniappTunnelPrefix)
		if !ok || id == "" || !spare[id] || carrying[id] {
			return false
		}
		t := miniappTunnelByID(tunnels, id)
		if t == nil || t.IsActiveDefault || t.RoutesDNS+t.RoutesStatic > 0 {
			return false
		}
	}
	return true
}

func miniappRouterSummaryFromAgent(a dashboardSummaryAgent) miniappRouterSummary {
	return miniappRouterSummary{
		ID:              a.ID,
		Nickname:        a.Nickname,
		Status:          a.Status,
		LastSeenAt:      a.LastSeenAt,
		LastSeenAgeSec:  a.LastSeenAgeSec,
		AgentVersion:    a.AgentVersion,
		Kind:            a.Kind,
		ActiveIncidents: a.ActiveIncidents,
	}
}

type miniappRoutersResp struct {
	Routers []miniappRouterSummary `json:"routers"`
}

// miniappRoutersHandler lists the routers visible to the caller: the full
// fleet for the Telegram admin, or only routers telegramUserID owns or
// operates (via db.AccessibleRouterIDs) for everyone else. The response
// deliberately excludes dashboardSummaryAgent's SSH/AWGM/deploy-metadata
// fields — mini-app callers include non-admin operators who shouldn't see
// those.
func miniappRoutersHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		summary, err := buildDashboardSummary(d.DB, time.Now().UTC(), dashboardStatusPolicyFromDeps(d))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "fleet summary failed")
			return
		}
		// Пустой слайс, а не nil: клиент делает .map по этому полю, и null
		// уронил бы экран человека, которому пока не выдали ни одного роутера.
		resp := miniappRoutersResp{Routers: []miniappRouterSummary{}}
		// Список роутеров -- из той же сводки, что у дашборда, а адрес панели
		// в ней уже прошёл safeDashboardAWGMURL; ссылка -- через panelAddress.
		fill := func(a dashboardSummaryAgent, role string) miniappRouterSummary {
			row := miniappRouterSummaryFromAgent(a)
			row.Checks, row.ReserveOnlyAlert = miniappServiceDots(d, a.ID, a.ActiveIncidents)
			row.PanelURL = miniappPanelURLFor(role, &a.AWGMURL)
			return row
		}
		if miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
			for _, a := range summary.Agents {
				resp.Routers = append(resp.Routers, fill(a, "admin"))
			}
		} else {
			allowed, err := d.DB.AccessibleRouterIDs(telegramUserID)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "access lookup failed")
				return
			}
			allowedSet := make(map[int64]bool, len(allowed))
			for _, id := range allowed {
				allowedSet[id] = true
			}
			for _, a := range summary.Agents {
				if !allowedSet[a.ID] {
					continue
				}
				// Роль нужна только ради ссылки на панель: владельцу -- да,
				// оператору -- нет. Ошибка чтения -- без ссылки, а не без строки.
				role, _ := d.DB.RouterAccessRole(a.ID, telegramUserID)
				resp.Routers = append(resp.Routers, fill(a, role))
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

type miniappRouterResp struct {
	Router    miniappRouterSummary `json:"router"`
	Incidents []miniappIncident    `json:"incidents"`
}

func miniappRouterDetailHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		summary, err := buildDashboardSummary(d.DB, time.Now().UTC(), dashboardStatusPolicyFromDeps(d))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "fleet summary failed")
			return
		}
		for _, a := range summary.Agents {
			if a.ID == routerID {
				incidents, err := d.DB.State().HardIncidentsForUser(routerID)
				if err != nil {
					writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "incident lookup failed")
					return
				}
				resp := miniappRouterResp{Router: miniappRouterSummaryFromAgent(a)}
				// The enriched top-level Incidents supersedes the summary's
				// lightweight active_incidents on the detail view; omitempty
				// then drops it from the JSON. (Kept on the fleet-list summary.)
				resp.Router.ActiveIncidents = nil
				for _, st := range incidents {
					resp.Incidents = append(resp.Incidents, miniappIncidentFromState(st))
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
		}
		writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
	}
}

type miniappCheckStatus struct {
	CheckName string `json:"check_name"`
	Status    string `json:"status"`
	Timestamp string `json:"ts"`
	// Facts — измеримое, что проверка узнала (miniapp_check_facts.go). Белый
	// список, а не details_json как есть; nil означает «агент не сказал», и
	// подменять его нулями нельзя.
	Facts *miniappCheckFacts `json:"facts,omitempty"`
	// Details — те поля details_json, по которым экран рисует ответ словами
	// (раздел «Раздельный DNS», строка «Свой DNS-сервер»). Тоже белый список
	// (miniappCheckDetailsFrom), только для этих двух проверок.
	Details map[string]any `json:"details,omitempty"`
}

type miniappRouterEventsResp struct {
	Checks []miniappCheckStatus `json:"checks"`
	// Tunnels is the projection of the tunnel_* rows' details -- the screen's
	// "are my tunnels alive" answer. Checks keeps carrying the same rows in flat
	// form for backwards compatibility; the two are views of one query, not two
	// sources of truth.
	Tunnels []miniappTunnel `json:"tunnels"`
	Traffic miniappTraffic  `json:"traffic"`
}

// miniappEventsWindow bounds how far back "the latest status of every
// check" looks. A pragmatic default, not a hard requirement — easy to tune
// later if it proves too short/long in practice.
const miniappEventsWindow = 30 * 24 * time.Hour

func miniappRouterEventsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		// An empty prefix matches every check_name, so this returns one row
		// per check: its latest known status — the same query shape the
		// bot's tunnels panel/alert formatter already use, not a full
		// chronological history.
		rows, err := d.DB.Events().LatestEventsByPrefixSince(routerID, "", time.Now().UTC().Add(-miniappEventsWindow))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "events lookup failed")
			return
		}
		// Every agent report carries agent_heartbeat, and all checks of one
		// report share one timestamp. A resolver_guard row strictly older
		// than the heartbeat row is not from the latest report — the check
		// stopped coming (watchdog switched off, or its incident closed as
		// watchdog_off) and its last row would otherwise sit here answering
		// "на запасных"/"работает" for up to 30 days after it stopped being
		// true.
		//
		// Tunnels are not judged by the heartbeat: when awg-manager does not
		// answer, the agent sends tunnels=fail and no tunnel rows at all, and
		// the last known tunnels must stay on screen. They are judged by the
		// inventory instead -- an OK "tunnels" row lists what is on the router
		// in that report, so a tunnel_* row strictly older than it did not come
		// with it: the tunnel is gone (deleted, or replaced by the config
		// wizard). Before this, deleted tunnels stayed for 30 days and the
		// screen named a removed tunnel as the egress (vvarg, 15.09.2026).
		var heartbeatTS, inventoryTS time.Time
		haveHeartbeat, haveInventory := false, false
		for _, row := range rows {
			switch row.CheckName {
			case "agent_heartbeat":
				heartbeatTS, haveHeartbeat = row.TS, true
			case miniappTunnelsInventoryCheck:
				if row.Status == "ok" {
					inventoryTS, haveInventory = row.TS, true
				}
			}
		}
		resp := miniappRouterEventsResp{Tunnels: []miniappTunnel{}}
		byCheck := make(map[string]db.EventRow, len(rows))
		for _, row := range rows {
			if row.CheckName == resolverGuardCheck && haveHeartbeat && row.TS.Before(heartbeatTS) {
				continue
			}
			if haveInventory && strings.HasPrefix(row.CheckName, miniappTunnelPrefix) && row.TS.Before(inventoryTS) {
				continue
			}
			byCheck[row.CheckName] = row
			resp.Checks = append(resp.Checks, miniappCheckStatus{
				CheckName: row.CheckName,
				Status:    row.Status,
				Timestamp: row.TS.UTC().Format(time.RFC3339),
				Facts:     miniappCheckFactsFrom(row.CheckName, row.DetailsJSON),
				Details:   miniappCheckDetailsFrom(row.CheckName, row.DetailsJSON),
			})
			if tu, ok := miniappTunnelFromEvent(row); ok {
				resp.Tunnels = append(resp.Tunnels, tu)
			}
		}
		sort.Slice(resp.Tunnels, func(i, j int) bool { return resp.Tunnels[i].TunnelID < resp.Tunnels[j].TunnelID })
		resp.Traffic = miniappDeriveTraffic(resp.Tunnels, byCheck)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// miniappTimelineEvent is one row of the router's timeline: what changed and
// when. Deliberately NOT the same shape as miniappTransition (the per-incident
// history): that one answers "how did THIS check flap", this one answers "what
// happened on the router", and squeezing both into one type would force every
// future reader to work out which question they are looking at.
type miniappTimelineEvent struct {
	CheckName string `json:"check_name"`
	Status    string `json:"status"`
	Timestamp string `json:"ts"`
}

// miniappTimelineIncident -- поломка глазами человека: что не работало, когда
// и сколько. Имя не пересекается с miniappIncident из miniapp_actions.go: тот
// про горящий инцидент и его заглушки, этот -- про прошедшую поломку в ленте.
// Сырые события остаются под ?raw=1: они нужны тому, кто полез разбираться, а
// не тому, кто спросил «что было».
type miniappTimelineIncident struct {
	CheckName string `json:"check_name"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	DownSec   int    `json:"down_sec"`
	Flaps     int    `json:"flaps"`
	Ongoing   bool   `json:"ongoing"`
}

type miniappTimelineResp struct {
	// Оба массива едут всегда и пустыми, а не отсутствующими: пропущенный
	// ключ приезжает на клиент как null, и экран, ждущий список, ломается на
	// пустой истории -- ровно то, что проверяет TestMiniappTimelineEmptyIsArrayNotNull.
	Incidents []miniappTimelineIncident `json:"incidents"`
	Events    []miniappTimelineEvent    `json:"events"`
	// Days is the window actually applied, not the one asked for -- the client
	// prints it, so a clamped request must not be reported back as honoured.
	Days      int  `json:"days"`
	Truncated bool `json:"truncated"`
}

const (
	miniappTimelineDefaultDays = 7
	miniappTimelineMaxDays     = 30
	// Потолок сырой ленты. При шести проверках в минуту это около полутора
	// часов -- поэтому режим «как есть» обязан называть своё окно словами.
	miniappTimelineMaxRows = 500
	// Сколько строк читаем, чтобы свернуть неделю. Порядка суток флаппинга
	// или недели спокойной жизни; упёрлись -- говорим truncated, а не
	// показываем кусок как целое.
	miniappTimelineMaxScanRows = 20000
)

// miniappTimelineDays reads the window from the query string. Anything absent,
// unparseable, or out of range falls back to the default instead of erroring:
// a timeline is a read-only screen, and refusing to draw it over a bad query
// parameter would be a worse answer than drawing the usual week.
func miniappTimelineDays(raw string) int {
	if raw == "" {
		return miniappTimelineDefaultDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return miniappTimelineDefaultDays
	}
	if n > miniappTimelineMaxDays {
		return miniappTimelineMaxDays
	}
	return n
}

func miniappRouterTimelineHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		days := miniappTimelineDays(r.URL.Query().Get("days"))
		since := time.Now().UTC().AddDate(0, 0, -days)
		// Сырая лента отдаётся только по явной просьбе: она отвечает на
		// вопрос «какая проверка моргнула», а экран спрашивает «что было».
		raw := r.URL.Query().Get("raw") == "1"
		limit := miniappTimelineMaxScanRows
		if raw {
			limit = miniappTimelineMaxRows
		}
		// Запрашиваем на одну строку больше предела: только так видно, что
		// строки кончились не потому, что событий больше нет.
		rows, err := d.DB.Events().ListAllSince(routerID, since, limit+1)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "timeline lookup failed")
			return
		}
		resp := miniappTimelineResp{
			Incidents: []miniappTimelineIncident{},
			Events:    []miniappTimelineEvent{},
			Days:      days,
		}
		if len(rows) > limit {
			rows = rows[:limit]
			resp.Truncated = true
		}
		if raw {
			for _, row := range rows {
				resp.Events = append(resp.Events, miniappTimelineEvent{
					CheckName: row.CheckName,
					Status:    row.Status,
					Timestamp: row.TS.UTC().Format(time.RFC3339),
				})
			}
		} else {
			for _, inc := range timeline.Fold(rows, time.Now().UTC()) {
				out := miniappTimelineIncident{
					CheckName: inc.CheckName,
					From:      inc.From.UTC().Format(time.RFC3339),
					DownSec:   inc.DownSec,
					Flaps:     inc.Flaps,
					Ongoing:   inc.Ongoing,
				}
				// Конец, которого ещё не было, не записывается временем.
				if !inc.To.IsZero() {
					out.To = inc.To.UTC().Format(time.RFC3339)
				}
				resp.Incidents = append(resp.Incidents, out)
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func parseMiniappRouterID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func miniappRouterAllowed(d Deps, telegramUserID, routerID int64) bool {
	if miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
		return true
	}
	role, err := d.DB.RouterAccessRole(routerID, telegramUserID)
	return err == nil && role != ""
}
