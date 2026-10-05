package awg3panel

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel/awg3paneltest"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type svcEnv struct {
	p    *awg3paneltest.Panel
	s    *Service
	path string
	clk  *testClock
	logs *bytes.Buffer
	opts Options
}

func newSvcEnv(t *testing.T, o awg3paneltest.Options) *svcEnv {
	t.Helper()
	p := startPanel(t, o)
	e := &svcEnv{p: p, path: filepath.Join(t.TempDir(), DefaultStoreName), clk: &testClock{t: time.Now()}, logs: &bytes.Buffer{}}
	e.opts = Options{RootCAs: p.CA.Pool, Now: e.clk.Now, Logger: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	e.s = NewService(e.path, e.opts)
	return e
}

func (e *svcEnv) input(t *testing.T, id, pw string) Input {
	t.Helper()
	pfx, err := e.p.CA.P12("anex", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), "p12-pw", false)
	if err != nil {
		t.Fatal(err)
	}
	return Input{ID: id, Label: "Main", BaseURL: e.p.URL, User: "admin", Password: pw, P12: pfx, P12Password: "p12-pw"}
}

// create -- годная панель; счётчики панели после неё обнулены.
func (e *svcEnv) create(t *testing.T, id string) {
	t.Helper()
	if _, res, err := e.s.Create(context.Background(), e.input(t, id, testPanelPass)); err != nil || !res.OK {
		t.Fatalf("создание: %+v %v", res, err)
	}
	e.p.ResetHits()
}

func respond429(w http.ResponseWriter, _ *http.Request) bool {
	http.Error(w, "слишком много неудачных попыток, попробуйте позже", http.StatusTooManyRequests)
	return true
}

func TestCreateChecksOnceAndStoresPEM(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	v, res, err := e.s.Create(context.Background(), e.input(t, "main", testPanelPass))
	if err != nil || !res.Ran || !res.OK || res.Ifaces != 1 {
		t.Fatalf("создание: %+v %v", res, err)
	}
	if e.p.TotalHits() != 1 || e.p.Hits("GET /api/ifaces") != 1 {
		t.Fatalf("проверка должна быть ровно одним GET /api/ifaces, было %d", e.p.TotalHits())
	}
	if !v.PasswordSet || !v.CertSet || v.CertSubject != "anex" || !v.Enabled || v.Lock != LockNone {
		t.Fatalf("вид: %+v", v)
	}
	raw, _ := os.ReadFile(e.path)
	if !bytes.Contains(raw, []byte("BEGIN CERTIFICATE")) || bytes.Contains(raw, []byte("p12-pw")) {
		t.Fatal("в файле нет PEM или лежит пароль .p12")
	}
	if fi, _ := os.Stat(e.path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("права: %v", fi.Mode())
	}
	if _, _, err := e.s.Create(context.Background(), e.input(t, "main", testPanelPass)); !errors.Is(err, ErrInstanceExists) {
		t.Fatalf("повтор id: %v", err)
	}
	if e.p.TotalHits() != 1 {
		t.Fatal("повтор id сходил в панель")
	}
}

func TestCreateBadP12MakesNoRequest(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	in := e.input(t, "main", testPanelPass)
	in.P12Password = "wrong"
	if _, _, err := e.s.Create(context.Background(), in); fieldOf(err) != "p12_password" {
		t.Fatalf("ждали отказ p12_password: %v", err)
	}
	if e.p.TotalHits() != 0 || e.p.Handshakes() != 0 {
		t.Fatal("с неверным .p12 бот сходил в панель")
	}
	if views, _ := e.s.List(); len(views) != 0 {
		t.Fatal("панель сохранена с неверным .p12")
	}
}

