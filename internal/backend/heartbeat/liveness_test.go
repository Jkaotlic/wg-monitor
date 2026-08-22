// internal/backend/heartbeat/liveness_test.go
package heartbeat

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Наблюдаемость сторожа: до этих счётчиков единственным доказательством того,
// что цикл жив, был сам факт прихода алерта. Молчащий сторож и сторож, которому
// не о чем сообщить, выглядели снаружи одинаково -- и отличить их было нечем.
func TestScanPublishesLivenessCounters(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "2222222222222222222222222222222222222222222222222222222222222222"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-10*time.Minute))

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})

	scansBefore := metricScans.Value()
	sentBefore := metricOfflineSent.Value()

	driveScan(w, now)

	if got := metricScans.Value(); got != scansBefore+1 {
		t.Fatalf("scans_total: было %d, стало %d", scansBefore, got)
	}
	if got := metricLastScanUnix.Value(); got != now.Unix() {
		t.Fatalf("last_scan_unix: %d, ждали %d", got, now.Unix())
	}
	if got := metricStaleUsers.Value(); got != 1 {
		t.Fatalf("stale_users: %d, ждали 1", got)
	}
	if got := metricOfflineSent.Value(); got != sentBefore+1 {
		t.Fatalf("offline_sent_total: было %d, стало %d", sentBefore, got)
	}
}

// Свежий роутер обнуляет счётчик просрочки: иначе gauge застревал бы на
// последнем ненулевом значении и врал бы о состоянии парка.
func TestScanResetsStaleGaugeWhenFleetHealthy(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "3333333333333333333333333333333333333333333333333333333333333333"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now)

	w := NewWatcher(d, &fakeOffline{}, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})
	metricStaleUsers.Set(7)

	driveScan(w, now)

	if got := metricStaleUsers.Value(); got != 0 {
		t.Fatalf("stale_users: %d, ждали 0", got)
	}
}

type blockingOffline struct {
	entered chan struct{}
}

func (b *blockingOffline) SendOffline(ctx context.Context, _ int64, _ string, _ time.Duration) error {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

// Один зависший sendMessage не имеет права заморозить обход парка: у каждой
// отправки свой дедлайн, и сканирование продолжается со следующим роутером.
// Без этого молчание одного Telegram-запроса означало молчание всего
// мониторинга -- и именно так это молчание было бы неотличимо от «всё хорошо».
func TestStuckSendDoesNotFreezeScan(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "4444444444444444444444444444444444444444444444444444444444444444"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	tok2 := "5555555555555555555555555555555555555555555555555555555555555555"
	uid2, _ := d.Users().Insert("petya", tok2, "1.1.1.2", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-10*time.Minute))
	d.Events().Insert(uid2, "agent_heartbeat", "ok", "", now.Add(-10*time.Minute))

	blocked := &blockingOffline{entered: make(chan struct{}, 1)}
	w := NewWatcher(d, blocked, Config{
		StaleAfter:  5 * time.Minute,
		ScanEvery:   time.Hour,
		SendTimeout: 50 * time.Millisecond,
	})
	errsBefore := metricOfflineErrors.Value()

	done := make(chan struct{})
	go func() { driveScan(w, now); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scan завис на отправке -- дедлайн отправки не сработал")
	}
	if got := metricOfflineErrors.Value(); got != errsBefore+2 {
		t.Fatalf("offline_errors_total: было %d, стало %d (ждали +2)", errsBefore, got)
	}
}

// Дедлайн отправки не должен превращать отмену всего сторожа в «ошибку
// отправки»: при остановке backend'а мы гасим цикл, а не жалуемся на Telegram.
func TestSendTimeoutDefaultsWhenUnset(t *testing.T) {
	w := NewWatcher(nil, nil, Config{})
	if got := w.sendTimeout(); got != defaultSendTimeout {
		t.Fatalf("sendTimeout: %v, ждали %v", got, defaultSendTimeout)
	}
	w2 := NewWatcher(nil, nil, Config{SendTimeout: time.Second})
	if got := w2.sendTimeout(); got != time.Second {
		t.Fatalf("sendTimeout: %v, ждали 1s", got)
	}
}

