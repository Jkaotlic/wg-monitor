package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestMiniappExitIPProbeOpenToOperatorWithOnlyTunnelID(t *testing.T) {
	d, ownedID, _, sink, h := maintenanceFleet(t, "v0.47.0")
	seedMiniappTunnelEvent(t, d, ownedID, "awg11", "Wireguard0")
	rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"exit_ip_probe","args":{"tunnel_id":"awg11","ndms_name":"Evil0","service":"https://evil.example"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("оператор: %d %s", rec.Code, rec.Body.String())
	}
	args := sink.enqueued[0].Args
	if len(args) != 1 || args["tunnel_id"] != "awg11" {
		t.Fatalf("до агента доезжает только tunnel_id: %+v", args)
	}
	if rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"exit_ip_probe","args":{"tunnel_id":"awg99"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("чужой туннель: %d", rec.Code)
	}
}

func TestMiniappV047CommandsNeedAgentFloor(t *testing.T) {
	d, ownedID, _, _, h := maintenanceFleet(t, "v0.46.0")
	seedMiniappTunnelEvent(t, d, ownedID, "awg11", "Wireguard0")
	for _, body := range []string{`{"action":"exit_ip_probe","args":{"tunnel_id":"awg11"}}`, `{"action":"awgm_logs"}`} {
		if rec := postMiniappCommand(t, h, ownedID, 100, body); rec.Code != http.StatusConflict {
			t.Errorf("%s на агенте v0.46.0: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

// Журнал -- владельцу и админу. Оператору 403 owner_only, как у всех
// owner-only действий.
func TestMiniappAwgmLogsOwnerOnly(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.47.0")
	if rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"awgm_logs"}`); rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte("owner_only")) {
		t.Fatalf("оператор: %d %s", rec.Code, rec.Body.String())
	}
	for _, who := range []int64{100, 999} {
		if rec := postMiniappCommand(t, h, ownedID, who, `{"action":"awgm_logs","args":{"level":"error"}}`); rec.Code != http.StatusAccepted {
			t.Fatalf("%d: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 2 {
		t.Fatalf("очередь: %+v", sink.enqueued)
	}
}

func TestMiniappAwgmLogsDropsClientSanitize(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.47.0")
	rec := postMiniappCommand(t, h, ownedID, 100, `{"action":"awgm_logs","args":{"level":"warn","group":"tunnel","limit":50,"sanitize":false,"bucket":"singbox"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got, _ := json.Marshal(sink.enqueued[0].Args)
	if string(got) != `{"group":"tunnel","level":"warn","limit":50}` {
		t.Fatalf("args = %s: sanitize и bucket до агента не доезжают", got)
	}
	for _, bad := range []string{`{"level":"debug"}`, `{"group":"evil"}`, `{"limit":0}`, `{"limit":201}`} {
		rec := postMiniappCommand(t, h, ownedID, 100, `{"action":"awgm_logs","args":`+bad+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}

// Опрос результата журнала оператором закрыт тем же гейтом.
func TestMiniappAwgmLogsResultClosedToOperator(t *testing.T) {
	d, ownedID, ownerTG, _, h := miniappRealQueueFleet(t)
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.47.0"); err != nil {
		t.Fatal(err)
	}
	rec := postMiniappCommand(t, h, ownedID, ownerTG, `{"action":"awgm_logs"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("владелец: %d %s", rec.Code, rec.Body.String())
	}
	var issued struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &issued)
	if res := miniappPollResult(t, h, ownedID, 555, issued.CmdID); res.Code != http.StatusForbidden {
		t.Fatalf("оператор дочитал журнал: %d %s", res.Code, res.Body.String())
	}
}

func TestDashboardUpdateAgentConfigPassesWakeHooksOff(t *testing.T) {
	_, sink, h := newAgentConfigMux(t)
	rec := postDashboardCommand(t, h, `{"action":"update_agent_config","args":{"wake_hooks_off":true}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, ok := sink.enqueued[0].Args["wake_hooks_off"].(bool); !ok || !v {
		t.Fatalf("args = %+v", sink.enqueued[0].Args)
	}
}
