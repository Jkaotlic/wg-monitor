package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

type confirmPhraseCase struct {
	Name   string `json:"name"`
	Phrase string `json:"phrase"`
	Typed  string `json:"typed"`
	OK     bool   `json:"ok"`
}

// Те же случаи прогоняет miniapp/test/confirmPhrase.shared.test.js против
// confirmReady: экран и сервер обязаны соглашаться, иначе человек увидит
// активную кнопку и получит отказ.
func TestConfirmPhraseSharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/confirm_phrase_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []confirmPhraseCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 8 {
		t.Fatalf("случаев %d -- файл обрезан?", len(cases))
	}
	for _, c := range cases {
		if got := confirmPhraseMatches(c.Typed, c.Phrase); got != c.OK {
			t.Errorf("%s: confirmPhraseMatches(%q, %q) = %v, want %v", c.Name, c.Typed, c.Phrase, got, c.OK)
		}
	}
}

// Пустое имя роутера не совпадает ни с чем: иначе пустой набор прошёл бы.
func TestConfirmPhraseEmptyRouterNameNeverMatches(t *testing.T) {
	if confirmPhraseMatches("", "") || confirmPhraseMatches("  ", " ") {
		t.Error("пустое имя роутера подтвердилось пустым набором")
	}
}

func TestRouterCooldownWindow(t *testing.T) {
	clock := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c := newRouterCooldown(5*time.Minute, func() time.Time { return clock })
	if !c.tryStart(1) {
		t.Fatal("первая перезагрузка обязана пройти")
	}
	if c.tryStart(1) {
		t.Fatal("повтор внутри окна прошёл")
	}
	if !c.tryStart(2) {
		t.Fatal("окно одного роутера задело другой")
	}
	clock = clock.Add(5 * time.Minute)
	if !c.tryStart(1) {
		t.Fatal("после окна перезагрузка снова разрешена")
	}
	c.release(1)
	if !c.tryStart(1) {
		t.Fatal("release обязан снять окно: постановка не удалась, роутер не перезагружается")
	}
}

func TestMiniappFirmwareInstallRequiresConfirm(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	for _, body := range []string{
		`{"action":"firmware_install"}`,
		`{"action":"firmware_install","confirm":"router-other"}`,
		`{"action":"firmware_install","confirm":"routerowned"}`,
	} {
		rec := postMiniappCommand(t, h, ownedID, 100, body)
		if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("confirm_mismatch")) {
			t.Errorf("%s: код %d тело %s, ожидался 400 confirm_mismatch", body, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("прошивка без верной фразы ушла агенту: %+v", sink.enqueued)
	}
	rec := postMiniappCommand(t, h, ownedID, 100, `{"action":"firmware_install","confirm":"  ROUTER\u2011OWNED "}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("верная фраза с другим регистром и неразрывным дефисом: код %d тело %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappRebootRequiresConfirmButServiceRestartDoesNot(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	if rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"service_restart","args":{"name":"router"}}`); rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("confirm_mismatch")) {
		t.Errorf("перезагрузка без фразы: код %d тело %s", rec.Code, rec.Body.String())
	}
	if rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"service_restart","args":{"name":"hrneo"}}`); rec.Code != http.StatusAccepted {
		t.Errorf("перезапуск HydraRoute не требует фразы: код %d тело %s", rec.Code, rec.Body.String())
	}
	if rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"service_restart","args":{"name":"router"},"confirm":"router-owned"}`); rec.Code != http.StatusAccepted {
		t.Errorf("перезагрузка с фразой: код %d тело %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 2 {
		t.Fatalf("в очереди %d команд, want 2: %+v", len(sink.enqueued), sink.enqueued)
	}
}