// «Увидел четверых просроченных, не отправил ни одной тревоги» -- это либо
// поломка, либо послушание: оператор мог сам заглушить роутер. Снаружи эти
// два случая до сих пор выглядели одинаково, и разбираться приходилось в
// базе. Счётчик заглушённых отвечает на вопрос сразу.
func TestScanCountsSuppressedSeparatelyFromSent(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "6666666666666666666666666666666666666666666666666666666666666666"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-10*time.Minute))
	st, _ := d.State().Get(uid, "agent_heartbeat")
	st.Acked = true
	if err := d.State().Save(uid, "agent_heartbeat", st); err != nil {
		t.Fatal(err)
	}

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})
	sentBefore := metricOfflineSent.Value()

	driveScan(w, now)

	if got := len(off.snapshot()); got != 0 {
		t.Fatalf("заглушённому роутеру ушло %d тревог", got)
	}
	if got := metricSuppressed.Value(); got != 1 {
		t.Fatalf("заглушённых: %d, ждали 1", got)
	}
	if got := metricStaleUsers.Value(); got != 1 {
		t.Fatalf("просроченных: %d, ждали 1 -- заглушённый всё равно просрочен", got)
	}
	if got := metricOfflineSent.Value(); got != sentBefore {
		t.Fatalf("счётчик отправленных сдвинулся на %d", got-sentBefore)
	}
}

// «Понятно, вижу» обещает: напомню ПОСЛЕ восстановления. Снятие этой отметки
// висело на условии current_status == "hard" -- а отметка живёт дольше
// самого состояния. Роутер, чей инцидент к моменту возвращения уже не значился
// «hard», возвращался с непогашенным ack и больше не получал тревог никогда.
//
// Ровно это и случилось с боевым роутером: сторож видел его просроченным,
// считал заглушённым и молчал сутки.
func TestFreshRouterClearsStickyAckEvenWhenStatusIsNotHard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "7777777777777777777777777777777777777777777777777777777777777777"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now)

	st, _ := d.State().Get(uid, "agent_heartbeat")
	st.CurrentStatus = "ok"
	st.Acked = true
	if err := d.State().Save(uid, "agent_heartbeat", st); err != nil {
		t.Fatal(err)
	}

	w := NewWatcher(d, &fakeOffline{}, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})
	driveScan(w, now)

	got, err := d.State().Get(uid, "agent_heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if got.Acked {
		t.Fatal("роутер вернулся, а отметка «вижу проблему» осталась -- тревог по нему больше не будет")
	}
}

// Пока роутер молчит, ack держится: он и означает «знаю, не напоминай».
func TestStaleRouterKeepsAckUntilItComesBack(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "8888888888888888888888888888888888888888888888888888888888888888"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-2*time.Hour))
	st, _ := d.State().Get(uid, "agent_heartbeat")
	st.Acked = true
	if err := d.State().Save(uid, "agent_heartbeat", st); err != nil {
		t.Fatal(err)
	}

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})
	driveScan(w, now)

	if len(off.snapshot()) != 0 {
		t.Fatal("заглушённому роутеру ушла тревога")
	}
	got, _ := d.State().Get(uid, "agent_heartbeat")
	if !got.Acked {
		t.Fatal("отметка снята с роутера, который так и не вернулся")
	}
}

// Тишина «до утра» -- обещание со сроком, и возвращение роутера её не
// отменяет: оператор просил не беспокоить до утра, а не до первого ответа.
func TestFreshRouterKeepsOperatorSilence(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "9999999999999999999999999999999999999999999999999999999999999999"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now)

	until := now.Add(6 * time.Hour)
	st, _ := d.State().Get(uid, "agent_heartbeat")
	st.CurrentStatus = "hard"
	st.SilencedUntil = &until
	if err := d.State().Save(uid, "agent_heartbeat", st); err != nil {
		t.Fatal(err)
	}

	w := NewWatcher(d, &fakeOffline{}, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})
	driveScan(w, now)

	got, _ := d.State().Get(uid, "agent_heartbeat")
	if got.SilencedUntil == nil {
		t.Fatal("тишина до утра снята возвращением роутера")
	}
}
