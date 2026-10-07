package maintnotify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// fakeTG -- поддельный Telegram: запоминает каждый sendMessage как пришёл, а
// fail=true отвечает 500 (временный сбой, повторять стоит).
type fakeTG struct {
	mu   sync.Mutex
	reqs []map[string]any
	fail bool
}

func (f *fakeTG) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"Internal Server Error"}`))
		return
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	f.reqs = append(f.reqs, m)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": len(f.reqs)}})
}

func (f *fakeTG) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.reqs...)
}

type fixture struct {
	d      *db.DB
	router int64
	tg     *fakeTG
	p      *Poller
	now    time.Time
}

var msk = time.FixedZone("MSK", 3*3600)

// newFixture -- роутер «router-a» с владельцем 1001, без админа; снимок
// версий с новостью о прошивке и поводом перезагрузиться. Часы -- 12:00 МСК.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("router-a", "tok-a", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().SetTelegramUserID(id, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterVersions().Upsert(id, db.RouterVersionSnapshot{
		FirmwareCurrent:   "5.02.A.8.0-3",
		FirmwareAvail:     "5.02.A.9.0-0",
		KmodVersion:       "3.1.20261001",
		KmodLoadedVersion: "3.1.20260906",
		Source:            "report",
	}); err != nil {
		t.Fatal(err)
	}
	ftg := &fakeTG{}
	srv := httptest.NewServer(http.HandlerFunc(ftg.handler))
	t.Cleanup(srv.Close)
	client := &tg.Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fixture{d: d, router: id, tg: ftg, now: time.Date(2026, 10, 7, 12, 0, 0, 0, msk)}
	p, err := NewPoller(d, nil, notify.NewFanout(d, client, logger, 0), Config{
		Enabled:        true,
		Location:       msk,
		MiniAppBaseURL: "https://example.com",
		Audit:          testAudit,
		AgentNews:      func(string) (string, bool) { return "", false },
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	p.SetNow(func() time.Time { return f.now })
	f.p = p
	return f
}

// testAudit -- то же преобразование, что backend.VersionAuditFromSnapshot, в
// объёме, который нужен тесту.
func testAudit(row db.RouterVersionRow) wire.VersionAudit {
	return wire.VersionAudit{
		FirmwareCurrent:   row.FirmwareCurrent,
		FirmwareAvail:     row.FirmwareAvail,
		KmodVersion:       row.KmodVersion,
		KmodLoadedVersion: row.KmodLoadedVersion,
	}
}

func TestPollerSendsOnceQuietlyWithButton(t *testing.T) {
	f := newFixture(t)
	f.p.Tick(context.Background())
	reqs := f.tg.sent()
	if len(reqs) != 1 {
		t.Fatalf("сообщений %d, ждали одно", len(reqs))
	}
	m := reqs[0]
	if m["disable_notification"] != true {
		t.Errorf("напоминание ушло со звуком: %+v", m)
	}
	if m["chat_id"] != float64(1001) {
		t.Errorf("не тому: %v", m["chat_id"])
	}
	text, _ := m["text"].(string)
	for _, want := range []string{"Роутер «router-a»", "5.02.A.8.0-3 → 5.02.A.9.0-0", "перезагрузка", "не срочно"} {
		if !strings.Contains(text, want) {
			t.Errorf("в тексте нет %q:\n%s", want, text)
		}
	}
	kb, _ := json.Marshal(m["reply_markup"])
	if !strings.Contains(string(kb), "https://example.com/miniapp/?router=") || !strings.Contains(string(kb), "Открыть в приложении") {
		t.Errorf("нет кнопки в приложение: %s", kb)
	}

	// Повтор -- ни одного нового сообщения: каждая новость один раз.
	f.now = f.now.Add(time.Hour)
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 1 {
		t.Fatalf("после повторного обхода сообщений %d, ждали одно", n)
	}
}

func TestPollerOnlyDuringDaytime(t *testing.T) {
	f := newFixture(t)
	for _, h := range []int{9, 20, 23, 3} {
		f.now = time.Date(2026, 10, 7, h, 30, 0, 0, msk)
		f.p.Tick(context.Background())
	}
	if n := len(f.tg.sent()); n != 0 {
		t.Fatalf("вне окна 10:00–20:00 ушло %d сообщений", n)
	}
	// Вне окна ничего не помечено: днём новость уйдёт.
	f.now = time.Date(2026, 10, 7, 10, 0, 0, 0, msk)
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 1 {
		t.Fatalf("в 10:00 сообщений %d, ждали одно", n)
	}
}

// Окно -- московское время, а не время сервера: 08:00 UTC -- это 11:00 МСК.
func TestPollerWindowIsInConfiguredZone(t *testing.T) {
	f := newFixture(t)
	f.now = time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 1 {
		t.Fatalf("в 11:00 МСК сообщений %d", n)
	}
}

func TestPollerSkipsHiddenAndSnoozed(t *testing.T) {
	f := newFixture(t)
	r := f.d.UpdateReminders()
	if err := r.Dismiss(f.router, "firmware", "5.02.A.9.0-0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Snooze(f.router, "kmod_reboot", "3.1.20261001", f.now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 0 {
		t.Fatalf("скрытое и отложенное разослано: %d сообщений", n)
	}
	// Срок отсрочки вышел -- уходит только перезагрузка, без скрытой прошивки.
	f.now = f.now.Add(25 * time.Hour)
	f.p.Tick(context.Background())
	reqs := f.tg.sent()
	if len(reqs) != 1 {
		t.Fatalf("после срока отсрочки сообщений %d", len(reqs))
	}
	text, _ := reqs[0]["text"].(string)
	if strings.Contains(text, "5.02.A.9.0-0") || !strings.Contains(text, "перезагрузка") {
		t.Errorf("в тексте скрытая прошивка или нет перезагрузки:\n%s", text)
	}
}

func TestPollerRetriesAfterFailedDelivery(t *testing.T) {
	f := newFixture(t)
	f.tg.fail = true
	f.p.Tick(context.Background())
	pending, err := f.d.UpdateReminders().PendingNotify(f.router, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("неудачная доставка пометила разосланным: к рассылке %+v", pending)
	}
	f.tg.fail = false
	f.now = f.now.Add(time.Hour)
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 1 {
		t.Fatalf("после восстановления сообщений %d, ждали одно", n)
	}
	if pending, _ := f.d.UpdateReminders().PendingNotify(f.router, f.now); len(pending) != 0 {
		t.Fatalf("после доставки к рассылке осталось %+v", pending)
	}
}

// Слать некому (владелец не привязан, админа нет) -- не «разослано»: появится
// получатель -- получит.
func TestPollerNobodyToNotifyRetriesLater(t *testing.T) {
	f := newFixture(t)
	if _, err := f.d.SQL().Exec(`UPDATE users SET telegram_user_id = NULL WHERE id = ?`, f.router); err != nil {
		t.Fatal(err)
	}
	f.p.Tick(context.Background())
	if pending, _ := f.d.UpdateReminders().PendingNotify(f.router, f.now); len(pending) != 2 {
		t.Fatalf("«слать некому» пометило разосланным: %+v", pending)
	}
	if err := f.d.Users().SetTelegramUserID(f.router, 1001); err != nil {
		t.Fatal(err)
	}
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 1 {
		t.Fatalf("после привязки владельца сообщений %d", n)
	}
}

// Новая версия той же новости -- новое сообщение, и только о ней.
func TestPollerNewVersionIsNewNews(t *testing.T) {
	f := newFixture(t)
	f.p.Tick(context.Background())
	if err := f.d.RouterVersions().Upsert(f.router, db.RouterVersionSnapshot{
		FirmwareCurrent:   "5.02.A.8.0-3",
		FirmwareAvail:     "5.02.A.10.0-0",
		KmodVersion:       "3.1.20261001",
		KmodLoadedVersion: "3.1.20260906",
		Source:            "report",
	}); err != nil {
		t.Fatal(err)
	}
	f.p.Tick(context.Background())
	reqs := f.tg.sent()
	if len(reqs) != 2 {
		t.Fatalf("сообщений %d, ждали два", len(reqs))
	}
	text, _ := reqs[1]["text"].(string)
	if !strings.Contains(text, "5.02.A.10.0-0") || strings.Contains(text, "перезагрузка") {
		t.Errorf("второе сообщение не только о новой прошивке:\n%s", text)
	}
}

func TestPollerAgentNews(t *testing.T) {
	f := newFixture(t)
	if err := f.d.Users().UpdateLastSeenAgentVersion(f.router, "v0.56.0"); err != nil {
		t.Fatal(err)
	}
	f.p.cfg.AgentNews = func(v string) (string, bool) {
		if v == "v0.56.0" {
			return "v0.57.0", true
		}
		return "", false
	}
	ageAgentNews(t, f, 73*time.Hour)
	f.p.Tick(context.Background())
	reqs := f.tg.sent()
	if len(reqs) != 1 {
		t.Fatalf("сообщений %d", len(reqs))
	}
	text, _ := reqs[0]["text"].(string)
	if !strings.Contains(text, "v0.56.0 → v0.57.0") {
		t.Errorf("нет строки агента:\n%s", text)
	}
	pending, _ := f.d.UpdateReminders().PendingNotify(f.router, f.now)
	if len(pending) != 0 {
		t.Errorf("новость агента не помечена: %+v", pending)
	}
}

func TestPollerDisabledSendsNothing(t *testing.T) {
	f := newFixture(t)
	f.p.cfg.Enabled = false
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 0 {
		t.Fatalf("выключенное напоминание отправило %d", n)
	}
}

func TestNewPollerNeedsSeams(t *testing.T) {
	if _, err := NewPoller(nil, nil, nil, Config{AgentNews: func(string) (string, bool) { return "", false }}, nil); err == nil {
		t.Error("без Audit пуллер создан")
	}
	if _, err := NewPoller(nil, nil, nil, Config{Audit: testAudit}, nil); err == nil {
		t.Error("без AgentNews пуллер создан")
	}
}

// Словарь владельца (заметка alerts-speak-to-owner): латиница только в
// ёлочках (имена роутера и продуктов) и «VPN»; версии -- числа, буква
// выпуска прошивки («A») словом не считается. Жаргона нет.
var (
	quotedRe    = regexp.MustCompile(`«[^»]*»`)
	latinWordRe = regexp.MustCompile(`[A-Za-z]{2,}`)
)

func latinOutsideQuotes(text string) []string {
	bare := quotedRe.ReplaceAllString(text, "«»")
	bare = strings.ReplaceAll(bare, "VPN", "")
	return latinWordRe.FindAllString(bare, -1)
}

func TestRenderSpeaksOwnerRussian(t *testing.T) {
	text := Render("router-a", []Item{
		{Component: "awgmgr", Installed: "2.19.9", Available: "2.20.0"},
		{Component: "hrneo", Installed: "3.18.3", Available: "3.19.0"},
		{Component: "firmware", Installed: "5.02.A.8.0-3", Available: "5.02.A.9.0-0"},
		{Component: "agent", Installed: "v0.56.0", Available: "v0.57.0"},
		{Component: "kmod_reboot"},
	})
	t.Logf("готовый текст:\n%s", text)
	if words := latinOutsideQuotes(text); len(words) > 0 {
		t.Errorf("латиница вне ёлочек %v:\n%s", words, text)
	}
	for _, bad := range []string{"hrneo", "awgmgr", "kmod", "reboot", "апстрим", "рестарт", "линия", "демон"} {
		if strings.Contains(strings.ToLower(text), bad) {
			t.Errorf("жаргон %q:\n%s", bad, text)
		}
	}
	for _, want := range []string{
		"🔔 Роутер «router-a»: есть что обновить",
		"«awg-manager»: 2.19.9 → 2.20.0",
		"Движок умной раздельной маршрутизации «HydraRoute Neo»: 3.18.3 → 3.19.0",
		"Нужна перезагрузка роутера: сменился модуль ядра «AmneziaWG» — VPN-туннели поднимутся после перезагрузки.",
		"Это не срочно — сделайте в удобное время. Подробности и кнопки — в приложении.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в тексте нет %q:\n%s", want, text)
		}
	}
	// Только перезагрузка -- заголовок о перезагрузке, а не «обновить».
	only := Render("router-a", []Item{{Component: "kmod_reboot"}})
	if !strings.Contains(only, "🔔 Роутер «router-a»: нужна перезагрузка") {
		t.Errorf("заголовок для одной перезагрузки:\n%s", only)
	}
}

// Отметка «разослано» не пишется (база упрямо отказывает) -- это не повод
// слать то же самое каждый час: процесс помнит доставленное сам.
func TestPollerDoesNotResendWhenMarkFails(t *testing.T) {
	f := newFixture(t)
	if _, err := f.d.SQL().Exec(`CREATE TRIGGER no_mark BEFORE UPDATE OF notified_at ON router_update_reminders
