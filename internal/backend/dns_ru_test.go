package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func dnsRuFail() wire.Check {
	return wire.Check{Name: "dns_ru", Status: "fail", Details: map[string]any{"ru_upstreams": 1, "ru_failed": 1, "router_resolves": true}}
}

// Порог dns_ru -- общий автомат (шумные DNS-проверки), своих правил нет:
// тревога после того же числа провалов, что у dns.
func TestReportDNSRuUsesCommonThreshold(t *testing.T) {
	disp, post, _, d := guardReportHarness(t, "5151515151515151515151515151515151515151515151515151515151515151", "dnsruthr")
	uid := mustUserID(t, d, "dnsruthr")
	for i := 0; i < 5; i++ {
		post(heartbeatCheck, dnsRuFail())
	}
	if st, _ := d.State().Get(uid, "dns_ru"); st.CurrentStatus == "hard" {
		t.Fatalf("HARD раньше общего порога шумных проверок: %+v", st)
	}
	post(heartbeatCheck, dnsRuFail())
	if st, _ := d.State().Get(uid, "dns_ru"); st.CurrentStatus != "hard" {
		t.Fatalf("после 6 провалов нет HARD: %+v", st)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if disp.calls[len(disp.calls)-1] != state.Hard {
		t.Fatalf("тревога не отправлена: %v", disp.calls)
	}
}

// Ру-апстримы убрали из настроек посреди аварии: агент перестал присылать
// строку dns_ru. Без закрытия напоминания шли бы вечно.
func TestReportClosesDNSRuHardWhenTheCheckIsGone(t *testing.T) {
	disp, post, send, d := guardReportHarness(t, "5252525252525252525252525252525252525252525252525252525252525252", "dnsrugone")
	uid := mustUserID(t, d, "dnsrugone")
	for i := 0; i < 6; i++ {
		post(heartbeatCheck, dnsRuFail())
	}
	if st, _ := d.State().Get(uid, "dns_ru"); st.CurrentStatus != "hard" {
		t.Fatalf("setup: want hard, got %q", st.CurrentStatus)
	}
	// Несвежий и неполный отчёты не закрывают.
	send(true, heartbeatCheck)
	post(wire.Check{Name: "dns", Status: "ok"})
	if st, _ := d.State().Get(uid, "dns_ru"); st.CurrentStatus != "hard" {
		t.Fatalf("HARD закрыт несвежим или неполным отчётом: %q", st.CurrentStatus)
	}
	post(heartbeatCheck, wire.Check{Name: "dns", Status: "ok"})
	if st, _ := d.State().Get(uid, "dns_ru"); st.CurrentStatus != "ok" {
		t.Fatalf("HARD dns_ru не закрыт: %q", st.CurrentStatus)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	last := disp.checks[len(disp.checks)-1]
	if disp.calls[len(disp.calls)-1] != state.Recovery || last.Name != "dns_ru" || last.Details["reason"] != alerts.DNSRuGoneReason {
		t.Fatalf("want Recovery dns_ru reason=%s, got %v %#v", alerts.DNSRuGoneReason, disp.calls, last)
	}
}

// Последняя строка dns_ru старше heartbeat -- проверки больше нет (ру-апстримы
// убрали): экран не рисует её вечным «да»/«нет».
func TestMiniappEventsDropsDNSRuStaleBeforeHeartbeat(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	ruTS := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if err := d.Events().Insert(ownedID, "dns_ru", "fail", `{"ru_upstreams":1,"ru_failed":1}`, ruTS); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(ownedID, "agent_heartbeat", "ok", "", ruTS.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var resp miniappRouterEventsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Checks {
		if c.CheckName == "dns_ru" {
			t.Fatalf("dns_ru старше heartbeat попал в checks: %+v", c)
		}
	}
}
