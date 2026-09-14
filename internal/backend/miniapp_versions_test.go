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

// С цикла 1 прошивку ставят и операторы -- новость о ней адресована им тоже.
func TestMiniappVersionsOperatorSeesFirmwareNews(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	if err := d.RouterOperators().Add(ownedID, 555, 100); err != nil {
		t.Fatalf("grant operator: %v", err)
	}
	h := versionsMux(d)

	_, resp := getVersions(t, h, ownedID, 555)
	if !hasRow(resp.Rows, "firmware") {
		t.Errorf("оператор не видит новость о прошивке, которую теперь может поставить: %+v", resp.Rows)
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

// Модуль сменили, ядро держит старый -- экран говорит о перезагрузке; после
// перезагрузки версии совпали, и предупреждение гаснет само, без нажатий.
func TestMiniappVersionsRebootHintFollowsLoadedModule(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		AwgmgrVersion: "2.19.1", KmodVersion: "3.2.20260930", KmodLoadedVersion: "3.1.20260906", Source: "report",
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

	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		KmodVersion: "3.2.20260930", KmodLoadedVersion: "3.2.20260930", Source: "report",
	}); err != nil {
		t.Fatal(err)
	}
	if _, after := getVersions(t, h, ownedID, telegramUserID); after.RebootHint != "" {
		t.Errorf("после перезагрузки предупреждение не погасло: %q", after.RebootHint)
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
//
// Компонент здесь ОБЯЗАН быть тем, по которому новость реально есть (прошивка:
// её приносит сам роутер, и она видна при выключенном апстриме). На «awgmgr»
// при выключенном источнике новости нет вовсе, и 404 приходил бы из ветки
// «прятать нечего» -- такой тест был бы зелен и без гейта прав. Поэтому рядом
// пинится 200 для владельца: без этой половины тест не различает разрешение и
// отсутствие новости.
func TestMiniappUpdateReminderOperatorCannotHideRouterNews(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	seedLiveSnapshot(t, d, ownedID)
	if err := d.RouterOperators().Add(ownedID, 555, 100); err != nil {
		t.Fatalf("grant operator: %v", err)
	}
	h := versionsMux(d)

	// Оператору отказ -- именно по правам.
	rec := putReminder(t, h, ownedID, 555, "firmware", `{"action":"dismiss"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("оператор скрыл новость всему роутеру (код %d)", rec.Code)
	}

	// Владельцу на том же компоненте -- можно. Если и здесь 404, значит тест
	// упёрся в «новости нет», а не в границу прав.
	ownerRec := putReminder(t, h, ownedID, ownerTG, "firmware", `{"action":"dismiss"}`)
	if ownerRec.Code != http.StatusOK {
		t.Fatalf("владелец не смог скрыть новость (код %d): %s -- тест не различает права и отсутствие новости",
			ownerRec.Code, ownerRec.Body.String())
	}
}

// Прошлая, никем не скрытая новость не имеет права заслонять новую.
//
// Нумерация KeeneticOS даёт инверсию на первом же переходе: строковое
// сравнение ставит «5.02.A.9.0-0» ПОСЛЕ «5.02.A.10.0-0». Пока видимость
// ключевалась одним компонентом, прошлая строка перетирала запись, совпадения
// не происходило, и вышедшее обновление не рисовалось вовсе -- экран говорил
// «обновлений нет» при доступной прошивке. Это отказ по главному критерию
// задачи с другой стороны: не незнание выдано за ответ, а настоящая новость
// выдана за её отсутствие.
func TestMiniappVersionsNewerNewsIsNotShadowedByOldRow(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	yes := true
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		AwgmgrVersion:   "2.18.2+r2",
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.10.0-0",
		KmodVersion:     "3.1.20260906",
		KmodLoaded:      &yes,
		Source:          "report",
	}); err != nil {
		t.Fatal(err)
	}
	// Прошлая новость: человек её просто не тронул. Ensure заводит такую
	// строку на каждой отрисовке, так что она появляется сама.
	if err := d.UpdateReminders().Ensure(ownedID, "firmware", "5.02.A.9.0-0"); err != nil {
		t.Fatal(err)
	}
	h := versionsMux(d)

	_, resp := getVersions(t, h, ownedID, telegramUserID)
	var got *miniappVersionRow
	for i := range resp.Rows {
		if resp.Rows[i].Component == "firmware" {
			got = &resp.Rows[i]
		}
	}
	if got == nil {
		t.Fatalf("новость про 5.02.A.10.0-0 заслонена прошлой строкой и не показана вовсе: %+v", resp.Rows)
	}
	if got.Available != "5.02.A.10.0-0" {
		t.Errorf("показана не та версия: %+v", got)
	}
}

// «Скрыть» относится к ТОЙ версии, о которой шла речь. Вышла следующая -- это
// новая новость, и её видно снова, даже если её номер строкой меньше.
func TestMiniappVersionsDismissedVersionDoesNotHideTheNext(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.9.0-0",
		Source:          "report",
	}); err != nil {
		t.Fatal(err)
	}
	h := versionsMux(d)

	// Увидели и скрыли новость про 9.0-0.
	if _, resp := getVersions(t, h, ownedID, telegramUserID); !hasRow(resp.Rows, "firmware") {
		t.Fatalf("новости про 5.02.A.9.0-0 нет: %+v", resp.Rows)
	}
	if rec := putReminder(t, h, ownedID, telegramUserID, "firmware", `{"action":"dismiss"}`); rec.Code != http.StatusOK {
		t.Fatalf("скрыть не удалось: %d %s", rec.Code, rec.Body.String())
	}
	if _, resp := getVersions(t, h, ownedID, telegramUserID); hasRow(resp.Rows, "firmware") {
		t.Fatalf("скрытая новость осталась на экране: %+v", resp.Rows)
	}

	// Вышла следующая прошивка.
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.10.0-0",
		Source:          "report",
	}); err != nil {
		t.Fatal(err)
	}
	_, resp := getVersions(t, h, ownedID, telegramUserID)
	if !hasRow(resp.Rows, "firmware") {
		t.Errorf("новая новость считается уже скрытой: %+v", resp.Rows)
	}
}

// «Скрыть» держится, даже когда рядом живёт прошлая нетронутая строка.
//
// Обратная сторона починки заслонки, и непокрытое место до этого теста.
// Видимость обязана считаться по паре «компонент + версия». Если считать её
// «есть ли у компонента хоть одна видимая строка», прошлая нетронутая новость
// про 9.0-0 сделает компонент «видимым» и вернёт на экран новость про 10.0-0,
// которую человек только что скрыл. Ни тест на заслонку, ни тест на «отложить»
// такую реализацию не ловят: при ней заслонка как раз проходит, потому что
// проверка стала слабее, а не строже.
func TestMiniappVersionsDismissHoldsWhileOlderRowLives(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.10.0-0",
		Source:          "report",
	}); err != nil {
		t.Fatal(err)
	}
	// Прошлая новость, которую человек не тронул: она остаётся видимой.
	if err := d.UpdateReminders().Ensure(ownedID, "firmware", "5.02.A.9.0-0"); err != nil {
		t.Fatal(err)
	}
	h := versionsMux(d)

	// Новость про 10.0-0 видна (это починка заслонки), и человек её скрывает.
	if _, resp := getVersions(t, h, ownedID, telegramUserID); !hasRow(resp.Rows, "firmware") {
		t.Fatalf("новости про 5.02.A.10.0-0 нет -- проверять скрытие нечем: %+v", resp.Rows)
	}
	if rec := putReminder(t, h, ownedID, telegramUserID, "firmware", `{"action":"dismiss"}`); rec.Code != http.StatusOK {
		t.Fatalf("скрыть не удалось: %d %s", rec.Code, rec.Body.String())
	}

	// Сценарий обязан быть настоящим: прошлая строка про 9.0-0 всё ещё в базе
	// и не скрыта. Без этой проверки тест мог бы стать зелёным по посторонней
	// причине -- если бы строка-заслонка почему-то исчезла.
	list, err := d.UpdateReminders().ListFor(ownedID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var olderAlive bool
	for _, rem := range list {
		if rem.Component == "firmware" && rem.Version == "5.02.A.9.0-0" {
			olderAlive = true
		}
	}
	if !olderAlive {
		t.Fatalf("прошлая строка исчезла -- сценарий не воспроизведён: %+v", list)
	}

	_, resp := getVersions(t, h, ownedID, telegramUserID)
	if hasRow(resp.Rows, "firmware") {
		t.Errorf("скрытая новость вернулась на экран из-за прошлой строки: %+v", resp.Rows)
	}
}

// «Про загрузку модуля ядра ответа нет» и «модуль не загружен» -- разные
// состояния. nil обязан уехать отсутствием ключа: false означает поломку, и
// выдавать молчание агента за неё нельзя (тот же инвариант, что у HydraRoute).
func TestMiniappVersionsNeverClaimsKmodUnloadedWhenUnknown(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		AwgmgrVersion: "2.18.2+r2",
		KmodVersion:   "3.1.20260906", // версия есть, а про загрузку агент молчит
		Source:        "report",
	}); err != nil {
		t.Fatal(err)
	}
	h := versionsMux(d)

	rec, _ := getVersions(t, h, ownedID, telegramUserID)
	if strings.Contains(rec.Body.String(), "kmod_loaded") {
		t.Errorf("неизвестность про загрузку модуля ядра уехала значением: %s", rec.Body.String())
	}
}

// А настоящий отрицательный ответ агента доезжает: false -- это поломка, и
// молчать о ней нельзя.
func TestMiniappVersionsReportsKmodUnloadedWhenAgentSaidSo(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	no := false
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		AwgmgrVersion: "2.18.2+r2",
		KmodVersion:   "3.1.20260906",
		KmodLoaded:    &no,
		Source:        "report",
	}); err != nil {
		t.Fatal(err)
	}
	h := versionsMux(d)

	rec, _ := getVersions(t, h, ownedID, telegramUserID)
	if !strings.Contains(rec.Body.String(), `"kmod_loaded":false`) {
		t.Errorf("агент сказал «не загружен», а в ответе этого нет: %s", rec.Body.String())
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
