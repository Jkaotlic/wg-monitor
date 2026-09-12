package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
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
	// Версии из снимка (router_versions), а не из горячих событий: снимок
	// переживает рестарт бэкенда, а кэш версий не переживал.
	AwgmgrVersion   string `json:"awgmgr_version,omitempty"`
	FirmwareCurrent string `json:"firmware_current,omitempty"`
	// UpdateHint -- готовая фраза «пора обновить: …». Считает бэкенд, потому
	// что сравнение версий у прошивки Keenetic не semver, и второй его копии
	// в клиенте быть не должно.
	UpdateHint string `json:"update_hint,omitempty"`
}

type miniappFleetUnreachable struct {
	TelegramUserID int64  `json:"telegram_user_id"`
	LastError      string `json:"last_error,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

type miniappFleetNotify struct {
	Unreachable              []miniappFleetUnreachable `json:"unreachable"`
	RoutersWithoutRecipients []string                  `json:"routers_without_recipients"`
}

type miniappFleetWatchdog struct {
	Alive                  bool   `json:"alive"`
	Reason                 string `json:"reason,omitempty"`
	LastScanAt             string `json:"last_scan_at,omitempty"`
	OfflineErrors          int64  `json:"offline_errors"`
	LastOfflineError       string `json:"last_offline_error,omitempty"`
	LastOfflineErrorRouter string `json:"last_offline_error_router,omitempty"`
	LastOfflineErrorAt     string `json:"last_offline_error_at,omitempty"`
}

type miniappFleetResp struct {
	GeneratedAt string                `json:"generated_at"`
	Totals      miniappFleetTotals    `json:"totals"`
	Backend     miniappFleetBackend   `json:"backend"`
	Routers     []miniappFleetRouter  `json:"routers"`
	Notify      miniappFleetNotify    `json:"notify"`
	Watchdog    *miniappFleetWatchdog `json:"watchdog,omitempty"`
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

		// Пустой список, а не nil: клиент перебирает это поле, и null уронил
		// бы экран в тот момент, когда в парке пока ни одного роутера.
		resp := miniappFleetResp{
			GeneratedAt: now.Format(time.RFC3339),
			Routers:     []miniappFleetRouter{},
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
				LastError:      t.LastError,
				UpdatedAt:      t.UpdatedAt,
			})
		}

		if d.HeartbeatStats != nil {
			st := d.HeartbeatStats()
			verdict := heartbeat.Judge(st, now)
			wd := &miniappFleetWatchdog{
				Alive:         verdict.Alive,
				Reason:        verdict.Reason,
				OfflineErrors: st.OfflineErrors,
			}
			if !st.LastScanAt.IsZero() {
				wd.LastScanAt = st.LastScanAt.UTC().Format(time.RFC3339)
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
