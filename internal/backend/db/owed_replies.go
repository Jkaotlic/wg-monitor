package db

import "time"

// OwedReply -- ответ на тревогу, который не дошёл ни одному получателю.
type OwedReply struct {
	UserID    int64
	CheckName string
	Text      string
	CreatedAt time.Time
}

// OwedRepliesRepo помнит недоставленные ответы на тревоги («восстановилось»),
// чтобы обход напоминаний повторил их. В базе, а не в памяти: перезапуск
// контейнера не должен оставлять человека с красной тревогой без ответа.
type OwedRepliesRepo struct{ d *DB }

func (d *DB) OwedReplies() *OwedRepliesRepo { return &OwedRepliesRepo{d: d} }

// Put запоминает долг по проверке. Новый долг заменяет прежний.
func (r *OwedRepliesRepo) Put(userID int64, checkName, text string, at time.Time) error {
	_, err := r.d.db.Exec(
		`INSERT INTO owed_replies (user_id, check_name, text, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, check_name) DO UPDATE SET
		   text       = excluded.text,
		   created_at = excluded.created_at`,
		userID, checkName, text, at.UTC())
	return err
}

// List -- все долги, старые первыми.
func (r *OwedRepliesRepo) List() ([]OwedReply, error) {
	rows, err := r.d.db.Query(
		`SELECT user_id, check_name, text, created_at FROM owed_replies ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OwedReply
	for rows.Next() {
		var o OwedReply
		if err := rows.Scan(&o.UserID, &o.CheckName, &o.Text, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Delete снимает долг: доставлен, устарел или его перекрыла новая тревога.
func (r *OwedRepliesRepo) Delete(userID int64, checkName string) error {
	_, err := r.d.db.Exec(
		`DELETE FROM owed_replies WHERE user_id = ? AND check_name = ?`, userID, checkName)
	return err
}
