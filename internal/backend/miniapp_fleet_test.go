package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
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

// B6: агент ниже agentSelfUpdateFloor не умеет self_update вовсе. /fleet не
// должен считать его «отстающим» (agent_behind=false, иначе кнопка и
// счётчик «Обновить всех отставших» обещали бы то, что кончится отказом
// agent_too_old) и обязан отдать предупреждение про переустановку. Причина
// прошлой (уже неактивной) попытки при этом не должна теряться -- сервер
// review нашёл, что PendingLastErrorText требовал verdict.Behind, а у
// слишком старого агента Behind всегда false.
func TestMiniappFleetCarriesTooOldAgentWarningAndKeepsLastError(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	old := serverVersion
	SetVersion("v0.33.0")
	t.Cleanup(func() { SetVersion(old) })
	d, ownedID, otherID, _ := seedMiniappFleet(t)

	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.12.0"); err != nil {
		t.Fatal(err)
	}
	// Отметка обновления уже снята (сдались или отменили), причина осталась
	// в базе -- как после giveUpPendingDeploy (deploy_attempts.go:154-167).
	if _, err := d.SQL().Exec(`UPDATE users SET pending_last_error = 'download: HTTP 502' WHERE id = ?`, ownedID); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(otherID, "v0.33.0"); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	rec := fleetRequest(t, h, 999)
	resp := fleetResponse(t, rec)
	rows := map[int64]miniappFleetRouter{}
	for _, r := range resp.Routers {
		rows[r.ID] = r
	}

	owned := rows[ownedID]
	if owned.AgentBehind {
		t.Errorf("слишком старый агент не должен считаться «отстающим»: %+v", owned)
	}
	if !strings.Contains(owned.AgentUpdateWarning, "переустановка") {
		t.Errorf("нет предупреждения про переустановку: %+v", owned)
	}
	if owned.PendingLastErrorText != "роутер не смог скачать обновление" {
		t.Errorf("причина прошлой попытки потерялась: %+v", owned)
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

// «На связи» решает сервер, тем же правилом, что и отложенное обновление
// (miniappWakeWindow): статус без инцидентов. Роутер в тревоге, который давно
// молчит, для обновления -- выключен, и клиент обязан это знать, а не
// угадывать своим порогом (final review M1).
func TestMiniappFleetCarriesAwayWithWakeWindowRule(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, ownedID, otherID, _ := seedMiniappFleet(t)
	now := time.Now().UTC()
	hardSince := now.Add(-3 * time.Hour)
	if err := d.State().Save(ownedID, "dns", db.IncidentState{
		UserID: ownedID, CheckName: "dns", CurrentStatus: "hard", ConsecutiveFails: 4, HardSince: &hardSince,
	}); err != nil {
		t.Fatal(err)
	}
	setDashboardTestLastSeen(t, d, ownedID, now.Add(-2*time.Hour))
	setDashboardTestLastSeen(t, d, otherID, now.Add(-10*time.Second))
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	rec := fleetRequest(t, h, 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("сводка парка: код %d, тело %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		Routers []map[string]any `json:"routers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, r := range raw.Routers {
		got[r["nickname"].(string)] = r
	}
	owned, other := got["router-owned"], got["router-other"]
	if owned["status"] != "alert" {
		t.Fatalf("предпосылка: ждали статус alert, получили %v", owned["status"])
	}
	u, err := d.Users().GetByID(ownedID)
	if err != nil {
		t.Fatal(err)
	}
	asleep, _, _ := miniappWakeWindow(Deps{DB: d}, u, "self_update", now)
	if owned["away"] != asleep || owned["away"] != true {
		t.Fatalf("тревожный роутер, молчит 2 ч: away=%v, правило отложенного обновления=%v", owned["away"], asleep)
	}
	if v, ok := other["away"]; !ok || v != false {
		t.Fatalf("роутер на связи: away=%v (есть поле: %v), ждали явное false", v, ok)
	}
}

// Оживление в строке парка: поимённая проекция IntentView, null там, где его
// не ставили, и признак «адрес панели известен» без самого адреса.
func TestMiniappFleetCarriesReviveState(t *testing.T) {
	d, ownedID, h, fake, _ := reviveTestMux(t)
	var otherID int64
	users, _ := d.Users().GetAll()
	for _, u := range users {
		if u.ID != ownedID {
			otherID = u.ID
		}
	}
	if _, err := d.SQL().Exec(`UPDATE users SET awgm_url = ? WHERE id = ?`, "https://panel.example.com", otherID); err != nil {
		t.Fatal(err)
	}
	probeAt := time.Date(2026, 9, 15, 9, 58, 0, 0, time.UTC)
	fake.views[ownedID] = &revive.IntentView{
		Status: "waiting", ExpiresAt: reviveTestExpires, Attempts: 2,
		LastErrorText: "панель не ответила вовремя", LastProbeText: "не отвечает", LastProbeAt: probeAt,
	}

	rec := fleetRequest(t, h, 999)
	body := rec.Body.String()
	resp := fleetResponse(t, rec)
	if !resp.ReviveEnabled || !strings.Contains(body, `"revive_enabled":true`) {
		t.Fatalf("revive_enabled: %s", body)
	}
	rows := map[int64]miniappFleetRouter{}
	for _, r := range resp.Routers {
		rows[r.ID] = r
	}
	got := rows[ownedID].Revive
	want := &miniappFleetRevive{
		Status: "waiting", ExpiresAt: "2026-10-15T12:00:00Z", Attempts: 2,
		LastErrorText: "панель не ответила вовремя", LastProbeText: "не отвечает", LastProbeAt: "2026-09-15T09:58:00Z",
	}
	if got == nil || *got != *want {
		t.Fatalf("revive router-owned: %+v", got)
	}
	if rows[otherID].Revive != nil {
		t.Fatalf("revive router-other: %+v", rows[otherID].Revive)
	}
	if rows[ownedID].PanelAddressKnown || !rows[otherID].PanelAddressKnown {
		t.Fatalf("panel_address_known: owned=%v other=%v", rows[ownedID].PanelAddressKnown, rows[otherID].PanelAddressKnown)
	}
	// Форма постоянная: null и false приходят явно.
	for _, field := range []string{`"revive":null`, `"panel_address_known":false`, `"panel_address_known":true`} {
		if !strings.Contains(body, field) {
			t.Errorf("нет %s в %s", field, body)
		}
	}
	if strings.Contains(body, "panel.example.com") {
		t.Fatalf("адрес панели уехал в сводку: %s", body)
	}
}

func TestMiniappFleetReviveZeroProbeTimeIsEmpty(t *testing.T) {
	_, ownedID, h, fake, _ := reviveTestMux(t)
	fake.views[ownedID] = &revive.IntentView{Status: "running", ExpiresAt: reviveTestExpires}
	resp := fleetResponse(t, fleetRequest(t, h, 999))
	for _, r := range resp.Routers {
		if r.ID == ownedID && (r.Revive == nil || r.Revive.LastProbeAt != "" || r.Revive.Status != "running") {
			t.Fatalf("revive: %+v", r.Revive)
		}
	}
}

func TestMiniappFleetReviveDisabledShape(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, ownedID, _, _ := seedMiniappFleet(t)

	// Сервиса нет вовсе: выключено, у всех null.
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	body := fleetRequest(t, h, 999).Body.String()
	if !strings.Contains(body, `"revive_enabled":false`) || strings.Count(body, `"revive":null`) != 2 {
		t.Fatalf("без сервиса: %s", body)
	}

	// Сервис есть, но выключен (ключа нет): кнопки не будет, а намерение,
	// поставленное до выключения, всё равно видно.
	fake := &fakeRevive{views: map[int64]*revive.IntentView{
		ownedID: {Status: "waiting", ExpiresAt: reviveTestExpires},
	}}
	h = NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, ReviveOverride: fake})
	resp := fleetResponse(t, fleetRequest(t, h, 999))
	if resp.ReviveEnabled {
		t.Fatal("revive_enabled=true у выключенного сервиса")
	}
	seen := false
	for _, r := range resp.Routers {
		if r.ID == ownedID {
			seen = r.Revive != nil && r.Revive.Status == "waiting"
		}
	}
	if !seen {
		t.Fatal("намерение выключенного сервиса пропало из строки")
	}
}

// Сбой чтения состояния -- добавка к строке, а не сама строка: экран
// обязан открыться, а текст ошибки в журнал не идёт.
func TestMiniappFleetReviveStatusErrorKeepsScreen(t *testing.T) {
	_, _, h, fake, logs := reviveTestMux(t)
	fake.statusErr = fmt.Errorf("sql: %s", miniappReviveRoot)
	rec := fleetRequest(t, h, 999)
	resp := fleetResponse(t, rec)
	for _, r := range resp.Routers {
		if r.Revive != nil {
			t.Fatalf("revive при сбое чтения: %+v", r.Revive)
		}
	}
	assertNoReviveSecrets(t, "журнал", logs.String())
}

// Путь чтения результата: админ ставит оживление с паролями, дальше пароли
// ищутся во всём, что после этого читается, а владелец роутера не видит
// оживления нигде.
func TestMiniappReviveNeverReachesReadPaths(t *testing.T) {
	_, ownedID, h, fake, logs := reviveTestMux(t)
	if rec := postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-owned", nil), 999); rec.Code != http.StatusAccepted {
		t.Fatalf("постановка: %d %s", rec.Code, rec.Body.String())
	}
	fake.views[ownedID] = &revive.IntentView{Status: "waiting", ExpiresAt: reviveTestExpires, LastProbeText: "не отвечает"}

	admin := fleetRequest(t, h, 999)
	if admin.Code != http.StatusOK {
		t.Fatalf("админ: %d", admin.Code)
	}
	assertNoReviveSecrets(t, "/fleet админу", admin.Body.String())

	if rec := fleetRequest(t, h, 100); rec.Code != http.StatusNotFound {
		t.Fatalf("/fleet владельцу: %d", rec.Code)
	}
	for _, path := range []string{"/v1/miniapp/routers", fmt.Sprintf("/v1/miniapp/routers/%d", ownedID)} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 100))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s владельцу: %d %s", path, rec.Code, rec.Body.String())
		}
		for _, leak := range []string{"revive", "last_probe", "panel_address_known"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("%s владельцу: есть %q: %s", path, leak, rec.Body.String())
			}
		}
		assertNoReviveSecrets(t, path, rec.Body.String())
	}
	assertNoReviveSecrets(t, "журнал", logs.String())
}