BEGIN SELECT RAISE(ABORT, 'отметка запрещена'); END`); err != nil {
		t.Fatal(err)
	}
	f.p.Tick(context.Background())
	if pending, _ := f.d.UpdateReminders().PendingNotify(f.router, f.now); len(pending) != 2 {
		t.Fatalf("триггер не сработал, к рассылке %+v", pending)
	}
	f.now = f.now.Add(time.Hour)
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 1 {
		t.Fatalf("при сломанной отметке сообщений %d, ждали одно", n)
	}
}

// cancelSender -- доставляет «одному» и посреди рассылки отменяет контекст:
// так выглядит остановка бэкенда во время веера.
type cancelSender struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancelSender) SendSilentKeyboard(context.Context, int64, string, string, *tg.InlineKeyboardMarkup) (int, error) {
	c.calls++
	c.cancel()
	return 1, nil
}

// Остановка посреди рассылки: часть получателей могла не получить, поэтому
// не помечаем -- лучше возможный повтор после рестарта, чем потерянные люди.
func TestPollerCancelledMidSendDoesNotMark(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cs := &cancelSender{cancel: cancel}
	f.p.s = cs
	f.p.Tick(ctx)
	if cs.calls != 1 {
		t.Fatalf("отправок %d", cs.calls)
	}
	if pending, _ := f.d.UpdateReminders().PendingNotify(f.router, f.now); len(pending) != 2 {
		t.Fatalf("прерванная рассылка помечена: к рассылке %+v", pending)
	}
	// И процесс её не запомнил: следующий живой обход отправит.
	f.p.s = cs
	f.p.Tick(context.Background())
	if cs.calls != 2 {
		t.Fatalf("после прерванной рассылки повтора не было: отправок %d", cs.calls)
	}
}

// Отменённый контекст -- ни одной отправки.
func TestPollerCancelledBeforeSendSendsNothing(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cs := &cancelSender{cancel: func() {}}
	f.p.s = cs
	if u, err := f.d.Users().GetByID(f.router); err == nil {
		row, _ := f.d.RouterVersions().Get(f.router)
		f.p.routerPass(ctx, u, row, f.now)
	}
	if cs.calls != 0 {
		t.Fatalf("при отменённом контексте отправок %d", cs.calls)
	}
}

// Start заводит WaitGroup до запуска горутины: WaitForExit сразу после Start
// не проскакивает мимо работающего пуллера.
func TestPollerStartAndWait(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.p.Start(ctx)
	cancel()
	done := make(chan struct{})
	go func() { f.p.WaitForExit(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("пуллер не вышел после отмены")
	}
}

// ageAgentNews заводит новость агента (v0.57.0) так, будто сервер узнал о ней
// age назад.
func ageAgentNews(t *testing.T, f *fixture, age time.Duration) {
	t.Helper()
	if err := f.d.UpdateReminders().Ensure(f.router, "agent", "v0.57.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.SQL().Exec(`UPDATE router_update_reminders SET first_seen_at = ? WHERE user_id = ? AND component = 'agent'`,
		f.now.Add(-age).UTC(), f.router); err != nil {
		t.Fatal(err)
	}
}

func agentBehind(f *fixture) {
	f.p.cfg.AgentNews = func(v string) (string, bool) {
		if v == "v0.56.0" {
			return "v0.57.0", true
		}
		return "", false
	}
}

// Решение ведущего цикла: агента обновляет авто-раскатка, и звать человека
// стоит, только если она не справилась -- новость агента старше 72 часов и
// назначенного обновления нет. Остальные новости уходят как обычно.
func TestPollerAgentNewsWaits72Hours(t *testing.T) {
	f := newFixture(t)
	if err := f.d.Users().UpdateLastSeenAgentVersion(f.router, "v0.56.0"); err != nil {
		t.Fatal(err)
	}
	agentBehind(f)
	ageAgentNews(t, f, 71*time.Hour)
	f.p.Tick(context.Background())
	reqs := f.tg.sent()
	if len(reqs) != 1 {
		t.Fatalf("сообщений %d", len(reqs))
	}
	if text, _ := reqs[0]["text"].(string); strings.Contains(text, "wg-monitor") {
		t.Fatalf("свежая новость агента разослана:\n%s", text)
	}
	if pending, _ := f.d.UpdateReminders().PendingNotify(f.router, f.now); len(pending) != 1 || pending[0].Component != "agent" {
		t.Fatalf("новость агента должна ждать неразосланной: %+v", pending)
	}
	// Прошло 72 часа -- отдельное сообщение только об агенте.
	f.now = f.now.Add(2 * time.Hour)
	f.p.Tick(context.Background())
	reqs = f.tg.sent()
	if len(reqs) != 2 {
		t.Fatalf("после 72 часов сообщений %d", len(reqs))
	}
	if text, _ := reqs[1]["text"].(string); !strings.Contains(text, "v0.56.0 → v0.57.0") || strings.Contains(text, "5.02") {
		t.Fatalf("второе сообщение не только об агенте:\n%s", text)
	}
}

func TestPollerAgentNewsSkippedWhileUpdateScheduled(t *testing.T) {
	f := newFixture(t)
	if err := f.d.Users().UpdateLastSeenAgentVersion(f.router, "v0.56.0"); err != nil {
		t.Fatal(err)
	}
	agentBehind(f)
	ageAgentNews(t, f, 100*time.Hour)
	if err := f.d.Users().MarkPendingDeploy(f.router, "v0.57.0", f.now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	f.p.Tick(context.Background())
	reqs := f.tg.sent()
	if len(reqs) != 1 {
		t.Fatalf("сообщений %d", len(reqs))
	}
	if text, _ := reqs[0]["text"].(string); strings.Contains(text, "wg-monitor") {
		t.Fatalf("при назначенном обновлении агента новость разослана:\n%s", text)
	}
}

// Пауза между роутерами внутри обхода: на первом запуске админ получает по
// сообщению на каждый роутер, и без паузы Telegram ответил бы 429.
func TestPollerPausesBetweenRouters(t *testing.T) {
	f := newFixture(t)
	for _, nick := range []string{"router-b", "router-c"} {
		id, err := f.d.Users().Insert(nick, "tok-"+nick, "203.0.113.10", "awg11")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.d.Users().SetTelegramUserID(id, 1001); err != nil {
			t.Fatal(err)
		}
		if err := f.d.RouterVersions().Upsert(id, db.RouterVersionSnapshot{
			FirmwareCurrent: "5.02.A.8.0-3", FirmwareAvail: "5.02.A.9.0-0", Source: "report",
		}); err != nil {
			t.Fatal(err)
		}
	}
	var pauses []time.Duration
	f.p.cfg.RouterPause = 1500 * time.Millisecond
	f.p.sleep = func(_ context.Context, d time.Duration) bool {
		pauses = append(pauses, d)
		return true
	}
	f.p.Tick(context.Background())
	if n := len(f.tg.sent()); n != 3 {
		t.Fatalf("сообщений %d, ждали три", n)
	}
	if len(pauses) != 2 || pauses[0] != 1500*time.Millisecond {
		t.Fatalf("паузы %v, ждали две по 1,5 с -- между тремя отправками", pauses)
	}
}
