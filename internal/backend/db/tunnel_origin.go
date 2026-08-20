package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TunnelOrigin -- происхождение конфига туннеля: чем он поднят и когда выпущен.
//
// Строки нет у туннелей, заведённых до мастера замены конфига или руками. Это
// не пробел в данных, а честное «неизвестно»: выдумать провайдера за них
// нечем, и экран говорит это словами.
type TunnelOrigin struct {
	TunnelID   string
	TunnelName string
	Provider   string
	Variant    string
	IssuedAt   time.Time
	IssuedBy   int64
}

type TunnelOriginRepo struct{ d *DB }

func (d *DB) TunnelOrigins() *TunnelOriginRepo { return &TunnelOriginRepo{d: d} }

// Record запоминает происхождение. Повторный выпуск той же страны на тот же
// туннель перезаписывает строку: актуально то, чем туннель поднят СЕЙЧАС.
func (r *TunnelOriginRepo) Record(userID int64, tunnelID, tunnelName, provider, variant string, issuedAt time.Time, issuedBy int64) error {
	if tunnelID == "" || provider == "" {
		return errors.New("tunnel_origin: tunnel_id and provider are required")
	}
	_, err := r.d.db.Exec(
		`INSERT INTO tunnel_config_origin(user_id, tunnel_id, tunnel_name, provider, variant, issued_at, issued_by)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(user_id, tunnel_id) DO UPDATE SET
		   tunnel_name = excluded.tunnel_name,
		   provider    = excluded.provider,
		   variant     = excluded.variant,
		   issued_at   = excluded.issued_at,
		   issued_by   = excluded.issued_by`,
		userID, tunnelID, tunnelName, provider, variant, issuedAt.UTC(), issuedBy,
	)
	if err != nil {
		return fmt.Errorf("tunnel_origin.Record: %w", err)
	}
	return nil
}

// Get возвращает происхождение одного туннеля. Второе значение false --
// «система этого не помнит», а не ошибка.
func (r *TunnelOriginRepo) Get(userID int64, tunnelID string) (TunnelOrigin, bool, error) {
	row := r.d.db.QueryRow(
		`SELECT tunnel_id, tunnel_name, provider, variant, issued_at, issued_by
		   FROM tunnel_config_origin WHERE user_id = ? AND tunnel_id = ?`, userID, tunnelID)
	var out TunnelOrigin
	if err := row.Scan(&out.TunnelID, &out.TunnelName, &out.Provider, &out.Variant, &out.IssuedAt, &out.IssuedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TunnelOrigin{}, false, nil
		}
		return TunnelOrigin{}, false, fmt.Errorf("tunnel_origin.Get: %w", err)
	}
	return out, true, nil
}

// List возвращает всё, что известно про туннели этого роутера.
func (r *TunnelOriginRepo) List(userID int64) ([]TunnelOrigin, error) {
	rows, err := r.d.db.Query(
		`SELECT tunnel_id, tunnel_name, provider, variant, issued_at, issued_by
		   FROM tunnel_config_origin WHERE user_id = ? ORDER BY tunnel_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("tunnel_origin.List: %w", err)
	}
	defer rows.Close()
	var out []TunnelOrigin
	for rows.Next() {
		var o TunnelOrigin
		if err := rows.Scan(&o.TunnelID, &o.TunnelName, &o.Provider, &o.Variant, &o.IssuedAt, &o.IssuedBy); err != nil {
			return nil, fmt.Errorf("tunnel_origin.List scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
