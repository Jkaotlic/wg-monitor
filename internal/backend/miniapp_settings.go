package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// miniappSettingsResp — то, по каким правилам бот судит об этом роутере.
//
// Экран настроек показывает их числами, и числа обязаны быть ЖИВЫМИ: в макете
// пороги стояли «60/120 сек» от руки, а на домашнем бэкенде это 120/180/3600.
// Нарисованная настройка, которой нет, хуже отсутствующей строки: по ней
// человек считает, через сколько придёт тревога.
//
// Здесь только то, что живёт в backend.yaml и больше нигде. Версии панели и
// прошивки экран берёт из фактов проверок (/events), и дублировать их сюда
// значило бы завести второй источник правды об одном.
type miniappSettingsResp struct {
	// SilenceAfterSec — сколько секунд без отчёта делают роутер «молчащим».
	// Значение уже выбрано по типу роутера: у мобильного оно другое, и
	// показывать оба, заставляя человека угадывать своё, незачем.
	SilenceAfterSec int `json:"silence_after_sec"`
	// OfflineAfterSec заполняется только у мобильных: у статичного «молчит» и
	// «выключен» -- одно и то же событие, и второе число там было бы копией.
	OfflineAfterSec  int    `json:"offline_after_sec,omitempty"`
	AlertAfterFails  int    `json:"alert_after_fails"`
	RecoveryAfterOKs int    `json:"recovery_after_oks"`
	AgentVersion     string `json:"agent_version,omitempty"`
	Mobile           bool   `json:"mobile,omitempty"`
}

func miniappRouterSettingsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		u, err := d.DB.Users().GetByID(routerID)
		if errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
			return
		}
		policy := dashboardStatusPolicyFromDeps(d)
		resp := miniappSettingsResp{
			AlertAfterFails:  d.Thresholds.Fail,
			RecoveryAfterOKs: d.Thresholds.Recovery,
			Mobile:           u.IsMobile(),
		}
		if u.IsMobile() {
			resp.SilenceAfterSec = int(policy.MobileStaleAfter / time.Second)
			resp.OfflineAfterSec = int(policy.MobileOfflineAfter / time.Second)
			// У мобильного порог тревоги свой: 4G-линия моргает сама по себе,
			// и общий счёт провалов дал бы тревогу на каждом переезде.
			if d.MobileFailThreshold > 0 {
				resp.AlertAfterFails = d.MobileFailThreshold
			}
		} else {
			resp.SilenceAfterSec = int(policy.StaticStaleAfter / time.Second)
		}
		if u.LastDeployedVersion != nil {
			resp.AgentVersion = *u.LastDeployedVersion
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
