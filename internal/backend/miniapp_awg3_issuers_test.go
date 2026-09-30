package backend

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
)

func issuableMain() []awg3panel.IssuablePanel {
	return []awg3panel.IssuablePanel{{ID: "main", Label: "Main", Ifaces: []awg3panel.Iface{{ID: "awg1", Title: "Нидерланды", Interface: "awg1"}}}}
}

// Одна таблица на обе ручки: список и выпуск обязаны сходиться.
func TestAwg3IssuerMatrix(t *testing.T) {
	cases := []struct {
		name    string
		who     int64
		grant   bool
		dropOp  bool
		allowed bool
	}{
		{"админ без допуска", cabAdmin, false, false, true},
		{"владелец с допуском", cabOwner, true, false, true},
		{"оператор с допуском", cabOperator, true, false, true},
		{"владелец без допуска", cabOwner, false, false, false},
		{"чужой с допуском", cabStranger, true, false, false},
		{"оператор снят, допуск остался", cabOperator, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newCabinetEnv(t)
			env.awg3.issuable = issuableMain()
			env.awg3.routerConf = awg3panel.RouterConfig{Conf: []byte("[Interface]\nPrivateKey = X\n"), PeerID: "p1"}
			if tc.grant {
				_, _ = env.awg3.AddIssuer("main", tc.who, cabAdmin)
			}
			if tc.dropOp {
				if err := env.d.RouterOperators().Remove(env.ownedID, cabOperator); err != nil {
					t.Fatal(err)
				}
			}
			list := env.do(t, tc.who, http.MethodGet, "/v1/miniapp/routers/{id}/vpn/awg3", "")
			listed := list.Code == http.StatusOK && strings.Contains(list.Body.String(), `"id":"main"`)
			issue := env.do(t, tc.who, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", `{"provider":"awg3panel","instance_id":"main","iface":"awg1"}`)
			issued := issue.Code == http.StatusAccepted
			if listed != tc.allowed || issued != tc.allowed {
				t.Fatalf("список %d %s; выпуск %d %s", list.Code, list.Body.String(), issue.Code, issue.Body.String())
			}
			if !tc.allowed && issue.Code != http.StatusNotFound {
				t.Fatalf("отказ не 404: %d", issue.Code)
			}
		})
	}
}

func TestAwg3IssueLogsWhoIssued(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.routerConf = awg3panel.RouterConfig{Conf: []byte("[Interface]\nPrivateKey = X\n"), PeerID: "p1"}
	_, _ = env.awg3.AddIssuer("main", cabOperator, cabAdmin)
	rec := env.do(t, cabOperator, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", `{"provider":"awg3panel","instance_id":"main","iface":"awg1"}`)
	if rec.Code != http.StatusAccepted || !strings.Contains(env.logs.String(), "by_tg=555") {
		t.Fatalf("%d %s\n%s", rec.Code, rec.Body.String(), env.logs.String())
	}
}

func TestAwg3IssuableListHasNoSecrets(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.issuable = issuableMain()
	_, _ = env.awg3.AddIssuer("main", cabOperator, cabAdmin)
	body := env.do(t, cabOperator, http.MethodGet, "/v1/miniapp/routers/{id}/vpn/awg3", "").Body.String()
	for _, bad := range []string{"base_url", "198.51.100", "user", "password", "cert", "p12", "peers", "issuers"} {
		if strings.Contains(body, bad) {
			t.Fatalf("в ответе %q: %s", bad, body)
		}
	}
}

func TestAwg3IssuersAdminOnlyAndValidated(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.views = []awg3panel.View{{ID: "main", Label: "Main", Enabled: true}}
	for _, who := range []int64{cabOwner, cabOperator, cabStranger} {
		if rec := env.do(t, who, http.MethodPost, "/v1/miniapp/awg3panels/main/issuers", `{"telegram_user_id":555}`); rec.Code != http.StatusNotFound {
			t.Fatalf("от %d: %d", who, rec.Code)
		}
		if rec := env.do(t, who, http.MethodDelete, "/v1/miniapp/awg3panels/main/issuers/555", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("удаление от %d: %d", who, rec.Code)
		}
	}
	for _, bad := range []string{`{"telegram_user_id":0}`, `{"telegram_user_id":-5}`, `{"telegram_user_id":"555"}`, `{}`} {
		rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels/main/issuers", bad)
		if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "bad_issuer_id" {
			t.Fatalf("%s → %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels/main/issuers", `{"telegram_user_id":555}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"telegram_user_id":555`) {
		t.Fatalf("добавление: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodDelete, "/v1/miniapp/awg3panels/main/issuers/555", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"telegram_user_id":555`) {
		t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAwg3DeviceStillAdminOnlyForIssuer(t *testing.T) {
	env := newCabinetEnv(t)
	_, _ = env.awg3.AddIssuer("main", cabOperator, cabAdmin)
	rec := env.do(t, cabOperator, http.MethodPost, "/v1/miniapp/awg3panels/main/device", `{"iface":"awg1","name":"phone"}`)
	if rec.Code != http.StatusNotFound || len(env.awg3.devices) != 0 {
		t.Fatalf("устройство допущенному: %d", rec.Code)
	}
}
