package db

import (
	"database/sql"
	"time"
)

// Reminder -- состояние новости об одном выпуске на одном роутере.
//
// Новость -- не тревога: у «вышла новая версия» нет потока ok/fail, её никто
// не «чинит» и она не будит человека ночью. Поэтому она живёт своей таблицей,
// а не псевдо-инцидентом в incident_state: инцидент всплыл бы в списке тревог
// мини-аппа и в счётчике дашборда, и обновление выглядело бы поломкой.
//
// Три указателя -- три разных состояния, и ни одно не выводится из остальных:
// ShownAt -- экран новость уже показывал; SnoozedUntil -- отложена до срока;
// DismissedAt -- скрыта совсем (про ЭТУ версию, а не про компонент навсегда).
type Reminder struct {
	Component    string
	Version      string
	FirstSeenAt  time.Time
	ShownAt      *time.Time
	SnoozedUntil *time.Time
	DismissedAt  *time.Time
}

// UpdateRemindersRepo -- состояние новостей об обновлениях на экранах.
type UpdateRemindersRepo struct{ d *DB }

func (d *DB) UpdateReminders() *UpdateRemindersRepo { return &UpdateRemindersRepo{d: d} }

const updateRemindersColumns = `component, version, first_seen_at, shown_at, snoozed_until, dismissed_at`

// Ensure заводит новость о выпуске, если её ещё нет.
//
// Повтор безвреден и ничего не трогает: ключ (user_id, component, version)
// держит правило «одна новость на выпуск» схемой, а не кодом, поэтому
// Ensure можно звать на каждую отрисовку экрана. Затирать им чужие отметки
// нельзя -- иначе открытие экрана отменяло бы «отложить».
func (r *UpdateRemindersRepo) Ensure(userID int64, component, version string) error {
	_, err := r.d.db.Exec(`
INSERT INTO router_update_reminders (user_id, component, version, first_seen_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(user_id, component, version) DO NOTHING`,
		userID, component, version, time.Now().UTC())
	return err
}

// MarkShown записывает, что экран показал новость. Отметка ставится один раз:
// COALESCE оставляет первый показ на месте, потому что интересна дата
// знакомства человека с новостью, а не дата последней перерисовки экрана.
//
// Показ новость не прячет -- это экран, а не рассылка.
func (r *UpdateRemindersRepo) MarkShown(userID int64, component, version string) error {
	now := time.Now().UTC()
	_, err := r.d.db.Exec(`
INSERT INTO router_update_reminders (user_id, component, version, first_seen_at, shown_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id, component, version) DO UPDATE SET
  shown_at = COALESCE(router_update_reminders.shown_at, excluded.shown_at)`,
		userID, component, version, now, now)
	return err
}

// Snooze прячет новость до срока. Повторное «отложить» продлевает срок:
// человек сказал это заново, и его последнее слово главнее прежнего.
func (r *UpdateRemindersRepo) Snooze(userID int64, component, version string, until time.Time) error {
	_, err := r.d.db.Exec(`
INSERT INTO router_update_reminders (user_id, component, version, first_seen_at, snoozed_until)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id, component, version) DO UPDATE SET
  snoozed_until = excluded.snoozed_until`,
		userID, component, version, time.Now().UTC(), until.UTC())
	return err
}

// Dismiss скрывает новость об этой версии насовсем.
//
// Дата первого скрытия сохраняется (COALESCE): по ней считает чистка, и
// обновлять её на каждом повторном нажатии значило бы откладывать удаление
// строки бесконечно.
func (r *UpdateRemindersRepo) Dismiss(userID int64, component, version string) error {
	now := time.Now().UTC()
	_, err := r.d.db.Exec(`
INSERT INTO router_update_reminders (user_id, component, version, first_seen_at, dismissed_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id, component, version) DO UPDATE SET
  dismissed_at = COALESCE(router_update_reminders.dismissed_at, excluded.dismissed_at)`,
		userID, component, version, now, now)
	return err
}

// ListFor -- новости, которые экран этого роутера имеет право показать сейчас.
//
// Скрытые не показываются никогда, отложенные -- пока не вышел срок. Выборка
// точечная по user_id (индекс idx_update_reminders_user); в горячие events за
// этим лезть не надо вовсе.
func (r *UpdateRemindersRepo) ListFor(userID int64, now time.Time) ([]Reminder, error) {
	rows, err := r.d.db.Query(`
SELECT `+updateRemindersColumns+`
FROM router_update_reminders
WHERE user_id = ?
  AND dismissed_at IS NULL
  AND (snoozed_until IS NULL OR snoozed_until <= ?)
ORDER BY component, version`, userID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		rem, err := scanReminder(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rem)
	}
	return out, rows.Err()
}

// PruneDismissedBefore убирает давно скрытые новости.
//
// Трогает ТОЛЬКО скрытые: новость, которую никто не скрывал, живёт, пока не
// сменится версия. Удалять её по возрасту значило бы, что экран сам забыл про
// невыполненное обновление -- и показал бы его заново как свежую новость.
func (r *UpdateRemindersRepo) PruneDismissedBefore(cutoff time.Time) (int64, error) {
	res, err := r.d.db.Exec(
		`DELETE FROM router_update_reminders WHERE dismissed_at IS NOT NULL AND dismissed_at < ?`,
		cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// scanReminder разбирает строку в том же порядке колонок, что и
// updateRemindersColumns: две копии этого списка разъехались бы молча.
func scanReminder(scan func(...any) error) (Reminder, error) {
	var (
		out                       Reminder
		shown, snoozed, dismissed sql.NullTime
	)
	if err := scan(&out.Component, &out.Version, &out.FirstSeenAt, &shown, &snoozed, &dismissed); err != nil {
		return Reminder{}, err
	}
	out.ShownAt = nullTime(shown)
	out.SnoozedUntil = nullTime(snoozed)
	out.DismissedAt = nullTime(dismissed)
	return out, nil
}
