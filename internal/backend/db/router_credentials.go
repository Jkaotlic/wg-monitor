package db

import (
	"database/sql"
	"errors"
	"time"
)

// RouterCredentialsRepo -- сохранённые учётные данные роутера для
// авто-оживления (v0.45). Здесь только шифртекст: расшифровать его может лишь
// revive.Service, у которого есть ключ. Пакет db открытого текста не видит.
type RouterCredentialsRepo struct{ d *DB }

func (d *DB) RouterCredentials() *RouterCredentialsRepo { return &RouterCredentialsRepo{d: d} }

// Put сохраняет (или заменяет) шифртекст роутера.
func (r *RouterCredentialsRepo) Put(routerID int64, nonce, ciphertext []byte, savedAt time.Time) error {
	_, err := r.d.db.Exec(`
INSERT INTO router_credentials (user_id, nonce, ciphertext, saved_at) VALUES (?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET nonce = excluded.nonce, ciphertext = excluded.ciphertext, saved_at = excluded.saved_at`,
		routerID, nonce, ciphertext, savedAt.UTC())
	return err
}

// Get -- шифртекст роутера; ok=false, если ничего не сохранено.
func (r *RouterCredentialsRepo) Get(routerID int64) (nonce, ciphertext []byte, savedAt time.Time, ok bool, err error) {
	err = r.d.db.QueryRow(`SELECT nonce, ciphertext, saved_at FROM router_credentials WHERE user_id = ?`, routerID).
		Scan(&nonce, &ciphertext, &savedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, nil, time.Time{}, false, err
	}
	return nonce, ciphertext, savedAt.UTC(), true, nil
}

// SavedAt -- у каких роутеров что-то сохранено и когда. Шифртекст не
// читается вовсе: экрану нужен только факт.
func (r *RouterCredentialsRepo) SavedAt() (map[int64]time.Time, error) {
	rows, err := r.d.db.Query(`SELECT user_id, saved_at FROM router_credentials`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var (
			id int64
			at time.Time
		)
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at.UTC()
	}
	return out, rows.Err()
}

// Delete стирает сохранённое. false -- стирать было нечего.
func (r *RouterCredentialsRepo) Delete(routerID int64) (bool, error) {
	return affectedOne(r.d.db.Exec(`DELETE FROM router_credentials WHERE user_id = ?`, routerID))
}

// DeleteIfMatches стирает строку, только если в ней всё ещё тот шифртекст,
// которого касался вызывающий (nonce случаен на каждое сохранение и служит
// меткой версии). Новый пароль, сохранённый админом в щель, не трогается.
func (r *RouterCredentialsRepo) DeleteIfMatches(routerID int64, nonce []byte) (bool, error) {
	return affectedOne(r.d.db.Exec(`DELETE FROM router_credentials WHERE user_id = ? AND nonce = ?`, routerID, nonce))
}
