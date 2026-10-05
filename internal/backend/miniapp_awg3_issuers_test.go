package backend

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel/awg3paneltest"
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
			callsBefore := len(env.awg3.routerCalls)
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
			if !tc.allowed && len(env.awg3.routerCalls) != callsBefore {
				t.Fatalf("отказанному ушёл запрос в панель: %v", env.awg3.routerCalls)
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

const issueMainBody = `{"provider":"awg3panel","instance_id":"main","iface":"awg1"}`

// Допуск к панели other не открывает main.
func TestAwg3GrantToOtherPanelDoesNotOpenMain(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.issuable = append(issuableMain(), awg3panel.IssuablePanel{ID: "other", Label: "Other", Ifaces: []awg3panel.Iface{{ID: "awg1", Title: "X", Interface: "awg1"}}})
	env.awg3.routerConf = awg3panel.RouterConfig{Conf: []byte("[Interface]\nPrivateKey = X\n"), PeerID: "p1"}
	_, _ = env.awg3.AddIssuer("other", cabOperator, cabAdmin)
	calls := len(env.awg3.routerCalls)
	if rec := env.do(t, cabOperator, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", issueMainBody); rec.Code != http.StatusNotFound {
		t.Fatalf("выпуск main: %d %s", rec.Code, rec.Body.String())
	}
	list := env.do(t, cabOperator, http.MethodGet, "/v1/miniapp/routers/{id}/vpn/awg3", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), `"id":"main"`) || !strings.Contains(list.Body.String(), `"id":"other"`) {
		t.Fatalf("список: %d %s", list.Code, list.Body.String())
	}
	if len(env.awg3.routerCalls) != calls {
		t.Fatalf("запрос в панель: %v", env.awg3.routerCalls)
	}
}

// Снятие допуска закрывает выпуск сразу.
func TestAwg3RevokedIssuerLosesAccess(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.issuable = issuableMain()
	env.awg3.routerConf = awg3panel.RouterConfig{Conf: []byte("[Interface]\nPrivateKey = X\n"), PeerID: "p1"}
	_, _ = env.awg3.AddIssuer("main", cabOperator, cabAdmin)
	if rec := env.do(t, cabOperator, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", issueMainBody); rec.Code != http.StatusAccepted {
		t.Fatalf("до снятия: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := env.awg3.RemoveIssuer("main", cabOperator); err != nil {
		t.Fatal(err)
	}
	if rec := env.do(t, cabOperator, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", issueMainBody); rec.Code != http.StatusNotFound {
		t.Fatalf("после снятия: %d %s", rec.Code, rec.Body.String())
	}
}

// A2.2: список панелей с допусками отдаётся админу, когда сама панель лежит
// (сервис читает хранилище и не ходит в сеть).
func TestAwg3ListShowsIssuersWhenPanelDown(t *testing.T) {
	p, err := awg3paneltest.Start(awg3paneltest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc := awg3panel.NewService(filepath.Join(t.TempDir(), awg3panel.DefaultStoreName), awg3panel.Options{RootCAs: p.CA.Pool})
	env := newCabinetEnv(t, func(d *Deps) { d.Awg3Panels = svc })
	pfx, err := p.CA.P12("anex", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "p12-pw", false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"id": "main", "label": "Main", "base_url": p.URL, "user": "admin",
		"password": "pw", "p12_base64": base64.StdEncoding.EncodeToString(pfx), "p12_password": "p12-pw",
	})
	if rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels", string(body)); rec.Code != http.StatusCreated {
		t.Fatalf("добавление: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels/main/issuers", `{"telegram_user_id":555}`); rec.Code != http.StatusOK {
		t.Fatalf("допуск: %d %s", rec.Code, rec.Body.String())
	}
	p.Close() // панель легла
	if rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/awg3panels/main/peers", ""); rec.Code == http.StatusOK {
		t.Fatalf("пиры лежащей панели: %d %s", rec.Code, rec.Body.String())
	}
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/awg3panels", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"telegram_user_id":555`) {
		t.Fatalf("список при лежащей панели: %d %s", rec.Code, rec.Body.String())
	}
}

// A2.3: допущенный не админ видит общий русский текст без админских подробностей
// (учётные данные, .p12, адрес); админ -- подробный.
func TestAwg3IssueErrorTextsByRole(t *testing.T) {
	adminOnly := []string{"учётн", "пересохран", ".p12", "сертификат", "адрес"}
	errs := []error{
		&awg3panel.Error{Kind: awg3panel.KindBadPassword},
		&awg3panel.Error{Kind: awg3panel.KindBanned, Until: time.Now().Add(time.Minute)},
		&awg3panel.Error{Kind: awg3panel.KindCert},
		&awg3panel.Error{Kind: awg3panel.KindServerCert},
		&awg3panel.Error{Kind: awg3panel.KindReadonly},
		&awg3panel.Error{Kind: awg3panel.KindUnreachable},
		&awg3panel.Error{Kind: awg3panel.KindBadResponse},
		awg3panel.ErrInstanceDisabled,
	}
	for _, e := range errs {
		env := newCabinetEnv(t)
		_, _ = env.awg3.AddIssuer("main", cabOwner, cabAdmin)
		env.awg3.routerErr = e
		rec := env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", issueMainBody)
		code, msg, _ := cabinetErrorBody(t, rec)
		if rec.Code == http.StatusAccepted || code != "awg3_unavailable" || !strings.ContainsAny(msg, "абвгдеёжзийклмнопрстуфхцчшщыьэюя") {
			t.Errorf("%v: допущенному %d %s", e, rec.Code, rec.Body.String())
		}
		low := strings.ToLower(msg)
		for _, w := range adminOnly {
			if strings.Contains(low, w) {
				t.Errorf("%v: админское «%s» дошло до допущенного: %s", e, w, msg)
			}
		}
		if strings.Contains(rec.Body.String(), "retry_at") {
			t.Errorf("%v: время паузы дошло до допущенного: %s", e, rec.Body.String())
		}
		rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", issueMainBody)
		if code, _, _ := cabinetErrorBody(t, rec); code == "awg3_unavailable" {
			t.Errorf("%v: админу достался общий текст", e)
		}
	}
}