func TestBadPasswordLocksUntilResave(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	ctx := context.Background()
	_, res, err := e.s.Create(ctx, e.input(t, "main", "WRONG-PW-MUST-NOT-LEAK"))
	if err != nil || !res.Ran || res.OK || res.Kind != KindBadPassword {
		t.Fatalf("проверка: %+v %v", res, err)
	}
	for range 3 {
		if _, err := e.s.Peers(ctx, "main", ""); KindOf(err) != KindBadPassword {
			t.Fatalf("экран: %v", err)
		}
	}
	// Перезапуск бэкенда предохранитель не снимает.
	if _, err := NewService(e.path, e.opts).Peers(ctx, "main", ""); KindOf(err) != KindBadPassword {
		t.Fatalf("после перезапуска: %v", err)
	}
	if e.p.TotalHits() != 1 {
		t.Fatalf("после 401 ушло ещё %d запросов", e.p.TotalHits()-1)
	}
	// Правка названия -- не пересохранение учётных данных.
	_, res2, err := e.s.Update(ctx, "main", Input{Label: "Main 2", BaseURL: e.p.URL, User: "admin"})
	if err != nil || res2 != nil || e.p.TotalHits() != 1 {
		t.Fatalf("правка названия: %+v %v hits=%d", res2, err, e.p.TotalHits())
	}
	v, res3, err := e.s.Update(ctx, "main", Input{Label: "Main 2", BaseURL: e.p.URL, User: "admin", Password: testPanelPass})
	if err != nil || res3 == nil || !res3.OK || v.Lock != LockNone || e.p.TotalHits() != 2 {
		t.Fatalf("пересохранение: %+v %+v %v hits=%d", v, res3, err, e.p.TotalHits())
	}
	if _, err := e.s.Peers(ctx, "main", ""); err != nil {
		t.Fatalf("после пересохранения: %v", err)
	}
	if strings.Contains(e.logs.String(), "WRONG-PW") || strings.Contains(e.logs.String(), testPanelPass) {
		t.Fatal("пароль в журнале")
	}
}

func TestBanPausesFifteenMinutes(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	ctx := context.Background()
	e.p.SetOverride(respond429)
	_, err := e.s.Peers(ctx, "main", "")
	var pe *Error
	if !errors.As(err, &pe) || pe.Kind != KindBanned || !pe.Until.Equal(e.clk.Now().Add(DefaultPause)) {
		t.Fatalf("пауза: %v", err)
	}
	if _, err := e.s.Peers(ctx, "main", ""); KindOf(err) != KindBanned {
		t.Fatalf("второй раз: %v", err)
	}
	if e.p.TotalHits() != 1 {
		t.Fatalf("за паузу ушло %d запросов", e.p.TotalHits())
	}
	if views, _ := e.s.List(); !views[0].PausedUntil.Equal(pe.Until) {
		t.Fatal("пауза не видна в списке")
	}
	e.p.SetOverride(nil)
	e.clk.Add(DefaultPause + time.Second)
	if _, err := e.s.Peers(ctx, "main", ""); err != nil {
		t.Fatalf("после паузы: %v", err)
	}
}

func TestCertRejectedLocks(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	foreign, _ := awg3paneltest.NewCA("чужой")
	in := e.input(t, "main", testPanelPass)
	in.P12, _ = foreign.P12("anex", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "p12-pw", false)
	_, res, err := e.s.Create(context.Background(), in)
	if err != nil || res.Kind != KindCert {
		t.Fatalf("проверка: %+v %v", res, err)
	}
	h := e.p.Handshakes()
	if _, err := e.s.Peers(context.Background(), "main", ""); KindOf(err) != KindCert {
		t.Fatalf("экран: %v", err)
	}
	if e.p.Handshakes() != h {
		t.Fatal("после отказа TLS бот снова стучится в панель")
	}
	if views, _ := e.s.List(); views[0].Lock != LockCert {
		t.Fatalf("замок: %+v", views[0])
	}
}

func TestPeersSingleflightAndCache(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{Peers: map[string][]awg3paneltest.Peer{"awg1": {{ID: "aaaaaaaaaaa1", Name: "iphone", Enabled: true}}}})
	e.create(t, "main") // интерфейсы уже в кэше после проверки
	e.p.SetDelay(100 * time.Millisecond)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			page, err := e.s.Peers(context.Background(), "main", "")
			if err == nil && (page.Iface != "awg1" || len(page.Peers) != 1) {
				err = errors.New("не та страница")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if e.p.Hits("GET /api/ifaces/awg1/peers") != 1 || e.p.Hits("GET /api/ifaces/awg1/summary") != 1 || e.p.TotalHits() != 2 {
		t.Fatalf("10 параллельных открытий: %d запросов", e.p.TotalHits())
	}
	e.p.SetDelay(0)
	if _, err := e.s.Peers(context.Background(), "main", "awg1"); err != nil || e.p.TotalHits() != 2 {
		t.Fatalf("кэш 30 с: %v hits=%d", err, e.p.TotalHits())
	}
	e.clk.Add(DefaultCacheTTL + time.Second)
	if _, err := e.s.Peers(context.Background(), "main", ""); err != nil || e.p.TotalHits() != 5 {
		t.Fatalf("после 30 с: %v hits=%d", err, e.p.TotalHits())
	}
}

