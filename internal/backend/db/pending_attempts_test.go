package db

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func pendingTestRouter(t *testing.T) (*DB, int64) {
	t.Helper()
	d := newTestDB(t)
	uid, err := d.Users().Insert("bronya", strings.Repeat("7", 64), "198.51.100.7", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	return d, uid
}

func TestMigrationAddsPendingAttemptColumns(t *testing.T) {
	d, _ := pendingTestRouter(t)
	var notNull int
	var dflt string
	if err := d.SQL().QueryRow(
		`SELECT "notnull", dflt_value FROM pragma_table_info('users') WHERE name='pending_attempts'`,
	).Scan(&notNull, &dflt); err != nil {
		t.Fatalf("нет колонки pending_attempts: %v", err)
	}
	if notNull != 1 || dflt != "0" {
		t.Fatalf("pending_attempts: notnull=%d default=%q, ждали NOT NULL DEFAULT 0", notNull, dflt)
	}
	var n int
	if err := d.SQL().QueryRow(
		`SELECT count(*) FROM pragma_table_info('users') WHERE name='pending_last_error'`,
	).Scan(&n); err != nil || n != 1 {
		t.Fatalf("нет колонки pending_last_error: n=%d err=%v", n, err)
	}
}

func TestPendingDeployCountsAttemptsAndKeepsLastError(t *testing.T) {
	d, uid := pendingTestRouter(t)
	if err := d.Users().MarkPendingDeploy(uid, "v0.32.0", "2026-09-11T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 2; want++ {
		got, matched, err := d.Users().IncrementPendingAttempts(uid, "v0.32.0")
		if err != nil || !matched || got != want {
			t.Fatalf("попытка %d: got=%d matched=%v err=%v", want, got, matched, err)
		}
	}
	attempts, matched, err := d.Users().RecordPendingDeployError(uid, "v0.32.0", "download checksums.txt: HTTP 502")
	if err != nil || !matched || attempts != 2 {
		t.Fatalf("запись ошибки: attempts=%d matched=%v err=%v", attempts, matched, err)
	}
	st, err := d.Users().PendingDeploy(uid)
	if err != nil {
		t.Fatal(err)
	}
	want := PendingDeployState{Version: "v0.32.0", Since: "2026-09-11T10:00:00Z", Attempts: 2, LastError: "download checksums.txt: HTTP 502"}
	if st != want {
		t.Fatalf("состояние = %+v, ждали %+v", st, want)
	}
	all, err := d.Users().PendingDeployStates()
	if err != nil {
		t.Fatal(err)
	}
	if all[uid] != want {
		t.Fatalf("PendingDeployStates()[%d] = %+v, ждали %+v", uid, all[uid], want)
	}
}

// Поздний ответ на команду прошлой цели не имеет права двигать счёт новой.
func TestPendingAttemptsIgnoreOtherTarget(t *testing.T) {
	d, uid := pendingTestRouter(t)
	if err := d.Users().MarkPendingDeploy(uid, "v0.32.0", "2026-09-11T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if n, matched, err := d.Users().IncrementPendingAttempts(uid, "v0.31.0"); err != nil || matched || n != 0 {
		t.Fatalf("чужая цель: n=%d matched=%v err=%v", n, matched, err)
	}
	if n, matched, err := d.Users().RecordPendingDeployError(uid, "v0.31.0", "boom"); err != nil || matched || n != 0 {
		t.Fatalf("чужая цель, ошибка: n=%d matched=%v err=%v", n, matched, err)
	}
	st, _ := d.Users().PendingDeploy(uid)
	if st.Attempts != 0 || st.LastError != "" {
		t.Fatalf("чужая цель сдвинула счёт: %+v", st)
	}
}

func TestPendingDeployGiveUpKeepsReasonUntilNewMark(t *testing.T) {
	d, uid := pendingTestRouter(t)
	_ = d.Users().MarkPendingDeploy(uid, "v0.32.0", "2026-09-11T10:00:00Z")
	_, _, _ = d.Users().IncrementPendingAttempts(uid, "v0.32.0")
	_, _, _ = d.Users().RecordPendingDeployError(uid, "v0.32.0", "insufficient /opt space")
	if cleared, err := d.Users().ClearPendingDeployIfMatches(uid, "v0.32.0"); err != nil || !cleared {
		t.Fatalf("сдача: cleared=%v err=%v", cleared, err)
	}
	st, _ := d.Users().PendingDeploy(uid)
	if st.Version != "" || st.Attempts != 1 || st.LastError != "insufficient /opt space" {
		t.Fatalf("после сдачи причина обязана остаться: %+v", st)
	}
	if err := d.Users().MarkPendingDeploy(uid, "v0.32.1", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	st, _ = d.Users().PendingDeploy(uid)
	if st.Version != "v0.32.1" || st.Attempts != 0 || st.LastError != "" {
		t.Fatalf("новая постановка обязана обнулить счёт: %+v", st)
	}
}

func TestClearPendingDeployResetsAttempts(t *testing.T) {
	d, uid := pendingTestRouter(t)
	_ = d.Users().MarkPendingDeploy(uid, "v0.32.0", "2026-09-11T10:00:00Z")
	_, _, _ = d.Users().IncrementPendingAttempts(uid, "v0.32.0")
	_, _, _ = d.Users().RecordPendingDeployError(uid, "v0.32.0", "boom")
	cleared, err := d.Users().ClearPendingDeploy(uid)
	if err != nil || !cleared {
		t.Fatalf("отмена: cleared=%v err=%v", cleared, err)
	}
	if st, _ := d.Users().PendingDeploy(uid); st != (PendingDeployState{}) {
		t.Fatalf("после отмены всё пусто, got %+v", st)
	}
}

// Отмена после сдачи: отметки уже нет (cleared=false, как раньше), но
// причина неудачи тоже уходит -- человек сказал «хватит».
func TestClearPendingDeployAfterGiveUpDropsReason(t *testing.T) {
	d, uid := pendingTestRouter(t)
	_ = d.Users().MarkPendingDeploy(uid, "v0.32.0", "2026-09-11T10:00:00Z")
	_, _, _ = d.Users().RecordPendingDeployError(uid, "v0.32.0", "boom")
	_, _ = d.Users().ClearPendingDeployIfMatches(uid, "v0.32.0")
	cleared, err := d.Users().ClearPendingDeploy(uid)
	if err != nil || cleared {
		t.Fatalf("отметки не было: cleared=%v err=%v", cleared, err)
	}
	if st, _ := d.Users().PendingDeploy(uid); st.LastError != "" || st.Attempts != 0 {
		t.Fatalf("причина осталась после отмены: %+v", st)
	}
}

func TestHeartbeatConfirmingTargetResetsAttempts(t *testing.T) {
	d, uid := pendingTestRouter(t)
	_ = d.Users().MarkPendingDeploy(uid, "v0.32.0", "2026-09-11T10:00:00Z")
	_, _, _ = d.Users().IncrementPendingAttempts(uid, "v0.32.0")
	_, _, _ = d.Users().RecordPendingDeployError(uid, "v0.32.0", "boom")
	upd, err := d.Users().UpdateLastSeenAgentVersionResult(uid, "v0.32.0")
	if err != nil || !upd.PendingCleared {
		t.Fatalf("подтверждение: %+v err=%v", upd, err)
	}
	if st, _ := d.Users().PendingDeploy(uid); st != (PendingDeployState{}) {
		t.Fatalf("после подтверждения версии всё пусто, got %+v", st)
	}
}

func TestRecordPendingDeployErrorTruncatesOnRuneBoundary(t *testing.T) {
	d, uid := pendingTestRouter(t)
	_ = d.Users().MarkPendingDeploy(uid, "v0.32.0", "2026-09-11T10:00:00Z")
	if _, _, err := d.Users().RecordPendingDeployError(uid, "v0.32.0", strings.Repeat("я", 3000)); err != nil {
		t.Fatal(err)
	}
	st, _ := d.Users().PendingDeploy(uid)
	if len(st.LastError) > MaxPendingLastErrorBytes || !utf8.ValidString(st.LastError) || st.LastError == "" {
		t.Fatalf("обрезка: len=%d valid=%v", len(st.LastError), utf8.ValidString(st.LastError))
	}
}
