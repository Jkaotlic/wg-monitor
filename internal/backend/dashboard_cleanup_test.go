package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Цикл 3: старый дашборд удалён, а вместе с ним -- запись роутера из его
// модального окна. Маршрут был мёртвым (app.js его не звал), и живой
// операторский API не должен его воскресить случайной регистрацией.
func TestDashboardEnrollmentsRouteIsGone(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{DB: d, DashboardToken: "secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/enrollments",
		strings.NewReader(`{"nickname":"client-g","kind":"mobile"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("enrollments: код %d, want 404/405 (тело %s)", rec.Code, rec.Body.String())
	}
	if _, err := d.Users().GetByNickname("client-g"); err == nil {
		t.Fatal("удалённый маршрут всё ещё заводит роутер")
	}
}

// Удаление старой страницы входа не трогает сам вход: неверный токен --
// 401 с кодом, по которому приложение и аварийная страница пишут
// «Токен не подошёл».
func TestDashboardLoginRejectsWrongToken(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", strings.NewReader(`{"token":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("код %d, want 401", rec.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "unauthorized" {
		t.Fatalf("тело %q, want code=unauthorized", rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("неверный токен выдал куку")
	}
}

// Bearer-канал удалённого управления (wgm-dash, wgm-deadman) раскатывает
// бэкенд через /v1/dashboard/backend/deploy -- аварийная страница ходит туда
// же. Маршрут обязан пережить удаление старого дашборда.
func TestDashboardBearerBackendDeployStillQueues(t *testing.T) {
	old := serverVersion
	SetVersion("v0.36.0")
	t.Cleanup(func() { SetVersion(old) })

	pending := filepath.Join(t.TempDir(), "backend-update.json")
	h := NewMux(Deps{DashboardToken: "secret", BackendUpdatePath: pending})
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/backend/deploy",
		strings.NewReader(`{"target_version":"v0.37.0"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "wgmonitor.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("код %d, want 202 (тело %s)", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(pending)
	if err != nil {
		t.Fatalf("заявка не записана: %v", err)
	}
	if !strings.Contains(string(raw), `"target_version": "v0.37.0"`) {
		t.Fatalf("заявка: %s", raw)
	}
}