func TestPeersUnknownIfaceMakesNoRequest(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	if _, err := e.s.Peers(context.Background(), "main", "nope"); !errors.Is(err, ErrIfaceNotFound) {
		t.Fatalf("%v", err)
	}
	if e.p.TotalHits() != 0 {
		t.Fatal("неизвестный интерфейс спрошен у панели")
	}
}

func TestIssueDeviceDoubleClickMakesOnePeer(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			issued, err := e.s.IssueDevice(context.Background(), "main", "awg1", "iphone-anex")
			if err == nil && issued.QRPNGBase64 == "" {
				err = errors.New("нет QR")
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var ok, taken int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrNameTaken):
			taken++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || taken != 1 || e.p.Hits("POST ") != 1 || len(e.p.PeerList("awg1")) != 1 {
		t.Fatalf("ok=%d taken=%d post=%d", ok, taken, e.p.Hits("POST "))
	}
}

func TestIssueDeviceRejectsBadNames(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	for _, name := range []string{"", "   ", strings.Repeat("я", 41), "a[b]", "x\ny", "wgmon-iphone", "WGMON-laptop"} {
		if _, err := e.s.IssueDevice(context.Background(), "main", "awg1", name); fieldOf(err) != "name" {
			t.Errorf("%q: %v", name, err)
		}
	}
	if e.p.TotalHits() != 0 {
		t.Fatal("негодное имя ушло в панель")
	}
}

func TestConfigForRouterPicksNewestDuplicate(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{Peers: map[string][]awg3paneltest.Peer{"awg1": {
		{ID: "old000000001", Name: "wgmon-router-owned", Address: "10.66.0.2/32", Enabled: true, CreatedAt: "2026-09-01T10:00:00Z"},
		{ID: "new000000002", Name: "wgmon-router-owned", Address: "10.66.0.3/32", Enabled: true, CreatedAt: "2026-09-20T10:00:00Z"},
		{ID: "man000000003", Name: "iphone", Address: "10.66.0.4/32", Enabled: true, CreatedAt: "2026-09-25T10:00:00Z"},
	}}})
	e.create(t, "main")
	rc, err := e.s.ConfigForRouter(context.Background(), "main", "awg1", "router-owned")
	if err != nil || !rc.Reused || rc.PeerID != "new000000002" || !bytes.Contains(rc.Conf, []byte("10.66.0.3/32")) {
		t.Fatalf("реюз: %+v %v", rc.PeerID, err)
	}
	if e.p.Hits("POST ") != 0 || e.p.Hits("GET /api/ifaces/awg1/peers/new000000002/config") != 1 {
		t.Fatal("реюз выпустил нового пира или не спросил конфиг")
	}
}

func TestConfigForRouterRaceMakesOnePeer(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	var wg sync.WaitGroup
	got := make(chan RouterConfig, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, err := e.s.ConfigForRouter(context.Background(), "main", "awg1", "router-owned")
			if err != nil {
				t.Error(err)
			}
			got <- rc
		}()
	}
	wg.Wait()
	close(got)
	reused := 0
	for rc := range got {
		if rc.Reused {
			reused++
		}
	}
	if e.p.Hits("POST ") != 1 || reused != 1 || len(e.p.PeerList("awg1")) != 1 {
		t.Fatalf("post=%d reused=%d", e.p.Hits("POST "), reused)
	}
}

