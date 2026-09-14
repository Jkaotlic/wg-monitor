package updatespoll

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type askedCommand struct {
	userID int64
	action string
}

type fakeSink struct {
	mu   sync.Mutex
	sent []askedCommand
}

func (f *fakeSink) Enqueue(userID int64, cmd wire.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, askedCommand{userID: userID, action: cmd.Action})
	return nil
}

func (f *fakeSink) all() []askedCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]askedCommand(nil), f.sent...)
}

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/d.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

var fixedNow = time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)

// Спящему роутеру команду в очередь не ставим.
//
// На этом уже наступали с деплоем (память deploy-ttl-vs-sleeping-router): у
// команды есть TTL, мобильный роутер просыпается позже, и к его пробуждению
// команда протухает. Очередь при этом копит мусор, а версии всё равно не
// приезжают.
func TestPoller_SkipsSleepingRouters(t *testing.T) {
	d := openDB(t)
	awake, err := d.Users().Insert("router-awake", "tok-awake-000000000000000000000000000000", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	asleep, err := d.Users().Insert("router-asleep", "tok-asleep-00000000000000000000000000000", "198.51.100.11", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	// Первый отчитался минуту назад, второй -- трое суток назад.
	if err := d.Events().Insert(awake, "dns", "ok", "{}", fixedNow.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(asleep, "dns", "ok", "{}", fixedNow.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}

	sink := &fakeSink{}
	p := NewPoller(d, sink, Config{})
	p.SetNow(func() time.Time { return fixedNow })
	p.TickForTest(context.Background())

	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("команд поставлено %d, а спросить надо было только бодрствующего: %+v", len(got), got)
	}
	if got[0].userID != awake {
		t.Errorf("спросили не тот роутер: %+v", got[0])
	}
	if got[0].action != "version_audit" {
		t.Errorf("действие %q, а доспрашивать надо version_audit", got[0].action)
	}
}

// Не чаще раза в сутки на роутер: опрос нужен ради двух полей, которых нет в
// обычном отчёте, и превращать его в постоянный фон на парке незачем.
func TestPoller_RunsOncePerDay(t *testing.T) {
	d := openDB(t)
	uid, err := d.Users().Insert("router-awake", "tok-awake-000000000000000000000000000000", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "dns", "ok", "{}", fixedNow.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	sink := &fakeSink{}
	now := fixedNow
	p := NewPoller(d, sink, Config{})
	p.SetNow(func() time.Time { return now })

	p.TickForTest(context.Background())
	p.TickForTest(context.Background())
	if got := sink.all(); len(got) != 1 {
		t.Fatalf("за один день команд %d, а должна быть одна: %+v", len(got), got)
	}

	// Сутки прошли -- спрашиваем снова. Отчёт двигаем следом, иначе роутер
	// станет «молчащим» и его пропустят по другой причине.
	now = fixedNow.Add(25 * time.Hour)
	if err := d.Events().Insert(uid, "dns", "ok", "{}", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	p.TickForTest(context.Background())
	if got := sink.all(); len(got) != 2 {
		t.Fatalf("через сутки команд %d, а должно стать две: %+v", len(got), got)
	}
}

// Ни одного похода в GitHub из поллера.
//
// Лимит анонимного API -- 60 запросов в час, поллер по парку сжёг бы его за
// минуты, а ошибка кэшируется на 12 часов: новости молча исчезли бы со всех
// экранов. Сравнение живёт только в общем upstream.Cache, и поллер про него не
// знает вовсе. Транспорт по умолчанию здесь подменён на ловушку -- любой
// незамеченный http-клиент внутри поллера уронит этот тест.
func TestPoller_NeverCallsGitHubDirectly(t *testing.T) {
	trap := &trapTransport{t: t}
	saved := http.DefaultTransport
	http.DefaultTransport = trap
	t.Cleanup(func() { http.DefaultTransport = saved })

	d := openDB(t)
	uid, err := d.Users().Insert("router-awake", "tok-awake-000000000000000000000000000000", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "dns", "ok", "{}", fixedNow.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	sink := &fakeSink{}
	p := NewPoller(d, sink, Config{})
	p.SetNow(func() time.Time { return fixedNow })
	p.TickForTest(context.Background())

	if n := trap.count(); n != 0 {
		t.Errorf("поллер сходил наружу %d раз(а), а не должен ни разу", n)
	}
}

type trapTransport struct {
	t  *testing.T
	mu sync.Mutex
	n  int
}

func (tr *trapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	tr.n++
	tr.mu.Unlock()
	tr.t.Errorf("поллер полез в сеть: %s", req.URL)
	return nil, http.ErrUseLastResponse
}

func (tr *trapTransport) count() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.n
}
