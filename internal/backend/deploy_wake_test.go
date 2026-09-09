package backend

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Мобильный роутер спал дольше TTL команды: отметка «ждёт v0.22.0» осталась,
// а команды в очереди давно нет. Первый же отчёт после пробуждения обязан
// положить её заново -- иначе обновление не доедет никогда.
func TestReportOnWakeRequeuesPendingDeploy(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "efef00efef00efef00efef00efef00efef00efef00efef00efef00efef00efef"
	if _, err := d.Users().InsertWithKind("mobile-router", tok, "1.1.1.1", "awg0", db.KindMobile); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("mobile-router", db.DeployInfo{
		Arch:                "arm64",
		LastDeployedVersion: "v0.14.1",
		Ring:                "stable",
		PendingVersion:      "v0.22.0",
		PendingSince:        "2026-09-09T10:51:23Z",
	}); err != nil {
		t.Fatal(err)
	}

	sink := &fakeCmdSink{}
	h := NewMux(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            d,
		Dispatcher:    &fakeDisp{},
		CommandSink:   sink,
		PublicBaseURL: "https://backend.example.com",
	})

	body := []byte(`{"ts":"2026-09-09T12:30:00Z","agent_version":"v0.14.1","checks":[{"name":"agent_heartbeat","status":"ok"}]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	queued := sink.snapshotEnqueued()
	if len(queued) != 1 {
		t.Fatalf("ждали одну команду при пробуждении, получили %d", len(queued))
	}
	if queued[0].Action != "self_update" {
		t.Fatalf("action=%q", queued[0].Action)
	}
	if got := queued[0].Args["version"]; got != "v0.22.0" {
		t.Fatalf("version=%v, ждали v0.22.0", got)
	}
	if got := queued[0].Args["repo_base"]; got != "https://backend.example.com/v1/releases/download" {
		t.Fatalf("repo_base=%v", got)
	}

	// Отметка остаётся: обновление ещё не доехало.
	u, err := d.Users().GetByNickname("mobile-router")
	if err != nil {
		t.Fatal(err)
	}
	if u.PendingVersion == nil || *u.PendingVersion != "v0.22.0" {
		t.Fatalf("pending_version=%v, отметка должна остаться", u.PendingVersion)
	}
}

// Отчёты идут раз в минуту. Пока команда в работе, второй отчёт не имеет
// права положить ещё одну -- иначе роутер полезет за бинарём столько раз,
// сколько успел отчитаться.
func TestReportOnWakeDoesNotStackCommands(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "abab00abab00abab00abab00abab00abab00abab00abab00abab00abab00abab"
	if _, err := d.Users().InsertWithKind("mobile-router", tok, "1.1.1.1", "awg0", db.KindMobile); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("mobile-router", db.DeployInfo{
		Arch:                "arm64",
		LastDeployedVersion: "v0.14.1",
		PendingVersion:      "v0.22.0",
		PendingSince:        "2026-09-09T10:51:23Z",
	}); err != nil {
		t.Fatal(err)
	}

	sink := &fakeCmdSink{active: map[string]bool{"self_update": true}}
	h := NewMux(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            d,
		Dispatcher:    &fakeDisp{},
		CommandSink:   sink,
		PublicBaseURL: "https://backend.example.com",
	})

	body := []byte(`{"ts":"2026-09-09T12:31:00Z","agent_version":"v0.14.1","checks":[{"name":"agent_heartbeat","status":"ok"}]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := len(sink.snapshotEnqueued()); got != 0 {
		t.Fatalf("команда уже в работе, ждали 0 новых, получили %d", got)
	}
}
