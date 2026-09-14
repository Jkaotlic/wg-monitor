package backend

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Срез версий для экрана: что стоит, что вышло и -- отдельно -- про что мы не
// знаем и почему.
//
// Форма собрана ПОИМЁННО, а не скопирована с операторской сводки
// (/v1/dashboard/summary): та отдаёт по каждому роутеру awgm_url, awgm_auth,
// ssh_host, ssh_user, expected_mac и telegram_chat_id (снято живьём
// 12.09.2026). Среди читателей мини-аппа есть операторы, и копирование чужой
// формы было бы утечкой, а не удобством.

// miniappVersionRow -- одна новость: «вышло X, у вас Y».
//
// Действия рядом с новостью нет ни одного, и это не упущение: точечного
// обновления пакета у агента не существует, валовый opkg_upgrade не отвечает
// на фразу «вышел awg-manager», а кнопка без бэкенда не рисуется.
type miniappVersionRow struct {
	Component string `json:"component"`
	Name      string `json:"name"`
	Installed string `json:"installed,omitempty"`
	Available string `json:"available,omitempty"`
	// Hint -- последствие обновления словами, если оно есть.
	Hint string `json:"hint,omitempty"`
}

// miniappUnknownRow -- «про этот компонент сведений нет, и вот почему».
// Причина всегда из закрытого списка upstream.Reason*: неизвестной причины
// наружу быть не может, иначе экран снова начнёт молчать вместо ответа.
type miniappUnknownRow struct {
	Component string `json:"component"`
	Reason    string `json:"reason"`
}

// miniappInstalledVersions -- что стоит на роутере по последнему снимку.
//
// HrneoInstalled и KmodLoaded -- указатели с omitempty: отсутствие ключа
// означает «опрос не дал ответа». Отдавать здесь false было бы прямым
// враньём владельцу, у которого HydraRoute стоит и работает (разведка
// 12.09.2026: стоит на всех проверенных роутерах).
type miniappInstalledVersions struct {
	Awgmgr         string `json:"awgmgr,omitempty"`
	Hrneo          string `json:"hrneo,omitempty"`
	HrneoInstalled *bool  `json:"hrneo_installed,omitempty"`
	Firmware       string `json:"firmware,omitempty"`
	KeeneticOS     string `json:"keenetic_os,omitempty"`
	Kmod           string `json:"kmod,omitempty"`
	KmodLoaded     *bool  `json:"kmod_loaded,omitempty"`
}

