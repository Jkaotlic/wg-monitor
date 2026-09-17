package backend

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

func seedVPS(env *cabinetEnv) {
	env.vps.instances = []selfhostedamnezia.Instance{{
		ID: "dacha", Label: "Дом", Enabled: true, EndpointHost: "vpn.example.com", EndpointPort: 47567,
		SSHHost: "203.0.113.7", SSHPort: 22, SSHUser: "root", SSHPassword: "SECRET-SSH-MUST-NOT-LEAK",
	}}
}

func TestMiniappSelfHostedAdminOnly(t *testing.T) {
	env := newCabinetEnv(t)
	seedVPS(env)
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/miniapp/selfhosted", ""},
		{http.MethodPost, "/v1/miniapp/selfhosted", `{"id":"work","endpoint_host":"vpn2.example.com","endpoint_port":1}`},
		{http.MethodPut, "/v1/miniapp/selfhosted/dacha", `{"endpoint_host":"vpn.example.com","endpoint_port":47567}`},
		{http.MethodPost, "/v1/miniapp/selfhosted/dacha/toggle", `{"enabled":true}`},
		{http.MethodPost, "/v1/miniapp/selfhosted/dacha/check", ""},
		{http.MethodDelete, "/v1/miniapp/selfhosted/dacha", `{"confirm":"нет"}`},
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
			t.Errorf("%s %s админу закрыт: %s", rt.method, rt.path, rec.Body.String())
		}
	}
	if len(env.vps.checks) != 1 {
		t.Fatalf("проверка должна была пройти один раз -- от админа: %v", env.vps.checks)
	}

	off := newCabinetEnv(t, func(d *Deps) { d.SelfHosted = nil })
	rec := off.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/selfhosted", "")
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "selfhosted_not_configured" {
		t.Fatalf("не настроено: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappSelfHostedListNeverShowsPassword(t *testing.T) {
	env := newCabinetEnv(t)
	seedVPS(env)
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/selfhosted", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "SECRET-SSH") || strings.Contains(body, "ssh_password") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	for _, want := range []string{`"password_set":true`, `"dns":[]`, `"endpoint_host":"vpn.example.com"`, `"defaults":{`, `"container":"amnezia-awg2"`} {
		if !strings.Contains(body, want) {
			t.Errorf("в ответе нет %s: %s", want, body)
		}
	}
}

func TestMiniappSelfHostedCreateAndUpdate(t *testing.T) {
	env := newCabinetEnv(t)
	seedVPS(env)
	const pass = "SECRET-NEW-SSH-MUST-NOT-LEAK"
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted",
		`{"id":"work","label":"Работа","endpoint_host":"vpn2.example.com","endpoint_port":51820,"dns":["1.1.1.1"],"ssh_host":"203.0.113.9","ssh_port":22,"ssh_user":"root","ssh_password":"`+pass+`"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id":"work"`) {
		t.Fatalf("добавление: %d %s", rec.Code, rec.Body.String())
	}
	work := env.vps.instances[1]
	if !work.Enabled || work.SSHPassword != pass || work.DNS[0] != "1.1.1.1" {
		t.Fatalf("сохранено: %+v", work)
	}
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted", `{"id":"off","enabled":false,"endpoint_host":"vpn3.example.com","endpoint_port":1}`)
	if rec.Code != http.StatusCreated || env.vps.instances[2].Enabled {
		t.Fatalf("выключенный при добавлении: %d %+v", rec.Code, env.vps.instances[2])
	}
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted", `{"id":"work","endpoint_host":"x.example.com","endpoint_port":1}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "instance_exists" {
		t.Fatalf("повтор: %d %s", rec.Code, rec.Body.String())
	}

	rec = env.do(t, cabAdmin, http.MethodPut, "/v1/miniapp/selfhosted/work",
		`{"label":"Работа-2","endpoint_host":"vpn2.example.com","endpoint_port":51820,"ssh_host":"203.0.113.9","ssh_port":22,"ssh_user":"root","ssh_password":""}`)
	if rec.Code != http.StatusNoContent || env.vps.instances[1].Label != "Работа-2" || env.vps.instances[1].SSHPassword != pass {
		t.Fatalf("изменение с пустым паролем: %d %s %+v", rec.Code, rec.Body.String(), env.vps.instances[1])
	}

	env.vps.updateErr = &selfhostedamnezia.FieldError{Field: "endpoint_port", Reason: "Порт для клиентов — число от 1 до 65535"}
	rec = env.do(t, cabAdmin, http.MethodPut, "/v1/miniapp/selfhosted/work", `{"endpoint_host":"vpn2.example.com","endpoint_port":0}`)
	if code, msg, field := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "invalid_field" || field != "endpoint_port" || !strings.Contains(msg, "Порт") {
		t.Fatalf("поле: %d %s", rec.Code, rec.Body.String())
	}
	env.vps.updateErr = nil
	for _, path := range []string{"/v1/miniapp/selfhosted/nope", "/v1/miniapp/selfhosted/Bad!"} {
		rec = env.do(t, cabAdmin, http.MethodPut, path, `{"endpoint_host":"vpn.example.com","endpoint_port":1}`)
		if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "instance_not_found" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if strings.Contains(env.logs.String(), "SECRET-NEW-SSH") {
		t.Fatal("пароль в журнале")
	}
}

