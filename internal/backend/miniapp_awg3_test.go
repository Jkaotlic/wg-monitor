package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel/awg3paneltest"
)

type fakeAwg3 struct {
	mu          sync.Mutex
	views       []awg3panel.View
	created     []awg3panel.Input
	updated     []awg3panel.Input
	deleted     []string
	check       awg3panel.CheckResult
	updateCheck *awg3panel.CheckResult
	createErr   error
	updateErr   error
	page        awg3panel.Page
	pageErr     error
	pageCalls   []string
	issued      awg3panel.Issued
	issueErr    error
	devices     []string
	routerConf  awg3panel.RouterConfig
	routerErr   error
	routerCalls []string
}

var _ Awg3Panels = (*fakeAwg3)(nil)

func (f *fakeAwg3) List() ([]awg3panel.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]awg3panel.View(nil), f.views...), nil
}

func (f *fakeAwg3) Create(_ context.Context, in awg3panel.Input) (awg3panel.View, awg3panel.CheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return awg3panel.View{}, awg3panel.CheckResult{}, f.createErr
	}
	f.created = append(f.created, in)
	v := awg3panel.View{ID: in.ID, Label: in.Label, BaseURL: in.BaseURL, User: in.User, Enabled: true, PasswordSet: true, CertSet: true}
	f.views = append(f.views, v)
	return v, f.check, nil
}

func (f *fakeAwg3) Update(_ context.Context, id string, in awg3panel.Input) (awg3panel.View, *awg3panel.CheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return awg3panel.View{}, nil, f.updateErr
	}
	in.ID = id
	f.updated = append(f.updated, in)
	return awg3panel.View{ID: id, Label: in.Label, BaseURL: in.BaseURL, User: in.User, Enabled: true, PasswordSet: true, CertSet: true}, f.updateCheck, nil
}

