package db

import (
	"database/sql"
	"errors"
)

// RepairSettingsRepo -- настройки починки на роутер.
type RepairSettingsRepo struct {
	d *DB
	// def -- чем считать отсутствие строки. nil означает «включено»:
	// роутеры, заведённые до этой таблицы, обязаны получить полуавтомат.
	def *bool
}

func (d *DB) RepairSettings() *RepairSettingsRepo { return &RepairSettingsRepo{d: d} }

// AutoRepair сообщает, разрешено ли движку чинить линию без кнопки.
// Отсутствие строки -- ВКЛЮЧЕНО: роутеры, заведённые до этой таблицы,
// обязаны получить полуавтомат, а не остаться без него молча.
func (r *RepairSettingsRepo) AutoRepair(userID int64) (bool, error) {
	var v int
	err := r.d.db.QueryRow(
		`SELECT auto_repair FROM router_repair_settings WHERE user_id = ?`, userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		if r.def != nil {
			return *r.def, nil
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// SetAutoRepair записывает решение владельца. Выключенный полуавтомат
// означает, что движок не отправит роутеру ни одной изменяющей команды без
// нажатой кнопки.
func (r *RepairSettingsRepo) SetAutoRepair(userID int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := r.d.db.Exec(`
INSERT INTO router_repair_settings(user_id, auto_repair, updated_at)
VALUES(?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id) DO UPDATE SET
  auto_repair=excluded.auto_repair,
  updated_at=excluded.updated_at`, userID, v)
	return err
}

// WithDefault задаёт, чем считать ОТСУТСТВИЕ строки. Нужен на время обкатки
// движка: настройка каждого роутера остаётся за владельцем, но парк, который
// её ни разу не трогал, идёт за решением оператора бэкенда. Без этого первое
// же обновление включило бы полуавтомат разом на всех, и обкатка на одном
// роутере стала бы невозможной.
func (r *RepairSettingsRepo) WithDefault(def bool) *RepairSettingsRepo {
	out := *r
	out.def = &def
	return &out
}