func TestMiniappSelfHostedToggleDeleteCheck(t *testing.T) {
	env := newCabinetEnv(t)
	seedVPS(env)
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/toggle", `{"enabled":false}`)
	if rec.Code != http.StatusNoContent || env.vps.instances[0].Enabled {
		t.Fatalf("выключение: %d %s", rec.Code, rec.Body.String())
	}
	env.vps.checkRes = selfhostedamnezia.CheckResult{OK: false, Message: "SSH не принял пользователя или пароль"}
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/check", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "не принял") {
		t.Fatalf("проверка: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/nope/check", "")
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "instance_not_found" {
		t.Fatalf("проверка несуществующего: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodDelete, "/v1/miniapp/selfhosted/dacha", `{"confirm":"dacha"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "confirm_mismatch" || len(env.vps.instances) != 1 {
		t.Fatalf("удаление с неверным подтверждением: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodDelete, "/v1/miniapp/selfhosted/dacha", `{"confirm":" дом "}`)
	if rec.Code != http.StatusNoContent || len(env.vps.instances) != 0 {
		t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
	}
}

// Свой сервер -- общий VPS, выпуск создаёт на нём клиента: только админ.
// В боте кнопка выпуска была открыта владельцу и оператору (дыра прав).
func TestMiniappVPNIssueSelfHostedAdminOnly(t *testing.T) {
	env := newCabinetEnv(t)
	seedVPS(env)
	const path = "/v1/miniapp/routers/{id}/vpn/issue"
	for _, who := range []int64{cabOperator, cabOwner} {
		rec := env.do(t, who, http.MethodPost, path, `{"provider":"selfhosted","instance_id":"dacha"}`)
		if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" {
			t.Fatalf("от %d: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	if len(env.vps.issued) != 0 {
		t.Fatalf("не-админ создал клиента: %v", env.vps.issued)
	}
	rec := env.do(t, cabAdmin, http.MethodPost, path, `{"provider":"selfhosted","instance_id":"dacha"}`)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"tunnel_name":"dacha_router-owned"`) || strings.Contains(rec.Body.String(), "VPS-CONF-SECRET") {
		t.Fatalf("админ: %d %s", rec.Code, rec.Body.String())
	}
	if len(env.sink.enqueued) != 1 || env.sink.enqueued[0].Action != "tunnel_import" || env.sink.enqueued[0].Args["name"] != "dacha_router-owned" || env.sink.enqueued[0].Args["replace"] != true {
		t.Fatalf("команда агенту: %+v", env.sink.enqueued)
	}
	raw, _ := base64.StdEncoding.DecodeString(env.sink.enqueued[0].Args["conf"].(string))
	if !strings.Contains(string(raw), "VPS-CONF-SECRET") || !strings.HasPrefix(env.vps.issued[0], "dacha:wgmon-router-owned-") {
		t.Fatalf("конфиг агенту: %q issued=%v", raw, env.vps.issued)
	}

	cases := []struct {
		name, body, code string
		status           int
		issueErr         error
	}{
		{"нет инстанса", `{"provider":"selfhosted"}`, "missing_instance", http.StatusBadRequest, nil},
		{"выключен", `{"provider":"selfhosted","instance_id":"dacha"}`, "instance_disabled", http.StatusConflict, selfhostedamnezia.ErrInstanceDisabled},
		{"не найден", `{"provider":"selfhosted","instance_id":"dacha"}`, "instance_not_found", http.StatusNotFound, selfhostedamnezia.ErrInstanceNotFound},
		{"SSH упал", `{"provider":"selfhosted","instance_id":"dacha"}`, "selfhosted_failed", http.StatusBadGateway, errors.New("ssh auth 203.0.113.7:22: unable to authenticate")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env.vps.issueErr = tc.issueErr
			rec := env.do(t, cabAdmin, http.MethodPost, path, tc.body)
			if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != tc.status || code != tc.code || strings.Contains(msg, "ssh auth") {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMiniappSendConfSelfHosted(t *testing.T) {
	env := newCabinetEnv(t)
	seedVPS(env)
	rec := env.do(t, cabOwner, http.MethodPost, sendConfPath, `{"provider":"selfhosted","instance_id":"dacha"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" || len(env.vps.issued) != 0 {
		t.Fatalf("владельцу свой сервер закрыт: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodPost, sendConfPath, `{"provider":"selfhosted","instance_id":"dacha"}`)
	if rec.Code != http.StatusAccepted || len(env.docs.sent) != 1 || env.docs.sent[0].filename != "dacha_router-owned.conf" || env.docs.sent[0].chatID != cabAdmin {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), env.docs.sent)
	}
	if len(env.sink.enqueued) != 0 {
		t.Fatal("файл в личку ничего не ставит в очередь роутера")
	}
}

// Свой сервер не должен попасть в мастер замены и движок починки: перевыпуск
// там плодил бы клиентов на VPS (спека цикла 3, решение 8).
func TestSelfHostedIsNotAReplaceOrRepairProvider(t *testing.T) {
	for _, p := range miniappVPNProviders {
		if p == "selfhosted" {
			t.Fatal("selfhosted в miniappVPNProviders -- мастер замены начнёт его перевыпускать")
		}
	}
	deps, ownedID, tgUser := replaceDeps(t)
	rec := postReplace(t, NewMux(deps), ownedID, tgUser, `{"provider":"selfhosted","option_id":"dacha","old_tunnel_id":"awg11","policy_name":"HydraRoute"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("мастер замены принял свой сервер: %d %s", rec.Code, rec.Body.String())
	}
}
