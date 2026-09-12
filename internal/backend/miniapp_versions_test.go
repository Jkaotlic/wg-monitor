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
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
)

// Живой снимок с рабочего роутера (замер 12.09.2026): панель 2.18.2+r2,
// HydraRoute 3.18.3, прошивка 5.02.A.8.0-3 при доступной 5.02.A.9.0-0, модуль
// ядра 3.1.20260906 на KN-1811.
func seedLiveSnapshot(t *testing.T, d *db.DB, uid int64) {
	t.Helper()
	yes := true
	if err := d.RouterVersions().Upsert(uid, db.RouterVersionSnapshot{
		AwgmgrVersion:   "2.18.2+r2",
		AwgmgrBackend:   "kernel",
		HrneoVersion:    "3.18.3",
		HrneoInstalled:  &yes,
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.9.0-0",
		KeeneticOS:      "KN-1811",
		KmodVersion:     "3.1.20260906",
		KmodModel:       "KN-1811",
		KmodLoaded:      &yes,
		Source:          "report",
	}); err != nil {
		t.Fatal(err)
	}
}

func versionsMux(d *db.DB) http.Handler {
	return NewMux(Deps{
		DB:                  d,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		// Ни одного источника: ровно то состояние, в котором парк живёт
		// сегодня -- ответ на шаг A1 волны 0 не получен, и «источник выключен»
		// обязан звучать словами, а не выглядеть как «всё актуально».
		Upstream: upstream.NewCache(time.Hour, nil),
	})
}

func getVersions(t *testing.T, h http.Handler, routerID, telegramUserID int64) (*httptest.ResponseRecorder, miniappVersionsResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/versions", routerID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp miniappVersionsResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("ответ не разобрался: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec, resp
}

// Экран получает и то, что стоит, и то, почему про остальное неизвестно.
func TestMiniappVersionsReturnsSnapshotAndReasons(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	h := versionsMux(d)

	rec, resp := getVersions(t, h, ownedID, telegramUserID)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Прошивку приносит сам роутер, и выключенный апстрим ей не мешает.
	var firmware *miniappVersionRow
	for i := range resp.Rows {
		if resp.Rows[i].Component == "firmware" {
			firmware = &resp.Rows[i]
		}
	}
	if firmware == nil {
		t.Fatalf("новости о прошивке нет: %+v", resp.Rows)
	}
	if firmware.Installed != "5.02.A.8.0-3" || firmware.Available != "5.02.A.9.0-0" {
		t.Errorf("прошивка описана неверно: %+v", firmware)
	}

	// А про панель источник выключен -- и это сказано причиной, а не молчанием.
	if !hasReason(resp.Unknown, "awgmgr", upstream.ReasonNotConfigured) {
		t.Errorf("выключенный источник не назван причиной: %+v", resp.Unknown)
	}
	if resp.CheckedAt == nil {
		t.Error("нет метки времени: строка про версии без неё обещает больше, чем мы знаем")
	}
}

// Снимка нет вовсе -- это «мы не знаем», а не «всё актуально». Состояние
// нормальное: гейт свежести отчёта мог ни разу не пропустить запись.
func TestMiniappVersionsNoSnapshotSaysSoInsteadOfFresh(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	h := versionsMux(d)

	rec, resp := getVersions(t, h, ownedID, telegramUserID)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(resp.Rows) != 0 {
		t.Errorf("без снимка новостей быть не может: %+v", resp.Rows)
	}
	if !hasReason(resp.Unknown, "awgmgr", upstream.ReasonNoSnapshot) {
		t.Errorf("причина «роутер ещё не рассказал» не названа: %+v", resp.Unknown)
	}
	if resp.CheckedAt != nil {
		t.Error("метка времени без снимка -- обещание того, чего не было")
	}
}

// Новость о прошивке адресована тому, кто один имеет право её нажать.
func TestMiniappVersionsHidesFirmwareNewsFromOperator(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	if err := d.RouterOperators().Add(ownedID, 555, 100); err != nil {
		t.Fatalf("grant operator: %v", err)
	}
	h := versionsMux(d)

	_, resp := getVersions(t, h, ownedID, 555)
	for _, r := range resp.Rows {
		if r.Component == "firmware" {
			t.Errorf("оператор видит новость о прошивке, которую ему не дадут поставить: %+v", r)
		}
	}
	for _, u := range resp.Unknown {
		if u.Component == "firmware" {
			t.Errorf("оператору незачем и причина про прошивку: %+v", u)
		}
	}

	// Владелец её при этом видит.
	_, ownerResp := getVersions(t, h, ownedID, 100)
	var seen bool
	for _, r := range ownerResp.Rows {
		if r.Component == "firmware" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("владелец обязан видеть новость о прошивке: %+v", ownerResp.Rows)
	}
}

// Срез версий несёт имена, версии и метку времени -- и ничего больше.
//
// Соседний операторский срез (/v1/dashboard/summary) отдаёт по каждому роутеру
// awgm_url, awgm_auth, ssh_host, ssh_user, expected_mac и telegram_chat_id
// (снято живьём 12.09.2026). Скопировать его форму сюда было бы утечкой,
// поэтому набор ключей пинится перечислением.
func TestMiniappVersionsCarriesNothingButNamesVersionsAndTimestamp(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	h := versionsMux(d)

	rec, _ := getVersions(t, h, ownedID, telegramUserID)
	body := rec.Body.String()
	for _, forbidden := range []string{
		"router_ip", "base_url", "awgm_url", "awgm_auth", "panel_host",
		"ssh_host", "ssh_user", "expected_mac", "ndms_name",
		"telegram_chat_id", "total_memory_mb",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("в ответе версий утекло %q: %s", forbidden, body)
		}
	}
	// Кнопки обновления пакета не существует: точечного обновления у агента
	// нет, а валовый opkg_upgrade не отвечает на фразу «вышел awg-manager».
	for _, forbidden := range []string{"opkg_upgrade", "firmware_install"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("рядом с новостью появилось действие %q, которого нет за кнопкой", forbidden)
		}
	}
}

