package backend

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{http.MethodPost, "/v1/miniapp/selfhosted/dacha/trust-host-key", `{"confirm":"нет"}`},
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
		{"ключ сменился", `{"provider":"selfhosted","instance_id":"dacha"}`, "selfhosted_host_key_changed", http.StatusConflict, &selfhostedamnezia.HostKeyChangedError{Label: "Дом"}},
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
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusBadRequest || body.Code != "unknown_provider" {
		t.Fatalf("мастер замены принял свой сервер: %d %s", rec.Code, rec.Body.String())
	}
}

// Движок починки перевыпускает линию по её происхождению. Выпуск на свой
// сервер происхождения не пишет -- значит, починке нечем его перевыпустить
// (а если строка когда-нибудь появится, callbacks.Router.IssueConfig
// «selfhosted» всё равно откажет: TestIssueConfigRefusesSelfHosted).
func TestSelfHostedIssueLeavesNoOriginForRepair(t *testing.T) {
	env := newCabinetEnv(t)
	seedVPS(env)
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", `{"provider":"selfhosted","instance_id":"dacha"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	origins, err := env.d.TunnelOrigins().List(env.ownedID)
	if err != nil || len(origins) != 0 {
		t.Fatalf("выпуск на свой сервер записал происхождение: %+v err=%v", origins, err)
	}
	if _, _, ok := LinkRepairOrigin(env.d).Get(env.ownedID, "dacha_router-owned"); ok {
		t.Fatal("починка видит происхождение своего сервера")
	}
}