// TestBreakerLocksInMemoryEvenWhenPersistFails -- ревью: trip() писал только
// на диск; если SaveStore не смог (SD-карта Pi на чтение), ready() читал
// только хранилище, и каждое открытие экрана снова слало неверный пароль,
// приближая бан по RemoteAddr самого оператора (Caddy банит источник за 5
// неудач/5 мин). trip() должен взводить замок в памяти ДО попытки записи, а
// ready() -- смотреть и туда: второй запрос обязан уйти в отказ без единого
// обращения к панели, даже если диск не пишется.
func TestBreakerLocksInMemoryEvenWhenPersistFails(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	ctx := context.Background()
	dir := filepath.Dir(e.path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	// Панель теперь отвергает пароль, но каталог хранилища read-only --
	// SaveStore внутри trip() провалится и только залогируется.
	e.p.SetPassword("NOW-WRONG-MUST-NOT-LEAK")
	if _, err := e.s.Peers(ctx, "main", ""); KindOf(err) != KindBadPassword {
		t.Fatalf("первый отказ: %v", err)
	}
	hits := e.p.TotalHits()
	if _, err := e.s.Peers(ctx, "main", ""); KindOf(err) != KindBadPassword {
		t.Fatalf("второй раз (замок в памяти): %v", err)
	}
	if e.p.TotalHits() != hits {
		t.Fatalf("второй Peers сходил в панель, хотя диск не пишется: было %d, стало %d", hits, e.p.TotalHits())
	}
	// Диск снова доступен на запись, пароль на панели -- прежний: пересохранение
	// снимает и дисковый, и оперативный замок.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	e.p.SetPassword(testPanelPass)
	_, res, err := e.s.Update(ctx, "main", Input{Label: "Main", BaseURL: e.p.URL, User: "admin", Password: testPanelPass})
	if err != nil || res == nil || !res.OK {
		t.Fatalf("пересохранение: %+v %v", res, err)
	}
	if _, err := e.s.Peers(ctx, "main", ""); err != nil {
		t.Fatalf("после пересохранения: %v", err)
	}
}

// TestBreakerPauseLocksInMemoryEvenWhenPersistFails -- то же самое для паузы
// 429: PausedUntil должен блокировать следующий запрос из памяти, даже если
// SaveStore не смог записать его на диск.
func TestBreakerPauseLocksInMemoryEvenWhenPersistFails(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	ctx := context.Background()
	dir := filepath.Dir(e.path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	e.p.SetOverride(respond429)
	if _, err := e.s.Peers(ctx, "main", ""); KindOf(err) != KindBanned {
		t.Fatalf("первый отказ: %v", err)
	}
	hits := e.p.TotalHits()
	if _, err := e.s.Peers(ctx, "main", ""); KindOf(err) != KindBanned {
		t.Fatalf("второй раз (замок в памяти): %v", err)
	}
	if e.p.TotalHits() != hits {
		t.Fatalf("второй Peers сходил в панель на паузе, хотя диск не пишется: было %d, стало %d", hits, e.p.TotalHits())
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	e.p.SetOverride(nil)
	_, res, err := e.s.Update(ctx, "main", Input{Label: "Main", BaseURL: e.p.URL, User: "admin", Password: testPanelPass})
	if err != nil || res == nil || !res.OK {
		t.Fatalf("пересохранение: %+v %v", res, err)
	}
	if _, err := e.s.Peers(ctx, "main", ""); err != nil {
		t.Fatalf("после пересохранения: %v", err)
	}
}

// TestReadonlyClearsOnCredentialResave -- ревью: Readonly раньше снимался
// только сменой BaseURL; пересохранение одного пароля (адрес и логин те же)
// его не трогало, хотя это тоже «пересохранение учётных данных».
func TestReadonlyClearsOnCredentialResave(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{Readonly: true})
	e.create(t, "main")
	ctx := context.Background()
	if _, err := e.s.IssueDevice(ctx, "main", "awg1", "iphone"); KindOf(err) != KindReadonly {
		t.Fatalf("%v", err)
	}
	if views, _ := e.s.List(); !views[0].Readonly {
		t.Fatal("readonly не запомнен")
	}
	_, res, err := e.s.Update(ctx, "main", Input{Label: "Main", BaseURL: e.p.URL, User: "admin", Password: testPanelPass})
	if err != nil || res == nil {
		t.Fatalf("пересохранение пароля: %+v %v", res, err)
	}
	if views, _ := e.s.List(); views[0].Readonly {
		t.Fatal("readonly не снят пересохранением пароля без смены адреса")
	}
}

func TestReadonlyPanelIsRemembered(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{Readonly: true})
	e.create(t, "main")
	ctx := context.Background()
	if _, err := e.s.IssueDevice(ctx, "main", "awg1", "iphone"); KindOf(err) != KindReadonly {
		t.Fatalf("%v", err)
	}
	hits := e.p.TotalHits()
	if views, _ := e.s.List(); !views[0].Readonly {
		t.Fatal("readonly не запомнен")
	}
	if _, err := e.s.IssueDevice(ctx, "main", "awg1", "ipad"); KindOf(err) != KindReadonly {
		t.Fatalf("%v", err)
	}
	if _, err := e.s.ConfigForRouter(ctx, "main", "awg1", "router-owned"); KindOf(err) != KindReadonly {
		t.Fatalf("%v", err)
	}
	if e.p.TotalHits() != hits {
		t.Fatal("readonly-панель снова спрошена о выпуске")
	}
	if page, err := e.s.Peers(ctx, "main", ""); err != nil || !page.Panel.Readonly {
		t.Fatalf("просмотр readonly-панели: %v", err)
	}
}

func TestDisabledAndDeleted(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	ctx := context.Background()
	off := false
	if _, res, err := e.s.Update(ctx, "main", Input{Label: "Main", BaseURL: e.p.URL, User: "admin", Enabled: &off}); err != nil || res != nil {
		t.Fatalf("выключение: %v", err)
	}
	if _, err := e.s.Peers(ctx, "main", ""); !errors.Is(err, ErrInstanceDisabled) || e.p.TotalHits() != 0 {
		t.Fatalf("выключенная: %v hits=%d", err, e.p.TotalHits())
	}
	if err := e.s.Delete("main"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Peers(ctx, "main", ""); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("удалённая: %v", err)
	}
	if err := e.s.Delete("main"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("повторное удаление: %v", err)
	}
}

func TestUpdateMovedAddressNeedsPassword(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	_, _, err := e.s.Update(context.Background(), "main", Input{Label: "Main", BaseURL: "https://other.example.com", User: "admin"})
	if fieldOf(err) != "password" {
		t.Fatalf("%v", err)
	}
	if e.p.TotalHits() != 0 {
		t.Fatal("сходили в панель")
	}
}

func TestSecretsNeverInLogs(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	ctx := context.Background()
	_, _, _ = e.s.Create(ctx, e.input(t, "bad", "WRONG-PW-MUST-NOT-LEAK"))
	e.create(t, "main")
	_, _ = e.s.IssueDevice(ctx, "main", "awg1", "iphone")
	_, _ = e.s.ConfigForRouter(ctx, "main", "awg1", "router-owned")
	logs := e.logs.String()
	for _, bad := range []string{"WRONG-PW", testPanelPass, "PRIVATE KEY", "FAKE-PRIVATE-KEY", "p12-pw", "iVBOR"} {
		if strings.Contains(logs, bad) {
			t.Fatalf("в журнале %q:\n%s", bad, logs)
		}
	}
}

func TestRouterPeerName(t *testing.T) {
	if n, err := RouterPeerName("router-owned"); err != nil || n != "wgmon-router-owned" {
		t.Fatalf("%q %v", n, err)
	}
	for _, nick := range []string{"", "  ", strings.Repeat("a", 35), "a[b", "a\nb"} {
		if _, err := RouterPeerName(nick); fieldOf(err) != "router" {
			t.Errorf("%q: %v", nick, err)
		}
	}
	if n, _ := RouterPeerName(strings.Repeat("a", 34)); len([]rune(n)) != 40 {
		t.Fatal("34 знака ника -- ровно 40 рун имени")
	}
}

func TestPeerState(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	cases := []struct {
		p     Peer
		state string
		age   int64
	}{
		{Peer{Enabled: true, LastHandshake: 0}, "never", -1},
		{Peer{Enabled: true, NeverConnected: true, LastHandshake: 0}, "never", -1},
		{Peer{Enabled: true, LastHandshake: now.Unix() - 60}, "online", 60},
		{Peer{Enabled: true, LastHandshake: now.Unix() - 180}, "online", 180},
		{Peer{Enabled: true, LastHandshake: now.Unix() - 181}, "idle", 181},
		{Peer{Enabled: true, LastHandshake: now.Unix() + 30}, "online", 0}, // часы VPS спешат
		{Peer{Enabled: false, LastHandshake: now.Unix() - 60}, "off", 60},
	}
	for _, tc := range cases {
		if st, age := PeerState(tc.p, now); st != tc.state || age != tc.age {
			t.Errorf("%+v: %s %d, ждали %s %d", tc.p, st, age, tc.state, tc.age)
		}
	}
}

// TestTunnelName -- решение задачи ревью «Коллизия имён»: у
// selfhostedamnezia.TunnelName та же форма «<id>_<x>», префикс «a3-»
// отличает имена панелей awg3, чтобы replace:true не мог заменить чужой
// VPN-туннель одноимённым.
func TestTunnelName(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"nl2", "awg1"}:                     "a3-nl2_awg1",
		{"Main", "AWG.2"}:                   "a3-main_awg-2",
		{"1x", "awg1"}:                      "a3-1x_awg1",
		{"verylongpanelname", "wg-long-01"}: "a3-verylongpanelname_wg-long-01",
	} {
		if got := TunnelName(in[0], in[1]); got != want || len(got) > 32 {
			t.Errorf("%v: %q, ждали %q", in, got, want)
		}
	}
}

