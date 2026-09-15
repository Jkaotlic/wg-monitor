package backend

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
