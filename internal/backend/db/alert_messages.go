package db

// AlertMessagesRepo помнит, кому какое сообщение о поломке ушло: на него
// отвечает «восстановилось» и в него дописывает статус мини-апп.
type AlertMessagesRepo struct{ d *DB }

func (d *DB) AlertMessages() *AlertMessagesRepo { return &AlertMessagesRepo{d: d} }

// Put запоминает сообщение конкретного получателя. Повторная отправка тому же
// человеку перезаписывает id: живой всегда последний.
func (r *AlertMessagesRepo) Put(userID int64, checkName string, telegramUserID, messageID int64) error {
	_, err := r.d.db.Exec(
		`INSERT INTO alert_messages (user_id, check_name, telegram_user_id, message_id, sent_at)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(user_id, check_name, telegram_user_id) DO UPDATE SET
		   message_id = excluded.message_id,
		   sent_at    = CURRENT_TIMESTAMP`,
		userID, checkName, telegramUserID, messageID)
	return err
}

// List -- получатель → id его сообщения по этой проверке.
func (r *AlertMessagesRepo) List(userID int64, checkName string) (map[int64]int64, error) {
	rows, err := r.d.db.Query(
		`SELECT telegram_user_id, message_id FROM alert_messages
		  WHERE user_id = ? AND check_name = ?`, userID, checkName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]int64)
	for rows.Next() {
		var tgID, msgID int64
		if err := rows.Scan(&tgID, &msgID); err != nil {
			return nil, err
		}
		out[tgID] = msgID
	}
	return out, rows.Err()
}

// Clear забывает переписку по проверке -- вызывается после «восстановилось»,
// чтобы следующая поломка начинала свою ветку с чистого листа.
func (r *AlertMessagesRepo) Clear(userID int64, checkName string) error {
	_, err := r.d.db.Exec(
		`DELETE FROM alert_messages WHERE user_id = ? AND check_name = ?`, userID, checkName)
	return err
}