// TestIssueDeviceInvalidatesPageCache -- страница «Пиры» открыта, кэш тёплый,
// админ выпускает устройство: следующее открытие ТОЙ ЖЕ страницы без сдвига
// часов (кэш 30 с ещё не истёк) должно увидеть новый пир, а не отдать
// протухшую копию из кэша.
func TestIssueDeviceInvalidatesPageCache(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{Peers: map[string][]awg3paneltest.Peer{"awg1": {{ID: "aaaaaaaaaaa1", Name: "iphone", Enabled: true}}}})
	e.create(t, "main")
	ctx := context.Background()
	if page, err := e.s.Peers(ctx, "main", "awg1"); err != nil || len(page.Peers) != 1 {
		t.Fatalf("прогрев кэша: %+v %v", page, err)
	}
	e.p.ResetHits()
	if _, err := e.s.IssueDevice(ctx, "main", "awg1", "laptop"); err != nil {
		t.Fatalf("выпуск устройства: %v", err)
	}
	page, err := e.s.Peers(ctx, "main", "awg1")
	if err != nil {
		t.Fatalf("после выпуска: %v", err)
	}
	if len(page.Peers) != 2 {
		t.Fatalf("новый пир не виден без сброса кэша страницы: %d пиров", len(page.Peers))
	}
	// IssueDevice сам сходил за списком (проверка имени) -- один GET; если бы
	// кэш страницы не сбросился, второго GET после выпуска не было бы.
	if got := e.p.Hits("GET /api/ifaces/awg1/peers"); got != 2 {
		t.Fatalf("страница не перечитана после выпуска устройства: %d GET .../peers, ждали 2", got)
	}
}

