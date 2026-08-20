package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeCabinet -- кабинет провайдера в тесте. Конфиг он отдаёт байтами, и
// главное, что проверяется ниже: эти байты не появляются ни в одном ответе
// клиенту, только в команде агенту.
type fakeCabinet struct {
	accounts map[string]VPNAccount
	conf     []byte
	err      error
	issued   []string
}

func (f *fakeCabinet) Account(_ context.Context, _ int64, provider string) (VPNAccount, error) {
	if f.err != nil {
		return VPNAccount{}, f.err
	}
	acc, ok := f.accounts[provider]
	if !ok {
		return VPNAccount{Provider: provider, Connected: false, Note: "кабинет не подключён"}, nil
	}
	return acc, nil
}

func (f *fakeCabinet) IssueConfig(_ context.Context, _ int64, provider, optionID string) (VPNIssuedConfig, error) {
	if f.err != nil {
		return VPNIssuedConfig{}, f.err
	}
	f.issued = append(f.issued, provider+":"+optionID)
	return VPNIssuedConfig{TunnelName: provider + "_" + optionID, Conf: f.conf}, nil
}

func cabinetDeps(t *testing.T, cab VPNCabinet, sink *dashboardActionSink) (Deps, int64, int64) {
	t.Helper()
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	return Deps{
		DB:                  d,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		CommandSink:         sink,
		VPNCabinet:          cab,
	}, ownedID, telegramUserID
}

func TestMiniappVPNAccountsListsCabinets(t *testing.T) {
	cab := &fakeCabinet{accounts: map[string]VPNAccount{
		"amnezia": {
			Provider: "amnezia", Label: "Amnezia Premium", Connected: true,
			Status: "active", EndsAt: "2026-12-01", DevicesUsed: 2, DevicesMax: 5,
			Options: []VPNOption{{ID: "nl", Label: "Нидерланды"}, {ID: "de", Label: "Германия", Issued: true}},
		},
	}}
	deps, ownedID, tgUser := cabinetDeps(t, cab, &dashboardActionSink{})
	h := NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/vpn", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp miniappVPNResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Accounts) != 2 {
		t.Fatalf("кабинета должно быть два (оба, даже неподключённый): %+v", resp.Accounts)
	}
	if resp.Accounts[0].DevicesMax != 5 || len(resp.Accounts[0].Options) != 2 {
		t.Fatalf("amnezia = %+v", resp.Accounts[0])
	}
	// Неподключённый кабинет -- это состояние, а не ошибка, и он обязан
	// объяснить себя словами.
	if resp.Accounts[1].Connected || resp.Accounts[1].Note == "" {
		t.Fatalf("hidemyname = %+v", resp.Accounts[1])
	}
}

// Главное свойство фазы: содержимое конфига не ходит через клиент. Клиент
// присылает выбор, ответ несёт только идентификатор команды, а сам конфиг
// уезжает агенту.
func TestMiniappVPNIssueKeepsConfigServerSide(t *testing.T) {
	conf := []byte("[Interface]\nPrivateKey = SECRET_KEY_MUST_NOT_LEAK\n")
	cab := &fakeCabinet{conf: conf, accounts: map[string]VPNAccount{"amnezia": {Provider: "amnezia", Connected: true}}}
	sink := &dashboardActionSink{}
	deps, ownedID, tgUser := cabinetDeps(t, cab, sink)
	h := NewMux(deps)

	body := `{"provider":"amnezia","option_id":"nl"}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/vpn/issue", ownedID), bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "SECRET_KEY_MUST_NOT_LEAK") || strings.Contains(rec.Body.String(), base64.StdEncoding.EncodeToString(conf)) {
		t.Fatalf("конфиг утёк в ответ клиенту: %s", rec.Body.String())
	}
	if len(sink.enqueued) != 1 || sink.enqueued[0].Action != "tunnel_import" {
		t.Fatalf("enqueued = %+v", sink.enqueued)
	}
	got, _ := sink.enqueued[0].Args["conf"].(string)
	if got != base64.StdEncoding.EncodeToString(conf) {
		t.Fatalf("агенту обязан уехать тот же конфиг: %q", got)
	}
	if sink.enqueued[0].Args["name"] != "amnezia_nl" || sink.enqueued[0].Args["replace"] != true {
		t.Fatalf("args = %+v", sink.enqueued[0].Args)
	}
}

// Оператор выпускает конфиг наравне с владельцем: спека рабочего места
// (2026-08-02, §4.2) прямо говорит, что оператора добавляют чинить, и дробить
// его права дальше -- значит показывать ему кнопку, которая ему запрещена.
// Прошивка -- отдельный случай: она меняет устройство и остаётся владельцу.
func TestMiniappVPNIssueAllowedForOperator(t *testing.T) {
	cab := &fakeCabinet{conf: []byte("x")}
	sink := &dashboardActionSink{}
	deps, ownedID, _ := cabinetDeps(t, cab, sink)
	if err := deps.DB.RouterOperators().Add(ownedID, 555, 100); err != nil {
		t.Fatalf("grant operator: %v", err)
	}
	h := NewMux(deps)

	body := `{"provider":"amnezia","option_id":"nl"}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/vpn/issue", ownedID), bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 555))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202 for operator, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 {
		t.Fatalf("команда обязана уйти агенту: %+v", sink.enqueued)
	}
}

func TestMiniappVPNIssueRejectsUnknownProvider(t *testing.T) {
	cab := &fakeCabinet{conf: []byte("x")}
	sink := &dashboardActionSink{}
	deps, ownedID, tgUser := cabinetDeps(t, cab, sink)
	h := NewMux(deps)

	body := `{"provider":"someone-else","option_id":"nl"}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/vpn/issue", ownedID), bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// tunnel_import по-прежнему закрыт для клиента: единственный путь конфига на
// роутер -- через кабинет на сервере.
func TestMiniappTunnelImportStillDeniedFromClient(t *testing.T) {
	cab := &fakeCabinet{conf: []byte("x")}
	sink := &dashboardActionSink{}
	deps, ownedID, tgUser := cabinetDeps(t, cab, sink)
	h := NewMux(deps)

	rec := postMiniappCommand(t, h, ownedID, tgUser, `{"action":"tunnel_import","args":{"conf":"aGk="}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("ничего не должно уйти агенту: %+v", sink.enqueued)
	}
}