func (f *fakeAwg3) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, v := range f.views {
		if v.ID == id {
			f.views = append(f.views[:i:i], f.views[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return awg3panel.ErrInstanceNotFound
}

func (f *fakeAwg3) Peers(_ context.Context, id, iface string) (awg3panel.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageCalls = append(f.pageCalls, id+"|"+iface)
	return f.page, f.pageErr
}

func (f *fakeAwg3) IssueDevice(_ context.Context, id, iface, name string) (awg3panel.Issued, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devices = append(f.devices, id+"|"+iface+"|"+name)
	return f.issued, f.issueErr
}

func (f *fakeAwg3) ConfigForRouter(_ context.Context, id, iface, nick string) (awg3panel.RouterConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routerCalls = append(f.routerCalls, id+"|"+iface+"|"+nick)
	return f.routerConf, f.routerErr
}

const awg3FormBody = `{"id":"main","label":"Main","base_url":"https://panel.example.com","user":"admin","password":"PANEL-PW-MUST-NOT-LEAK","p12_base64":"UDEyLUJZVEVT","p12_password":"P12-PW-MUST-NOT-LEAK"}`

func TestMiniappAwg3AdminOnly(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.views = []awg3panel.View{{ID: "main", Label: "Main", Enabled: true}}
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/miniapp/awg3panels", ""},
		{http.MethodPost, "/v1/miniapp/awg3panels", awg3FormBody},
		{http.MethodPut, "/v1/miniapp/awg3panels/main", `{"label":"x","base_url":"https://panel.example.com","user":"admin"}`},
		{http.MethodDelete, "/v1/miniapp/awg3panels/main", `{"confirm":"нет"}`},
		{http.MethodGet, "/v1/miniapp/awg3panels/main/peers", ""},
		{http.MethodPost, "/v1/miniapp/awg3panels/main/device", `{"iface":"awg1","name":"x"}`},
	}
	for _, rt := range routes {
		for _, who := range []int64{cabStranger, cabOperator, cabOwner} {
			rec := env.do(t, who, rt.method, rt.path, rt.body)
			if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" {
				t.Errorf("%s %s от %d: %d %s", rt.method, rt.path, who, rec.Code, rec.Body.String())
			}
		}
		rec := env.do(t, cabAdmin, rt.method, rt.path, rt.body)
		if rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), `"code":"not_found"`) {
			t.Errorf("%s %s админу закрыт", rt.method, rt.path)
		}
	}
	if len(env.awg3.created) != 1 || len(env.awg3.pageCalls) != 1 {
		t.Fatalf("не-админ дошёл до сервиса: created=%d pages=%d", len(env.awg3.created), len(env.awg3.pageCalls))
	}
	off := newCabinetEnv(t, func(d *Deps) { d.Awg3Panels = nil })
	rec := off.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/awg3panels", "")
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "awg3_not_configured" {
		t.Fatalf("не настроено: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappAwg3ListNeverShowsSecrets(t *testing.T) {
	env := newCabinetEnv(t)
	until := time.Now().Add(10 * time.Minute)
	env.awg3.views = []awg3panel.View{
		{ID: "main", Label: "Main", BaseURL: "https://panel.example.com", User: "admin", Enabled: true, PasswordSet: true, CertSet: true, CertSubject: "anex", CertNotAfter: time.Date(2028, 11, 26, 0, 0, 0, 0, time.UTC)},
		{ID: "nl2", Enabled: true, Lock: awg3panel.LockBadPassword, Readonly: true},
		{ID: "bad", Enabled: true, Lock: awg3panel.LockCert},
		{ID: "ban", Enabled: true, PausedUntil: until},
		{ID: "off", Enabled: false},
	}
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/awg3panels", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, body)
	}
	var resp struct {
		Panels []map[string]any `json:"panels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Panels) != 5 {
		t.Fatalf("%v %s", err, body)
	}
	want := []string{"ok", "bad_password", "cert_rejected", "paused", "disabled"}
	for i, p := range resp.Panels {
		if p["state"] != want[i] {
			t.Errorf("%v: state=%v, ждали %s", p["id"], p["state"], want[i])
		}
		for _, k := range []string{"password", "cert_pem", "key_pem", "p12_password"} {
			if _, ok := p[k]; ok {
				t.Errorf("в ответе поле %s", k)
			}
		}
	}
	if resp.Panels[0]["password_set"] != true || resp.Panels[0]["cert_set"] != true || resp.Panels[0]["cert_not_after"] != "2028-11-26T00:00:00Z" || resp.Panels[1]["readonly"] != true {
		t.Fatalf("поля: %s", body)
	}
	if resp.Panels[3]["paused_until"] != until.UTC().Format(time.RFC3339) {
		t.Fatalf("paused_until: %v", resp.Panels[3]["paused_until"])
	}
}

func TestMiniappAwg3CreatePassesP12AndReportsCheck(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.check = awg3panel.CheckResult{Ran: true, Kind: awg3panel.KindBadPassword}
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels", awg3FormBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	in := env.awg3.created[0]
	if string(in.P12) != "P12-BYTES" || in.Password != "PANEL-PW-MUST-NOT-LEAK" || in.P12Password != "P12-PW-MUST-NOT-LEAK" || in.ID != "main" {
		t.Fatalf("в сервис ушло не то: id=%q p12=%q", in.ID, in.P12)
	}
	var resp struct {
		Panel map[string]any `json:"panel"`
		Check struct {
			Ran     bool   `json:"ran"`
			OK      bool   `json:"ok"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"check"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Panel["id"] != "main" || !resp.Check.Ran || resp.Check.OK || resp.Check.Code != "awg3_bad_password" || !strings.Contains(resp.Check.Message, "пароль") {
		t.Fatalf("ответ: %s", rec.Body.String())
	}
	for _, leak := range []string{"PANEL-PW", "P12-PW", "UDEyLUJZVEVT", "P12-BYTES"} {
		if strings.Contains(rec.Body.String(), leak) || strings.Contains(env.logs.String(), leak) {
			t.Fatalf("%q в ответе или журнале", leak)
		}
	}
}

func TestMiniappAwg3CreateFieldErrors(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.createErr = &awg3panel.FieldError{Field: "p12_password", Reason: "Пароль от файла .p12 не подошёл"}
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels", awg3FormBody)
	if code, msg, field := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "invalid_field" || field != "p12_password" || !strings.Contains(msg, "не подошёл") {
		t.Fatalf("поле: %d %s", rec.Code, rec.Body.String())
	}
	env.awg3.createErr = nil
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels", `{"id":"main","p12_base64":"@@@не-base64"}`)
	if code, _, field := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "invalid_field" || field != "p12" {
		t.Fatalf("битый base64: %d %s", rec.Code, rec.Body.String())
	}
	big := `{"id":"main","p12_base64":"` + strings.Repeat("A", 200<<10) + `"}`
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels", big)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusRequestEntityTooLarge || code != "awg3_too_large" {
		t.Fatalf("перебор: %d %s", rec.Code, rec.Body.String())
	}
	if len(env.awg3.created) != 0 {
		t.Fatal("негодная форма дошла до сервиса")
	}
}

