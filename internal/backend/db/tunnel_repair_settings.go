package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TunnelRepairSetting -- настройка автопочинки одного VPN-туннеля.
//
// Строки нет -- автопочинка выключена: включение только явное. Пустой Provider
// -- урезанный режим, лесенка умеет только перезапуск.
type TunnelRepairSetting struct {
	UserID        int64
	TunnelID      string
	Enabled       bool
	Provider      string // "" | amnezia | hidemyname | awg3
	Option        string
	AllowRelocate bool
	UpdatedBy     int64
	UpdatedAt     time.Time
}

type TunnelRepairSettingsRepo struct{ d *DB }

func (d *DB) TunnelRepairSettings() *TunnelRepairSettingsRepo {
	return &TunnelRepairSettingsRepo{d: d}
}

// Get возвращает настройку туннеля. Второе значение false -- строки нет, то
// есть автопочинка выключена; это ответ, а не ошибка.
func (r *TunnelRepairSettingsRepo) Get(userID int64, tunnelID string) (TunnelRepairSetting, bool, error) {
	row := r.d.db.QueryRow(
		`SELECT user_id, tunnel_id, enabled, provider, option, allow_relocate, updated_by, updated_at
		   FROM tunnel_repair_settings WHERE user_id = ? AND tunnel_id = ?`, userID, tunnelID)
	var s TunnelRepairSetting
	if err := row.Scan(&s.UserID, &s.TunnelID, &s.Enabled, &s.Provider, &s.Option, &s.AllowRelocate, &s.UpdatedBy, &s.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TunnelRepairSetting{}, false, nil
		}
		return TunnelRepairSetting{}, false, fmt.Errorf("tunnel_repair_settings.Get: %w", err)
	}
	return s, true, nil
}

// List возвращает только включённые настройки роутера, по tunnel_id.
func (r *TunnelRepairSettingsRepo) List(userID int64) ([]TunnelRepairSetting, error) {
	rows, err := r.d.db.Query(
		`SELECT user_id, tunnel_id, enabled, provider, option, allow_relocate, updated_by, updated_at
		   FROM tunnel_repair_settings WHERE user_id = ? AND enabled = 1 ORDER BY tunnel_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("tunnel_repair_settings.List: %w", err)
	}
	defer rows.Close()
	var out []TunnelRepairSetting
	for rows.Next() {
		var s TunnelRepairSetting
		if err := rows.Scan(&s.UserID, &s.TunnelID, &s.Enabled, &s.Provider, &s.Option, &s.AllowRelocate, &s.UpdatedBy, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("tunnel_repair_settings.List scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Put записывает настройку (upsert); UpdatedAt ставится сейчас, в UTC.
func (r *TunnelRepairSettingsRepo) Put(s TunnelRepairSetting) error {
	if s.TunnelID == "" {
		return errors.New("tunnel_repair_settings: tunnel_id is required")
	}
	_, err := r.d.db.Exec(
		`INSERT INTO tunnel_repair_settings(user_id, tunnel_id, enabled, provider, option, allow_relocate, updated_by, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(user_id, tunnel_id) DO UPDATE SET
		   enabled        = excluded.enabled,
		   provider       = excluded.provider,
		   option         = excluded.option,
		   allow_relocate = excluded.allow_relocate,
		   updated_by     = excluded.updated_by,
		   updated_at     = excluded.updated_at`,
		s.UserID, s.TunnelID, s.Enabled, s.Provider, s.Option, s.AllowRelocate, s.UpdatedBy, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("tunnel_repair_settings.Put: %w", err)
	}
	return nil
}
