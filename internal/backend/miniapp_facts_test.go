package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func factsFixture(now time.Time, received time.Time) map[string]db.RouterFact {
	return map[string]db.RouterFact{
		db.FactExit:      {Kind: db.FactExit, ReceivedAt: received, Body: []byte(`{"at":"2026-09-28T07:00:00Z","tunnels":{"awg11":{"vpn_ip":"203.0.113.7","direct_ip":"198.51.100.4","changed":true,"source":"agent","at":"2026-09-28T07:00:00Z","err":"через VPN-туннель: dial nwg0"}}}`)},
		db.FactWAN:       {Kind: db.FactWAN, ReceivedAt: now, Body: []byte(`{"at":"2026-09-28T07:00:00Z","links":[{"name":"eth3","label":"Подключение Ethernet","role":"primary","up":true,"priority":58248},{"name":"cdc_br0","label":"Huawei Mobile Broadband","role":"backup","up":false,"priority":36405,"pingcheck":""},{"name":"usb1","label":"LTE","role":"backup","up":false,"priority":10,"pingcheck":"default"}]}`)},
		db.FactNativeDNS: {Kind: db.FactNativeDNS, ReceivedAt: now, Body: []byte(`{"at":"2026-09-28T07:00:00Z","lists":[{"name":"youtube","domains":14,"target":"Wireguard0","tunnel_id":"awg11","mode":"auto","owner":"firmware","issue":"target_down"}]}`)},
		db.FactHooks:     {Kind: db.FactHooks, ReceivedAt: now, Body: []byte(`{"at":"2026-09-28T07:00:00Z","state":"installed","last_wake_at":"2026-09-28T07:10:00Z","wakes_1h":2,"suppressed_1h":5}`)},
	}
}

// Топология роутера (интерфейсы, источник и текст ошибки замера) -- только
// админу. Та же граница, что у белого списка miniapp_tunnels.go.
func TestBuildMiniappFactsHidesTopologyFromNonAdmin(t *testing.T) {
	now := time.Date(2026, 9, 28, 7, 20, 0, 0, time.UTC)
	owner, _ := json.Marshal(buildMiniappFacts(factsFixture(now, now), false, now))
	for _, secret := range []string{"eth3", "cdc_br0", "Wireguard0", "dial nwg0", `"source"`} {
		if strings.Contains(string(owner), secret) {
			t.Errorf("владельцу утекло %q: %s", secret, owner)
		}
	}
	admin, _ := json.Marshal(buildMiniappFacts(factsFixture(now, now), true, now))
	for _, want := range []string{"eth3", "Wireguard0", "dial nwg0", `"source":"agent"`} {
		if !strings.Contains(string(admin), want) {
			t.Errorf("админу не хватает %q: %s", want, admin)
		}
	}
}

func TestBuildMiniappFactsStaleAndPingCheckWords(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	got := buildMiniappFacts(factsFixture(now, now.Add(-2*time.Hour)), false, now)
	if got.Exit == nil || !got.Exit.Stale || got.WAN.Stale {
		t.Fatalf("stale: exit=%+v wan=%+v", got.Exit, got.WAN)
	}
	words := []string{got.WAN.Links[0].PingCheck, got.WAN.Links[1].PingCheck, got.WAN.Links[2].PingCheck}
	if fmt.Sprint(words) != "[ unset set]" {
		t.Fatalf("pingcheck = %q (nil -- не знаем, \"\" -- не задан, имя -- задан)", words)
	}
	if got.Hooks.LastWakeAt != "2026-09-28T07:10:00Z" || got.Hooks.Wakes1h != 2 {
		t.Fatalf("hooks = %+v", got.Hooks)
	}
}

func getMiniappFacts(t *testing.T, h http.Handler, routerID, tg int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/facts", routerID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tg))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiniappFactsEndpoint(t *testing.T) {
	d, ownedID, _, _, h := maintenanceFleet(t, "v0.47.0")
	now := time.Now().UTC()
	if err := d.RouterFacts().Upsert(ownedID, db.FactExit, []byte(`{"tunnels":{"awg11":{"vpn_ip":"203.0.113.7","direct_ip":"198.51.100.4","changed":true,"source":"awgm","at":"2026-09-28T07:00:00Z"}}}`), now, now); err != nil {
		t.Fatal(err)
	}
	for _, r := range []db.PingRunRow{
		{TunnelID: "awg11", From: now.Add(-3 * time.Hour), To: now.Add(-3 * time.Hour).Add(time.Minute), Fails: 2},
		{TunnelID: "awg11", From: now.Add(-time.Hour), To: now.Add(-time.Hour).Add(time.Minute), Fails: 3},
	} {
		if err := d.PingRuns().Upsert(ownedID, r); err != nil {
			t.Fatal(err)
		}
	}
	rec := getMiniappFacts(t, h, ownedID, 555) // оператор
	if rec.Code != http.StatusOK {
		t.Fatalf("оператор: %d %s", rec.Code, rec.Body.String())
	}
	var resp miniappFactsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Supported || resp.Exit == nil || resp.Exit.Tunnels["awg11"].VPNIP != "203.0.113.7" || resp.PingFails24h["awg11"] != 5 {
		t.Fatalf("resp = %+v", resp)
	}
	if rec := getMiniappFacts(t, h, ownedID, 777); rec.Code != http.StatusNotFound {
		t.Fatalf("посторонний: %d", rec.Code)
	}
}

func TestMiniappFactsOldAgent(t *testing.T) {
	_, ownedID, _, _, h := maintenanceFleet(t, "v0.46.0")
	rec := getMiniappFacts(t, h, ownedID, 100)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"supported":false`) || !strings.Contains(rec.Body.String(), `"ping_fails_24h":{}`) {
		t.Fatalf("старый агент: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"exit"`) {
		t.Fatalf("фактов нет -- блока нет: %s", rec.Body.String())
	}
}
