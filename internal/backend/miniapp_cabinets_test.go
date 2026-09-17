package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

const (
	cabOwner    int64 = 100 // владелец router-owned (seedMiniappFleet)
	cabOperator int64 = 555
	cabAdmin    int64 = 999
	cabStranger int64 = 200 // владелец чужого роутера
)

type fakeCabinetKeys struct {
	mu        sync.Mutex
	list      map[string][]CabinetSecret
	addErr    error
	added     []string // provider:label -- сам секрет фейк не хранит
	revoked   []string
	revokeErr error
}

func (f *fakeCabinetKeys) Secrets(_ int64, provider string) ([]CabinetSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CabinetSecret{}, f.list[provider]...), nil
}

func (f *fakeCabinetKeys) AddSecret(_ context.Context, _ int64, provider, secret, label string) (CabinetSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return CabinetSecret{}, f.addErr
	}
	f.added = append(f.added, provider+":"+label)
	s := CabinetSecret{ID: fmt.Sprintf("%s-%d", provider, len(f.list[provider])+1), Label: label, Mask: "••••", Active: true}
	if len(secret) > 8 {
		s.Mask = "••••" + secret[len(secret)-4:]
	}
	for i := range f.list[provider] {
		f.list[provider][i].Active = false
	}
	f.list[provider] = append(f.list[provider], s)
	return s, nil
}

func (f *fakeCabinetKeys) SetActiveSecret(_ int64, provider, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.list[provider]
	found := false
	for _, s := range list {
		found = found || s.ID == id
	}
	if !found {
		return ErrCabinetSecretNotFound
	}
	for i := range list {
		list[i].Active = list[i].ID == id
	}
	return nil
}

func (f *fakeCabinetKeys) DeleteSecret(_ int64, provider, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.list[provider]
	for i, s := range list {
		if s.ID == id {
			f.list[provider] = append(list[:i:i], list[i+1:]...)
			return nil
		}
	}
	return ErrCabinetSecretNotFound
}

func (f *fakeCabinetKeys) RevokeSlot(_ context.Context, _ int64, country string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked = append(f.revoked, country)
	return nil
}

type fakeSelfHosted struct {
	mu        sync.Mutex
	instances []selfhostedamnezia.Instance
	createErr error
	updateErr error
	checks    []string
	checkRes  selfhostedamnezia.CheckResult
	issued    []string
	issueErr  error
}

func (f *fakeSelfHosted) List() ([]selfhostedamnezia.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]selfhostedamnezia.Instance{}, f.instances...), nil
}

func (f *fakeSelfHosted) Defaults() selfhostedamnezia.Config {
	return selfhostedamnezia.Config{Container: "amnezia-awg2", Interface: "awg0", ConfigPath: "/opt/amnezia/awg/awg0.conf", DNS: []string{"1.1.1.1"}, SSHPort: 22, SSHUser: "root"}
}

func (f *fakeSelfHosted) find(id string) int {
	for i, inst := range f.instances {
		if inst.ID == id {
			return i
		}
	}
	return -1
}

func (f *fakeSelfHosted) Create(inst selfhostedamnezia.Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	if f.find(inst.ID) >= 0 {
		return selfhostedamnezia.ErrInstanceExists
	}
	f.instances = append(f.instances, inst)
	return nil
}

func (f *fakeSelfHosted) Update(id string, inst selfhostedamnezia.Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	i := f.find(id)
	if i < 0 {
		return selfhostedamnezia.ErrInstanceNotFound
	}
	inst.ID, inst.Enabled = id, f.instances[i].Enabled
	if inst.SSHPassword == "" {
		inst.SSHPassword = f.instances[i].SSHPassword
	}
	f.instances[i] = inst
	return nil
}

func (f *fakeSelfHosted) SetEnabled(id string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(id)
	if i < 0 {
		return selfhostedamnezia.ErrInstanceNotFound
	}
	f.instances[i].Enabled = enabled
	return nil
}

func (f *fakeSelfHosted) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(id)
	if i < 0 {
		return selfhostedamnezia.ErrInstanceNotFound
	}
	f.instances = append(f.instances[:i:i], f.instances[i+1:]...)
	return nil
}

func (f *fakeSelfHosted) Check(_ context.Context, id string) (selfhostedamnezia.CheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.find(id) < 0 {
		return selfhostedamnezia.CheckResult{}, selfhostedamnezia.ErrInstanceNotFound
	}
	f.checks = append(f.checks, id)
	return f.checkRes, nil
}

