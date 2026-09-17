package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const importConfFixture = "[Interface]\n" +
	"PrivateKey = PRIVKEY-MUST-NOT-LEAK=\n" +
	"Address = 10.8.0.2/32, fd00::2/128\n" +
	"DNS = 203.0.113.53, 198.51.100.53\n" +
	"MTU = 1280\n" +
	"# комментарий\n" +
	"\n" +
	"[Peer]\n" +
	"PublicKey = PUBKEY=\n" +
	"PresharedKey = PSK-MUST-NOT-LEAK=\n" +
	"Endpoint = vpn.example.com:51820\n" +
	"AllowedIPs = 0.0.0.0/0\n"

func importBody(name, conf string) string {
	return `{"name":` + strconv.Quote(name) + `,"conf_b64":"` + base64.StdEncoding.EncodeToString([]byte(conf)) + `"}`
}

func analyzeAnswer(status, output string) func(wire.Command) (wire.CommandResult, bool) {
	return func(cmd wire.Command) (wire.CommandResult, bool) {
		if cmd.Action != "tunnel_analyze" {
			return wire.CommandResult{Status: "ok", Output: `✅ Туннель "x" создан (id=awg21)`}, true
		}
		return wire.CommandResult{Status: status, Output: output}, true
	}
}

func importRespBody(t *testing.T, rec *httptest.ResponseRecorder) miniappImportResp {
	t.Helper()
	var body miniappImportResp
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("ответ не JSON: %d %s", rec.Code, rec.Body.String())
	}
	return body
}

func assertNoConfLeak(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range []string{"PRIVKEY-MUST-NOT-LEAK", "PSK-MUST-NOT-LEAK", base64.StdEncoding.EncodeToString([]byte(importConfFixture))} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s: конфиг утёк: %s", where, text)
		}
	}
}

func TestParseMiniappImportConf(t *testing.T) {
	p, ok := parseMiniappImportConf([]byte(importConfFixture))
	if !ok {
		t.Fatal("годный конфиг не разобран")
	}
	if p.Endpoint != "vpn.example.com:51820" || p.MTU != 1280 ||
		fmt.Sprint(p.Addresses) != "[10.8.0.2/32 fd00::2/128]" || fmt.Sprint(p.DNS) != "[203.0.113.53 198.51.100.53]" ||
		p.Problems == nil || len(p.Problems) != 0 {
		t.Fatalf("предпросмотр: %+v", p)
	}
	assertNoConfLeak(t, "предпросмотр", fmt.Sprintf("%+v", p))
	for name, conf := range map[string]string{
		"без PrivateKey": strings.Replace(importConfFixture, "PrivateKey = PRIVKEY-MUST-NOT-LEAK=\n", "", 1),
		"без PublicKey":  strings.Replace(importConfFixture, "PublicKey = PUBKEY=\n", "", 1),
		"без Endpoint":   strings.Replace(importConfFixture, "Endpoint = vpn.example.com:51820\n", "", 1),
		"не текст":       "\xff\xfe\x00[Interface]",
		"пусто":          "",
	} {
		if _, ok := parseMiniappImportConf([]byte(conf)); ok {
			t.Errorf("%s: разобран как годный", name)
		}
	}
	// Без Address/DNS/MTU конфиг годный: массивы пустые, MTU не пишется.
	minimal := "[Interface]\nPrivateKey = K=\n[Peer]\nPublicKey = P=\nEndpoint = 203.0.113.7:51820\n"
	p, ok = parseMiniappImportConf([]byte(minimal))
	if !ok || p.Addresses == nil || p.DNS == nil || p.MTU != 0 {
		t.Fatalf("минимальный: %+v %v", p, ok)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "mtu") || !strings.Contains(string(b), `"addresses":[]`) {
		t.Fatalf("json: %s", b)
	}
}