func TestMiniappAwg3UpdateAndDelete(t *testing.T) {
	env := newCabinetEnv(t)
	env.awg3.views = []awg3panel.View{{ID: "main", Label: "Main", Enabled: true}}
	rec := env.do(t, cabAdmin, http.MethodPut, "/v1/miniapp/awg3panels/main", `{"label":"Main 2","base_url":"https://panel.example.com","user":"admin","password":""}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"check":null`) {
		t.Fatalf("правка: %d %s", rec.Code, rec.Body.String())
	}
	if u := env.awg3.updated[0]; u.ID != "main" || u.Label != "Main 2" || u.P12 != nil || u.Password != "" {
		t.Fatalf("в сервис: %+v", u.ID)
	}
	rec = env.do(t, cabAdmin, http.MethodDelete, "/v1/miniapp/awg3panels/main", `{"confirm":"не то"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "confirm_mismatch" {
		t.Fatalf("набор: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodDelete, "/v1/miniapp/awg3panels/main", `{"confirm":"Main"}`)
	if rec.Code != http.StatusNoContent || len(env.awg3.deleted) != 1 {
		t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/v1/miniapp/awg3panels/nope", "/v1/miniapp/awg3panels/Bad!"} {
		rec = env.do(t, cabAdmin, http.MethodDelete, path, `{"confirm":"x"}`)
		if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "awg3_not_found" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestMiniappAwg3PeersView(t *testing.T) {
	env := newCabinetEnv(t)
	now := time.Now()
	env.awg3.page = awg3panel.Page{
		Panel:   awg3panel.View{ID: "main", Label: "Main", Enabled: true},
		Ifaces:  []awg3panel.Iface{{ID: "awg1", Title: "main", Interface: "awg1"}, {ID: "awg2", Title: "reserve", Interface: "awg2"}},
		Iface:   "awg2",
		Summary: awg3panel.Summary{PeersTotal: 3, PeersOnline: 1, PeersNever: 1, RxBytes: 10, TxBytes: 20},
		Peers: []awg3panel.Peer{
			{ID: "p1", Name: "wgmon-router-owned", Address: "10.66.0.2/32", Enabled: true, LastHandshake: now.Add(-time.Minute).Unix(), RxBytes: 5, TxBytes: 6},
			{ID: "p2", Name: "iphone", Address: "10.66.0.3/32", Enabled: true, NeverConnected: true},
			{ID: "p3", Name: "wgmon-ghost", Address: "10.66.0.4/32", Enabled: true, LastHandshake: now.Add(-2 * time.Hour).Unix()},
		},
		FetchedAt: now,
	}
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/awg3panels/main/peers?iface=awg2", "")
	if rec.Code != http.StatusOK || env.awg3.pageCalls[0] != "main|awg2" {
		t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), env.awg3.pageCalls)
	}
	var resp struct {
		Iface   string `json:"iface"`
		Ifaces  []struct{ ID, Title string }
		Summary struct {
			PeersTotal  int `json:"peers_total"`
			PeersOnline int `json:"peers_online"`
		} `json:"summary"`
		Peers []struct {
			Name   string `json:"name"`
			State  string `json:"state"`
			Age    int64  `json:"handshake_age_sec"`
			Router *struct {
				ID       int64  `json:"id"`
				Nickname string `json:"nickname"`
			} `json:"router"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Iface != "awg2" || len(resp.Ifaces) != 2 || resp.Summary.PeersTotal != 3 || resp.Summary.PeersOnline != 1 {
		t.Fatalf("шапка: %s", rec.Body.String())
	}
	p := resp.Peers
	if p[0].State != "online" || p[0].Age < 55 || p[0].Age > 70 || p[0].Router == nil || p[0].Router.ID != env.ownedID || p[0].Router.Nickname != "router-owned" {
		t.Fatalf("пир роутера: %+v", p[0])
	}
	if p[1].State != "never" || p[1].Age != -1 || p[1].Router != nil {
		t.Fatalf("устройство: %+v", p[1])
	}
	if p[2].State != "idle" || p[2].Router != nil {
		t.Fatalf("wgmon- без роутера в парке: %+v", p[2])
	}
}

func TestMiniappAwg3ErrorsSpeakRussian(t *testing.T) {
	until := time.Now().Add(15 * time.Minute)
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{&awg3panel.Error{Kind: awg3panel.KindBadPassword}, http.StatusConflict, "awg3_bad_password"},
		{&awg3panel.Error{Kind: awg3panel.KindBanned, Until: until}, http.StatusConflict, "awg3_paused"},
		{&awg3panel.Error{Kind: awg3panel.KindCert}, http.StatusConflict, "awg3_cert_rejected"},
		{&awg3panel.Error{Kind: awg3panel.KindServerCert}, http.StatusConflict, "awg3_server_cert_rejected"},
		{&awg3panel.Error{Kind: awg3panel.KindReadonly}, http.StatusConflict, "awg3_readonly"},
		{&awg3panel.Error{Kind: awg3panel.KindUnreachable, Msg: "панель недоступна"}, http.StatusBadGateway, "awg3_unreachable"},
		{&awg3panel.Error{Kind: awg3panel.KindBadResponse}, http.StatusBadGateway, "awg3_bad_response"},
		{&awg3panel.Error{Kind: awg3panel.KindInvalid}, http.StatusBadRequest, "awg3_invalid_name"},
		{awg3panel.ErrIfaceNotFound, http.StatusNotFound, "awg3_iface_not_found"},
		{awg3panel.ErrInstanceDisabled, http.StatusConflict, "awg3_disabled"},
		{awg3panel.ErrInstanceNotFound, http.StatusNotFound, "awg3_not_found"},
		{awg3panel.ErrNameTaken, http.StatusConflict, "awg3_name_taken"},
		{errors.New("диск кончился"), http.StatusInternalServerError, errCodeInternal},
	}
	for _, tc := range cases {
		env := newCabinetEnv(t)
		env.awg3.pageErr = tc.err
		rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/awg3panels/main/peers", "")
		code, msg, _ := cabinetErrorBody(t, rec)
		if rec.Code != tc.status || code != tc.code || !strings.ContainsAny(msg, "абвгдеёжзийклмнопрстуфхцчшщыьэюя") {
			t.Errorf("%v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
		if tc.code == "awg3_paused" && !strings.Contains(rec.Body.String(), `"retry_at":"`+until.UTC().Format(time.RFC3339)+`"`) {
			t.Errorf("пауза без retry_at: %s", rec.Body.String())
		}
	}
}

// Настоящий сервис и поддельная панель за маршрутом: форма с настоящим .p12
// проходит, проверка -- ровно один запрос, в списке нет секретов.
func TestMiniappAwg3RealServiceEndToEnd(t *testing.T) {
	p, err := awg3paneltest.Start(awg3paneltest.Options{Password: "PANEL-PW-MUST-NOT-LEAK"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	svc := awg3panel.NewService(filepath.Join(t.TempDir(), awg3panel.DefaultStoreName), awg3panel.Options{RootCAs: p.CA.Pool})
	env := newCabinetEnv(t, func(d *Deps) { d.Awg3Panels = svc })
	pfx, err := p.CA.P12("anex", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "P12-PW-MUST-NOT-LEAK", false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"id": "main", "label": "Main", "base_url": p.URL, "user": "admin",
		"password": "PANEL-PW-MUST-NOT-LEAK", "p12_base64": base64.StdEncoding.EncodeToString(pfx), "p12_password": "P12-PW-MUST-NOT-LEAK",
	})
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels", string(body))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"ok":true`) || p.TotalHits() != 1 {
		t.Fatalf("добавление: %d %s hits=%d", rec.Code, rec.Body.String(), p.TotalHits())
	}
	rec = env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/awg3panels", "")
	if strings.Contains(rec.Body.String(), "PANEL-PW") || strings.Contains(rec.Body.String(), "BEGIN") || !strings.Contains(rec.Body.String(), `"cert_subject":"anex"`) {
		t.Fatalf("список: %s", rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/awg3panels/main/peers", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"iface":"awg1"`) {
		t.Fatalf("пиры: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(env.logs.String(), "PW-MUST-NOT-LEAK") {
		t.Fatal("секрет в журнале")
	}
}