// TestConfigForRouterInvalidatesPageCacheOnNewPeer -- то же самое для роутера,
// когда пира «wgmon-…» ещё не было и панель выпустила новый.
func TestConfigForRouterInvalidatesPageCacheOnNewPeer(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{Peers: map[string][]awg3paneltest.Peer{"awg1": {{ID: "aaaaaaaaaaa1", Name: "iphone", Enabled: true}}}})
	e.create(t, "main")
	ctx := context.Background()
	if page, err := e.s.Peers(ctx, "main", "awg1"); err != nil || len(page.Peers) != 1 {
		t.Fatalf("прогрев кэша: %+v %v", page, err)
	}
	e.p.ResetHits()
	rc, err := e.s.ConfigForRouter(ctx, "main", "awg1", "router-owned")
	if err != nil || rc.Reused {
		t.Fatalf("выпуск нового пира роутера: %+v %v", rc, err)
	}
	page, err := e.s.Peers(ctx, "main", "awg1")
	if err != nil {
		t.Fatalf("после выпуска: %v", err)
	}
	if len(page.Peers) != 2 {
		t.Fatalf("новый пир роутера не виден без сброса кэша страницы: %d пиров", len(page.Peers))
	}
	if got := e.p.Hits("GET /api/ifaces/awg1/peers"); got != 2 {
		t.Fatalf("страница не перечитана после выпуска пира роутера: %d GET .../peers, ждали 2", got)
	}
}

// TestIssueDeviceSurvivesCallerCancelDuringPost -- ушедший вызывающий (закрытая
// вкладка мини-аппа) не должен превращать успешный выпуск в ошибку: панель уже
// начала создавать пира, когда родительский ctx отменяется.
func TestIssueDeviceSurvivesCallerCancelDuringPost(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.p.SetOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost {
			once.Do(func() { close(started) })
			<-release
		}
		return false
	})
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		issued Issued
		err    error
	}
	done := make(chan result, 1)
	go func() {
		issued, err := e.s.IssueDevice(ctx, "main", "awg1", "iphone-anex")
		done <- result{issued, err}
	}()
	<-started
	cancel()
	close(release)
	res := <-done
	if res.err != nil {
		t.Fatalf("ушедший вызывающий не должен ронять выпуск: %v", res.err)
	}
	if res.issued.ID == "" || res.issued.QRPNGBase64 == "" {
		t.Fatalf("нет выпущенного пира: %+v", res.issued)
	}
	if got := len(e.p.PeerList("awg1")); got != 1 {
		t.Fatalf("пиров на панели: %d, ждали 1", got)
	}
}