type miniappVersionsResp struct {
	Rows      []miniappVersionRow       `json:"rows"`
	Unknown   []miniappUnknownRow       `json:"unknown"`
	Installed *miniappInstalledVersions `json:"installed,omitempty"`
	// RebootHint -- предупреждение о перезагрузке. Пусто, если повода нет:
	// право сказать «нужна перезагрузка» даёт только наблюдаемая смена модуля
	// ядра между снимками, а не номер версии панели.
	RebootHint string `json:"reboot_hint,omitempty"`
	// CheckedAt -- когда роутер в последний раз рассказал про версии. Без
	// метки времени строка о версиях обещает больше, чем мы знаем; снимка нет
	// вовсе -- ключа нет вовсе.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// miniappUpdateComponents -- закрытый список того, про что бывает новость.
// kmod_reboot -- не версия пакета, а предупреждение о перезагрузке, и
// отложить его человек имеет право так же, как любую другую новость.
var miniappUpdateComponents = map[string]bool{
	"awgmgr":      true,
	"hrneo":       true,
	"firmware":    true,
	"kmod_reboot": true,
}

// miniappSnoozeFor -- «Отложить на неделю».
const miniappSnoozeFor = 7 * 24 * time.Hour

// newsKey -- ключ новости: компонент И версия, про которую шла речь.
//
// Одним компонентом ключевать нельзя. ListFor отдаёт ВСЕ несокрытые строки
// этого компонента, а прошлая, никем не скрытая новость живёт в базе до смены
// версии. При ключе из одного компонента она перетирала запись о новой, и
// сравнение на равенство не совпадало: вышедшее обновление не рисовалось
// вовсе -- экран говорил «обновлений нет» при доступной прошивке.
//
// Нумерация делает это не краевым случаем, а обычным: строковое сравнение
// ставит «5.02.A.9.0-0» ПОСЛЕ «5.02.A.10.0-0», а у панели «2.9.x» после
// «2.19.x». То есть заслонка появлялась на первом же переходе через десяток.
//
// \x00 в разделителе -- чтобы склейка была однозначной: в компонентах и
// версиях нулевого байта не бывает.
func newsKey(component, version string) string { return component + "\x00" + version }

// VersionAuditFromSnapshot восстанавливает форму ответа агента из снимка базы.
//
// Так сравнение обновлений идёт через ОДИН ComputeUpdates и для свежего ответа
// роутера, и для снимка: второй сравниватель рядом разъехался бы с первым, и
// панель в боте начала бы показывать не то, что экран в приложении.
//
// Экспортирована ради умного ответа бота (package callbacks): его блок
// обновлений после рестарта бэкенда пустел, потому что читал только кэш в
// памяти, и ему нужен тот же переход «снимок -> сравнение».
func VersionAuditFromSnapshot(row db.RouterVersionRow) wire.VersionAudit {
	return wire.VersionAudit{
		AwgmgrVersion:   row.AwgmgrVersion,
		AwgmgrBackend:   row.AwgmgrBackend,
		HrneoVersion:    row.HrneoVersion,
		HrneoInstalled:  row.HrneoInstalled,
		FirmwareCurrent: row.FirmwareCurrent,
		FirmwareAvail:   row.FirmwareAvail,
		KmodVersion:     row.KmodVersion,
		KmodModel:       row.KmodModel,
		KmodLoaded:      row.KmodLoaded,
	}
}

// miniappRouterVersionsHandler отдаёт экрану снимок версий, новости и причины
// незнания.
//
// Читают владелец, оператор и админ. Новости видят все трое: с цикла 1
// прошивку ставят и операторы.
func miniappRouterVersionsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		row, err := d.DB.RouterVersions().Get(routerID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "versions lookup failed")
			return
		}
		resp := miniappVersionsBody(r, d, routerID, row, time.Now().UTC())

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// miniappVersionsBody собирает ответ и заодно ведёт состояние новостей.
//
// Порядок важен: сначала заводим новость на каждый выпуск (Ensure), потом
// спрашиваем, какие из них экран имеет право показать (ListFor), и только
// показанные помечаем показанными. Иначе «отложить» отменялось бы самим
// открытием экрана.
func miniappVersionsBody(r *http.Request, d Deps, routerID int64, row db.RouterVersionRow, now time.Time) miniappVersionsResp {
	updates, unknown := upstream.ComputeUpdates(r.Context(), d.Upstream, VersionAuditFromSnapshot(row))
	rebootHint := upstream.RebootHint(row.PrevKmodVersion, row.KmodVersion)

	reminders := d.DB.UpdateReminders()
	for _, u := range updates {
		if err := reminders.Ensure(routerID, u.Component, u.Available); err != nil && d.Logger != nil {
			d.Logger.Warn("miniapp: update reminder ensure failed", "router_id", routerID, "err", err)
		}
	}
	if rebootHint != "" {
		if err := reminders.Ensure(routerID, "kmod_reboot", row.KmodVersion); err != nil && d.Logger != nil {
			d.Logger.Warn("miniapp: reboot reminder ensure failed", "router_id", routerID, "err", err)
		}
	}

	// Что экран имеет право показать: отложенное и скрытое сюда не попадает.
	//
	// Множество по паре «компонент + версия», а не карта по компоненту:
	// состояние «скрыто/отложено» относится к ТОЙ новости, о которой шла речь,
	// и прошлая строка не имеет права заслонять новую (см. newsKey).
	visible := make(map[string]bool)
	if list, err := reminders.ListFor(routerID, now); err != nil {
		if d.Logger != nil {
			d.Logger.Warn("miniapp: update reminders list failed", "router_id", routerID, "err", err)
		}
	} else {
		for _, rem := range list {
			visible[newsKey(rem.Component, rem.Version)] = true
		}
	}

	// Пустые слайсы, а не nil: клиент делает .map по этим полям, и null уронил
	// бы экран роутера, про который новостей нет.
	resp := miniappVersionsResp{
		Rows:    []miniappVersionRow{},
		Unknown: []miniappUnknownRow{},
	}
	for _, u := range updates {
		if !visible[newsKey(u.Component, u.Available)] {
			continue
		}
		resp.Rows = append(resp.Rows, miniappVersionRow{
			Component: u.Component,
			Name:      u.Name,
			Installed: u.Installed,
			Available: u.Available,
			Hint:      u.Hint,
		})
		if err := reminders.MarkShown(routerID, u.Component, u.Available); err != nil && d.Logger != nil {
			d.Logger.Warn("miniapp: update reminder mark shown failed", "router_id", routerID, "err", err)
		}
	}
	for _, u := range unknown {
		resp.Unknown = append(resp.Unknown, miniappUnknownRow{Component: u.Component, Reason: u.Reason})
	}

	if rebootHint != "" && visible[newsKey("kmod_reboot", row.KmodVersion)] {
		resp.RebootHint = rebootHint
		if err := reminders.MarkShown(routerID, "kmod_reboot", row.KmodVersion); err != nil && d.Logger != nil {
			d.Logger.Warn("miniapp: reboot reminder mark shown failed", "router_id", routerID, "err", err)
		}
	}

	// Снимка нет вовсе -- ни версий, ни метки времени. Это нормальное
	// состояние (гейт свежести отчёта мог ни разу не пропустить запись), и
	// звучать оно обязано как «роутер ещё не рассказал», а не как пустые
	// версии со свежей датой.
	if !row.UpdatedAt.IsZero() {
		checked := row.UpdatedAt
		resp.CheckedAt = &checked
		resp.Installed = &miniappInstalledVersions{
			Awgmgr:         row.AwgmgrVersion,
			Hrneo:          row.HrneoVersion,
			HrneoInstalled: row.HrneoInstalled,
			Firmware:       row.FirmwareCurrent,
			KeeneticOS:     row.KeeneticOS,
			Kmod:           row.KmodVersion,
			KmodLoaded:     row.KmodLoaded,
		}
	}
	return resp
}

