package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestDBForRevive(t *testing.T) (*DB, int64) {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "revive.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("bronya", "tok-bronya", "198.51.100.20", "awg0")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return d, id
}

var reviveT0 = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

func putWaiting(t *testing.T, d *DB, id int64) {
	t.Helper()
	err := d.Revive().Put(ReviveIntent{
		RouterID: id, CreatedAt: reviveT0, ExpiresAt: reviveT0.Add(30 * 24 * time.Hour), RequestedBy: 42,
	}, []byte("nonce-12byte"), []byte("cipher"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
}

func TestRevive_PutGetAndSecret(t *testing.T) {
	d, id := newTestDBForRevive(t)
	if got, err := d.Revive().Get(id); err != nil || got != nil {
		t.Fatalf("до постановки: %+v, %v", got, err)
	}
	putWaiting(t, d, id)

	got, err := d.Revive().Get(id)
	if err != nil || got == nil {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if got.Status != ReviveWaiting || got.Attempts != 0 || got.RequestedBy != 42 {
		t.Fatalf("намерение: %+v", got)
	}
	if !got.ExpiresAt.Equal(reviveT0.Add(30*24*time.Hour)) || !got.LastProbeAt.IsZero() {
		t.Fatalf("время: %+v", got)
	}
	nonce, ct, ok, err := d.Revive().Secret(id)
	if err != nil || !ok || string(nonce) != "nonce-12byte" || string(ct) != "cipher" {
		t.Fatalf("секрет: %q %q %v %v", nonce, ct, ok, err)
	}
}

func TestRevive_PutReplacesWaitingButNotRunning(t *testing.T) {
	d, id := newTestDBForRevive(t)
	putWaiting(t, d, id)
	if err := d.Revive().RecordProbe(id, reviveT0, "reachable", 1, reviveT0); err != nil {
		t.Fatal(err)
	}
	// Переставить ожидание -- можно: человек ввёл пароль заново.
	if err := d.Revive().Put(ReviveIntent{RouterID: id, CreatedAt: reviveT0, ExpiresAt: reviveT0.Add(time.Hour)},
		[]byte("nonce-second"), []byte("cipher2")); err != nil {
		t.Fatal(err)
	}
	got, _ := d.Revive().Get(id)
	if got.ReachableProbes != 0 || got.LastProbeState != "" {
		t.Fatalf("переставленное намерение обязано начаться с нуля: %+v", got)
	}
	_, ct, _, _ := d.Revive().Secret(id)
	if string(ct) != "cipher2" {
		t.Fatalf("секрет не заменён: %q", ct)
	}

	if ok, err := d.Revive().MarkRunning(id, reviveT0); err != nil || !ok {
		t.Fatalf("mark running: %v %v", ok, err)
	}
	err := d.Revive().Put(ReviveIntent{RouterID: id, CreatedAt: reviveT0, ExpiresAt: reviveT0.Add(time.Hour)},
		[]byte("nonce-third!"), []byte("cipher3"))
	if !errors.Is(err, ErrReviveRunning) {
		t.Fatalf("поверх идущей переустановки ставить нельзя, err=%v", err)
	}
	_, ct, _, _ = d.Revive().Secret(id)
	if string(ct) != "cipher2" {
		t.Fatalf("секрет идущей переустановки подменён: %q", ct)
	}
}

func TestRevive_MarkRunningCountsAttemptOnce(t *testing.T) {
	d, id := newTestDBForRevive(t)
	putWaiting(t, d, id)
	if ok, _ := d.Revive().MarkRunning(id, reviveT0); !ok {
		t.Fatal("первый запуск обязан пройти")
	}
	if ok, _ := d.Revive().MarkRunning(id, reviveT0); ok {
		t.Fatal("второй запуск поверх running обязан вернуть false")
	}
	got, _ := d.Revive().Get(id)
	if got.Status != ReviveRunning || got.Attempts != 1 {
		t.Fatalf("%+v", got)
	}
	if ok, _ := d.Revive().BackToWaiting(id, "роутер не ответил вовремя", reviveT0); !ok {
		t.Fatal("back to waiting")
	}
	got, _ = d.Revive().Get(id)
	if got.Status != ReviveWaiting || got.Attempts != 1 || got.LastError != "роутер не ответил вовремя" || got.ReachableProbes != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestRevive_FinishWipesSecretOnceAndOnlyFromExpectedStatus(t *testing.T) {
	d, id := newTestDBForRevive(t)
	putWaiting(t, d, id)

	// Из running закрыть нельзя: намерение ждёт, а не идёт.
	if ok, _ := d.Revive().Finish(id, []string{ReviveRunning}, ReviveDone, "", reviveT0); ok {
		t.Fatal("переход не из того статуса обязан вернуть false")
	}
	if _, _, ok, _ := d.Revive().Secret(id); !ok {
		t.Fatal("несостоявшийся переход не имеет права стирать секрет")
	}

	ok, err := d.Revive().Finish(id, []string{ReviveWaiting, ReviveRunning}, ReviveCancelled, "", reviveT0)
	if err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if _, _, ok, _ := d.Revive().Secret(id); ok {
		t.Fatal("после отмены секрет обязан быть стёрт")
	}
	// Повторное закрытие -- false: уведомление не уйдёт дважды.
	if ok, _ := d.Revive().Finish(id, []string{ReviveWaiting}, ReviveExpired, "", reviveT0); ok {
		t.Fatal("закрытое намерение не закрывается второй раз")
	}
	got, _ := d.Revive().Get(id)
	if got.Status != ReviveCancelled {
		t.Fatalf("%+v", got)
	}
}

func TestRevive_ResetRunningAndList(t *testing.T) {
	d, id := newTestDBForRevive(t)
	putWaiting(t, d, id)
	_, _ = d.Revive().MarkRunning(id, reviveT0)

	list, err := d.Revive().ListByStatus(ReviveRunning)
	if err != nil || len(list) != 1 || list[0].RouterID != id {
		t.Fatalf("list: %+v %v", list, err)
	}
	n, err := d.Revive().ResetRunning("переустановка прервалась: сервер перезапускался", reviveT0)
	if err != nil || n != 1 {
		t.Fatalf("reset: %d %v", n, err)
	}
	got, _ := d.Revive().Get(id)
	if got.Status != ReviveWaiting || got.Attempts != 1 {
		t.Fatalf("попытка обязана остаться засчитанной: %+v", got)
	}
}

func TestRevive_RouterDeleteCascades(t *testing.T) {
	d, id := newTestDBForRevive(t)
	putWaiting(t, d, id)
	if _, err := d.SQL().Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.SQL().QueryRow(`SELECT COUNT(*) FROM revive_secrets`).Scan(&n)
	if n != 0 {
		t.Fatalf("секрет удалённого роутера остался: %d", n)
	}
}

// Fix round 1, Minor #3 (мандатное ревью): запуск, отказавший потому что
// движок переустановки занят ЧУЖИМ заданием на этом же роутере (дашборд уже
// чинит/ставит), -- не вина оживления и не повод жечь одну из пяти попыток.
// BackToWaitingNoAttempt -- как BackToWaiting, но отменяет инкремент,
// который MarkRunning уже внёс в БД.
func TestRevive_BackToWaitingNoAttemptUndoesMarkRunningIncrement(t *testing.T) {
	d, id := newTestDBForRevive(t)
	putWaiting(t, d, id)
	if ok, _ := d.Revive().MarkRunning(id, reviveT0); !ok {
		t.Fatal("mark running")
	}
	got, _ := d.Revive().Get(id)
	if got.Attempts != 1 {
		t.Fatalf("после MarkRunning: %+v", got)
	}
	if ok, err := d.Revive().BackToWaitingNoAttempt(id, "движок занят другим заданием", reviveT0); err != nil || !ok {
		t.Fatalf("back to waiting no attempt: %v %v", ok, err)
	}
	got, _ = d.Revive().Get(id)
	if got.Status != ReviveWaiting || got.Attempts != 0 || got.LastError != "движок занят другим заданием" || got.ReachableProbes != 0 {
		t.Fatalf("попытка обязана быть отменена: %+v", got)
	}
}

// Attempts не имеет права уйти в отрицательное значение (защитный MAX(...,0)
// на случай гонки/повторного вызова).
func TestRevive_BackToWaitingNoAttemptNeverGoesNegative(t *testing.T) {
	d, id := newTestDBForRevive(t)
	putWaiting(t, d, id)
	if ok, _ := d.Revive().MarkRunning(id, reviveT0); !ok {
		t.Fatal("mark running")
	}
	if ok, _ := d.Revive().BackToWaitingNoAttempt(id, "тест", reviveT0); !ok {
		t.Fatal("первый возврат")
	}
	if ok, _ := d.Revive().MarkRunning(id, reviveT0); !ok {
		t.Fatal("mark running снова")
	}
	// Искусственно обнуляем attempts прямо в базе, чтобы проверить защиту от ухода в минус.
	if _, err := d.SQL().Exec(`UPDATE revive_intents SET attempts = 0 WHERE user_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.Revive().BackToWaitingNoAttempt(id, "тест2", reviveT0); !ok {
		t.Fatal("второй возврат")
	}
	got, _ := d.Revive().Get(id)
	if got.Attempts != 0 {
		t.Fatalf("не имеет права уйти в минус: %+v", got)
	}
}

// Fix round 1, Important #2 (мандатное ревью): беспарольный сторож.
// Просроченные намерения (waiting ИЛИ running) обязаны стираться независимо
// от ключа оживления -- решение оператора «затем стирается» действует и
// когда revive.key_file потерян. Непросроченные не трогает.
func TestRevive_ExpireOverdueWipesOnlyOverdueWaitingAndRunning(t *testing.T) {
	d, waitingID := newTestDBForRevive(t)
	runningID, err := d.Users().Insert("gachi", "tok-gachi", "198.51.100.21", "awg1")
	if err != nil {
		t.Fatal(err)
	}
	freshID, err := d.Users().Insert("elysia", "tok-elysia", "198.51.100.22", "awg2")
	if err != nil {
		t.Fatal(err)
	}

	now := reviveT0.Add(48 * time.Hour)
	overdue := reviveT0 // в прошлом относительно now
	fresh := now.Add(24 * time.Hour)

	if err := d.Revive().Put(ReviveIntent{RouterID: waitingID, CreatedAt: reviveT0, ExpiresAt: overdue}, []byte("nonce-12byte"), []byte("cipher-w")); err != nil {
		t.Fatal(err)
	}
	if err := d.Revive().Put(ReviveIntent{RouterID: runningID, CreatedAt: reviveT0, ExpiresAt: overdue}, []byte("nonce-12byte"), []byte("cipher-r")); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.Revive().MarkRunning(runningID, reviveT0); err != nil || !ok {
		t.Fatalf("mark running: %v %v", ok, err)
	}
	if err := d.Revive().Put(ReviveIntent{RouterID: freshID, CreatedAt: reviveT0, ExpiresAt: fresh}, []byte("nonce-12byte"), []byte("cipher-f")); err != nil {
		t.Fatal(err)
	}

	n, err := d.Revive().ExpireOverdue(now, "срок оживления вышел")
	if err != nil {
		t.Fatalf("expire overdue: %v", err)
	}
	if n != 2 {
		t.Fatalf("просроченных обязано быть 2, получили %d", n)
	}

	gotW, _ := d.Revive().Get(waitingID)
	if gotW.Status != ReviveExpired || gotW.LastError != "срок оживления вышел" {
		t.Fatalf("waiting: %+v", gotW)
	}
	if _, _, ok, _ := d.Revive().Secret(waitingID); ok {
		t.Fatal("секрет просроченного waiting обязан быть стёрт")
	}

	gotR, _ := d.Revive().Get(runningID)
	if gotR.Status != ReviveExpired {
		t.Fatalf("running: %+v", gotR)
	}
	if _, _, ok, _ := d.Revive().Secret(runningID); ok {
		t.Fatal("секрет просроченного running обязан быть стёрт")
	}

	gotF, _ := d.Revive().Get(freshID)
	if gotF.Status != ReviveWaiting {
		t.Fatalf("непросроченное намерение не должно было тронуться: %+v", gotF)
	}
	if _, _, ok, _ := d.Revive().Secret(freshID); !ok {
		t.Fatal("секрет непросроченного намерения обязан остаться -- ключ может вернуться")
	}
}

func TestUsers_SetAWGMURLIfEmpty(t *testing.T) {
	d, id := newTestDBForRevive(t)
	ok, err := d.Users().SetAWGMURLIfEmpty(id, "https://awg.example.com")
	if err != nil || !ok {
		t.Fatalf("первая запись: %v %v", ok, err)
	}
	ok, err = d.Users().SetAWGMURLIfEmpty(id, "https://other.example.com")
	if err != nil || ok {
		t.Fatalf("поверх существующего адреса писать нельзя: %v %v", ok, err)
	}
	u, _ := d.Users().GetByID(id)
	if u.AWGMURL == nil || *u.AWGMURL != "https://awg.example.com" {
		t.Fatalf("адрес: %v", u.AWGMURL)
	}
}
