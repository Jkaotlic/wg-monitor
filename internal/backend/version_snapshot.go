package backend

import (
	"encoding/json"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// awgManagerCheckDetails — те поля details проверки awg_manager, из которых
// собирается снимок версий. Отчёт приходит каждые полторы минуты и приносит
// версии бесплатно: доспрашивать роутер ради них не нужно, а переживают
// рестарт бэкенда они только в базе.
//
// Свой разбор, а не общий с miniappCheckFacts: тот — БЕЛЫЙ СПИСОК того, что
// можно показать оператору мини-аппа, и расширять его ради нужд базы значило
// бы протащить наружу лишнее. Здесь же читается только то, что ложится в
// снимок.
//
// KmodLoaded — указатель: старый агент поля не присылает, и его молчание
// обязано остаться «неизвестно», а не превратиться в «не загружен».
type awgManagerCheckDetails struct {
	Version       string `json:"version"`
	Firmware      string `json:"firmware"`
	KeeneticOS    string `json:"keenetic_os"`
	ActiveBackend string `json:"active_backend"`
	KmodVersion   string `json:"kernel_module_version"`
	KmodModel     string `json:"kernel_module_model"`
	KmodLoaded    *bool  `json:"kernel_module_loaded"`
}

// versionSnapshotFromReport собирает снимок из details проверки awg_manager.
//
// Второй результат — «есть ли что писать». Упавшая проверка кладёт в details
// один адрес панели, версий там нет вовсе, и такой отчёт не имеет права
// стереть то, что мы уже знали: пустой снимок ушёл бы в базу перезаписью
// пустым и обнулил бы историю.
func versionSnapshotFromReport(detailsJSON string) (db.RouterVersionSnapshot, bool) {
	if strings.TrimSpace(detailsJSON) == "" {
		return db.RouterVersionSnapshot{}, false
	}
	var d awgManagerCheckDetails
	if err := json.Unmarshal([]byte(detailsJSON), &d); err != nil {
		return db.RouterVersionSnapshot{}, false
	}
	if d.Version == "" && d.Firmware == "" && d.KeeneticOS == "" && d.KmodVersion == "" {
		return db.RouterVersionSnapshot{}, false
	}
	return db.RouterVersionSnapshot{
		AwgmgrVersion:   d.Version,
		AwgmgrBackend:   d.ActiveBackend,
		FirmwareCurrent: d.Firmware,
		KeeneticOS:      d.KeeneticOS,
		KmodVersion:     d.KmodVersion,
		KmodModel:       d.KmodModel,
		KmodLoaded:      d.KmodLoaded,
		Source:          "report",
	}, true
}

// VersionSnapshotFromAudit переводит ответ version_audit в снимок для базы.
//
// HrneoInstalled и KmodLoaded приходят указателями уже от агента и уезжают в
// базу КАК ЕСТЬ. Придумывать за них значение нельзя: nil означает, что опрос не
// дал ответа, и слияние в Upsert пропускает такое поле мимо, сохраняя известное.
// Пока здесь стоял `&hrneoInstalled`, снятый с обычного bool, один неудачный
// опрос HydraRoute затирал ранее известное «установлен» на «не установлен» --
// это была порча накопленных данных, а не кривая надпись.
//
// Полей, которых version_audit не знает (KeeneticOS), мы не выдумываем: пустая
// строка означает «этот источник такого не приносит», и Upsert оставит на месте
// то, что уже принёс отчёт.
//
// Экспортирована потому, что вызывающих ровно два и они в разных пакетах: приём
// результата команды (здесь, package backend) и отрисовка панели обслуживания
// (package callbacks). Две копии одного отображения разъехались бы молча -- а
// разъехавшись, дали бы разные снимки на разных путях об одном роутере.
func VersionSnapshotFromAudit(va wire.VersionAudit) db.RouterVersionSnapshot {
	return db.RouterVersionSnapshot{
		AwgmgrVersion:   va.AwgmgrVersion,
		AwgmgrBackend:   va.AwgmgrBackend,
		HrneoVersion:    va.HrneoVersion,
		HrneoInstalled:  va.HrneoInstalled,
		FirmwareCurrent: va.FirmwareCurrent,
		FirmwareAvail:   va.FirmwareAvail,
		KmodVersion:     va.KmodVersion,
		KmodModel:       va.KmodModel,
		KmodLoaded:      va.KmodLoaded,
		Source:          "version_audit",
	}
}
