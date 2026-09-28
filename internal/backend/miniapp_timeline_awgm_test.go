package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestMiniappTimelineCarriesAwgmAnnotation(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	now := time.Now().UTC().Truncate(time.Second)
	_ = d.Events().Insert(ownedID, "tunnel_awg11", "fail", "{}", now.Add(-2*time.Hour))
	_ = d.Events().Insert(ownedID, "tunnel_awg11", "ok", "{}", now.Add(-110*time.Minute))
	if err := d.PingRuns().Upsert(ownedID, db.PingRunRow{TunnelID: "awg11", From: now.Add(-122 * time.Minute), To: now.Add(-115 * time.Minute), Fails: 4, WentDown: true, Recovered: true}); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/timeline", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp miniappTimelineResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Incidents) != 1 || resp.Incidents[0].AwgmFails != 4 || resp.Incidents[0].AwgmFirstFail == "" || !resp.Incidents[0].AwgmWentDown {
		t.Fatalf("incidents = %+v", resp.Incidents)
	}
}