// Настоящий сервис за маршрутом: смена SSH-адреса без нового пароля --
// 400 invalid_field по полю ssh_password, а не тихий перенос старого пароля.
func TestMiniappSelfHostedUpdateNewSSHTargetNeedsPassword(t *testing.T) {
	svc := selfhostedamnezia.NewService(filepath.Join(t.TempDir(), "s.json"), selfhostedamnezia.Config{})
	if err := svc.Create(selfhostedamnezia.Instance{ID: "dacha", Enabled: true, EndpointHost: "vpn.example.com", EndpointPort: 1,
		SSHHost: "203.0.113.7", SSHPassword: "SECRET-SSH-MUST-NOT-LEAK"}); err != nil {
		t.Fatal(err)
	}
	env := newCabinetEnv(t, func(d *Deps) { d.SelfHosted = svc })
	rec := env.do(t, cabAdmin, http.MethodPut, "/v1/miniapp/selfhosted/dacha",
		`{"endpoint_host":"vpn.example.com","endpoint_port":1,"ssh_host":"198.51.100.9","ssh_password":""}`)
	if code, msg, field := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "invalid_field" || field != "ssh_password" || strings.Contains(msg, "SECRET") {
		t.Fatalf("смена адреса: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodPut, "/v1/miniapp/selfhosted/dacha",
		`{"label":"Дача","endpoint_host":"vpn.example.com","endpoint_port":1,"ssh_host":"203.0.113.7","ssh_password":""}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("тот же адрес: %d %s", rec.Code, rec.Body.String())
	}
	insts, _ := svc.List()
	if insts[0].SSHPassword != "SECRET-SSH-MUST-NOT-LEAK" {
		t.Fatal("пароль того же входа потерян")
	}
	rec = env.do(t, cabAdmin, http.MethodPut, "/v1/miniapp/selfhosted/dacha", `{"endpoint_host":"vpn.example.com","endpoint_port":1,"ssh_host":""}`)
	if insts, _ = svc.List(); rec.Code != http.StatusNoContent || insts[0].SSHPassword != "" || insts[0].SSHHost != "" {
		t.Fatalf("стёртый адрес: %d %+v", rec.Code, insts[0])
	}
}

// B2 (v0.55): отпечаток ключа хоста виден админу в карточке, «Доверять
// новому ключу» -- с подтверждением именем, смена ключа при выпуске --
// словами с именем сервера.
func TestMiniappSelfHostedHostKey(t *testing.T) {
	env := newCabinetEnv(t)
	seedVPS(env)
	const fp = "SHA256:0+YzwylrV4vzNCZQZ4WDA6yEr1elQ6zIgwId6M/F9OA"
	env.vps.instances[0].SSHHostKey = fp
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/selfhosted", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ssh_host_key":"`+fp+`"`) {
		t.Fatalf("отпечатка нет в карточке: %d %s", rec.Code, rec.Body.String())
	}

	const trust = "/v1/miniapp/selfhosted/dacha/trust-host-key"
	rec = env.do(t, cabAdmin, http.MethodPost, trust, `{"confirm":"dacha"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "confirm_mismatch" || env.vps.instances[0].SSHHostKey != fp {
		t.Fatalf("доверие без верного имени: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/nope/trust-host-key", `{"confirm":"Дом"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "instance_not_found" {
		t.Fatalf("доверие несуществующему: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabAdmin, http.MethodPost, trust, `{"confirm":" дом "}`)
	if rec.Code != http.StatusNoContent || env.vps.instances[0].SSHHostKey != "" {
		t.Fatalf("доверие: %d %s ключ=%q", rec.Code, rec.Body.String(), env.vps.instances[0].SSHHostKey)
	}

	env.vps.issueErr = &selfhostedamnezia.HostKeyChangedError{Label: "Дом"}
	rec = env.do(t, cabAdmin, http.MethodPost, sendConfPath, `{"provider":"selfhosted","instance_id":"dacha"}`)
	want := "Ключ сервера «Дом» изменился — если вы переустанавливали сервер, подтвердите новый ключ в карточке"
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "selfhosted_host_key_changed" || msg != want {
		t.Fatalf("файл при смене ключа: %d %s", rec.Code, rec.Body.String())
	}
}

const (
	revKeyA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	revKeyB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
	revKeyC = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="
)

// seedRevokeClients -- три подключения «Дома»: два выдано роутеру router-owned
// (живое -- новое), третье чужое (выдано в приложении Amnezia).
func seedRevokeClients(t *testing.T, env *cabinetEnv, withTunnel bool) {
	t.Helper()
	seedVPS(env)
	old := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	env.vps.clients = []selfhostedamnezia.Client{
		{PublicKey: revKeyA, Name: "wgmon-router-owned-20261001-100000", Address: "10.8.1.2/32", CreatedAt: old},
		{PublicKey: revKeyB, Name: "wgmon-router-owned-20261003-120000", Address: "10.8.1.3/32", CreatedAt: old.Add(48 * time.Hour)},
		{PublicKey: revKeyC, Name: "Phone of Ann", Address: "10.8.1.4/32", CreatedAt: old.Add(72 * time.Hour)},
	}
	if withTunnel {
		name := selfhostedamnezia.TunnelName("dacha", "router-owned")
		if err := env.d.Events().Insert(env.ownedID, "tunnel_awg20", "ok", `{"tunnel_id":"awg20","tunnel_name":"`+name+`"}`, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
}

// Туннель поднят подключением A, затем админ взял файл в личку для того же
// роутера -- появилось B (импорта не было). Отзыв A убил бы туннель, поэтому
// предупреждение стоит и на A.
func TestMiniappSelfHostedClientsFileToDMAfterImportStillWarnsOnOlder(t *testing.T) {
	env := newCabinetEnv(t)
	seedRevokeClients(t, env, true)
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/selfhosted/dacha/clients", "")
	var resp struct {
		Clients []struct {
			ID    string `json:"id"`
			InUse *struct {
				Likely bool `json:"likely"`
			} `json:"in_use"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Clients {
		if c.ID == revKeyA && (c.InUse == nil || c.InUse.Likely) {
			t.Fatalf("у прежнего подключения нет предупреждения «возможно»: %s", rec.Body.String())
		}
	}
}

func TestMiniappSelfHostedClientsListWarnsAboutLiveTunnel(t *testing.T) {
	env := newCabinetEnv(t)
	seedRevokeClients(t, env, true)
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/selfhosted/dacha/clients", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Clients []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Address string `json:"address"`
			InUse   *struct {
				Router string `json:"router"`
				Tunnel string `json:"tunnel"`
				Likely bool   `json:"likely"`
			} `json:"in_use"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Clients) != 3 {
		t.Fatalf("%v %s", err, rec.Body.String())
	}
	// Туннель роутера жив: помечены ВСЕ его подключения (файл в личку выдаётся
	// под тем же именем, и по имени не сказать, какое несёт туннель). Самое
	// новое -- «скорее всего», прежнее -- «возможно»; чужое не помечено.
	for _, c := range resp.Clients {
		wantUse := c.ID == revKeyA || c.ID == revKeyB
		if (c.InUse != nil) != wantUse {
			t.Errorf("%s: in_use=%v, ждали %v", c.Name, c.InUse, wantUse)
		}
		if c.InUse != nil && c.InUse.Likely != (c.ID == revKeyB) {
			t.Errorf("%s: likely=%v", c.Name, c.InUse.Likely)
		}
		if c.InUse != nil && (c.InUse.Router != "router-owned" || c.InUse.Tunnel != selfhostedamnezia.TunnelName("dacha", "router-owned")) {
			t.Errorf("in_use: %+v", c.InUse)
		}
	}
	for _, leak := range []string{"SECRET-SSH", "ssh_password"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("утечка %s", leak)
		}
	}
}

func TestMiniappSelfHostedClientsNoTunnelNoWarning(t *testing.T) {
	env := newCabinetEnv(t)
	seedRevokeClients(t, env, false)
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/selfhosted/dacha/clients", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"in_use":{`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappSelfHostedRevokeAdminOnlyAndConfirmed(t *testing.T) {
	env := newCabinetEnv(t)
	seedRevokeClients(t, env, true)
	body := `{"client_id":"` + revKeyB + `","confirm":"Дом"}`
	for _, who := range []int64{cabStranger, cabOperator, cabOwner} {
		for _, rt := range []struct{ m, p string }{
			{http.MethodGet, "/v1/miniapp/selfhosted/dacha/clients"},
			{http.MethodPost, "/v1/miniapp/selfhosted/dacha/clients/revoke"},
		} {
			rec := env.do(t, who, rt.m, rt.p, body)
			if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" {
				t.Errorf("%s от %d: %d %s", rt.p, who, rec.Code, rec.Body.String())
			}
		}
	}
	if len(env.vps.revoked) != 0 {
		t.Fatalf("не админ отозвал: %v", env.vps.revoked)
	}
	// Имя сервера набрано неверно -- 400 на бэкенде, ничего не отозвано.
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/clients/revoke", `{"client_id":"`+revKeyB+`","confirm":"дача"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "confirm_mismatch" || len(env.vps.revoked) != 0 {
		t.Fatalf("неверное имя: %d %s %v", rec.Code, rec.Body.String(), env.vps.revoked)
	}
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/clients/revoke", `{"client_id":"`+revKeyB+`"}`)
	if rec.Code != http.StatusBadRequest || len(env.vps.revoked) != 0 {
		t.Fatalf("без подтверждения: %d %s", rec.Code, rec.Body.String())
	}
	// Верное имя (регистр и пробелы прощаются, как у «Доверять новому ключу») -- отзыв.
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/clients/revoke", `{"client_id":"`+revKeyB+`","confirm":" дом "}`)
	if rec.Code != http.StatusNoContent || len(env.vps.revoked) != 1 || env.vps.revoked[0] != "dacha:"+revKeyB {
		t.Fatalf("отзыв: %d %s %v", rec.Code, rec.Body.String(), env.vps.revoked)
	}
	if !strings.Contains(env.logs.String(), "отозвано") || strings.Contains(env.logs.String(), revKeyB) {
		t.Fatalf("журнал: %s", env.logs.String())
	}
	// Повтор -- подключения уже нет: 404 словами.
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/clients/revoke", `{"client_id":"`+revKeyB+`","confirm":"Дом"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "client_not_found" {
		t.Fatalf("повтор: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappSelfHostedRevokeFailuresAreWords(t *testing.T) {
	env := newCabinetEnv(t)
	seedRevokeClients(t, env, false)
	env.vps.revokeErr = &selfhostedamnezia.HostKeyChangedError{Label: "Дом"}
	rec := env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/clients/revoke", `{"client_id":"`+revKeyB+`","confirm":"Дом"}`)
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "selfhosted_host_key_changed" || !strings.Contains(msg, "«Дом»") {
		t.Fatalf("ключ хоста: %d %s", rec.Code, rec.Body.String())
	}
	env.vps.revokeErr = errors.New("remote docker wg set: SSH-ПОДРОБНОСТЬ-НЕ-НАРУЖУ")
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/clients/revoke", `{"client_id":"`+revKeyB+`","confirm":"Дом"}`)
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadGateway || code != "selfhosted_revoke_failed" || strings.Contains(rec.Body.String(), "ПОДРОБНОСТЬ") || msg == "" {
		t.Fatalf("сбой сервера: %d %s", rec.Code, rec.Body.String())
	}
	env.vps.revokeErr = &selfhostedamnezia.RevokePartialError{Err: errors.New("disk full ПОДРОБНОСТЬ")}
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted/dacha/clients/revoke", `{"client_id":"`+revKeyB+`","confirm":"Дом"}`)
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadGateway || code != "selfhosted_revoke_partial" || !strings.Contains(msg, "уже отключено") || strings.Contains(rec.Body.String(), "ПОДРОБНОСТЬ") {
		t.Fatalf("частичный отзыв: %d %s", rec.Code, rec.Body.String())
	}
	env.vps.listErr = selfhostedamnezia.ErrInstanceDisabled
	rec = env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/selfhosted/dacha/clients", "")
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "instance_disabled" {
		t.Fatalf("выключен: %d %s", rec.Code, rec.Body.String())
	}
}
