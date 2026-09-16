package revive

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

var testT0 = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type launchCall struct {
	RouterID int64
	Secrets  Secrets
	Target   string
}

// fakeEngine -- движок переустановки. Итог одинаков для всех заданий и
// задаётся тестом; lost=true -- задание «потерялось» (TTL/рестарт).
//
// launchBlock/launchEntered (Fix round 1, Important #1): дают тесту застать
// Launch НА СЕРЕДИНЕ -- launchEntered закрывается сразу, как только Launch
// вызван (то есть MarkRunning уже прошёл), а сам вызов виснет на
// launchBlock, пока тест его не закроет. Так тест проверяет, что s.work
// отпущен ДО похода в "сеть" (здесь -- до этого зависания), а не только
// после него.
type fakeEngine struct {
	mu            sync.Mutex
	launches      []launchCall
	launchErr     error
	outcome       Outcome
	lost          bool
	launchBlock   chan struct{}
	launchEntered chan struct{}
}

func (f *fakeEngine) Launch(_ context.Context, routerID int64, s Secrets, target string) (string, error) {
	f.mu.Lock()
	f.launches = append(f.launches, launchCall{routerID, s, target})
	n := len(f.launches)
	err := f.launchErr
	block := f.launchBlock
	entered := f.launchEntered
	f.mu.Unlock()

	if entered != nil {
		close(entered)
	}
	if block != nil {
		<-block
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("job-%d", n), nil
}

func (f *fakeEngine) Outcome(string) (Outcome, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lost {
		return Outcome{}, false
	}
	return f.outcome, true
}

func (f *fakeEngine) set(fn func(*fakeEngine)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

func (f *fakeEngine) calls() []launchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]launchCall(nil), f.launches...)
}

type sentNotice struct {
	RouterID int64
	Text     string
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent []sentNotice
}

// Send отказывает на отменённом/просроченном ctx -- как отказал бы настоящий
// http-based Notifier. Нужно, чтобы тест "уведомление уходит даже при
// отменённом ctx вызывающего" (fix round 2, Minor #2) реально отличал
// context.WithoutCancel от переданного как есть отменённого ctx, а не просто
// молча писал в sent независимо от него.
func (n *fakeNotifier) Send(ctx context.Context, routerID int64, text, _ string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, sentNotice{routerID, text})
	return 1, nil
}

func (n *fakeNotifier) all() []sentNotice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]sentNotice(nil), n.sent...)
}

// scriptedProbe отдаёт состояния по очереди (последнее повторяется) или, если
// задан delegate, зовёт настоящий Prober -- так тесты воркера ходят в
// httptest-панель.
type scriptedProbe struct {
	mu       sync.Mutex
	states   []string
	calls    int
	delegate func(ctx context.Context, awgmURL string) string
}

func (p *scriptedProbe) Probe(ctx context.Context, awgmURL string) string {
	p.mu.Lock()
	p.calls++
	if d := p.delegate; d != nil {
		p.mu.Unlock()
		return d(ctx, awgmURL)
	}
	defer p.mu.Unlock()
	i := p.calls - 1
	if i >= len(p.states) {
		i = len(p.states) - 1
	}
	return p.states[i]
}

func (p *scriptedProbe) script(states ...string) {
	p.mu.Lock()
	p.states, p.calls = states, 0
	p.mu.Unlock()
}
func (p *scriptedProbe) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

type testEnv struct {
	svc      *Service
	db       *db.DB
	router   int64
	engine   *fakeEngine
	notifier *fakeNotifier
	clock    *fakeClock
	probe    *scriptedProbe
	logs     *syncBuffer
	key      []byte
	// panelURL -- адрес httptest-панели (http://127.0.0.1:...). Проверка
	// безопасности адреса перед запуском пропускает РОВНО его: иначе тесты
	// воркера с настоящим Prober не дошли бы до запуска.
	panelURL string
}

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// newEnv -- сервис на временной базе с роутером «bronya». По умолчанию у
// роутера есть адрес панели, агент молчит (last_seen_at NULL), панель
// «не отвечает».
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "revive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("bronya", "tok-bronya", "198.51.100.20", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.Users().SetAWGMURLIfEmpty(id, "https://awg.example.com"); err != nil || !ok {
		t.Fatalf("awgm_url: %v %v", ok, err)
	}
	env := &testEnv{
		db: d, router: id,
		engine:   &fakeEngine{outcome: Outcome{}},
		notifier: &fakeNotifier{},
		clock:    &fakeClock{t: testT0},
		probe:    &scriptedProbe{states: []string{awgmstate.Offline}},
		logs:     &syncBuffer{},
		key:      randomKey(t),
	}
	env.svc = env.newService(t, env.key)
	return env
}