func TestMiniappRebootCooldown(t *testing.T) {
	_, ownedID, otherID, sink, h := maintenanceFleet(t, "v0.32.0")
	const reboot = `{"action":"service_restart","args":{"name":"router"},"confirm":"router-owned"}`
	if rec := postMiniappCommand(t, h, ownedID, 555, reboot); rec.Code != http.StatusAccepted {
		t.Fatalf("первая перезагрузка: код %d тело %s", rec.Code, rec.Body.String())
	}
	rec := postMiniappCommand(t, h, ownedID, 100, reboot)
	if rec.Code != http.StatusTooManyRequests || !bytes.Contains(rec.Body.Bytes(), []byte("reboot_cooldown")) ||
		!bytes.Contains(rec.Body.Bytes(), []byte("роутер уже перезагружается")) {
		t.Fatalf("повтор: код %d тело %s, ожидался 429 reboot_cooldown", rec.Code, rec.Body.String())
	}
	if rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"service_restart","args":{"name":"hrneo"}}`); rec.Code != http.StatusAccepted {
		t.Errorf("кулдаун перезагрузки задел перезапуск HydraRoute: код %d", rec.Code)
	}
	if rec := postMiniappCommand(t, h, otherID, 999, `{"action":"service_restart","args":{"name":"router"},"confirm":"router-other"}`); rec.Code != http.StatusAccepted {
		t.Errorf("кулдаун одного роутера задел другой: код %d тело %s", rec.Code, rec.Body.String())
	}
	reboots := 0
	for _, c := range sink.enqueued {
		if c.Action == "service_restart" && c.Args["name"] == "router" {
			reboots++
		}
	}
	if reboots != 2 {
		t.Errorf("перезагрузок в очереди %d, want 2", reboots)
	}
}

// Спящему роутеру команда не откладывается до пробуждения: экран честно
// говорит, сколько она его подождёт.
type miniappEnqueueResp struct {
	CmdID         string `json:"cmd_id"`
	RouterAsleep  bool   `json:"router_asleep"`
	RouterStatus  string `json:"router_status"`
	WakeWindowMin int    `json:"wake_window_min"`
}

func TestMiniappCommandResponseSaysRouterAsleep(t *testing.T) {
	d, ownedID, _, _, h := maintenanceFleet(t, "v0.32.0")
	rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"hrneo_update"}`)
	var resp miniappEnqueueResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusAccepted {
		t.Fatalf("код %d тело %s err %v", rec.Code, rec.Body.String(), err)
	}
	if resp.CmdID == "" || !resp.RouterAsleep || resp.RouterStatus != "offline" || resp.WakeWindowMin != 10 {
		t.Errorf("роутер без отчётов: %+v, ожидались router_asleep=true, router_status=offline и 10 минут", resp)
	}

	setDashboardTestLastSeen(t, d, ownedID, time.Now())
	rec = postMiniappCommand(t, h, ownedID, 555, `{"action":"opkg_upgrade"}`)
	if rec.Code != http.StatusAccepted || bytes.Contains(rec.Body.Bytes(), []byte("router_asleep")) {
		t.Errorf("роутер на связи: код %d тело %s, признака сна быть не должно", rec.Code, rec.Body.String())
	}
}

// M7 (fix round 1): «спит» и «не на связи» -- разные состояния для экрана
// (разный текст для владельца), и ответ обязан их различать полем
// router_status, а не сплющивать в один булев router_asleep.
func TestMiniappCommandResponseDistinguishesSleepingFromOffline(t *testing.T) {
	d, ownedID, _, _, h := maintenanceFleet(t, "v0.32.0")

	// Офлайн: отчётов не было вовсе -- age==nil в dashboardAgentFromUser.
	rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"hrneo_update"}`)
	var offline miniappEnqueueResp
	if err := json.Unmarshal(rec.Body.Bytes(), &offline); err != nil || rec.Code != http.StatusAccepted {
		t.Fatalf("офлайн: код %d тело %s err %v", rec.Code, rec.Body.String(), err)
	}
	if !offline.RouterAsleep || offline.RouterStatus != "offline" {
		t.Errorf("роутер без отчётов: %+v, ожидался router_status=offline", offline)
	}

	// Спящий: мобильный роутер, последний отчёт 40 минут назад -- внутри окна
	// sleeping (30 мин -- 24 часа для mobile, dashboardStatusPolicy).
	if _, err := d.SQL().Exec(`UPDATE users SET kind = 'mobile' WHERE id = ?`, ownedID); err != nil {
		t.Fatal(err)
	}
	setDashboardTestLastSeen(t, d, ownedID, time.Now().Add(-40*time.Minute))
	rec = postMiniappCommand(t, h, ownedID, 555, `{"action":"opkg_upgrade"}`)
	var sleeping miniappEnqueueResp
	if err := json.Unmarshal(rec.Body.Bytes(), &sleeping); err != nil || rec.Code != http.StatusAccepted {
		t.Fatalf("спящий: код %d тело %s err %v", rec.Code, rec.Body.String(), err)
	}
	if !sleeping.RouterAsleep || sleeping.RouterStatus != "sleeping" {
		t.Errorf("спящий мобильный роутер: %+v, ожидался router_status=sleeping", sleeping)
	}
}
