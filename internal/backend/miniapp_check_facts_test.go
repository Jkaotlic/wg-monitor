package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Экран диагностики печатает строки данных, и строка без значения справа --
// это повод дописать бэкенд, а не поставить прочерк. Значения у проверок
// есть: агент кладёт их в details_json каждого события. Наружу они идут
// БЕЛЫМ СПИСКОМ, а не как есть -- в том же details лежит топология роутера
// (base_url, router_ip), которой мини-аппу видеть незачем.
func TestMiniappEventsCarriesCheckFacts(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	now := time.Now()
	seed := map[string]string{
		"dns":         `{"endpoints":3,"failed_count":1,"skipped_count":0,"rkn_probed":2,"rkn_suspect":0,"endpoints_detail":[{"target":"1.1.1.1"}]}`,
		"hydraroute":  `{"installed":true,"running":true,"routes_hrneo":26,"routes_ndms":2,"routes_static":0,"active_backend":"hydraroute","singbox_router_active":false}`,
		"awg_manager": `{"version":"2.17.2","firmware":"4.3.7","keenetic_os":"4.3.7","router_ip":"192.168.0.1","base_url":"http://192.168.0.1:2222"}`,
		"external_reach": `{"targets_total":3,"targets_failed":[{"name":"ya.ru","err":"timeout"}],"targets_ok":["google.com","cloudflare.com"],"via_interface":"ISP"}`,
	}
	for name, details := range seed {
		if err := d.Events().Insert(ownedID, name, "ok", details, now); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappRouterEventsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]miniappCheckStatus, len(resp.Checks))
	for _, c := range resp.Checks {
		byName[c.CheckName] = c
	}

	dns := byName["dns"].Facts
	if dns == nil || dns.Resolvers == nil || *dns.Resolvers != 3 || dns.ResolversFailed == nil || *dns.ResolversFailed != 1 {
		t.Fatalf("dns facts = %+v, want resolvers=3 failed=1", dns)
	}
	if dns.RKNProbed == nil || *dns.RKNProbed != 2 || dns.RKNSuspect == nil || *dns.RKNSuspect != 0 {
		t.Fatalf("dns facts = %+v, want rkn_probed=2 rkn_suspect=0", dns)
	}

	hr := byName["hydraroute"].Facts
	if hr == nil || hr.RoutesHRNeo == nil || *hr.RoutesHRNeo != 26 || hr.RoutesNDMS == nil || *hr.RoutesNDMS != 2 {
		t.Fatalf("hydraroute facts = %+v, want 26 hr-neo и 2 ndms", hr)
	}
	if hr.ActiveBackend != "hydraroute" {
		t.Fatalf("hydraroute facts = %+v, want active_backend=hydraroute", hr)
	}

	awgm := byName["awg_manager"].Facts
	if awgm == nil || awgm.Version != "2.17.2" || awgm.Firmware != "4.3.7" {
		t.Fatalf("awg_manager facts = %+v, want version 2.17.2 и firmware 4.3.7", awgm)
	}

	ext := byName["external_reach"].Facts
	if ext == nil || ext.TargetsTotal == nil || *ext.TargetsTotal != 3 || ext.TargetsFailed == nil || *ext.TargetsFailed != 1 {
		t.Fatalf("external_reach facts = %+v, want 3 цели и 1 провал", ext)
	}

	body := rec.Body.String()
	for _, leak := range []string{"192.168.0.1", "2222", "ISP", "endpoints_detail"} {
		if strings.Contains(body, leak) {
			t.Fatalf("топология %q утекла в мини-апп: %s", leak, body)
		}
	}
}

// Старый агент шлёт события без details, и выдумывать за него нечего:
// отсутствие фактов -- это отсутствие поля, а не нули.
func TestMiniappEventsWithoutDetailsHasNoFacts(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	if err := d.Events().Insert(ownedID, "dns", "ok", "", time.Now()); err != nil {
		t.Fatalf("insert dns: %v", err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp miniappRouterEventsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Checks {
		if c.CheckName == "dns" && c.Facts != nil {
			t.Fatalf("dns facts = %+v, want nil", c.Facts)
		}
	}
}

// Проверка туннеля своей проекцией уже описана (miniappTunnel), и второй
// набор тех же чисел рядом развёл бы два источника правды об одном.
func TestMiniappTunnelChecksCarryNoFacts(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	if err := d.Events().Insert(ownedID, "tunnel_awg12", "ok", `{"tunnel_id":"awg12","handshake_age_sec":12}`, time.Now()); err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp miniappRouterEventsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Checks {
		if strings.HasPrefix(c.CheckName, "tunnel_") && c.Facts != nil {
			t.Fatalf("%s facts = %+v, want nil", c.CheckName, c.Facts)
		}
	}
}