func TestMiniappImportReqHidesConf(t *testing.T) {
	req := miniappTunnelImportReq{Name: "vpn-new", ConfB64: base64.StdEncoding.EncodeToString([]byte(importConfFixture))}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "req", req)
	for where, text := range map[string]string{"%v": fmt.Sprintf("%v", req), "%+v": fmt.Sprintf("%+v", req), "%#v": fmt.Sprintf("%#v", req), "slog": buf.String()} {
		assertNoConfLeak(t, where, text)
	}
}

func TestMiniappImportPreviewsExpireAndOnePerPerson(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s := newMiniappImportPreviews(5*time.Minute, func() time.Time { return now })
	first, err := s.put(miniappImportEntry{routerID: 1, tgUser: 100, name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.put(miniappImportEntry{routerID: 1, tgUser: 200, name: "b"})
	if _, ok := s.get(first, 1, 100); !ok {
		t.Fatal("свежий предпросмотр не найден")
	}
	if _, ok := s.get(first, 2, 100); ok {
		t.Fatal("токен сработал на чужом роутере")
	}
	if _, ok := s.get(first, 1, 200); ok {
		t.Fatal("токен сработал у чужого человека")
	}
	// Второй предпросмотр того же человека на том же роутере вытесняет первый.
	second, _ := s.put(miniappImportEntry{routerID: 1, tgUser: 100, name: "c"})
	if _, ok := s.get(first, 1, 100); ok {
		t.Fatal("старый предпросмотр того же человека остался")
	}
	if _, ok := s.get(other, 1, 200); !ok {
		t.Fatal("чужой предпросмотр вытеснен")
	}
	now = now.Add(5*time.Minute + time.Second)
	if _, ok := s.get(second, 1, 100); ok {
		t.Fatal("истёкший предпросмотр найден")
	}
	if _, ok := s.take(other, 1, 200); ok {
		t.Fatal("истёкший предпросмотр подтверждён")
	}
}

func postImport(t *testing.T, env *cabinetEnv, tgUser int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	return env.do(t, tgUser, http.MethodPost, "/v1/miniapp/routers/{id}/tunnels/import", body)
}

func confirmImport(t *testing.T, env *cabinetEnv, tgUser int64, token string) *httptest.ResponseRecorder {
	t.Helper()
	return env.do(t, tgUser, http.MethodPost, "/v1/miniapp/routers/{id}/tunnels/import/confirm", `{"token":`+strconv.Quote(token)+`}`)
}

// Решение 1: импорт -- админ и владелец; оператору и постороннему 404, и
// роутеру ничего не уходит.
func TestMiniappTunnelImportGateByRole(t *testing.T) {
	for _, tc := range []struct {
		user    int64
		allowed bool
	}{{cabOwner, true}, {cabAdmin, true}, {cabOperator, false}, {777, false}} {
		env, sink := newTunnelEnv(t, analyzeAnswer("ok", `{"supported":true,"version":"2.0","errors":[],"warnings":[]}`))
		rec := postImport(t, env, tc.user, importBody("vpn-new", importConfFixture))
		if !tc.allowed {
			if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" || len(sink.enqueued) != 0 {
				t.Errorf("user %d: %d %s очередь %v", tc.user, rec.Code, rec.Body.String(), sink.actions())
			}
			for _, path := range []string{"/v1/miniapp/routers/{id}/tunnels/import/confirm"} {
				if rec := env.do(t, tc.user, http.MethodPost, path, `{"token":"x"}`); rec.Code != http.StatusNotFound {
					t.Errorf("user %d %s: %d", tc.user, path, rec.Code)
				}
			}
			if rec := env.do(t, tc.user, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/import/x", ""); rec.Code != http.StatusNotFound {
				t.Errorf("user %d GET: %d", tc.user, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Errorf("user %d: %d %s", tc.user, rec.Code, rec.Body.String())
		}
	}
}

// Главный путь: предпросмотр с замечаниями роутера, подтверждение, команда
// «добавить как новый» с тем же конфигом. Конфиг не появляется ни в одном
// ответе и ни в журнале.
func TestMiniappTunnelImportPreviewThenConfirm(t *testing.T) {
	env, sink := newTunnelEnv(t, analyzeAnswer("ok", `{"supported":true,"version":"2.0","errors":[],"warnings":[{"code":"mtu","message":"MTU меньше обычного"}]}`))
	rec := postImport(t, env, cabOwner, importBody(" VPN-New ", importConfFixture))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	assertNoConfLeak(t, "ответ предпросмотра", rec.Body.String())
	body := importRespBody(t, rec)
	if body.Token == "" || body.Name != "vpn-new" || body.State != "ready" || !body.Analyzed || !body.CanConfirm {
		t.Fatalf("предпросмотр: %+v", body)
	}
	if body.Preview.Endpoint != "vpn.example.com:51820" || len(body.Preview.Problems) != 1 ||
		body.Preview.Problems[0] != (miniappImportProblem{Severity: "warning", Code: "mtu", Message: "MTU меньше обычного"}) {
		t.Fatalf("preview: %+v", body.Preview)
	}
	if got := sink.actions(); len(got) != 1 || got[0] != "tunnel_analyze" {
		t.Fatalf("очередь: %v", got)
	}
	if sink.enqueued[0].Args["conf"] != base64.StdEncoding.EncodeToString([]byte(importConfFixture)) {
		t.Fatalf("анализу ушёл не тот конфиг")
	}

	// Опрос готового предпросмотра роутер больше не спрашивает.
	rec = env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/import/"+body.Token, "")
	if rec.Code != http.StatusOK || importRespBody(t, rec).State != "ready" || len(sink.enqueued) != 1 {
		t.Fatalf("GET: %d %s очередь %v", rec.Code, rec.Body.String(), sink.actions())
	}
	assertNoConfLeak(t, "ответ GET", rec.Body.String())

	rec = confirmImport(t, env, cabOwner, body.Token)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("подтверждение: %d %s", rec.Code, rec.Body.String())
	}
	assertNoConfLeak(t, "ответ подтверждения", rec.Body.String())
	state := tunnelStateBody(t, rec)
	imp := sink.enqueued[len(sink.enqueued)-1]
	if imp.Action != "tunnel_import" || state.State != "queued" || state.CmdID != imp.ID || state.TunnelName != "vpn-new" {
		t.Fatalf("ответ %+v, команда %+v", state, imp.Action)
	}
	// Решение 3: только «Добавить как новый».
	if imp.Args["replace"] != false || imp.Args["backend"] != "nativewg" || imp.Args["name"] != "vpn-new" ||
		imp.Args["conf"] != base64.StdEncoding.EncodeToString([]byte(importConfFixture)) || len(imp.Args) != 4 {
		t.Fatalf("аргументы импорта: name=%v replace=%v backend=%v keys=%d", imp.Args["name"], imp.Args["replace"], imp.Args["backend"], len(imp.Args))
	}

	// Токен одноразовый.
	if rec := confirmImport(t, env, cabOwner, body.Token); rec.Code != http.StatusGone {
		t.Fatalf("повторное подтверждение: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/import/"+body.Token, ""); rec.Code != http.StatusGone {
		t.Fatalf("GET после подтверждения: %d", rec.Code)
	}
	if code, _, _ := cabinetErrorBody(t, confirmImport(t, env, cabOwner, body.Token)); code != "preview_expired" {
		t.Fatalf("код: %s", code)
	}
	assertNoConfLeak(t, "журнал", env.logs.String())
}

// Ошибки анализа -- роутер конфиг не примет: подтвердить нельзя.
func TestMiniappTunnelImportAnalyzeErrorsBlockConfirm(t *testing.T) {
	env, sink := newTunnelEnv(t, analyzeAnswer("ok", `{"supported":true,"errors":[{"code":"h1h2","message":"H1 и H2 пересекаются"}],"warnings":[]}`))
	body := importRespBody(t, postImport(t, env, cabOwner, importBody("vpn-new", importConfFixture)))
	if body.State != "ready" || !body.Analyzed || body.CanConfirm || len(body.Preview.Problems) != 1 || body.Preview.Problems[0].Severity != "error" {
		t.Fatalf("предпросмотр: %+v", body)
	}
	rec := confirmImport(t, env, cabOwner, body.Token)
	if code, message, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "conf_rejected" || message == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, a := range sink.actions() {
		if a == "tunnel_import" {
			t.Fatal("импорт ушёл роутеру")
		}
	}
}

// Роутер не успел ответить: предпросмотр «в процессе», подтверждать рано,
// опрос не ставит второй анализ, а пришедший ответ доводит до «готово».
func TestMiniappTunnelImportAnalyzingThenReady(t *testing.T) {
	env, sink := newTunnelEnv(t, nil)
	body := importRespBody(t, postImport(t, env, cabOwner, importBody("vpn-new", importConfFixture)))
	if body.State != "analyzing" || body.Analyzed || body.CanConfirm || body.Preview.Endpoint == "" {
		t.Fatalf("предпросмотр: %+v", body)
	}
	if rec := confirmImport(t, env, cabOwner, body.Token); rec.Code != http.StatusConflict {
		t.Fatalf("подтверждение до анализа: %d %s", rec.Code, rec.Body.String())
	} else if code, _, _ := cabinetErrorBody(t, rec); code != "preview_not_ready" {
		t.Fatalf("код: %s", code)
	}
	path := "/v1/miniapp/routers/{id}/tunnels/import/" + body.Token
	if got := importRespBody(t, env.do(t, cabOwner, http.MethodGet, path, "")); got.State != "analyzing" {
		t.Fatalf("GET: %+v", got)
	}
	if got := sink.actions(); len(got) != 1 {
		t.Fatalf("опрос поставил лишний анализ: %v", got)
	}
	sink.results = map[string]wire.CommandResult{sink.enqueued[0].ID: {ID: sink.enqueued[0].ID, Status: "ok", Output: `{"supported":true,"errors":[],"warnings":[]}`}}
	got := importRespBody(t, env.do(t, cabOwner, http.MethodGet, path, ""))
	if got.State != "ready" || !got.Analyzed || !got.CanConfirm {
		t.Fatalf("после ответа: %+v", got)
	}
}

// Анализ пропускается словами, а не молча: старый агент, старая панель,
// ошибка роутера. Подтверждать при этом можно.
func TestMiniappTunnelImportAnalyzeSkipped(t *testing.T) {
	cases := []struct {
		name    string
		version string
		answer  func(wire.Command) (wire.CommandResult, bool)
		queued  int
	}{
		{"агент старше v0.28.0", "v0.27.3", analyzeAnswer("ok", `{"supported":true}`), 0},
		{"панель без анализа", "v0.38.0", analyzeAnswer("ok", `{"supported":false}`), 1},
		{"роутер ответил ошибкой", "", analyzeAnswer("err", "unknown action"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, sink := newTunnelEnv(t, tc.answer)
			if tc.version != "" {
				if err := env.d.Users().UpdateLastSeenAgentVersion(env.ownedID, tc.version); err != nil {
					t.Fatal(err)
				}
			}
			body := importRespBody(t, postImport(t, env, cabOwner, importBody("vpn-new", importConfFixture)))
			if body.State != "ready" || body.Analyzed || !body.CanConfirm || !strings.Contains(body.Note, "проверка пропущена") {
				t.Fatalf("предпросмотр: %+v", body)
			}
			if len(sink.enqueued) != tc.queued {
				t.Fatalf("очередь: %v", sink.actions())
			}
		})
	}
}

// Токен привязан к роутеру и человеку: админ с чужим токеном и чужой роутер
// с этим токеном получают «устарел», а не чужой конфиг.
func TestMiniappTunnelImportTokenBoundToRouterAndPerson(t *testing.T) {
	env, sink := newTunnelEnv(t, analyzeAnswer("ok", `{"supported":true}`))
	token := importRespBody(t, postImport(t, env, cabOwner, importBody("vpn-new", importConfFixture))).Token
	if rec := confirmImport(t, env, cabAdmin, token); rec.Code != http.StatusGone {
		t.Fatalf("админ с токеном владельца: %d %s", rec.Code, rec.Body.String())
	}
	other, err := env.d.Users().GetByNickname("router-other")
	if err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, cabAdmin, http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/tunnels/import/%s", other.ID, token), "")
	if rec.Code != http.StatusGone {
		t.Fatalf("чужой роутер: %d %s", rec.Code, rec.Body.String())
	}
	for _, a := range sink.actions() {
		if a == "tunnel_import" {
			t.Fatal("импорт ушёл роутеру")
		}
	}
	// Владелец своим токеном по-прежнему подтверждает.
	if rec := confirmImport(t, env, cabOwner, token); rec.Code != http.StatusAccepted {
		t.Fatalf("владелец: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappTunnelImportRejectsBadInput(t *testing.T) {
	big := importConfFixture + "# " + strings.Repeat("x", 51<<10) + "\n"
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"имя с цифры", importBody("1vpn", importConfFixture), http.StatusBadRequest, "invalid_name"},
		{"имя кириллицей", importBody("впн", importConfFixture), http.StatusBadRequest, "invalid_name"},
		{"пустое имя", importBody("", importConfFixture), http.StatusBadRequest, "invalid_name"},
		{"не base64", `{"name":"vpn-new","conf_b64":"%%%"}`, http.StatusBadRequest, "invalid_conf"},
		{"не конфиг", importBody("vpn-new", "hello"), http.StatusBadRequest, "invalid_conf"},
		{"файл больше 50 КиБ", importBody("vpn-new", big), http.StatusBadRequest, "conf_too_large"},
		{"тело больше предела", `{"name":"vpn-new","conf_b64":"` + strings.Repeat("A", 100<<10) + `"}`, http.StatusBadRequest, "conf_too_large"},
		{"не JSON", `{"name":`, http.StatusBadRequest, "bad_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, sink := newTunnelEnv(t, analyzeAnswer("ok", `{"supported":true}`))
			rec := postImport(t, env, cabOwner, tc.body)
			if code, message, _ := cabinetErrorBody(t, rec); rec.Code != tc.status || code != tc.code || message == "" {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if len(sink.enqueued) != 0 {
				t.Fatalf("роутеру ушло: %v", sink.actions())
			}
		})
	}
}

// Имя, которое роутер назвал в отчёте за последние 10 минут, занято: второй
// VPN-туннель с тем же именем путал бы и экран, и правила.
func TestMiniappTunnelImportNameTaken(t *testing.T) {
	env, _ := newTunnelEnv(t, analyzeAnswer("ok", `{"supported":true}`))
	if err := env.d.Events().Insert(env.ownedID, "tunnel_awg12", "ok", `{"tunnel_id":"awg12","tunnel_name":"vpn-nl"}`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := env.d.Events().Insert(env.ownedID, "tunnel_awg9", "ok", `{"tunnel_id":"awg9","tunnel_name":"vpn-old"}`, time.Now().UTC().Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	rec := postImport(t, env, cabOwner, importBody("VPN-NL", importConfFixture))
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "name_taken" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := postImport(t, env, cabOwner, importBody("vpn-old", importConfFixture)); rec.Code != http.StatusOK {
		t.Fatalf("имя давно удалённого туннеля: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappTunnelImportWithoutQueue(t *testing.T) {
	env := newCabinetEnv(t, func(d *Deps) { d.CommandSink = nil })
	rec := postImport(t, env, cabOwner, importBody("vpn-new", importConfFixture))
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "commands_not_configured" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
