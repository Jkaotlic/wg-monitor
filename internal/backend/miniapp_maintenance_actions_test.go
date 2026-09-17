package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const deadFeedURL = "https://feed.example.com/aarch64-k3.10/Packages.gz"

// maintenanceFleet -- парк из seedMiniappFleet: владелец 100 у ownedID,
// оператор 555 у ownedID, админ бота 999, агент ownedID на заданной версии.
func maintenanceFleet(t *testing.T, agentVersion string) (*db.DB, int64, int64, *dashboardActionSink, http.Handler) {
	t.Helper()
	d, ownedID, otherID, _ := seedMiniappFleet(t)
	if err := d.RouterOperators().Add(ownedID, 555, 100); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, agentVersion); err != nil {
		t.Fatal(err)
	}
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})
	return d, ownedID, otherID, sink, h
}

// Кнопки обслуживания переехали из бота, и круг у них тот же: админ, владелец
// и операторы. Оператор -- самый узкий из трёх, поэтому проверяем им.
// До агента доезжают только разрешённые аргументы.
func TestMiniappMaintenanceActionsOpenToOperator(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	for _, tc := range []struct {
		body     string
		action   string
		wantArgs map[string]any
	}{
		{`{"action":"awgm_update","args":{"force":true}}`, "awgm_update", map[string]any{}},
		{`{"action":"hrneo_update","args":{"pkg":"evil"}}`, "hrneo_update", map[string]any{}},
		{`{"action":"opkg_upgrade","args":{"x":1}}`, "opkg_upgrade", map[string]any{}},
		{`{"action":"service_restart","args":{"name":"hrneo","extra":"x"}}`, "service_restart", map[string]any{"name": "hrneo"}},
		{`{"action":"service_restart","args":{"name":"awgmgr"}}`, "service_restart", map[string]any{"name": "awgmgr"}},
		{`{"action":"opkg_feed_disable","args":{"url":"` + deadFeedURL + `","extra":"x"}}`, "opkg_feed_disable", map[string]any{"url": deadFeedURL}},
		{`{"action":"firmware_install","args":{},"confirm":"router-owned"}`, "firmware_install", map[string]any{}},
	} {
		sink.enqueued = nil
		rec := postMiniappCommand(t, h, ownedID, 555, tc.body)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: код %d тело %s", tc.body, rec.Code, rec.Body.String())
		}
		if len(sink.enqueued) != 1 || sink.enqueued[0].Action != tc.action {
			t.Fatalf("%s: в очереди %+v", tc.body, sink.enqueued)
		}
		if fmt.Sprint(sink.enqueued[0].Args) != fmt.Sprint(tc.wantArgs) {
			t.Errorf("%s: агенту ушло %v, want %v", tc.body, sink.enqueued[0].Args, tc.wantArgs)
		}
	}
}

func TestMiniappServiceRestartRejectsForeignName(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	for _, body := range []string{
		`{"action":"service_restart","args":{"name":"wat"}}`,
		`{"action":"service_restart","args":{}}`,
	} {
		rec := postMiniappCommand(t, h, ownedID, 555, body)
		if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("invalid_service")) {
			t.Errorf("%s: код %d тело %s, ожидался 400 invalid_service", body, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("постороннее имя ушло агенту: %+v", sink.enqueued)
	}
}

func TestMiniappFeedDisableRejectsBadURL(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	for _, body := range []string{
		`{"action":"opkg_feed_disable","args":{}}`,
		`{"action":"opkg_feed_disable","args":{"url":"ftp://feed.example.com/Packages.gz"}}`,
		`{"action":"opkg_feed_disable","args":{"url":"https://feed.example.com/a b"}}`,
		`{"action":"opkg_feed_disable","args":{"url":"https://user:pw@example.com/x"}}`,
		`{"action":"opkg_feed_disable","args":{"url":"https://feed.example.com/x?y=1"}}`,
	} {
		rec := postMiniappCommand(t, h, ownedID, 555, body)
		if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("invalid_feed_url")) {
			t.Errorf("%s: код %d тело %s, ожидался 400 invalid_feed_url", body, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("негодный адрес ушёл агенту: %+v", sink.enqueued)
	}
}

func TestMiniappMaintenanceStrangerGets404(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	rec := postMiniappCommand(t, h, ownedID, 777, `{"action":"awgm_update"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("посторонний: код %d, ожидался 404", rec.Code)
	}
	if len(sink.enqueued) != 0 {
		t.Fatal("команда постороннего встала в очередь")
	}
}

// Пол версии -- до очереди: старый агент ответил бы «unknown action», а
// экран обещал бы обновление, которого не будет.
func TestMiniappUpdatesRefusedToOldAgent(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.31.1")
	for _, action := range []string{"awgm_update", "hrneo_update"} {
		rec := postMiniappCommand(t, h, ownedID, 555, fmt.Sprintf(`{"action":%q}`, action))
		if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte("agent_too_old")) {
			t.Errorf("%s на v0.31.1: код %d тело %s, ожидался 409 agent_too_old", action, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("старому агенту ушло: %+v", sink.enqueued)
	}
	// opkg_upgrade агент умеет давно -- пола у него нет.
	if rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"opkg_upgrade"}`); rec.Code != http.StatusAccepted {
		t.Errorf("opkg_upgrade на v0.31.1: код %d, ожидался 202", rec.Code)
	}
}

// «Запрет на входе, выход открыт»: кто вправе нажать -- вправе и прочитать
// итог; посторонний не читает ни то, ни другое.
func TestMiniappMaintenanceResultReadableByOperatorNotStranger(t *testing.T) {
	d, ownedID, _, q, h := miniappRealQueueFleet(t)
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.32.0"); err != nil {
		t.Fatal(err)
	}
	rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"awgm_update"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("оператор, awgm_update: код %d тело %s", rec.Code, rec.Body.String())
	}
	var issued struct {
		CmdID string `json:"cmd_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(context.Background(), ownedID, 0); !ok {
		t.Fatal("агент не забрал команду")
	}
	const outcome = `{"updated":true,"from":"2.19.0+r2","to":"2.19.1","kmod_installed":"3.2.20260930","kmod_loaded":"3.1.20260906","reboot_needed":true}`
	if err := q.RecordResult(ownedID, wire.CommandResult{ID: issued.CmdID, Status: "ok", Output: outcome}); err != nil {
		t.Fatal(err)
	}
	if res := miniappPollResult(t, h, ownedID, 555, issued.CmdID); res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("reboot_needed")) {
		t.Errorf("оператор, итог: код %d тело %s", res.Code, res.Body.String())
	}
	if res := miniappPollResult(t, h, ownedID, 777, issued.CmdID); res.Code != http.StatusNotFound || bytes.Contains(res.Body.Bytes(), []byte("reboot_needed")) {
		t.Errorf("посторонний, итог: код %d тело %s", res.Code, res.Body.String())
	}
}
