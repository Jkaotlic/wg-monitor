package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Админский экран парка в мини-аппе. Источник -- ПРОЕКЦИЯ сводки дашборда,
// а не второй сборщик: иначе парк в браузере и парк в приложении разойдутся
// молча, и никто этого не заметит.

func fleetRequest(t *testing.T, h http.Handler, telegramUserID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/fleet", nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func fleetResponse(t *testing.T, rec *httptest.ResponseRecorder) miniappFleetResp {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("сводка парка: код %d, тело %s", rec.Code, rec.Body.String())
	}
	var resp miniappFleetResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("разбор сводки: %v", err)
	}
	return resp
}

// Экран парка -- только админу. Владельцу роутера 404, как и на остальных
// админских поверхностях мини-аппа: по коду ответа нельзя узнать, есть ли
// такая поверхность вообще.
func TestMiniappFleetDeniedToNonAdminWith404(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, _, _, ownerTGID := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	rec := fleetRequest(t, h, ownerTGID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("владелец роутера: код %d, want 404 (тело %s)", rec.Code, rec.Body.String())
	}
}

// Соседний срез -- /v1/dashboard/summary -- отдаёт ssh, креды панели и чат
// уведомлений. Форму оттуда копировать нельзя: поля переписываются поимённо,
// и этот тест сторожит именно то, что копии не случилось.
func TestMiniappFleetNeverLeaksRouterSecrets(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, ownedID, _, _ := seedMiniappFleet(t)
	const (
		awgmURL     = "https://panel.example.com"
		awgmAuth    = "Basic ZXhhbXBsZQ=="
		sshHost     = "198.51.100.20"
		sshUser     = "root"
		expectedMAC = "02:00:00:00:00:01"
		chatID      = -1009876543210
	)
	if _, err := d.SQL().Exec(
		`UPDATE users SET awgm_url=?, awgm_auth=?, ssh_host=?, ssh_user=?, expected_mac=?, telegram_chat_id=? WHERE id=?`,
		awgmURL, awgmAuth, sshHost, sshUser, expectedMAC, chatID, ownedID); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	rec := fleetRequest(t, h, 999)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("сводка парка: код %d, тело %s", rec.Code, body)
	}
	// Ни значений, ни имён полей: имя поля в ответе означает, что значение
	// приедет туда завтра, когда его кто-нибудь заполнит.
	for _, secret := range []string{awgmURL, awgmAuth, sshHost, expectedMAC, "-1009876543210"} {
		if strings.Contains(body, secret) {
			t.Errorf("в сводке парка утекло %q: %s", secret, body)
		}
	}
	for _, field := range []string{"awgm_url", "awgm_auth", "panel_host", "ssh_host", "ssh_user", "expected_mac", "ndms_name", "telegram_chat_id"} {
		if strings.Contains(body, field) {
			t.Errorf("в сводке парка есть поле %q -- секреты уедут в него завтра", field)
		}
	}
}

