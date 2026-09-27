package backend

import (
	"net/http"
	"testing"
)

// VER-01: ошибка чтения напоминаний не имеет права превращаться в «всё
// актуально» со свежей датой -- экран получает ошибку, а не пустой список.
func TestMiniappVersionsReminderDBErrorIsNotAllUpToDate(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	if _, err := d.SQL().Exec(`DROP TABLE router_update_reminders`); err != nil {
		t.Fatal(err)
	}
	rec, resp := getVersions(t, versionsMux(d), ownedID, telegramUserID)
	if rec.Code == http.StatusOK {
		t.Fatalf("ошибка базы отдана как 200: rows=%+v checked_at=%v", resp.Rows, resp.CheckedAt)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