func (e *testEnv) newService(t *testing.T, key []byte) *Service {
	t.Helper()
	svc, err := New(Config{
		DB: e.db, Key: append([]byte(nil), key...), Engine: e.engine, Notifier: e.notifier,
		Probe: e.probe.Probe, Now: e.clock.Now,
		Sleep:   func(_ context.Context, d time.Duration) bool { e.clock.Advance(d); return true },
		BaseCtx: context.Background(),
		Logger:  slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Wait)
	e.allowTestPanel(svc)
	return svc
}

func (e *testEnv) allowTestPanel(svc *Service) {
	if e.panelURL == "" {
		return
	}
	allowed := e.panelURL
	svc.panelURLSafe = func(raw string) bool {
		return raw == allowed || panelURLSafe(raw)
	}
}

func (e *testEnv) setLastSeen(t *testing.T, ts time.Time) {
	t.Helper()
	if _, err := e.db.SQL().Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, ts.UTC().Format(time.RFC3339Nano), e.router); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) clearAWGMURL(t *testing.T) {
	t.Helper()
	if _, err := e.db.SQL().Exec(`UPDATE users SET awgm_url = NULL WHERE id = ?`, e.router); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) intent(t *testing.T) *db.ReviveIntent {
	t.Helper()
	in, err := e.db.Revive().Get(e.router)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func (e *testEnv) hasSecret(t *testing.T) bool {
	t.Helper()
	_, _, ok, err := e.db.Revive().Secret(e.router)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func fixtureRequest() ScheduleRequest {
	return ScheduleRequest{RootPassword: fixtureRoot, AWGMLogin: fixtureLogin, AWGMPassword: fixturePanel, AWGMAPIKey: fixtureAPIKey, RequestedBy: 42}
}

// intentRowsDump -- все строки revive_intents текстом.
func intentRowsDump(t *testing.T, d *db.DB) []byte {
	t.Helper()
	rows, err := d.SQL().Query(`SELECT * FROM revive_intents`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out bytes.Buffer
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			fmt.Fprintf(&out, "%v|", v)
			if b, ok := v.([]byte); ok {
				out.Write(b)
			}
		}
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// rawDBFiles -- сырые байты файла базы и его WAL.
func rawDBFiles(t *testing.T, d *db.DB) []byte {
	t.Helper()
	var out []byte
	for _, p := range []string{d.Path(), d.Path() + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out = append(out, b...)
	}
	return out
}

// testPanel -- панель роутера на httptest: отвечает кодами по очереди,
// последний повторяется; запоминает заголовок авторизации каждого обращения.
type testPanel struct {
	mu    sync.Mutex
	codes []int
	auths []string
}

func (p *testPanel) hits() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.auths) }

func (p *testPanel) authHeaders() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.auths...)
}

// panel поднимает httptest-панель, прописывает её адрес роутеру и включает
// настоящий Prober вместо сценария.
func (e *testEnv) panel(t *testing.T, codes ...int) *testPanel {
	t.Helper()
	p := &testPanel{codes: codes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		i := len(p.auths)
		if i >= len(p.codes) {
			i = len(p.codes) - 1
		}
		code := p.codes[i]
		p.auths = append(p.auths, r.Header.Get("Authorization"))
		p.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	if _, err := e.db.SQL().Exec(`UPDATE users SET awgm_url = ? WHERE id = ?`, srv.URL, e.router); err != nil {
		t.Fatal(err)
	}
	e.panelURL = srv.URL
	e.allowTestPanel(e.svc)
	prober := NewProber(time.Second)
	e.probe.mu.Lock()
	e.probe.delegate = prober.Probe
	e.probe.mu.Unlock()
	return p
}

// seedWaiting кладёт ожидающее намерение с секретами фикстуры в обход
// Schedule (без фоновой проверки после постановки).
func (e *testEnv) seedWaiting(t *testing.T) {
	t.Helper()
	box, err := NewBox(e.key)
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := box.Seal(e.router, fixtureSecrets())
	if err != nil {
		t.Fatal(err)
	}
	now := e.clock.Now()
	if err := e.db.Revive().Put(db.ReviveIntent{
		RouterID: e.router, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour), RequestedBy: 42,
	}, nonce, ct); err != nil {
		t.Fatal(err)
	}
}

// tick -- обход воркера и шаг часов на период опроса.
func (e *testEnv) tick(t *testing.T) {
	t.Helper()
	e.svc.Tick(context.Background())
	e.clock.Advance(DefaultProbeEvery)
}

var latinOutsideQuotes = regexp.MustCompile(`[A-Za-z]`)

// assertOwnerText: вне «ёлочек» латиницы нет, внутренних имён нет.
func assertOwnerText(t *testing.T, text string) {
	t.Helper()
	stripped := regexp.MustCompile(`«[^»]*»`).ReplaceAllString(text, "")
	if latinOutsideQuotes.MatchString(stripped) {
		t.Fatalf("латиница вне «ёлочек»: %q", text)
	}
	for _, bad := range []string{"awg", "relay", "backend", "heartbeat", "терминал"} {
		if strings.Contains(strings.ToLower(text), bad) {
			t.Fatalf("внутреннее имя %q в тексте: %q", bad, text)
		}
	}
}