// Проекция, а не второй сборщик: числа обязаны сойтись с тем, что видит
// дашборд в ту же секунду.
func TestMiniappFleetMatchesDashboardSummaryTotals(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, ownedID, _, _ := seedMiniappFleet(t)
	seedHardIncident(t, d, ownedID, "dns")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	resp := fleetResponse(t, fleetRequest(t, h, 999))
	want, err := buildDashboardSummary(d, time.Now().UTC(), dashboardStatusPolicyFromDeps(Deps{DB: d}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Totals.Routers != want.Totals.Agents {
		t.Errorf("роутеров = %d, у дашборда %d", resp.Totals.Routers, want.Totals.Agents)
	}
	if resp.Totals.Alerts != want.Totals.Alerts {
		t.Errorf("с тревогами = %d, у дашборда %d", resp.Totals.Alerts, want.Totals.Alerts)
	}
	if len(resp.Routers) != len(want.Agents) {
		t.Fatalf("строк парка = %d, у дашборда %d", len(resp.Routers), len(want.Agents))
	}
	var withAlert *miniappFleetRouter
	for i := range resp.Routers {
		if resp.Routers[i].ID == ownedID {
			withAlert = &resp.Routers[i]
		}
	}
	if withAlert == nil {
		t.Fatalf("роутера %d нет в сводке", ownedID)
	}
	if len(withAlert.Incidents) == 0 || withAlert.Incidents[0] != "dns" {
		t.Errorf("имена тревог = %v, want [dns]", withAlert.Incidents)
	}
	if resp.Backend.Version == "" || resp.Backend.LatestVersion != "v0.31.0" {
		t.Errorf("строка о бэкенде = %+v, want свою и доступную версии", resp.Backend)
	}
}

// Пустой парк -- это строка, а не пустой экран: список обязан быть массивом,
// иначе клиент получит null и уронит экран.
func TestMiniappFleetSaysWhenParkIsEmpty(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, err := db.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	rec := fleetRequest(t, h, 999)
	if !strings.Contains(rec.Body.String(), `"routers":[]`) {
		t.Fatalf("пустой парк отдан как null, а не пустым списком: %s", rec.Body.String())
	}
	resp := fleetResponse(t, rec)
	if resp.Totals.Routers != 0 {
		t.Fatalf("роутеров = %d, want 0", resp.Totals.Routers)
	}
}

// Версии берутся из снимка, а не из горячих событий: на экране парка нужны
// «что стоит» по каждому роутеру и «пора обновить», когда доступна новее.
func TestMiniappFleetCarriesVersionsFromSnapshot(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, ownedID, _, _ := seedMiniappFleet(t)
	if err := d.RouterVersions().Upsert(ownedID, db.RouterVersionSnapshot{
		AwgmgrVersion:   "2.18.2",
		FirmwareCurrent: "4.2.7",
		FirmwareAvail:   "4.3.0",
		Source:          "version_audit",
	}); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	resp := fleetResponse(t, fleetRequest(t, h, 999))
	var row *miniappFleetRouter
	for i := range resp.Routers {
		if resp.Routers[i].ID == ownedID {
			row = &resp.Routers[i]
		}
	}
	if row == nil {
		t.Fatalf("роутера %d нет в сводке", ownedID)
	}
	if row.AwgmgrVersion != "2.18.2" {
		t.Errorf("версия панели = %q, want 2.18.2", row.AwgmgrVersion)
	}
	if row.UpdateHint == "" || !strings.Contains(row.UpdateHint, "4.3.0") {
		t.Errorf("подсказка об обновлении = %q, want упоминание доступной 4.3.0", row.UpdateHint)
	}
}

// Дыры уведомлений -- то, ради чего дашборд открывают каждый день: бот может
// не иметь права написать человеку, а у роутера может не оказаться ни одного
// адресата. Оба состояния тихие.
func TestMiniappFleetCarriesNotifyGaps(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, _, _, _ := seedMiniappFleet(t)
	if err := d.Unreachable().Mark(100, "bot was blocked by the user"); err != nil {
		t.Fatal(err)
	}
	orphanID, err := d.Users().Insert("router-orphan", "tok-orphan-000000000000000000000000000000000000000000000000000", "1.1.1.3", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	_ = orphanID
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	resp := fleetResponse(t, fleetRequest(t, h, 999))
	if len(resp.Notify.Unreachable) != 1 || resp.Notify.Unreachable[0].TelegramUserID != 100 {
		t.Errorf("недоступные = %+v, want одного со 100", resp.Notify.Unreachable)
	}
	if len(resp.Notify.RoutersWithoutRecipients) != 1 || resp.Notify.RoutersWithoutRecipients[0] != "router-orphan" {
		t.Errorf("роутеры без адресатов = %v, want [router-orphan]", resp.Notify.RoutersWithoutRecipients)
	}
}

// Экран «Парк» показывает, что с обновлением агента: сколько попыток, почему
// не ставится, отстал ли агент и о чём предупредить. Причина -- по-русски,
// сырой вывод агента в приложение не уезжает.
func TestMiniappFleetCarriesAgentUpdateState(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	old := serverVersion
	SetVersion("v0.33.0")
	t.Cleanup(func() { SetVersion(old) })
	d, ownedID, otherID, _ := seedMiniappFleet(t)

	// router-owned: старый агент, обновление назначено, две попытки, мало места.
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.14.1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().MarkPendingDeploy(ownedID, "v0.33.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := d.Users().IncrementPendingAttempts(ownedID, "v0.33.0"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := d.Users().RecordPendingDeployError(ownedID, "v0.33.0", "self_update: insufficient /opt space: 1200 KB free"); err != nil {
		t.Fatal(err)
	}
	// router-other: уже на версии бэкенда, но в базе висит давняя причина.
	if err := d.Users().UpdateLastSeenAgentVersion(otherID, "v0.33.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL().Exec(`UPDATE users SET pending_last_error = 'download: HTTP 502' WHERE id = ?`, otherID); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	rec := fleetRequest(t, h, 999)
	body := rec.Body.String()
	resp := fleetResponse(t, rec)
	rows := map[int64]miniappFleetRouter{}
	for _, r := range resp.Routers {
		rows[r.ID] = r
	}

	owned := rows[ownedID]
	if owned.PendingAttempts != 2 || !owned.AgentBehind ||
		owned.PendingLastErrorText != "мало свободного места в разделе /opt" ||
		!strings.Contains(owned.AgentUpdateWarning, "64 МБ") {
		t.Errorf("router-owned: %+v", owned)
	}
	other := rows[otherID]
	if other.AgentBehind || other.PendingLastErrorText != "" || other.AgentUpdateWarning != "" {
		t.Errorf("router-other на версии бэкенда: %+v", other)
	}
	for _, raw := range []string{"insufficient", "HTTP 502", "self_update"} {
		if strings.Contains(body, raw) {
			t.Errorf("в сводке парка сырой текст агента %q", raw)
		}
	}
	for _, field := range []string{`"pending_attempts":0`, `"agent_behind":false`, `"pending_last_error_text":""`, `"agent_update_warning":""`} {
		if !strings.Contains(body, field) {
			t.Errorf("форма строки непостоянна: нет %s в %s", field, body)
		}
	}
}

// Решение оператора 15.09: админ выключает уведомления по роутеру, «но и
// одновременно при желании зайти глянуть, что не так». Парк показывает
// выключатель честно и не прячет выключенный роутер.
func TestMiniappFleetCarriesNotifyMutedForCallingAdmin(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, ownedID, otherID, ownerTGID := seedMiniappFleet(t)
	if err := d.NotifyMutes().SetMuted(999, ownedID, true); err != nil {
		t.Fatal(err)
	}
	// Владелец выключил другой роутер -- это не выбор админа.
	if err := d.NotifyMutes().SetMuted(ownerTGID, otherID, true); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	rec := fleetRequest(t, h, 999)
	resp := fleetResponse(t, rec)
	if len(resp.Routers) != 2 {
		t.Fatalf("роутеров %d, ждали 2: выключенный роутер обязан остаться в парке", len(resp.Routers))
	}
	byID := map[int64]miniappFleetRouter{}
	for _, row := range resp.Routers {
		byID[row.ID] = row
	}
	if !byID[ownedID].NotifyMuted {
		t.Errorf("router-owned: notify_muted=false, ждали true")
	}
	if byID[otherID].NotifyMuted {
		t.Errorf("router-other: notify_muted=true -- чужое выключение попало в ответ админу")
	}
	if !strings.Contains(rec.Body.String(), `"notify_muted":false`) {
		t.Errorf("явного false нет в теле -- клиент не отличит «включено» от «поле не пришло»: %s", rec.Body.String())
	}
}