// TestConfigForRouterSurvivesCallerCancelDuringPost -- та же гарантия для
// выпуска конфига роутера.
func TestConfigForRouterSurvivesCallerCancelDuringPost(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.p.SetOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost {
			once.Do(func() { close(started) })
			<-release
		}
		return false
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		rc  RouterConfig
		err error
	}, 1)
	go func() {
		rc, err := e.s.ConfigForRouter(ctx, "main", "awg1", "router-owned")
		done <- struct {
			rc  RouterConfig
			err error
		}{rc, err}
	}()
	<-started
	cancel()
	close(release)
	res := <-done
	if res.err != nil {
		t.Fatalf("ушедший вызывающий не должен ронять выпуск: %v", res.err)
	}
	if res.rc.PeerID == "" || res.rc.Reused {
		t.Fatalf("нет выпущенного пира роутера: %+v", res.rc)
	}
	if got := len(e.p.PeerList("awg1")); got != 1 {
		t.Fatalf("пиров на панели: %d, ждали 1", got)
	}
}

// TestServerCertUntrustedLocksWithDistinctMessage -- когда НАШ клиент не
// доверяет сертификату ПАНЕЛИ (в отличие от TestCertRejectedLocks, где панель
// отвергает наш клиентский сертификат), сообщение и класс должны это различать:
// не «загрузите .p12 заново» (это не про наш сертификат), а «проверьте адрес и
// сертификат на сервере».
func TestServerCertUntrustedLocksWithDistinctMessage(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	path := filepath.Join(t.TempDir(), DefaultStoreName)
	// RootCAs не заданы -- системные корни, тестовый CA панели туда не входит.
	s := NewService(path, Options{})
	pfx, err := p.CA.P12("anex", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), "p12-pw", false)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{ID: "main", Label: "Main", BaseURL: p.URL, User: "admin", Password: testPanelPass, P12: pfx, P12Password: "p12-pw"}
	_, res, err := s.Create(context.Background(), in)
	if err != nil || res.Kind != KindServerCert {
		t.Fatalf("проверка: %+v %v", res, err)
	}
	h := p.Handshakes()
	if _, err := s.Peers(context.Background(), "main", ""); KindOf(err) != KindServerCert || !strings.Contains(err.Error(), "проверьте адрес") {
		t.Fatalf("экран: %v", err)
	}
	if p.Handshakes() != h {
		t.Fatal("после отказа сертификата панели бот снова стучится в панель")
	}
	if views, _ := s.List(); views[0].Lock != LockServerCert {
		t.Fatalf("замок: %+v", views[0])
	}
}

func TestIssuersAddRemoveAndSurviveReload(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	v, err := e.s.AddIssuer("main", 555, 999)
	if err != nil || len(v.Issuers) != 1 || v.Issuers[0].TelegramUserID != 555 || v.Issuers[0].GrantedBy != 999 {
		t.Fatalf("%+v %v", v.Issuers, err)
	}
	if v, err = e.s.AddIssuer("main", 555, 999); err != nil || len(v.Issuers) != 1 {
		t.Fatalf("повтор: %+v %v", v.Issuers, err)
	}
	s2 := NewService(e.path, e.opts)
	if ok, err := s2.IsIssuer("main", 555); !ok || err != nil {
		t.Fatalf("после перечитывания: %v %v", ok, err)
	}
	if v, err = s2.RemoveIssuer("main", 555); err != nil || len(v.Issuers) != 0 {
		t.Fatalf("удаление: %+v %v", v.Issuers, err)
	}
	if _, err := e.s.AddIssuer("main", 0, 999); err == nil {
		t.Fatal("ноль принят")
	}
	if e.p.TotalHits() != 0 {
		t.Fatalf("допуск ходил в панель: %d", e.p.TotalHits())
	}
}

func TestDeletePanelDropsIssuers(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	if _, err := e.s.AddIssuer("main", 555, 999); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Delete("main"); err != nil {
		t.Fatal(err)
	}
	e.create(t, "main")
	if ok, _ := e.s.IsIssuer("main", 555); ok {
		t.Fatal("допуск пережил удаление панели")
	}
}

func TestIssuablePanelsOnlyGranted(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	e.create(t, "other")
	if _, err := e.s.AddIssuer("main", 555, 999); err != nil {
		t.Fatal(err)
	}
	got, err := e.s.IssuablePanels(context.Background(), 555, false)
	if err != nil || len(got) != 1 || got[0].ID != "main" || len(got[0].Ifaces) == 0 || got[0].Unavailable {
		t.Fatalf("%+v %v", got, err)
	}
	all, err := e.s.IssuablePanels(context.Background(), 999, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("админ: %+v %v", all, err)
	}
	if none, _ := e.s.IssuablePanels(context.Background(), 777, false); len(none) != 0 {
		t.Fatalf("чужой: %+v", none)
	}
}

