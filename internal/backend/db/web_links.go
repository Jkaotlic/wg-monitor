package db

import "time"

// WebLinkMaxLive -- сколько живых грантов на вход в веб-управление человек
// может иметь одновременно. Число выбрано так, чтобы хватило телефона,
// ноутбука и запасного устройства, но связку ключей ко всему парку накопить
// было нельзя: выдача следующего гасит самый старый.
const WebLinkMaxLive = 3

// WebLinksRepo -- гранты на вход в веб-управление (таблица web_links).
//
// Наружу уходит случайное значение, здесь хранится только его sha256: база,
// утёкшая целиком, не даёт войти. Сравнение идёт по хешу, а сам грант не
// появляется ни в ответах, ни в журналах.
type WebLinksRepo struct{ d *DB }

func (d *DB) WebLinks() *WebLinksRepo { return &WebLinksRepo{d: d} }

// Issue записывает новый грант и в той же транзакции гасит лишние: у одного
// человека остаются WebLinkMaxLive самых свежих по created_at.
//
// Почему вытеснение считает строки, а не спрашивает «живые сейчас»: срок у
// всех грантов один, поэтому порядок по created_at совпадает с порядком по
// expires_at, и «самый старый» -- это всегда тот, кто умрёт первым.
// Просроченные строки уходят этим же правилом и добиваются чисткой по сроку
// (PruneBefore), а не занимают место живых.
func (r *WebLinksRepo) Issue(hash string, tgID int64, expiresAt time.Time) error {
	tx, err := r.d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO web_links(token_hash, telegram_user_id, expires_at) VALUES (?, ?, ?)`,
		hash, tgID, expiresAt.UTC()); err != nil {
		return err
	}
	// rowid в разрыве -- второй ключ сортировки: created_at пишется
	// CURRENT_TIMESTAMP с точностью до секунды, и две выдачи подряд иначе
	// были бы неразличимы по возрасту.
	if _, err := tx.Exec(
		`DELETE FROM web_links
		  WHERE telegram_user_id = ?
		    AND rowid NOT IN (
		        SELECT rowid FROM web_links WHERE telegram_user_id = ?
		         ORDER BY created_at DESC, rowid DESC LIMIT ?)`,
		tgID, tgID, WebLinkMaxLive); err != nil {
		return err
	}
	return tx.Commit()
}

// Redeem отмечает предъявление гранта и отвечает числом затронутых строк:
// 1 -- грант есть и он не просрочен, 0 -- нет ни одного такого живого гранта.
//
// Условия «ещё не использован» здесь нет намеренно (ссылка многоразовая), и
// пишется КАЖДЫЙ обмен: это и есть та улика, которой оплачена снятая
// одноразовость. Ответ по числу строк, а не по предварительному чтению:
// между чтением и записью грант мог истечь или быть отозван.
func (r *WebLinksRepo) Redeem(hash string, now time.Time, remote string) (int64, error) {
	res, err := r.d.db.Exec(
		`UPDATE web_links
		    SET last_used_at = ?, last_used_remote = ?, use_count = use_count + 1
		  WHERE token_hash = ? AND expires_at > ?`,
		now.UTC(), remote, hash, now.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ActiveFor -- хеши грантов этого человека, действующих на момент now, от
// свежего к старому.
//
// Нулевой now означает «все его гранты, включая просроченные»: на этом стоит
// различение «ссылка просрочена» и «такой ссылки нет» в журнале обмена --
// наружу оба случая отвечают одним текстом.
func (r *WebLinksRepo) ActiveFor(tgID int64, now time.Time) ([]string, error) {
	rows, err := r.d.db.Query(
		`SELECT token_hash FROM web_links
		  WHERE telegram_user_id = ? AND expires_at > ?
		  ORDER BY created_at DESC, rowid DESC`,
		tgID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, WebLinkMaxLive)
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Delete -- точечный отзыв одной ссылки. Идемпотентен: отсутствие строки не
// ошибка, отозвать дважды -- нормальная просьба человека.
func (r *WebLinksRepo) Delete(hash string) error {
	_, err := r.d.db.Exec(`DELETE FROM web_links WHERE token_hash = ?`, hash)
	return err
}

// DeleteForUser гасит все ссылки одного человека разом.
func (r *WebLinksRepo) DeleteForUser(tgID int64) error {
	_, err := r.d.db.Exec(`DELETE FROM web_links WHERE telegram_user_id = ?`, tgID)
	return err
}

// PruneBefore убирает гранты, истёкшие до cutoff. Строка живёт ещё сутки
// после смерти ссылки намеренно: журнал обмена должно быть с чем сверить.
func (r *WebLinksRepo) PruneBefore(cutoff time.Time) (int64, error) {
	res, err := r.d.db.Exec(`DELETE FROM web_links WHERE expires_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