func (f *fakeSelfHosted) Issue(_ context.Context, id, clientName string) (selfhostedamnezia.IssuedConfig, selfhostedamnezia.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issueErr != nil {
		return selfhostedamnezia.IssuedConfig{}, selfhostedamnezia.Instance{}, f.issueErr
	}
	i := f.find(id)
	if i < 0 {
		return selfhostedamnezia.IssuedConfig{}, selfhostedamnezia.Instance{}, selfhostedamnezia.ErrInstanceNotFound
	}
	f.issued = append(f.issued, id+":"+clientName)
	return selfhostedamnezia.IssuedConfig{
		Name:    clientName,
		Address: "10.8.1.2/32",
		Config:  []byte("[Interface]\nPrivateKey = VPS-CONF-SECRET-MUST-NOT-LEAK\n"),
	}, f.instances[i], nil
}

type fakeSentDoc struct {
	chatID   int64
	filename string
	data     []byte
	caption  string
}

type fakeDocSender struct {
	mu   sync.Mutex
	sent []fakeSentDoc
	err  error
}

func (f *fakeDocSender) SendDocument(_ context.Context, chatID int64, _ *int64, filename string, data []byte, caption string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.sent = append(f.sent, fakeSentDoc{chatID: chatID, filename: filename, data: append([]byte{}, data...), caption: caption})
	return 1, nil
}

type cabinetEnv struct {
	h       http.Handler
	d       *db.DB
	ownedID int64
	keys    *fakeCabinetKeys
	cab     *fakeCabinet
	sink    *dashboardActionSink
	logs    *bytes.Buffer
	docs    *fakeDocSender
	vps     *fakeSelfHosted
}

