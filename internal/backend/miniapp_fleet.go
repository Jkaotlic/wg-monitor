package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
)

// Админский экран парка в мини-аппе.
//
// Источник -- ПРОЕКЦИЯ дашбордной сводки (buildDashboardSummary), а не второй
// сборщик: иначе парк в браузере и парк в приложении разошлись бы молча, и
// заметили бы это в тот день, когда по одному из них принимают решение.
//
// Проекция поимённая, а не «скопировать структуру и что-нибудь убрать».
// Соседний срез /v1/dashboard/summary отдаёт ssh-доступ, креды панели и чат
// уведомлений; копия его формы протащила бы всё это в приложение, у которого
// другой круг читателей. Поэтому здесь перечислены только те поля, которые
// экрану нужны, и каждое новое поле придётся дописать руками.

type miniappFleetTotals struct {
	Routers        int `json:"routers"`
	Online         int `json:"online"`
	Sleeping       int `json:"sleeping"`
	Offline        int `json:"offline"`
	Alerts         int `json:"alerts"`
	PendingDeploys int `json:"pending_deploys"`
}

type miniappFleetBackend struct {
	Version         string `json:"version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
}

// miniappFleetRevive -- оживление агента в строке парка. Поимённая проекция
// revive.IntentView: паролей там нет, и каждое новое поле придётся дописать
// руками. Тексты причины и опроса сервис части 1 уже отдаёт по-русски.
type miniappFleetRevive struct {
	Status        string `json:"status"`
	ExpiresAt     string `json:"expires_at"`
	Attempts      int    `json:"attempts"`
	LastErrorText string `json:"last_error_text"`
	LastProbeText string `json:"last_probe_text"`
	// Пустая строка -- опроса ещё не было. Экран считает «N мин назад» от
	// generated_at этого же ответа, а не от своих часов.
	LastProbeAt string `json:"last_probe_at"`
	// Auto -- поставлено авто-проходом по сохранённому паролю (v0.45), а не
	// админом. Отмена та же.
	Auto bool `json:"auto"`
}

// miniappFleetLastDeploy -- последняя раскатка агента. At -- users.last_deploy:
// его пишет только консольный deploy, раскатки из приложения и дашборда его не
// двигают. Version -- версия агента из последнего отчёта, а не версия той
// раскатки. OK -- НЕ «раскатка удалась»: false значит только, что у ждущего
// обновления записана ошибка последней попытки (users.pending_last_error);
// true -- такой ошибки нет, в том числе когда ничего и не ждёт. Никаких
// адресов и доступов.
type miniappFleetLastDeploy struct {
	Version string `json:"version"`
	At      string `json:"at"`
	OK      bool   `json:"ok"`
}

// miniappFleetIncident -- свёртка активных тревог роутера для строки парка:
// с какого времени (самая ранняя) и сколько раз подряд (наибольшее).
type miniappFleetIncident struct {
	HardSince string `json:"hard_since"`
	FailCount int    `json:"fail_count"`
}

// miniappFleetRouter -- одна строка парка. Ни ssh, ни адреса панели, ни
// чата уведомлений здесь нет и быть не может.
type miniappFleetRouter struct {
	ID             int64    `json:"id"`
	Nickname       string   `json:"nickname"`
	Status         string   `json:"status"`
	LastSeenAgeSec *int64   `json:"last_seen_age_sec,omitempty"`
	Incidents      []string `json:"incidents,omitempty"`
	AgentVersion   string   `json:"agent_version,omitempty"`
	PendingVersion string   `json:"pending_version,omitempty"`
	// PendingStale -- назначенная версия старше версии бэкенда: «Обновить всех
	// отставших» её вытеснит (agentDeployCore). Считает сервер, не клиент.
	PendingStale bool `json:"pending_stale,omitempty"`
	// Версии из снимка (router_versions), а не из горячих событий: снимок
	// переживает рестарт бэкенда, а кэш версий не переживал.
	AwgmgrVersion   string `json:"awgmgr_version,omitempty"`
	FirmwareCurrent string `json:"firmware_current,omitempty"`
	// UpdateHint -- готовая фраза «пора обновить: …». Считает бэкенд, потому
	// что сравнение версий у прошивки Keenetic не semver, и второй его копии
	// в клиенте быть не должно.
	UpdateHint string `json:"update_hint,omitempty"`
	// Состояние обновления агента. Причина неудачи -- уже по-русски
	// (deployFailureText): сырой вывод агента в приложение не уезжает.
	// Без omitempty: форма строки постоянная, клиент не гадает об отсутствии.
	PendingAttempts      int    `json:"pending_attempts"`
	PendingLastErrorText string `json:"pending_last_error_text"`
	AgentBehind          bool   `json:"agent_behind"`
	AgentUpdateWarning   string `json:"agent_update_warning"`
	// NotifyMuted -- вызвавший админ выключил уведомления по этому роутеру.
	// Без omitempty: переключателю нужно явное false. Роутер при этом в парке
	// остаётся -- доступ к экранам от выключения не зависит.
	NotifyMuted bool `json:"notify_muted"`
	// Away -- роутер не на связи для команд и обновления: то же правило, по
	// которому сервер откладывает обновление (miniappWakeWindow -- статус без
	// инцидентов). Считает сервер, чтобы лист, итог и строка парка не
	// расходились с решением «отложено» (final review M1). Без omitempty.
	Away bool `json:"away"`
	// PanelAddressKnown -- у роутера записан годный адрес панели (тот же
	// panelAddress, что у ссылки panel_url). Сводке парка самого адреса не
	// нужно (TestMiniappFleetNeverLeaksRouterSecrets): листу оживления надо
	// только решить, спрашивать ли его (bronya).
	PanelAddressKnown bool `json:"panel_address_known"`
	// Revive -- оживление агента; null, когда его не ставили. Без omitempty.
	Revive *miniappFleetRevive `json:"revive"`
	// RootPasswordSaved -- для роутера сохранён пароль root (v0.45). Только
	// признак: ни пароля, ни шифртекста, ни их полей в ответе нет и быть не
	// может (TestStoredRouterCredentialsNeverLeak).
	RootPasswordSaved bool `json:"root_password_saved"`
	// AutoReviveBlocked -- роутер «давно не обновлялся» (agentLongNotUpdated),
	// но авто-оживление не начнётся: чего не хватает, по-русски. Пусто --
	// либо не нужно, либо ничто не мешает, либо оживление уже поставлено.
	AutoReviveBlocked string `json:"auto_revive_blocked"`
	// PendingSince -- когда назначено ждущее обновление; null -- не ждёт.
	PendingSince *string `json:"pending_since"`
	// LastDeploy -- null, если раскатки не было. Без omitempty: форма строки
	// постоянная.
	LastDeploy *miniappFleetLastDeploy `json:"last_deploy"`
	// Incident -- null без активных тревог.
	Incident *miniappFleetIncident `json:"incident"`
}

// miniappFleetUnreachable -- человек, которому бот не может написать.
//
// Текста ошибки Telegram здесь нет намеренно: «Forbidden: bot was blocked by
// the user» -- машинная строка по-английски, а экрану нужно последствие
// («не получит тревогу, пока сам не напишет боту»). Поэтому форма отличается
// от дашбордной dashboardUnreachable, и это не случайность: совпади они
// полем в поле -- следующее добавленное туда поле уехало бы в приложение
// само собой.
type miniappFleetUnreachable struct {
	TelegramUserID int64  `json:"telegram_user_id"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

type miniappFleetNotify struct {
	Unreachable              []miniappFleetUnreachable `json:"unreachable"`
	RoutersWithoutRecipients []string                  `json:"routers_without_recipients"`
}

type miniappFleetWatchdog struct {
	Alive           bool   `json:"alive"`
	Reason          string `json:"reason,omitempty"`
	ScansTotal      int64  `json:"scans_total"`
	StaleUsers      int64  `json:"stale_users"`
	SuppressedUsers int64  `json:"suppressed_users"`
	LastScanMs      int64  `json:"last_scan_ms"`
	// LastScanAt -- когда был последний обход (RFC3339); null -- обхода ещё
	// не было. Ключ есть всегда: клиент не гадает об отсутствии.
	LastScanAt             *string `json:"last_scan_at"`
	OfflineErrors          int64   `json:"offline_errors"`
	LastOfflineError       string  `json:"last_offline_error,omitempty"`
	LastOfflineErrorRouter string  `json:"last_offline_error_router,omitempty"`
	LastOfflineErrorAt     string  `json:"last_offline_error_at,omitempty"`
}

type miniappFleetResp struct {
	GeneratedAt string                `json:"generated_at"`
	Totals      miniappFleetTotals    `json:"totals"`
	Backend     miniappFleetBackend   `json:"backend"`
	Routers     []miniappFleetRouter  `json:"routers"`
	Notify      miniappFleetNotify    `json:"notify"`
	Watchdog    *miniappFleetWatchdog `json:"watchdog,omitempty"`
	// ReviveEnabled -- сервер умеет оживлять (есть ключ). false -- экран
	// говорит «не настроено» и кнопку не показывает.
	ReviveEnabled bool `json:"revive_enabled"`
}

// miniappFleetHandler -- GET /v1/miniapp/fleet. Только админу: радиус у
// экрана парковый, и отказ 404, как на всех админских поверхностях
// мини-аппа.
func miniappFleetHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		if !miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		if d.DB == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "db_not_configured", "db not configured")
			return
		}
		now := time.Now().UTC()
		summary, err := buildDashboardSummary(d.DB, now, dashboardStatusPolicyFromDeps(d))
		if err != nil {
			if d.Logger != nil {
				d.Logger.Error("сводка парка не собралась", "err", err)
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "не удалось собрать сводку парка")
			return
		}
		versions, err := d.DB.RouterVersions().All()
		if err != nil {
			// Снимок версий -- добавка к строке, а не сама строка: без него
			// экран обязан открыться и сказать про состояние.
			if d.Logger != nil {
				d.Logger.Warn("сводка парка: снимок версий не прочитан", "err", err)
			}
			versions = nil
		}
		pending, err := d.DB.Users().PendingDeployStates()
		if err != nil {
			// Как и снимок версий -- добавка к строке: экран обязан открыться.
			if d.Logger != nil {
				d.Logger.Warn("сводка парка: состояние обновлений не прочитано", "err", err)
			}
			pending = nil
		}
		mutedByAdmin, err := d.DB.NotifyMutes().MutedRoutersOf(telegramUserID)
		if err != nil {
			// Как со снимком версий: экран обязан открыться. Переключатели
			// покажут «включено», в журнале -- почему.
			if d.Logger != nil {
				d.Logger.Warn("сводка парка: выключатели уведомлений не прочитаны", "err", err)
			}
			mutedByAdmin = nil
		}

		// Строки users для правила «не на связи»: сводка отдаёт статус С
		// инцидентами, а отложенное обновление решается статусом БЕЗ них.
		users, err := d.DB.Users().GetAll()
		if err != nil {
			// Добавка к строке: без неё away берётся из статуса сводки.
			if d.Logger != nil {
				d.Logger.Warn("сводка парка: роутеры для признака «не на связи» не прочитаны", "err", err)
			}
			users = nil
		}
		savedCreds, err := d.DB.RouterCredentials().SavedAt()
		if err != nil {
			// Добавка к строке: без неё признак «пароль сохранён» -- false.
			if d.Logger != nil {
				d.Logger.Warn("сводка парка: сохранённые пароли не прочитаны", "err", err)
			}
			savedCreds = nil
		}
		usersByID := make(map[int64]int, len(users))
		for i := range users {
			usersByID[users[i].ID] = i
		}

		// Пустой список, а не nil: клиент перебирает это поле, и null уронил
		// бы экран в тот момент, когда в парке пока ни одного роутера.
		reviveSvc := miniappRevive(d)
		resp := miniappFleetResp{
			GeneratedAt:   now.Format(time.RFC3339),
			ReviveEnabled: miniappReviveEnabled(d),
			Routers:       []miniappFleetRouter{},
			Totals: miniappFleetTotals{
				Routers:        summary.Totals.Agents,
				Online:         summary.Totals.Online,
				Sleeping:       summary.Totals.Sleeping,
				Offline:        summary.Totals.Offline,
				Alerts:         summary.Totals.Alerts,
				PendingDeploys: summary.Totals.PendingDeploys,
			},
		}
		for _, a := range summary.Agents {
			row := miniappFleetRouter{
				ID:             a.ID,
				Nickname:       a.Nickname,
				Status:         a.Status,
				LastSeenAgeSec: a.LastSeenAgeSec,
				AgentVersion:   a.AgentVersion,
				PendingVersion: a.PendingVersion,
				NotifyMuted:    mutedByAdmin[a.ID],
				Away:           a.Status == "sleeping" || a.Status == "offline",
			}
			_, row.RootPasswordSaved = savedCreds[a.ID]
			if i, ok := usersByID[a.ID]; ok {
				row.Away, _, _ = miniappWakeWindow(d, &users[i], "self_update", now)
				_, row.PanelAddressKnown = panelAddress(users[i].AWGMURL)
			}
			for _, inc := range a.ActiveIncidents {
				row.Incidents = append(row.Incidents, inc.CheckName)
			}
			if snap, ok := versions[a.ID]; ok {
				row.AwgmgrVersion = snap.AwgmgrVersion
				row.FirmwareCurrent = snap.FirmwareCurrent
				if upstream.FirmwareNewerThan(snap.FirmwareCurrent, snap.FirmwareAvail) {
					row.UpdateHint = "пора обновить: прошивка " + snap.FirmwareAvail
				}
			}
			verdict := agentUpdateVerdictFor(a.AgentVersion, serverVersion)
			row.AgentBehind = verdict.Behind
			row.AgentUpdateWarning = verdict.Warning
			if st, ok := pending[a.ID]; ok {
				row.PendingAttempts = st.Attempts
				// TooOld тоже держит причину: у него Behind всегда false
				// (агент вообще не умеет self_update, B6), но прошлая
				// попытка (например, до того как агент постарел настолько)
				// не должна пропадать из строки.
				if strings.TrimSpace(st.LastError) != "" && (st.Version != "" || verdict.Behind || verdict.TooOld) {
					row.PendingLastErrorText = deployFailureText(st.LastError)
				}
			}
			row.PendingStale = a.PendingVersion != "" && isVersionDowngrade(a.PendingVersion, serverVersion)
			if a.PendingVersion != "" && strings.TrimSpace(a.PendingSince) != "" {
				since := a.PendingSince
				row.PendingSince = &since
			}
			if at := strings.TrimSpace(a.LastDeploy); at != "" {
				// OK -- только «у ждущего обновления нет ошибки попытки», см. тип.
				ld := &miniappFleetLastDeploy{Version: a.AgentVersion, At: at, OK: true}
				if st, ok := pending[a.ID]; ok && strings.TrimSpace(st.LastError) != "" {
					ld.OK = false
				}
				row.LastDeploy = ld
			}
			row.Incident = miniappFleetIncidentFrom(a.ActiveIncidents)
			if reviveSvc != nil {
				view, err := reviveSvc.StatusFor(a.ID)
				if err != nil {
					// Добавка к строке: экран обязан открыться. Текст ошибки
					// не пишем -- правило файла оживления, только факт.
					if d.Logger != nil {
						d.Logger.Warn("сводка парка: состояние оживления не прочитано", "router_id", a.ID)
					}
				} else if view != nil {
					row.Revive = miniappFleetReviveFrom(view)
				}
			}
			if i, ok := usersByID[a.ID]; ok && resp.ReviveEnabled {
				row.AutoReviveBlocked = autoReviveBlockedText(&users[i], row.RootPasswordSaved, row.Revive, now)
			}
			resp.Routers = append(resp.Routers, row)
		}

		// Строка о бэкенде. Свой дедлайн, как у дашборда: справочное «доступна
		// версия N» не имеет права задерживать ответ о парке.
		latest := ""
		lookupCtx, cancel := context.WithTimeout(r.Context(), dashboardLatestLookupBudget)
		latest, err = lookupDashboardLatestVersion(lookupCtx)
		cancel()
		if err != nil && d.Logger != nil {
			d.Logger.Warn("сводка парка: доступная версия не узнана", "err", err)
		}
		resp.Backend = miniappFleetBackend{
			Version:         serverVersion,
			LatestVersion:   latest,
			UpdateAvailable: upstream.SoftwareNewerThan(serverVersion, latest),
		}

		gaps := buildDashboardNotifyGaps(d)
		resp.Notify = miniappFleetNotify{
			Unreachable:              []miniappFleetUnreachable{},
			RoutersWithoutRecipients: gaps.RoutersWithoutRecipients,
		}
		if resp.Notify.RoutersWithoutRecipients == nil {
			resp.Notify.RoutersWithoutRecipients = []string{}
		}
		for _, t := range gaps.Unreachable {
			resp.Notify.Unreachable = append(resp.Notify.Unreachable, miniappFleetUnreachable{
				TelegramUserID: t.TelegramUserID,
				UpdatedAt:      t.UpdatedAt,
			})
		}

		if d.HeartbeatStats != nil {
			st := d.HeartbeatStats()
			verdict := heartbeat.Judge(st, now)
			wd := &miniappFleetWatchdog{
				Alive:           verdict.Alive,
				Reason:          verdict.Reason,
				OfflineErrors:   st.OfflineErrors,
				ScansTotal:      st.ScansTotal,
				StaleUsers:      st.StaleUsers,
				SuppressedUsers: st.Suppressed,
				LastScanMs:      st.LastScanMs,
			}
			if !st.LastScanAt.IsZero() {
				at := st.LastScanAt.UTC().Format(time.RFC3339)
				wd.LastScanAt = &at
			}
			if st.LastOfflineError != "" {
				wd.LastOfflineError = st.LastOfflineError
				wd.LastOfflineErrorRouter = st.LastOfflineErrorRouter
				wd.LastOfflineErrorAt = st.LastOfflineErrorAt.UTC().Format(time.RFC3339)
			}
			resp.Watchdog = wd
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func miniappFleetReviveFrom(v *revive.IntentView) *miniappFleetRevive {
	out := &miniappFleetRevive{
		Status:        v.Status,
		ExpiresAt:     v.ExpiresAt.UTC().Format(time.RFC3339),
		Attempts:      v.Attempts,
		LastErrorText: v.LastErrorText,
		LastProbeText: v.LastProbeText,
		Auto:          v.Auto,
	}
	if !v.LastProbeAt.IsZero() {
		out.LastProbeAt = v.LastProbeAt.UTC().Format(time.RFC3339)
	}
	return out
}

func miniappFleetIncidentFrom(incidents []dashboardIncident) *miniappFleetIncident {
	if len(incidents) == 0 {
		return nil
	}
	out := &miniappFleetIncident{}
	for _, inc := range incidents {
		// RFC3339 в UTC сравнивается как строка.
		if inc.HardSince != "" && (out.HardSince == "" || inc.HardSince < out.HardSince) {
			out.HardSince = inc.HardSince
		}
		if inc.FailCount > out.FailCount {
			out.FailCount = inc.FailCount
		}
	}
	return out
}

const (
	autoReviveBlockedNoRoot  = "для авто-оживления нужен пароль root"
	autoReviveBlockedNoPanel = "для авто-оживления нужен внешний адрес панели"
)

// autoReviveBlockedText -- почему авто-проход не оживит «давно не
// обновлявшийся» роутер. Те же условия, что у revive.AutoSchedule: пароль
// root сохранён, адрес панели проходит проверку постановки. Идущее или
// ждущее оживление -- не жалоба: строка оживления и так говорит о нём.
func autoReviveBlockedText(u *db.User, passwordSaved bool, rv *miniappFleetRevive, now time.Time) string {
	if !agentLongNotUpdated(u, serverVersion, now) {
		return ""
	}
	if rv != nil && (rv.Status == revive.StatusWaiting || rv.Status == revive.StatusRunning) {
		return ""
	}
	if !passwordSaved {
		return autoReviveBlockedNoRoot
	}
	if _, ok := revive.NormalizePanelURL(stringValue(u.AWGMURL)); !ok {
		return autoReviveBlockedNoPanel
	}
	return ""
}