func TestIssuablePanelsSkipsBrokenPanel(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	if _, err := e.s.AddIssuer("main", 555, 999); err != nil {
		t.Fatal(err)
	}
	e.s.forget("main") // кэш интерфейсов пуст -- панель спросят заново
	e.p.SetOverride(respond429)
	got, err := e.s.IssuablePanels(context.Background(), 555, false)
	if err != nil || len(got) != 1 || !got[0].Unavailable || len(got[0].Ifaces) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

// TestFreshConfigForRouter_AddsPeerEvenIfExists -- ступень «пересоздать»
// лесенки автопочинки: пир «wgmon-<ник>» уже есть, но нужен новый. Старый
// не трогается -- удаление пира на панели решает админ, а не автоматика.
func TestFreshConfigForRouter_AddsPeerEvenIfExists(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{Peers: map[string][]awg3paneltest.Peer{"awg1": {
		{ID: "old000000001", Name: "wgmon-router-owned", Address: "10.66.0.2/32", Enabled: true, CreatedAt: "2026-09-01T10:00:00Z"},
	}}})
	e.create(t, "main")
	rc, err := e.s.FreshConfigForRouter(context.Background(), "main", "awg1", "router-owned")
	if err != nil {
		t.Fatalf("FreshConfigForRouter: %v", err)
	}
	if rc.Reused || rc.PeerID == "" || rc.PeerID == "old000000001" || len(rc.Conf) == 0 {
		t.Fatalf("ждали новый пир с конфигом: %+v", rc)
	}
	if e.p.Hits("POST ") != 1 {
		t.Fatalf("новый пир не выпущен: POST=%d", e.p.Hits("POST "))
	}
	if n := len(e.p.PeerList("awg1")); n != 2 {
		t.Fatalf("пиров на интерфейсе %d, ждали 2: старый остаётся", n)
	}
}

func TestFreshConfigForRouter_ReadonlyRefused(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{Readonly: true})
	e.create(t, "main")
	if _, err := e.s.FreshConfigForRouter(context.Background(), "main", "awg1", "router-owned"); KindOf(err) != KindReadonly {
		t.Fatalf("панель только для просмотра обязана отказать: %v", err)
	}
	if n := len(e.p.PeerList("awg1")); n != 0 {
		t.Fatalf("на панели только для просмотра появился пир: %d", n)
	}
}

// Пересохранение учётных данных снимает паузу 429 не только в памяти, но и на
// диске: после перезапуска сервиса панель не должна снова оказаться на паузе.
func TestResaveClearsPauseOnDiskAfterRestart(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	ctx := context.Background()
	e.p.SetOverride(respond429)
	if _, err := e.s.Peers(ctx, "main", ""); KindOf(err) != KindBanned {
		t.Fatalf("первый отказ: %v", err)
	}
	if views, _ := NewService(e.path, e.opts).List(); views[0].PausedUntil.IsZero() {
		t.Fatal("пауза не легла на диск")
	}
	e.p.SetOverride(nil)
	if _, res, err := e.s.Update(ctx, "main", Input{Label: "Main", BaseURL: e.p.URL, User: "admin", Password: testPanelPass}); err != nil || res == nil || !res.OK {
		t.Fatalf("пересохранение: %+v %v", res, err)
	}
	s2 := NewService(e.path, e.opts) // «перезапуск»: память пуста, остался диск
	views, err := s2.List()
	if err != nil || !views[0].PausedUntil.IsZero() {
		t.Fatalf("пауза осталась на диске: %+v %v", views, err)
	}
	if _, err := s2.Peers(ctx, "main", ""); err != nil {
		t.Fatalf("после перезапуска: %v", err)
	}
}

// Список панелей (а с ним и допуски к выпуску) отдаётся из хранилища и не ходит
// в панель: лежащая панель, пауза и замок его не прячут.
func TestListKeepsIssuersWhenPanelDown(t *testing.T) {
	e := newSvcEnv(t, awg3paneltest.Options{})
	e.create(t, "main")
	if _, err := e.s.AddIssuer("main", 555, 999); err != nil {
		t.Fatal(err)
	}
	e.p.SetOverride(respond429)
	_, _ = e.s.Peers(context.Background(), "main", "")
	e.p.Close()
	hits := e.p.TotalHits()
	views, err := NewService(e.path, e.opts).List()
	if err != nil || len(views) != 1 || len(views[0].Issuers) != 1 || views[0].Issuers[0].TelegramUserID != 555 {
		t.Fatalf("допуски: %+v %v", views, err)
	}
	if e.p.TotalHits() != hits {
		t.Fatal("список ходил в панель")
	}
}
