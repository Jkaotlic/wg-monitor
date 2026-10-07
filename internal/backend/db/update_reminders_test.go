package db

import (
	"testing"
	"time"
)

// Ключ включает version -- «одна новость на выпуск» держится схемой, а не
// кодом: о той же версии второй строки не будет, а новая версия покажет себя
// сама.
func TestUpdateRemindersOneRowPerVersion(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	r := d.UpdateReminders()
	now := time.Now().UTC()

	if err := r.Ensure(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Ensure(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}

	list, err := r.ListFor(uid, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("о той же версии заведено %d новостей, а должна быть одна: %+v", len(list), list)
	}
	if list[0].Component != "awgmgr" || list[0].Version != "2.18.0" {
		t.Errorf("новость потеряла имя выпуска: %+v", list[0])
	}
}

// «Отложить» прячет новость до срока и сам собой возвращает её потом: экран не
// будит никого, и забыть о новости насовсем он не имеет права.
func TestUpdateRemindersSnoozeHidesUntilDeadline(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	r := d.UpdateReminders()
	now := time.Now().UTC()

	if err := r.Ensure(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Snooze(uid, "awgmgr", "2.18.0", now.Add(7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	list, err := r.ListFor(uid, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("отложенная новость видна раньше срока: %+v", list)
	}

	list, err = r.ListFor(uid, now.Add(8*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("после срока новость обязана вернуться, got %+v", list)
	}
}

// «Скрыть» прячет новость об ЭТОЙ версии, а не про компонент навсегда: иначе
// одно нажатие выключило бы все будущие выпуски панели.
func TestUpdateRemindersDismissIsPerVersion(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	r := d.UpdateReminders()
	now := time.Now().UTC()

	if err := r.Ensure(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Dismiss(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Ensure(uid, "awgmgr", "2.19.0"); err != nil {
		t.Fatal(err)
	}

	list, err := r.ListFor(uid, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("видимых новостей %d, а должна быть одна (2.19.0): %+v", len(list), list)
	}
	if list[0].Version != "2.19.0" {
		t.Errorf("скрытие одной версии утащило за собой следующую: %+v", list[0])
	}
}

// Показ -- это не доставка: новость, которую экран уже показал, со экрана не
// исчезает. Исчезают только «отложенная» и «скрытая».
func TestUpdateRemindersMarkShownKeepsNewsVisible(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	r := d.UpdateReminders()
	now := time.Now().UTC()

	if err := r.Ensure(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkShown(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}

	list, err := r.ListFor(uid, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("показанная новость пропала с экрана: %+v", list)
	}
	if list[0].ShownAt == nil {
		t.Error("отметка о показе не сохранилась")
	}
}

// Чистка трогает только скрытые новости. Новость, которую никто не скрывал,
// живёт, пока не сменится версия: её удаление означало бы, что экран забыл про
// невыполненное обновление.
func TestUpdateRemindersPruneTouchesOnlyDismissed(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	r := d.UpdateReminders()
	now := time.Now().UTC()

	if err := r.Ensure(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Dismiss(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Ensure(uid, "hrneo", "3.18.3"); err != nil {
		t.Fatal(err)
	}

	deleted, err := r.PruneDismissedBefore(now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("удалено строк %d, а скрытой была одна", deleted)
	}

	list, err := r.ListFor(uid, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Component != "hrneo" {
		t.Errorf("чистка задела не скрытую новость: %+v", list)
	}
}

// Мягкое напоминание (v0.57): в рассылку идёт новость, которую не скрыли, не
// отложили до будущего и ещё не разослали. Отметка «разослано» -- своя
// колонка, и экран она не трогает: разосланное приложение показывает как
// прежде.
func TestUpdateRemindersPendingNotify(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	r := d.UpdateReminders()
	now := time.Now().UTC()
	for _, c := range []string{"awgmgr", "hrneo", "firmware", "agent"} {
		if err := r.Ensure(uid, c, "1.0.0"); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Dismiss(uid, "hrneo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Snooze(uid, "firmware", "1.0.0", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkShown(uid, "agent", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	got, err := r.PendingNotify(uid, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Component != "agent" || got[1].Component != "awgmgr" {
		t.Fatalf("к рассылке %+v, ждали agent и awgmgr (скрытое и отложенное -- нет)", got)
	}

	if err := r.MarkNotified(uid, "awgmgr", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	got, err = r.PendingNotify(uid, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Component != "agent" {
		t.Fatalf("после отметки к рассылке %+v, ждали только agent", got)
	}
	// Срок «отложить» вышел -- новость снова к рассылке.
	got, err = r.PendingNotify(uid, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Component != "firmware" {
		t.Fatalf("после срока отсрочки %+v, ждали agent и firmware", got)
	}
	// Экран разосланное показывает: рассылка не прячет новость.
	list, err := r.ListFor(uid, now)
	if err != nil {
		t.Fatal(err)
	}
	var awgm *Reminder
	for i := range list {
		if list[i].Component == "awgmgr" {
			awgm = &list[i]
		}
	}
	if awgm == nil || awgm.NotifiedAt == nil {
		t.Fatalf("экран потерял разосланную новость или её отметку: %+v", list)
	}
	// Повторная отметка не сдвигает первую.
	first := *awgm.NotifiedAt
	if err := r.MarkNotified(uid, "awgmgr", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	list, _ = r.ListFor(uid, now)
	for _, rem := range list {
		if rem.Component == "awgmgr" && !rem.NotifiedAt.Equal(first) {
			t.Errorf("повторная отметка сдвинула дату: %v -> %v", first, rem.NotifiedAt)
		}
	}
}
