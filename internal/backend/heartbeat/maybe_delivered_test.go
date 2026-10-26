package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// errOffline -- отправка «не на связи», всегда падающая заданной ошибкой.
type errOffline struct {
	err error
	n   int
}

func (e *errOffline) SendOffline(context.Context, int64, string, time.Duration) error {
	e.n++
	return e.err
}

func (e *errOffline) SendBackOnline(context.Context, int64, string, time.Time) error { return nil }

func offlineFixture(t *testing.T, nick, tok, ip string) (*db.DB, int64, time.Time) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	uid, err := d.Users().Insert(nick, tok, ip, "awg0")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC()
	if err := d.Events().Insert(uid, "agent_heartbeat", "ok", "", t0.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	return d, uid, t0
}

// FRESH-02: Telegram ответил медленно -- срок контекста вышел, а сообщение
// могло уже дойти. Повтор через 1 и 5 минут слал владельцу дубли «Роутер не
// на связи». «Могло дойти» -- как у HARD-тревог: считаем тревогу ушедшей.
func TestWatcherOfflineMaybeDeliveredIsNotRepeated(t *testing.T) {
	d, uid, t0 := offlineFixture(t, "slowtg",
		"3535353535353535353535353535353535353535353535353535353535353535", "198.51.100.35")
	off := &errOffline{err: fmt.Errorf("%w: %w", notify.ErrNoneDelivered, context.DeadlineExceeded)}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: 30 * time.Second})

	driveScan(w, t0)
	driveScan(w, t0.Add(2*time.Minute))
	if off.n != 1 {
		t.Fatalf("тревога, которая могла дойти, отправлена %d раз, ждали 1", off.n)
	}
	st, err := d.State().Get(uid, "agent_heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentStatus != "hard" {
		t.Fatalf("agent_heartbeat = %q, ждали hard: тревога считается ушедшей", st.CurrentStatus)
	}
}

// Обратный случай: Telegram заведомо отказал -- сообщения у владельца нет,
// повтор после паузы обязателен, отметки hard нет.
func TestWatcherOfflineKnownNotSentIsRetried(t *testing.T) {
	d, uid, t0 := offlineFixture(t, "refused",
		"3636363636363636363636363636363636363636363636363636363636363636", "198.51.100.36")
	notSent := tg.NotSent(errors.New("dial tcp: connection refused"))
	off := &errOffline{err: fmt.Errorf("%w: получателей 1: %w", notify.ErrNoneDelivered, notSent)}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: 30 * time.Second})

	driveScan(w, t0)
	driveScan(w, t0.Add(2*time.Minute))
	if off.n != 2 {
		t.Fatalf("заведомо неотправленная тревога: попыток %d, ждали 2", off.n)
	}
	st, err := d.State().Get(uid, "agent_heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentStatus == "hard" {
		t.Fatal("тревога не дошла, а сторож записал hard")
	}
}

// «Могло дойти» -- не ошибка: тревога, скорее всего, у владельца. В сводке
// панели она шла счётчиком ошибок и «последней ошибкой», и оператор искал
// поломку там, где её нет. Теперь у неё свой счётчик и своя строка.
func TestWatcherOfflineMaybeDeliveredIsNotAnError(t *testing.T) {
	d, _, t0 := offlineFixture(t, "maybe-tg",
		"3737373737373737373737373737373737373737373737373737373737373737", "198.51.100.37")
	off := &errOffline{err: fmt.Errorf("%w: %w", notify.ErrNoneDelivered, context.DeadlineExceeded)}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: 30 * time.Second})

	before := w.Snapshot()
	driveScan(w, t0)
	after := w.Snapshot()

	if got := after.OfflineErrors - before.OfflineErrors; got != 0 {
		t.Fatalf("«могло дойти» посчитано ошибкой: +%d", got)
	}
	if got := after.OfflineMaybeDelivered - before.OfflineMaybeDelivered; got != 1 {
		t.Fatalf("счётчик «могло дойти» +%d, ждали +1", got)
	}
	if nick, text, _ := w.LastOfflineError(); nick != "" || text != "" {
		t.Fatalf("«могло дойти» записано последней ошибкой: %q %q", nick, text)
	}
	if after.LastOfflineMaybeDeliveredRouter != "maybe-tg" || after.LastOfflineMaybeDelivered == "" ||
		!after.LastOfflineMaybeDeliveredAt.Equal(t0) {
		t.Fatalf("строка «могло дойти»: %q %q %v", after.LastOfflineMaybeDeliveredRouter,
			after.LastOfflineMaybeDelivered, after.LastOfflineMaybeDeliveredAt)
	}
}
