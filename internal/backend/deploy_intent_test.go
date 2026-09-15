package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Роутер выключили на четыре дня с назначенным обновлением (bronya,
// gachimikhail: 11.09 → 15.09). Очередь -- НАСТОЯЩАЯ, обработчик выброшенных
// команд подключён так же, как в cmd/backend/main.go:158. На fakeCmdSink эта
// поломка не видна: у него нет ни вытеснения, ни onDrop.
type sleptRouter struct {
	d   *db.DB
	q   *cmdpkg.Queue
	h   http.Handler
	uid int64
	tok string
}

const sleptTarget = "v0.32.0"

func seedSleptRouter(t *testing.T, tok string) sleptRouter {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	uid, err := d.Users().InsertWithKind("bronya", tok, "198.51.100.7", "awg0", db.KindStatic)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(uid, "v0.31.0"); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().MarkPendingDeploy(uid, sleptTarget, time.Now().UTC().Add(-4*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := cmdpkg.New()
	AttachDeployExpiryHandler(q, d, logger)
	issued := time.Now().Add(-4 * 24 * time.Hour)
	if err := q.Enqueue(uid, wire.Command{
		ID:        "cmd-before-poweroff",
		Action:    "self_update",
		Args:      map[string]any{"version": sleptTarget, "repo_base": "https://backend.example.com/v1/releases/download"},
		IssuedAt:  issued,
		ExpiresAt: issued.Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{
		Logger:        logger,
		DB:            d,
		Dispatcher:    &fakeDisp{},
		CommandSink:   q,
		PublicBaseURL: "https://backend.example.com",
	})
	return sleptRouter{d: d, q: q, h: h, uid: uid, tok: tok}
}

func (s sleptRouter) poll(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/cmd?wait=0", nil)
	req.Header.Set("Authorization", "Bearer "+s.tok)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("опрос команд: код %d, тело %s", rec.Code, rec.Body.String())
	}
	return rec
}

func (s sleptRouter) report(t *testing.T, agentVersion string) {
	t.Helper()
	body := []byte(`{"ts":"` + time.Now().UTC().Format(time.RFC3339) + `","agent_version":"` + agentVersion +
		`","checks":[{"name":"agent_heartbeat","status":"ok"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("отчёт: код %d, тело %s", rec.Code, rec.Body.String())
	}
}

func (s sleptRouter) pendingVersion(t *testing.T) string {
	t.Helper()
	u, err := s.d.Users().GetByID(s.uid)
	if err != nil {
		t.Fatal(err)
	}
	return stringValue(u.PendingVersion)
}

// Путь 1: включившийся агент сначала опрашивает команды, потом отчитывается.
func TestPoweredOffRouterKeepsDeployIntent_PollBeforeReport(t *testing.T) {
	s := seedSleptRouter(t, "a1a100a1a100a1a100a1a100a1a100a1a100a1a100a1a100a1a100a1a100a1a1")

	s.poll(t)
	s.report(t, "v0.31.0")

	if got := s.pendingVersion(t); got != sleptTarget {
		t.Errorf("отметка обновления = %q, ждали %q: протухшая команда стёрла намерение оператора", got, sleptTarget)
	}
	if !s.q.HasActiveCommand(s.uid, "self_update") {
		t.Errorf("после включения у роутера нет команды обновления: обновление потеряно молча")
	}
}

// Путь 2: отчёт раньше опроса. Новая команда вытесняет протухшую, и onDrop
// снимает отметку -- при сорванной попытке повтора уже не будет.
func TestPoweredOffRouterKeepsDeployIntent_ReportBeforePoll(t *testing.T) {
	s := seedSleptRouter(t, "b2b200b2b200b2b200b2b200b2b200b2b200b2b200b2b200b2b200b2b200b2b2")

	s.report(t, "v0.31.0")
	s.poll(t)

	if got := s.pendingVersion(t); got != sleptTarget {
		t.Errorf("отметка обновления = %q, ждали %q: вытесненная команда стёрла намерение оператора", got, sleptTarget)
	}
	if !s.q.HasActiveCommand(s.uid, "self_update") {
		t.Errorf("после включения у роутера нет команды обновления")
	}
}

// Включившийся роутер получает обновление на ПЕРВОМ же опросе -- свежей
// командой, а не протухшей, с тем же адресом загрузки.
func TestPoweredOffRouterGetsUpdateOnFirstPoll(t *testing.T) {
	s := seedSleptRouter(t, "c3c300c3c300c3c300c3c300c3c300c3c300c3c300c3c300c3c300c3c300c3c3")

	rec := s.poll(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("первый опрос: код %d, ждали 200 с командой обновления", rec.Code)
	}
	var got wire.Command
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != "self_update" || got.Args["version"] != sleptTarget {
		t.Fatalf("выдано %s %v, ждали self_update %s", got.Action, got.Args["version"], sleptTarget)
	}
	if got.ID == "cmd-before-poweroff" {
		t.Fatal("выдана протухшая команда")
	}
	if got.Args["repo_base"] != "https://backend.example.com/v1/releases/download" {
		t.Fatalf("repo_base=%v", got.Args["repo_base"])
	}
}

// Агент опрашивает каждые 12 секунд. Пока выданная команда в работе, второй
// опрос не имеет права выдать ещё одну.
func TestPollDoesNotRepeatUpdateWhileInFlight(t *testing.T) {
	s := seedSleptRouter(t, "d4d400d4d400d4d400d4d400d4d400d4d400d4d400d4d400d4d400d4d400d4d4")

	if rec := s.poll(t); rec.Code != http.StatusOK {
		t.Fatalf("первый опрос: код %d", rec.Code)
	}
	if rec := s.poll(t); rec.Code != http.StatusNoContent {
		t.Fatalf("второй опрос: код %d, тело %s -- обновление выдано повторно", rec.Code, rec.Body.String())
	}
}

// withDeployNotifier пересобирает обработчик с записывающим уведомителем.
func (s *sleptRouter) withDeployNotifier() *fakeDeployNotifier {
	dn := &fakeDeployNotifier{}
	s.h = NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             s.d,
		Dispatcher:     &fakeDisp{},
		CommandSink:    s.q,
		DeployNotifier: dn,
		PublicBaseURL:  "https://backend.example.com",
	})
	return dn
}

func (s sleptRouter) result(t *testing.T, cmdID, status, output string) {
	t.Helper()
	body, _ := json.Marshal(wire.CommandResult{ID: cmdID, Status: status, Output: output})
	req := httptest.NewRequest(http.MethodPost, "/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("результат: код %d, тело %s", rec.Code, rec.Body.String())
	}
}

func (s sleptRouter) pollUpdate(t *testing.T) wire.Command {
	t.Helper()
	rec := s.poll(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("ждали выдачу обновления, код %d", rec.Code)
	}
	var c wire.Command
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Action != "self_update" {
		t.Fatalf("выдано %q, ждали self_update", c.Action)
	}
	return c
}

func waitDeployCalls(dn *fakeDeployNotifier, n int) []deployRec {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(dn.snapshot()) >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return dn.snapshot()
}

// Две неудачи -- тихие повторы с запомненной причиной; третья -- сдаёмся,
// снимаем отметку и говорим людям роутера по-русски.
func TestFailingUpdateRetriesThenGivesUpOnThird(t *testing.T) {
	s := seedSleptRouter(t, "e5e500e5e500e5e500e5e500e5e500e5e500e5e500e5e500e5e500e5e500e5e5")
	dn := s.withDeployNotifier()

	for attempt := 1; attempt <= pendingDeployMaxAttempts; attempt++ {
		c := s.pollUpdate(t)
		st, err := s.d.Users().PendingDeploy(s.uid)
		if err != nil {
			t.Fatal(err)
		}
		if st.Attempts != attempt {
			t.Fatalf("после выдачи %d счёт попыток = %d", attempt, st.Attempts)
		}
		s.result(t, c.ID, "err", "download checksums.txt: HTTP 502")
		if attempt < pendingDeployMaxAttempts {
			if got := s.pendingVersion(t); got != sleptTarget {
				t.Fatalf("неудача %d сняла отметку: %q", attempt, got)
			}
			if calls := dn.snapshot(); len(calls) != 0 {
				t.Fatalf("неудача %d уже написала людям: %+v", attempt, calls)
			}
			// Имитируем истёкший TTL выданной команды: иначе HasActiveCommand
			// законно не даёт повторить раньше чем через полчаса.
			s.q.Sweep(time.Nanosecond)
		}
	}

	if got := s.pendingVersion(t); got != "" {
		t.Fatalf("после третьей неудачи отметка осталась: %q", got)
	}
	calls := waitDeployCalls(dn, 1)
	if len(calls) != 1 {
		t.Fatalf("ждали одно уведомление о сдаче, получили %d", len(calls))
	}
	if calls[0].target != sleptTarget || calls[0].output != "роутер не смог скачать обновление" {
		t.Fatalf("уведомление: %+v", calls[0])
	}
	st, _ := s.d.Users().PendingDeploy(s.uid)
	if st.Attempts != 3 || !strings.Contains(st.LastError, "HTTP 502") {
		t.Fatalf("причина обязана остаться для экрана «Парк»: %+v", st)
	}
	s.q.Sweep(time.Nanosecond)
	if rec := s.poll(t); rec.Code != http.StatusNoContent {
		t.Fatalf("после сдачи обновление выдано снова: код %d", rec.Code)
	}
}

// Три выдачи без итога (агент ушёл в перезагрузку, ответ потерян): четвёртый
// контакт не ставит команду, а сдаётся с понятной причиной.
func TestLostUpdateResultsGiveUpOnNextContact(t *testing.T) {
	s := seedSleptRouter(t, "f6f600f6f600f6f600f6f600f6f600f6f600f6f600f6f600f6f600f6f600f6f6")
	dn := s.withDeployNotifier()

	for attempt := 1; attempt <= pendingDeployMaxAttempts; attempt++ {
		s.pollUpdate(t)
		s.q.Sweep(time.Nanosecond)
	}
	if rec := s.poll(t); rec.Code != http.StatusNoContent {
		t.Fatalf("четвёртая выдача: код %d, ждали отказ от попыток", rec.Code)
	}
	if got := s.pendingVersion(t); got != "" {
		t.Fatalf("отметка осталась после исчерпания попыток: %q", got)
	}
	calls := waitDeployCalls(dn, 1)
	if len(calls) != 1 || !strings.Contains(calls[0].output, "версия не сменилась") {
		t.Fatalf("уведомление о потерянных попытках: %+v", calls)
	}
}

// Намерение трёхмесячной давности -- уже не намерение: роутер включили через
// квартал, и обновлять его «по старой памяти» нельзя.
func TestContactDropsDeployIntentOlderThan90Days(t *testing.T) {
	s := seedSleptRouter(t, "a7a700a7a700a7a700a7a700a7a700a7a700a7a700a7a700a7a700a7a700a7a7")
	old := time.Now().UTC().Add(-91 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := s.d.SQL().Exec(`UPDATE users SET pending_since = ? WHERE id = ?`, old, s.uid); err != nil {
		t.Fatal(err)
	}
	if rec := s.poll(t); rec.Code != http.StatusNoContent {
		t.Fatalf("просроченное намерение выдано: код %d", rec.Code)
	}
	if got := s.pendingVersion(t); got != "" {
		t.Fatalf("просроченная отметка осталась: %q", got)
	}
}
