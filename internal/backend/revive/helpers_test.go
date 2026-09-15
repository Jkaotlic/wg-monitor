package revive

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
type fakeEngine struct {
	mu        sync.Mutex
	launches  []launchCall
	launchErr error
	outcome   Outcome
	lost      bool
}

func (f *fakeEngine) Launch(_ context.Context, routerID int64, s Secrets, target string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches = append(f.launches, launchCall{routerID, s, target})
	if f.launchErr != nil {
		return "", f.launchErr
	}
	return fmt.Sprintf("job-%d", len(f.launches)), nil
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

func (n *fakeNotifier) Send(_ context.Context, routerID int64, text, _ string) (int, error) {
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

// scriptedProbe отдаёт состояния по очереди; последнее повторяется.
type scriptedProbe struct {
	mu     sync.Mutex
	states []string
	calls  int
}

func (p *scriptedProbe) Probe(context.Context, string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.calls
	if i >= len(p.states) {
		i = len(p.states) - 1
	}
	p.calls++
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
	return svc
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