// reviveNoLaunchEngine -- движок, который не должен понадобиться: роутер
// «не отвечает», до переустановки дело не доходит.
type reviveNoLaunchEngine struct{ t *testing.T }

func (e reviveNoLaunchEngine) Launch(context.Context, int64, revive.Secrets, string) (string, error) {
	e.t.Error("переустановка запущена у неотвечающего роутера")
	return "", errors.New("не должно вызываться")
}

func (reviveNoLaunchEngine) Outcome(string) (revive.Outcome, bool) { return revive.Outcome{}, false }

// Прод-путь: настоящий *revive.Service в Deps.Revive (без ReviveOverride).
// Постановка, сводка, повтор с адресом, отмена -- и ни одного пароля ни в
// ответах, ни в журнале, ни открытым текстом в базе.
func TestMiniappReviveProductionServicePath(t *testing.T) {
	stubLatestVersion(t, "v0.33.0")
	d, ownedID, _, _ := seedMiniappFleet(t)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := revive.New(revive.Config{
		DB: d, Key: bytes.Repeat([]byte{7}, revive.KeySize), Engine: reviveNoLaunchEngine{t},
		Probe:  func(context.Context, string) string { return awgmstate.Offline },
		Sleep:  func(context.Context, time.Duration) bool { return true },
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, Logger: logger, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, Revive: svc})

	// Финальное ревью 15.09, I1: локальный адрес, IP и http отказывают до
	// любой записи -- с текстом, который говорит, какой адрес нужен.
	for _, bad := range []string{"https://192.168.31.1:2222", "http://router.example.com", "https://203.0.113.14:2222", "https://router.local"} {
		rec := postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-owned", map[string]any{"awgm_url": bad}), 999)
		code, _, msg := decodeDeployError(t, rec)
		if rec.Code != http.StatusBadRequest || code != "invalid_awgm_url" ||
			msg != "Нужен внешний адрес панели с https — например, имя KeenDNS. Локальные адреса не подходят." {
			t.Fatalf("адрес %q: %d %q %q", bad, rec.Code, code, msg)
		}
		if in, _ := d.Revive().Get(ownedID); in != nil {
			t.Fatalf("адрес %q: намерение записано при отказе", bad)
		}
		if u, _ := d.Users().GetByID(ownedID); u.AWGMURL != nil {
			t.Fatalf("адрес %q записан при отказе", bad)
		}
	}

	body := reviveBody("router-owned", map[string]any{"awgm_url": "https://router.example.com:2222", "expires_days": 7})
	rec := postMiniappJSON(t, h, revivePath(ownedID), body, 999)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"waiting"`) {
		t.Fatalf("постановка: %d %s", rec.Code, rec.Body.String())
	}
	assertNoReviveSecrets(t, "ответ 202", rec.Body.String())
	svc.Wait()

	fleet := fleetRequest(t, h, 999)
	resp := fleetResponse(t, fleet)
	assertNoReviveSecrets(t, "/fleet", fleet.Body.String())
	if !resp.ReviveEnabled {
		t.Fatal("revive_enabled=false у включённого сервиса")
	}
	var row *miniappFleetRouter
	for i := range resp.Routers {
		if resp.Routers[i].ID == ownedID {
			row = &resp.Routers[i]
		}
	}
	if row == nil || row.Revive == nil || row.Revive.Status != "waiting" || !row.PanelAddressKnown {
		t.Fatalf("строка после постановки: %+v", row)
	}
	if row.Revive.LastProbeText != "роутер не отвечает" || row.Revive.LastProbeAt == "" {
		t.Fatalf("опрос: %+v", row.Revive)
	}

	// Адрес уже записан: повтор с ДРУГИМ адресом -- 409 с текстом, где его
	// поменять; с тем же -- не конфликт (двойное нажатие).
	rec = postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-owned", map[string]any{"awgm_url": "https://other.example.com"}), 999)
	if code, _, msg := decodeDeployError(t, rec); rec.Code != http.StatusConflict || code != "awgm_url_already_set" ||
		msg != "Адрес панели у роутера уже записан. Поменять его можно в веб-дашборде." {
		t.Fatalf("повтор с другим адресом: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postMiniappJSON(t, h, revivePath(ownedID), body, 999); rec.Code != http.StatusAccepted {
		t.Fatalf("повтор с тем же адресом: %d %s", rec.Code, rec.Body.String())
	}
	svc.Wait()
	assertNoReviveSecrets(t, "ответ 409", rec.Body.String())

	// В базе -- только шифротекст.
	var dump strings.Builder
	rows, err := d.SQL().Query(`SELECT * FROM revive_intents`)
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			fmt.Fprintf(&dump, "%s|", v)
		}
	}
	_ = rows.Close()
	assertNoReviveSecrets(t, "revive_intents", dump.String())

	rec = deleteMiniapp(t, h, revivePath(ownedID), 999)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"cleared":true}` {
		t.Fatalf("отмена: %d %s", rec.Code, rec.Body.String())
	}
	resp = fleetResponse(t, fleetRequest(t, h, 999))
	for _, r := range resp.Routers {
		if r.ID == ownedID && (r.Revive == nil || r.Revive.Status != "cancelled") {
			t.Fatalf("после отмены: %+v", r.Revive)
		}
	}
	assertNoReviveSecrets(t, "журнал", logs.String())
}

