package backend

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// DEP-02: правка метаданных -- не read-modify-write служебных полей. Пока
// обработчик держал прочитанную строку, отчёт агента снял назначенное
// обновление (агент встал на цель); запись старого снимка возвращала
// очищенный pending, и раскатка шла заново на уже обновлённый роутер.
func TestDashboardEditAgentDoesNotResurrectClearedPending(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedEditAgent(t, d)
	u, _ := d.Users().GetByNickname("client-g")
	if err := d.Users().MarkPendingDeploy(u.ID, "v0.46.0", "2026-09-27T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	dashboardEditAfterRead = func() {
		// Отчёт агента в щели: версия встала, отметка снята.
		if err := d.Users().UpdateLastSeenAgentVersion(u.ID, "v0.46.0"); err != nil {
			t.Error(err)
		}
	}
	defer func() { dashboardEditAfterRead = nil }()

	h := NewMux(Deps{DB: d, DashboardToken: "secret"})
	req := httptest.NewRequest(http.MethodPut, "/v1/dashboard/agents/client-g",
		strings.NewReader(`{"awgm_url":"https://new.router.example"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d %s", rec.Code, rec.Body.String())
	}
	got, _ := d.Users().GetByNickname("client-g")
	if stringValue(got.PendingVersion) != "" {
		t.Fatalf("очищенное назначенное обновление вернулось: %q", stringValue(got.PendingVersion))
	}
	if stringValue(got.LastDeployedVersion) != "v0.46.0" {
		t.Fatalf("версия агента откатилась к снимку: %q", stringValue(got.LastDeployedVersion))
	}
	if stringValue(got.AWGMURL) != "https://new.router.example" {
		t.Fatalf("правка не записана: %q", stringValue(got.AWGMURL))
	}
}