// miniappUpdateReminderHandler прячет новость: «отложить на неделю» или
// «скрыть эту новость».
//
// Нажимают владелец и админ, оператор получает 404. Причина в том, что строка
// новости живёт на РОУТЕРЕ, а не у человека: скрыв её, оператор убрал бы
// новость с экрана владельца тоже. Личных выключателей у новостей нет вовсе --
// им незачем затухание, потому что экран никого не будит.
func miniappUpdateReminderHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappIsOwner(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		component := r.PathValue("component")
		if !miniappUpdateComponents[component] {
			writeJSONError(w, http.StatusBadRequest, "bad_component", "unknown component")
			return
		}
		var body struct {
			Action string     `json:"action"`
			Until  *time.Time `json:"until,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
		if body.Action != "snooze" && body.Action != "dismiss" {
			writeJSONError(w, http.StatusBadRequest, "bad_action", "action must be snooze or dismiss")
			return
		}

		row, err := d.DB.RouterVersions().Get(routerID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "versions lookup failed")
			return
		}
		version := miniappNewsVersion(r, d, component, row)
		if version == "" {
			// Новости про этот компонент сейчас нет -- прятать нечего.
			writeJSONError(w, http.StatusNotFound, "not_found", "no news for this component")
			return
		}

		now := time.Now().UTC()
		reminders := d.DB.UpdateReminders()
		if body.Action == "snooze" {
			until := now.Add(miniappSnoozeFor)
			if body.Until != nil {
				until = body.Until.UTC()
			}
			err = reminders.Snooze(routerID, component, version, until)
		} else {
			err = reminders.Dismiss(routerID, component, version)
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "reminder update failed")
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			OK bool `json:"ok"`
		}{OK: true})
	}
}

// miniappNewsVersion -- о какой версии сейчас новость по этому компоненту.
//
// Версию считает сервер, а не присылает клиент: иначе «отложить» могло бы
// попасть в выпуск, которого человек не видел, и новость о настоящем
// обновлении молча исчезла бы.
func miniappNewsVersion(r *http.Request, d Deps, component string, row db.RouterVersionRow) string {
	if component == "kmod_reboot" {
		if upstream.RebootHint(row.PrevKmodVersion, row.KmodVersion) == "" {
			return ""
		}
		return row.KmodVersion
	}
	updates, _ := upstream.ComputeUpdates(r.Context(), d.Upstream, VersionAuditFromSnapshot(row))
	for _, u := range updates {
		if u.Component == component {
			return u.Available
		}
	}
	return ""
}