// Сторож и отложенное (спека цикла 2, п. 10): только нечувствительные поля.
func TestMiniappFleetPendingLastDeployIncidentAndWatchdog(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, ownedID, otherID, _ := seedMiniappFleet(t)
	if err := d.Users().MarkPendingDeploy(ownedID, "v0.32.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL().Exec(`UPDATE users SET last_deploy = ?, last_deployed_version = ?, pending_last_error = ? WHERE id = ?`,
		"2026-09-10T08:00:00Z", "v0.31.0", "curl: (22) 404", ownedID); err != nil {
		t.Fatal(err)
	}
	early := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	for _, inc := range []db.IncidentState{
		{UserID: ownedID, CheckName: "dns", CurrentStatus: "hard", HardSince: &late, ConsecutiveFails: 3},
		{UserID: ownedID, CheckName: "tunnel_a", CurrentStatus: "hard", HardSince: &early, ConsecutiveFails: 7},
	} {
		if err := d.State().Save(ownedID, inc.CheckName, inc); err != nil {
			t.Fatal(err)
		}
	}
	stats := heartbeat.Stats{ScansTotal: 42, StaleUsers: 2, Suppressed: 1, LastScanMs: 15, LastScanAt: time.Now().Add(-10 * time.Second), ScanEvery: time.Minute}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, HeartbeatStats: func() heartbeat.Stats { return stats }})

	rec := fleetRequest(t, h, 999)
	resp := fleetResponse(t, rec)
	if wd := resp.Watchdog; wd == nil || wd.ScansTotal != 42 || wd.StaleUsers != 2 || wd.SuppressedUsers != 1 || wd.LastScanMs != 15 ||
		wd.LastScanAt == nil || *wd.LastScanAt != stats.LastScanAt.UTC().Format(time.RFC3339) {
		t.Fatalf("сторож: %+v", resp.Watchdog)
	}
	var owned *miniappFleetRouter
	for i := range resp.Routers {
		if resp.Routers[i].ID == ownedID {
			owned = &resp.Routers[i]
		}
	}
	if owned == nil {
		t.Fatal("нет строки router-owned")
	}
	if owned.PendingSince == nil || *owned.PendingSince != "2026-09-15T10:00:00Z" {
		t.Fatalf("pending_since: %v", owned.PendingSince)
	}
	if owned.LastDeploy == nil || *owned.LastDeploy != (miniappFleetLastDeploy{Version: "v0.31.0", At: "2026-09-10T08:00:00Z", OK: false}) {
		t.Fatalf("last_deploy: %+v", owned.LastDeploy)
	}
	if owned.Incident == nil || *owned.Incident != (miniappFleetIncident{HardSince: "2026-09-16T09:00:00Z", FailCount: 7}) {
		t.Fatalf("incident: %+v", owned.Incident)
	}

	// Пустое -- явный null, а не отсутствие ключа: клиент не гадает.
	var raw struct {
		Routers []map[string]json.RawMessage `json:"routers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, row := range raw.Routers {
		if string(row["id"]) != fmt.Sprint(otherID) {
			continue
		}
		for _, key := range []string{"pending_since", "last_deploy", "incident"} {
			if v, ok := row[key]; !ok || string(v) != "null" {
				t.Errorf("router-other %s = %s (есть=%v), ждали null", key, v, ok)
			}
		}
	}

	if _, err := d.SQL().Exec(`UPDATE users SET pending_last_error = NULL WHERE id = ?`, ownedID); err != nil {
		t.Fatal(err)
	}
	resp = fleetResponse(t, fleetRequest(t, h, 999))
	for _, r := range resp.Routers {
		if r.ID == ownedID && (r.LastDeploy == nil || !r.LastDeploy.OK) {
			t.Fatalf("без ошибки попытки ok=true: %+v", r.LastDeploy)
		}
	}
}

func TestMiniappFleetWatchdogLastScanAtNullBeforeFirstScan(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, _, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999,
		HeartbeatStats: func() heartbeat.Stats { return heartbeat.Stats{ScanEvery: time.Minute} }})
	rec := fleetRequest(t, h, 999)
	if !strings.Contains(rec.Body.String(), `"last_scan_at":null`) {
		t.Fatalf("до первого обхода ждали last_scan_at:null: %s", rec.Body.String())
	}
}
