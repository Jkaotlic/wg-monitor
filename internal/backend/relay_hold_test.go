package backend

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Бэкенд живёт за облачным релеем KeenDNS, а тот рвёт любой ответ, который
// задержался дольше 15 секунд (замер 11.09.2026: ожидание 10 с -- честный
// ответ за 10,25 с, ожидание 25 с -- обрыв ровно на 15,1 с). Агент просит
// ждать команду 30 секунд, и пустой ответ 204 через релей не доходил никогда:
// каждый простой считался ошибкой, агент сидел на предельной паузе, а
// результат, который однажды не удалось отправить, не переотправлялся вовсе
// -- переотправка идёт только после 204. Так застрял отчёт диагностики
// рабочего роутера.
const keendnsRelayCut = 15 * time.Second

// holdRecorder запоминает, сколько обработчик попросил очередь подержать запрос.
type holdRecorder struct {
	dashboardActionSink
	mu    sync.Mutex
	holds []time.Duration
}

func (s *holdRecorder) record(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holds = append(s.holds, d)
}

func (s *holdRecorder) last(t *testing.T) time.Duration {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.holds) == 0 {
		t.Fatal("очередь не спросили ни разу")
	}
	return s.holds[len(s.holds)-1]
}

func (s *holdRecorder) Dequeue(_ context.Context, _ int64, hold time.Duration) (*wire.Command, bool) {
	s.record(hold)
	return nil, false
}

func (s *holdRecorder) AwaitResult(_ context.Context, _ int64, _ string, timeout time.Duration) (*wire.CommandResult, bool) {
	s.record(timeout)
	return nil, false
}

func relayHoldEnv(t *testing.T) (*httptest.Server, *holdRecorder, string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	tok := "0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c"
	if _, err := d.Users().Insert("client-b", tok, "198.51.100.31", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &holdRecorder{}
	srv := httptest.NewServer(NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		CommandSink:    sink,
		DashboardToken: "secret",
	}))
	t.Cleanup(srv.Close)
	return srv, sink, tok
}

func getWithBearer(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestCmdLongPollFitsUnderRelayCut(t *testing.T) {
	srv, sink, tok := relayHoldEnv(t)
	// Ровно то, что шлёт агент (cmd/agent/main.go: cmdloop.New(..., 30)),
	// плюс запрос без параметра и запрос сверх прежнего предела.
	for _, q := range []string{"?wait=30", "", "?wait=90"} {
		if code := getWithBearer(t, srv.URL+"/v1/cmd"+q, tok); code != http.StatusNoContent {
			t.Fatalf("/v1/cmd%s: ждали 204, получили %d", q, code)
		}
		if hold := sink.last(t); hold >= keendnsRelayCut {
			t.Errorf("/v1/cmd%s держит %v -- релей оборвёт ответ на %v, и 204 до агента не дойдёт", q, hold, keendnsRelayCut)
		}
	}
}

func TestCmdLongPollKeepsShortWait(t *testing.T) {
	srv, sink, tok := relayHoldEnv(t)
	getWithBearer(t, srv.URL+"/v1/cmd?wait=5", tok)
	if hold := sink.last(t); hold != 5*time.Second {
		t.Fatalf("короткое ожидание не трогаем: ждали 5s, получили %v", hold)
	}
}

func TestDashboardCommandResultFitsUnderRelayCut(t *testing.T) {
	srv, sink, _ := relayHoldEnv(t)
	// Без параметра так опрашивает страница дашборда; 25 -- то, чем мерили.
	for _, q := range []string{"", "&wait_sec=25", "&wait_sec=60"} {
		getWithBearer(t, srv.URL+"/v1/dashboard/commands/abc?nickname=client-b"+q, "secret")
		if hold := sink.last(t); hold >= keendnsRelayCut {
			t.Errorf("результат команды%s ждёт %v -- релей оборвёт ответ на %v", q, hold, keendnsRelayCut)
		}
	}
}

// Мини-апп ходит через тот же релей. useCommand.js просил 25 секунд за раз, и
// любая команда дольше 15 секунд -- диагностика, перезапуск -- кончалась у
// владельца ошибкой вместо «ещё не готово, ждём дальше».
func TestMiniappCommandResultFitsUnderRelayCut(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	sink := &holdRecorder{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})
	for _, q := range []string{"", "?wait_sec=25", "?wait_sec=30"} {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/commands/deadbeef%s", ownedID, q), nil)
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
		h.ServeHTTP(httptest.NewRecorder(), req)
		if hold := sink.last(t); hold >= keendnsRelayCut {
			t.Errorf("мини-апп%s ждёт %v -- релей оборвёт ответ на %v", q, hold, keendnsRelayCut)
		}
	}
}

func TestDashboardCommandResultKeepsShortWait(t *testing.T) {
	srv, sink, _ := relayHoldEnv(t)
	getWithBearer(t, srv.URL+"/v1/dashboard/commands/abc?nickname=client-b&wait_sec=3", "secret")
	if hold := sink.last(t); hold != 3*time.Second {
		t.Fatalf("короткое ожидание не трогаем: ждали 3s, получили %v", hold)
	}
}
