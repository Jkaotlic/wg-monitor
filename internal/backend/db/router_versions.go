package db

import (
	"database/sql"
	"errors"
	"time"
)

// RouterVersionSnapshot -- что источник узнал о версиях роутера прямо сейчас.
//
// Пустая строка означает «поле в этом источнике не приезжает», а не «поле
// пустое»: отчёт агента знает awg-manager, прошивку, KeeneticOS и модуль ядра,
// а про HydraRoute Neo и доступную прошивку знает только version_audit.
// Поэтому Upsert смешивает источники ДОПОЛНЕНИЕМ.
//
// KmodLoaded и HrneoInstalled -- указатели: nil = «агент не сказал», и это
// другой ответ, чем false. Старый агент про модуль ядра молчит, и выдавать
// его молчание за «не загружен» значило бы пугать владельца выдуманной
// поломкой.
type RouterVersionSnapshot struct {
	AwgmgrVersion   string
	AwgmgrBackend   string
	HrneoVersion    string
	HrneoInstalled  *bool
	FirmwareCurrent string
	FirmwareAvail   string
	FirmwareChannel string
	KeeneticOS      string
	KmodVersion     string
	KmodModel       string
	KmodLoaded      *bool
	// KmodLoadedVersion -- версия модуля, которую держит ядро. Пусто -- источник
	// её не принёс (старый агент или version_audit), и Upsert оставит известное
	// -- ЕСЛИ KmodLoadedVersionReported не взведён (см. ниже).
	KmodLoadedVersion string
	// KmodLoadedVersionReported -- источник ЯВНО сообщил о состоянии модуля
	// (в отчёте есть ключ kernel_module_loaded), и потому Upsert обязан
	// записать KmodLoadedVersion КАК ПРИШЛА, даже пустой строкой, а не
	// применять общее правило «пусто -- оставить прежнее» (M2, fix round 1).
	//
	// Без этого флага отчёт сразу после настоящей перезагрузки, транзиентно
	// пришедший с пустой загруженной версией, навсегда оставил бы старую
	// версию в базе, и RebootHint звал бы перезагрузку вечно. Ставит его
	// только путь отчёта (versionSnapshotFromReport) -- version_audit
	// по-прежнему не стирает известное.
	KmodLoadedVersionReported bool
	Source                    string // "report" | "version_audit"
}

// RouterVersionRow -- снимок вместе с историей: «было» и когда менялось.
// Нулевой UpdatedAt означает, что снимка нет вовсе, -- экран обязан сказать
// «неизвестно», а не рисовать пустые версии.
type RouterVersionRow struct {
	RouterVersionSnapshot
	PrevAwgmgrVersion string
	PrevKmodVersion   string
	ChangedAt         *time.Time
	UpdatedAt         time.Time
}

// RouterVersionsRepo -- снимок версий на роутер (одна строка на роутер).
type RouterVersionsRepo struct{ d *DB }

func (d *DB) RouterVersions() *RouterVersionsRepo { return &RouterVersionsRepo{d: d} }

const routerVersionsColumns = `user_id, awgmgr_version, awgmgr_backend, hrneo_version, hrneo_installed,
	       firmware_current, firmware_avail, firmware_channel, keenetic_os,
	       kmod_version, kmod_model, kmod_loaded,
	       prev_awgmgr_version, prev_kmod_version, changed_at, source, updated_at,
	       kmod_loaded_version`