// «Про HydraRoute сведений нет» и «HydraRoute не установлен» -- разные ответы.
// Опрос мог не дать ответа, и тогда поле обязано отсутствовать, а не приехать
// false: у владельца, у которого HydraRoute стоит, false был бы прямым враньём.
func TestMiniappVersionsNeverClaimsHrneoMissingWhenUnknown(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		AwgmgrVersion: "2.18.2+r2",
		Source:        "report", // отчёт про HydraRoute не знает вовсе
	}); err != nil {
		t.Fatal(err)
	}
	h := versionsMux(d)

	rec, resp := getVersions(t, h, ownedID, telegramUserID)
	if strings.Contains(rec.Body.String(), "hrneo_installed") {
		t.Errorf("неизвестность про HydraRoute уехала значением: %s", rec.Body.String())
	}
	if !hasReason(resp.Unknown, "hrneo", upstream.ReasonNoSnapshot) {
		t.Errorf("про HydraRoute не названо ни версии, ни причины: %+v", resp.Unknown)
	}
}

// Старый агент про модуль ядра молчит, и п.6 по такому роутеру молчит тоже --
// вместо выдуманной перезагрузки экран говорит про старого агента.
func TestMiniappVersionsSaysAgentTooOldAboutKernelModule(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		AwgmgrVersion: "2.17.2",
		Source:        "report",
	}); err != nil {
		t.Fatal(err)
	}
	h := versionsMux(d)

	_, resp := getVersions(t, h, ownedID, telegramUserID)
	if !hasReason(resp.Unknown, "kmod", upstream.ReasonAgentTooOld) {
		t.Errorf("молчание агента о модуле ядра не названо: %+v", resp.Unknown)
	}
	if resp.RebootHint != "" {
		t.Errorf("без сведений о модуле ядра предупреждение выдумано: %q", resp.RebootHint)
	}
}

// Смена модуля ядра между снимками -- наблюдаемый факт, и только он даёт право
// сказать «нужна перезагрузка».
func TestMiniappVersionsWarnsAboutKernelModuleChange(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	// Панель обновили, и модуль ядра сменился.
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		AwgmgrVersion: "2.19.0",
		KmodVersion:   "3.2.20260930",
		Source:        "report",
	}); err != nil {
		t.Fatal(err)
	}
	h := versionsMux(d)

	_, resp := getVersions(t, h, ownedID, telegramUserID)
	for _, want := range []string{"модуль ядра", "VPN-туннели", "перезагрузки роутера"} {
		if !strings.Contains(resp.RebootHint, want) {
			t.Errorf("предупреждение не говорит про %q: %q", want, resp.RebootHint)
		}
	}
}

func TestMiniappVersionsStrangerGets404(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	h := versionsMux(d)

	rec, _ := getVersions(t, h, ownedID, 777)
	if rec.Code != http.StatusNotFound {
		t.Errorf("незнакомец получил %d, а должен 404 до того, как узнает о роутере", rec.Code)
	}
}

func putReminder(t *testing.T, h http.Handler, routerID, telegramUserID int64, component, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/v1/miniapp/routers/%d/updates/%s", routerID, component),
		strings.NewReader(body))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiniappUpdateReminderRejectsUnknownAction(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	h := versionsMux(d)

	rec := putReminder(t, h, ownedID, telegramUserID, "firmware", `{"action":"delete_forever"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bad_action") {
		t.Errorf("код отказа не bad_action: %s", rec.Body.String())
	}
}

// «Отложить» убирает новость с экрана до срока -- и это видно на самом экране.
func TestMiniappUpdateReminderSnoozeHidesNewsFromScreen(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	h := versionsMux(d)

	// Новость сначала есть.
	_, before := getVersions(t, h, ownedID, telegramUserID)
	if !hasRow(before.Rows, "firmware") {
		t.Fatalf("новости о прошивке нет до «отложить»: %+v", before.Rows)
	}

	rec := putReminder(t, h, ownedID, telegramUserID, "firmware", `{"action":"snooze"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("отложить не удалось: %d %s", rec.Code, rec.Body.String())
	}

	_, after := getVersions(t, h, ownedID, telegramUserID)
	if hasRow(after.Rows, "firmware") {
		t.Errorf("отложенная новость осталась на экране: %+v", after.Rows)
	}
}

// Строка новости живёт на роутере, а не у человека: «скрыть» решает за всех
// получателей этого роутера, поэтому нажать может только владелец или админ.
func TestMiniappUpdateReminderOperatorCannotHideRouterNews(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	if err := d.RouterOperators().Add(ownedID, 555, 100); err != nil {
		t.Fatalf("grant operator: %v", err)
	}
	h := versionsMux(d)

	rec := putReminder(t, h, ownedID, 555, "awgmgr", `{"action":"dismiss"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("оператор скрыл новость всему роутеру (код %d)", rec.Code)
	}
}

func hasReason(list []miniappUnknownRow, component, reason string) bool {
	for _, u := range list {
		if u.Component == component && u.Reason == reason {
			return true
		}
	}
	return false
}

func hasRow(rows []miniappVersionRow, component string) bool {
	for _, r := range rows {
		if r.Component == component {
			return true
		}
	}
	return false
}