func newCabinetEnv(t *testing.T, mods ...func(*Deps)) *cabinetEnv {
	t.Helper()
	d, ownedID, _, _ := seedMiniappFleet(t)
	if err := d.RouterOperators().Add(ownedID, cabOperator, cabOwner); err != nil {
		t.Fatalf("оператор: %v", err)
	}
	env := &cabinetEnv{
		d:       d,
		ownedID: ownedID,
		keys:    &fakeCabinetKeys{list: map[string][]CabinetSecret{}},
		cab:     &fakeCabinet{conf: []byte("[Interface]\nPrivateKey = CONF-SECRET-MUST-NOT-LEAK\n")},
		sink:    &dashboardActionSink{},
		logs:    &bytes.Buffer{},
		docs:    &fakeDocSender{},
		vps:     &fakeSelfHosted{},
	}
	deps := Deps{
		DB:                  d,
		Logger:              slog.New(slog.NewTextHandler(env.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		CommandSink:         env.sink,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: cabAdmin,
		VPNCabinet:          env.cab,
		VPNCabinetKeys:      env.keys,
		SelfHosted:          env.vps,
		MiniappDocs:         env.docs,
	}
	for _, m := range mods {
		m(&deps)
	}
	env.h = NewMux(deps)
	return env
}

func (e *cabinetEnv) do(t *testing.T, tgUser int64, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, strings.ReplaceAll(path, "{id}", strconv.FormatInt(e.ownedID, 10)), rdr)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func cabinetErrorBody(t *testing.T, rec *httptest.ResponseRecorder) (code, message, field string) {
	t.Helper()
	var body struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
		Field   string `json:"field"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("ответ не JSON: %d %s", rec.Code, rec.Body.String())
	}
	if body.Code != body.Error {
		t.Fatalf("code %q != error %q", body.Code, body.Error)
	}
	return body.Code, body.Message, body.Field
}

var (
	cabAnyAccess = map[int64]bool{cabOperator: true, cabOwner: true, cabAdmin: true}
	cabOwnerOnly = map[int64]bool{cabOwner: true, cabAdmin: true}
)

func TestMiniappCabinetRoutesGateByRole(t *testing.T) {
	env := newCabinetEnv(t)
	env.keys.list["amnezia"] = []CabinetSecret{{ID: "k1", Label: "Ключ #1", Mask: "••••abcd", Active: true}}
	env.keys.list["hidemyname"] = []CabinetSecret{{ID: "c1", Label: "Код #1", Mask: "••••4321", Active: true}}
	cases := []struct {
		method, path, body string
		allowed            map[int64]bool
	}{
		{http.MethodGet, "/v1/miniapp/routers/{id}/cabinets", "", cabAnyAccess},
		{http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":"vpn://role-key-0001"}`, cabAnyAccess},
		{http.MethodPut, "/v1/miniapp/routers/{id}/cabinets/amnezia/active", `{"id":"k1"}`, cabAnyAccess},
		{http.MethodDelete, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys/k1", "", cabOwnerOnly},
		{http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/hidemy/codes", `{"access_code":"123456789012345"}`, cabAnyAccess},
		{http.MethodPut, "/v1/miniapp/routers/{id}/cabinets/hidemy/active", `{"id":"c1"}`, cabAnyAccess},
		{http.MethodDelete, "/v1/miniapp/routers/{id}/cabinets/hidemy/codes/c1", "", cabOwnerOnly},
	}
	for _, tc := range cases {
		for _, who := range []int64{cabStranger, cabOperator, cabOwner, cabAdmin} {
			rec := env.do(t, who, tc.method, tc.path, tc.body)
			denied := rec.Code == http.StatusNotFound
			if denied {
				code, _, _ := cabinetErrorBody(t, rec)
				denied = code == "not_found"
			}
			if denied == tc.allowed[who] {
				t.Errorf("%s %s от %d: статус %d %s (разрешено=%v)", tc.method, tc.path, who, rec.Code, rec.Body.String(), tc.allowed[who])
			}
		}
	}
	// Роутер, которого нет, -- 404 и админу.
	rec := env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/routers/424242/cabinets", "")
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" {
		t.Fatalf("несуществующий роутер: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappCabinetsListShape(t *testing.T) {
	env := newCabinetEnv(t)
	env.keys.list["amnezia"] = []CabinetSecret{{ID: "k1", Label: "Дом", Mask: "••••abcd", Active: true}}
	rec := env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/cabinets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"codes":[]`) {
		t.Fatalf("пустой список кодов обязан быть массивом: %s", rec.Body.String())
	}
	var resp miniappCabinetsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Amnezia.Keys) != 1 || resp.Amnezia.Keys[0].Mask != "••••abcd" || resp.SelfHosted.Available {
		t.Fatalf("владельцу: %+v", resp)
	}
	rec = env.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/routers/{id}/cabinets", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.SelfHosted.Available {
		t.Fatalf("админу свой сервер доступен: %s", rec.Body.String())
	}

	noVPS := newCabinetEnv(t, func(d *Deps) { d.SelfHosted = nil })
	rec = noVPS.do(t, cabAdmin, http.MethodGet, "/v1/miniapp/routers/{id}/cabinets", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.SelfHosted.Available {
		t.Fatal("без настроенных своих серверов available=false и админу")
	}

	off := newCabinetEnv(t, func(d *Deps) { d.VPNCabinetKeys = nil })
	rec = off.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/cabinets", "")
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "cabinets_not_configured" {
		t.Fatalf("не настроено: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappCabinetAddKeyNeverLeaksSecret(t *testing.T) {
	env := newCabinetEnv(t)
	const key = "vpn://SECRET-KEY-MUST-NOT-LEAK-abcd1234"
	rec := env.do(t, cabOperator, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":"`+key+`","label":"Дом"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "••••1234") {
		t.Fatalf("добавление: %d %s", rec.Code, rec.Body.String())
	}

	env.keys.addErr = &CabinetRejectedError{Reason: "Кабинет Amnezia Premium не принял ключ"}
	rec = env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":"`+key+`"}`)
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "cabinet_rejected" || msg != "Кабинет Amnezia Premium не принял ключ" {
		t.Fatalf("отказ кабинета: %d %s", rec.Code, rec.Body.String())
	}

	env.keys.addErr = errors.New("write /var/lib/wg-monitor/x.tmp: no space left on device")
	rec = env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":"`+key+`"}`)
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusInternalServerError || code != "internal" || strings.Contains(msg, "space") {
		t.Fatalf("сбой хранилища: %d %s", rec.Code, rec.Body.String())
	}

	for _, where := range []string{rec.Body.String(), env.logs.String()} {
		if strings.Contains(where, "SECRET-KEY") {
			t.Fatalf("ключ утёк: %s", where)
		}
	}
	req := miniappCabinetSecretReq{VPNKey: key, AccessCode: "123456789012345", Label: "Дом"}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "req", req)
	printed := fmt.Sprintf("%v %+v %#v %s", req, req, req, buf.String())
	if strings.Contains(printed, "SECRET-KEY") || strings.Contains(printed, "123456789012345") {
		t.Fatalf("печать запроса раскрывает секрет: %s", printed)
	}
}

func TestMiniappCabinetAddRefusals(t *testing.T) {
	env := newCabinetEnv(t)
	cases := []struct {
		name, path, body, code string
		addErr                 error
	}{
		{"битый json", "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":`, "bad_json", nil},
		{"ключ неверного вида", "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":"abc"}`, "invalid_key", ErrCabinetSecretInvalid},
		{"код неверного вида", "/v1/miniapp/routers/{id}/cabinets/hidemy/codes", `{"access_code":"12"}`, "invalid_code", ErrCabinetSecretInvalid},
		{"длинная подпись", "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":"vpn://k","label":"` + strings.Repeat("я", 41) + `"}`, "invalid_label", nil},
		{"подпись с переводом строки", "/v1/miniapp/routers/{id}/cabinets/hidemy/codes", `{"access_code":"123456789012345","label":"a\nb"}`, "invalid_label", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env.keys.addErr = tc.addErr
			env.keys.added = nil
			rec := env.do(t, cabOwner, http.MethodPost, tc.path, tc.body)
			if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != tc.code || msg == "" {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if tc.code == "invalid_label" && len(env.keys.added) != 0 {
				t.Fatal("с неверной подписью кабинет не спрашивают")
			}
		})
	}
}

func TestMiniappCabinetActiveAndDelete(t *testing.T) {
	env := newCabinetEnv(t)
	env.keys.list["amnezia"] = []CabinetSecret{{ID: "k1", Active: true}, {ID: "k2"}}
	rec := env.do(t, cabOperator, http.MethodPut, "/v1/miniapp/routers/{id}/cabinets/amnezia/active", `{"id":"k2"}`)
	if rec.Code != http.StatusNoContent || !env.keys.list["amnezia"][1].Active {
		t.Fatalf("активный: %d %s %+v", rec.Code, rec.Body.String(), env.keys.list["amnezia"])
	}
	rec = env.do(t, cabOperator, http.MethodPut, "/v1/miniapp/routers/{id}/cabinets/amnezia/active", `{"id":"nope"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "secret_not_found" {
		t.Fatalf("несуществующий: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabOwner, http.MethodDelete, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys/k1", "")
	if rec.Code != http.StatusNoContent || len(env.keys.list["amnezia"]) != 1 {
		t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabOwner, http.MethodDelete, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys/k1", "")
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "secret_not_found" {
		t.Fatalf("повторное удаление: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappCabinetTextsAreRussian(t *testing.T) {
	cyr := func(s string) bool {
		return strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) })
	}
	for code, text := range miniappCabinetTexts {
		if !cyr(text) {
			t.Errorf("%s: текст не по-русски: %q", code, text)
		}
	}
	if miniappCabinetErrorText("no_such_code") != miniappCabinetTexts[errCodeInternal] {
		t.Fatal("неизвестный код -- общий текст")
	}
}

func TestMiniappCabinetRevoke(t *testing.T) {
	env := newCabinetEnv(t)
	const path = "/v1/miniapp/routers/{id}/cabinets/amnezia/revoke"
	rec := env.do(t, cabOperator, http.MethodPost, path, `{"country":"de","confirm":"router-owned"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" {
		t.Fatalf("оператору отзыв закрыт: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabOwner, http.MethodPost, path, `{"country":"de","confirm":"другой"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "confirm_mismatch" || len(env.keys.revoked) != 0 {
		t.Fatalf("неверное подтверждение: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabOwner, http.MethodPost, path, `{"country":"d e","confirm":"router-owned"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "invalid_country" {
		t.Fatalf("страна: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabOwner, http.MethodPost, path, `{"country":"DE","confirm":" Router-Owned "}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"country":"de"`) || len(env.keys.revoked) != 1 || env.keys.revoked[0] != "de" {
		t.Fatalf("отзыв: %d %s %v", rec.Code, rec.Body.String(), env.keys.revoked)
	}
	env.keys.revokeErr = ErrCabinetSecretNotFound
	rec = env.do(t, cabAdmin, http.MethodPost, path, `{"country":"de","confirm":"router-owned"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "cabinet_not_connected" {
		t.Fatalf("без ключа: %d %s", rec.Code, rec.Body.String())
	}
	env.keys.revokeErr = errors.New("amnezia revoke-country-config: HTTP 500: boom")
	rec = env.do(t, cabAdmin, http.MethodPost, path, `{"country":"de","confirm":"router-owned"}`)
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadGateway || code != "cabinet_failed" || strings.Contains(msg, "boom") {
		t.Fatalf("сбой кабинета: %d %s", rec.Code, rec.Body.String())
	}
}