// Upsert записывает то, что узнал источник, и сам двигает историю.
//
// Два правила, и оба здесь, а не у вызывающих:
//
//  1. Дополнение, а не перезапись пустым. Пустая строка и nil означают «этот
//     источник такого не приносит», и старое значение остаётся на месте. Иначе
//     отчёт, приходящий раз в полторы минуты, стирал бы версию HydraRoute Neo,
//     которую знает только version_audit.
//  2. prev_* и changed_at двигаются ТОЛЬКО при смене известного значения.
//     Повтор того же отчёта не имеет права затереть историю собой, а первое
//     знакомство с версией -- это не смена: «было» тогда ещё не существовало.
func (r *RouterVersionsRepo) Upsert(userID int64, s RouterVersionSnapshot) error {
	now := time.Now().UTC()
	_, err := r.d.db.Exec(`
INSERT INTO router_versions (`+routerVersionsColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', NULL, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET
  awgmgr_version   = CASE WHEN excluded.awgmgr_version   = '' THEN router_versions.awgmgr_version   ELSE excluded.awgmgr_version   END,
  awgmgr_backend   = CASE WHEN excluded.awgmgr_backend   = '' THEN router_versions.awgmgr_backend   ELSE excluded.awgmgr_backend   END,
  hrneo_version    = CASE WHEN excluded.hrneo_version    = '' THEN router_versions.hrneo_version    ELSE excluded.hrneo_version    END,
  hrneo_installed  = CASE WHEN excluded.hrneo_installed IS NULL THEN router_versions.hrneo_installed ELSE excluded.hrneo_installed END,
  firmware_current = CASE WHEN excluded.firmware_current = '' THEN router_versions.firmware_current ELSE excluded.firmware_current END,
  firmware_avail   = CASE WHEN excluded.firmware_avail   = '' THEN router_versions.firmware_avail   ELSE excluded.firmware_avail   END,
  firmware_channel = CASE WHEN excluded.firmware_channel = '' THEN router_versions.firmware_channel ELSE excluded.firmware_channel END,
  keenetic_os      = CASE WHEN excluded.keenetic_os      = '' THEN router_versions.keenetic_os      ELSE excluded.keenetic_os      END,
  kmod_version     = CASE WHEN excluded.kmod_version     = '' THEN router_versions.kmod_version     ELSE excluded.kmod_version     END,
  kmod_model       = CASE WHEN excluded.kmod_model       = '' THEN router_versions.kmod_model       ELSE excluded.kmod_model       END,
  kmod_loaded      = CASE WHEN excluded.kmod_loaded    IS NULL THEN router_versions.kmod_loaded      ELSE excluded.kmod_loaded      END,
  kmod_loaded_version = CASE
      WHEN ? = 1 THEN excluded.kmod_loaded_version
      WHEN excluded.kmod_loaded_version = '' THEN router_versions.kmod_loaded_version
      ELSE excluded.kmod_loaded_version
    END,
  prev_awgmgr_version = CASE
      WHEN excluded.awgmgr_version <> '' AND router_versions.awgmgr_version <> ''
       AND excluded.awgmgr_version <> router_versions.awgmgr_version
      THEN router_versions.awgmgr_version ELSE router_versions.prev_awgmgr_version END,
  prev_kmod_version = CASE
      WHEN excluded.kmod_version <> '' AND router_versions.kmod_version <> ''
       AND excluded.kmod_version <> router_versions.kmod_version
      THEN router_versions.kmod_version ELSE router_versions.prev_kmod_version END,
  changed_at = CASE
      WHEN (excluded.awgmgr_version <> '' AND router_versions.awgmgr_version <> ''
            AND excluded.awgmgr_version <> router_versions.awgmgr_version)
        OR (excluded.kmod_version <> '' AND router_versions.kmod_version <> ''
            AND excluded.kmod_version <> router_versions.kmod_version)
      THEN excluded.updated_at ELSE router_versions.changed_at END,
  source     = CASE WHEN excluded.source = '' THEN router_versions.source ELSE excluded.source END,
  updated_at = excluded.updated_at`,
		userID, s.AwgmgrVersion, s.AwgmgrBackend, s.HrneoVersion, versionBoolArg(s.HrneoInstalled),
		s.FirmwareCurrent, s.FirmwareAvail, s.FirmwareChannel, s.KeeneticOS,
		s.KmodVersion, s.KmodModel, versionBoolArg(s.KmodLoaded),
		s.Source, now, s.KmodLoadedVersion, boolToIntArg(s.KmodLoadedVersionReported))
	return err
}

// Get отдаёт снимок роутера. Снимка нет -- это ответ «не знаем», а не ошибка:
// возвращается нулевая строка с нулевым UpdatedAt.
func (r *RouterVersionsRepo) Get(userID int64) (RouterVersionRow, error) {
	row := r.d.db.QueryRow(
		`SELECT `+routerVersionsColumns+` FROM router_versions WHERE user_id = ?`, userID)
	out, _, err := scanRouterVersionRow(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return RouterVersionRow{}, nil
	}
	if err != nil {
		return RouterVersionRow{}, err
	}
	return out, nil
}

// All -- снимки всего парка для сводки админа. Строк здесь столько же, сколько
// роутеров (десятки), поэтому скан таблицы допустим -- в отличие от events.
func (r *RouterVersionsRepo) All() (map[int64]RouterVersionRow, error) {
	rows, err := r.d.db.Query(`SELECT ` + routerVersionsColumns + ` FROM router_versions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]RouterVersionRow)
	for rows.Next() {
		rv, uid, err := scanRouterVersionRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out[uid] = rv
	}
	return out, rows.Err()
}

// scanRouterVersionRow разбирает одну строку в том же порядке колонок, что и
// routerVersionsColumns. Общий разбор для Get и All: две копии этого списка
// разъехались бы молча.
func scanRouterVersionRow(scan func(...any) error) (RouterVersionRow, int64, error) {
	var (
		out                     RouterVersionRow
		userID                  int64
		hrneoInst, kmodLoaded   sql.NullBool
		changedAt               sql.NullTime
		prevAwgmgr, prevKmodVer string
	)
	err := scan(&userID, &out.AwgmgrVersion, &out.AwgmgrBackend, &out.HrneoVersion, &hrneoInst,
		&out.FirmwareCurrent, &out.FirmwareAvail, &out.FirmwareChannel, &out.KeeneticOS,
		&out.KmodVersion, &out.KmodModel, &kmodLoaded,
		&prevAwgmgr, &prevKmodVer, &changedAt, &out.Source, &out.UpdatedAt,
		&out.KmodLoadedVersion)
	if err != nil {
		return RouterVersionRow{}, 0, err
	}
	out.HrneoInstalled = versionBoolPtr(hrneoInst)
	out.KmodLoaded = versionBoolPtr(kmodLoaded)
	out.PrevAwgmgrVersion = prevAwgmgr
	out.PrevKmodVersion = prevKmodVer
	out.ChangedAt = nullTime(changedAt)
	return out, userID, nil
}

// versionBoolArg отдаёт NULL за nil: «агент не сказал» обязано доехать до
// базы неизвестностью, а не нулём.
func versionBoolArg(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

func versionBoolPtr(n sql.NullBool) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Bool
	return &v
}

// boolToIntArg -- явный флаг (в отличие от versionBoolArg) не бывает
// неизвестным: это не поле снимка, а «форсировать перезапись или нет»,
// поэтому NULL здесь не нужен, только 0/1.
func boolToIntArg(b bool) int {
	if b {
		return 1
	}
	return 0
}
