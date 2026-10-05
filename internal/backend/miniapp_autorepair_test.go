package backend

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
)

// autorepairEnv -- кабинетное окружение (владелец 100, оператор 555, админ
// 999) с движком починки: экран автопочинки читает и счётчик попыток, и
// кабинеты, и панели своих серверов.
func autorepairEnv(t *testing.T) *cabinetEnv {
	t.Helper()
	env := newCabinetEnv(t, func(d *Deps) {
		d.LinkRepair = &linkrepair.Deps{Attempts: linkrepair.Attempts{KV: d.DB.KV()}}
	})
	env.cab.accounts = map[string]VPNAccount{
		"amnezia": {Provider: "amnezia", Label: "Amnezia Premium", Connected: true,
			Options: []VPNOption{{ID: "nl", Label: "Нидерланды", Issued: true}, {ID: "de", Label: "Германия", Issued: true}, {ID: "fi", Label: "Финляндия"}}},
	}
	env.awg3.issuable = []awg3panel.IssuablePanel{{ID: "main", Label: "Main (Амстердам)",
		Ifaces: []awg3panel.Iface{{ID: "awg1", Title: "main"}, {ID: "awg2", Title: "reserve"}}}}
	env.awg3.issuers = map[string][]int64{"main": {cabOperator}}
	return env
}

func seedAutorepairTunnel(t *testing.T, env *cabinetEnv, tid, name string) {
	t.Helper()
	details := `{"tunnel_id":"` + tid + `","tunnel_name":"` + name + `"}`
	if err := env.d.Events().Insert(env.ownedID, "tunnel_"+tid, "ok", details, time.Now()); err != nil {
		t.Fatalf("событие VPN-туннеля: %v", err)
	}
}

func decodeAutorepair(t *testing.T, body []byte) miniappAutorepairResp {
	t.Helper()
	var resp miniappAutorepairResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("ответ не разобрать: %v: %s", err, body)
	}
	return resp
}

func (e *cabinetEnv) nickname(t *testing.T) string {
	t.Helper()
	u, err := e.d.Users().GetByID(e.ownedID)
	if err != nil {
		t.Fatalf("роутер: %v", err)
	}
	return u.Nickname
}

func TestAutorepair_GetDefaultsOff(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg12", "Дача")
	rec := env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeAutorepair(t, rec.Body.Bytes())
	if resp.Enabled || resp.Provider != "" || !resp.CanEdit {
		t.Fatalf("строки нет -- выключено, править может владелец: %+v", resp)
	}
	if resp.Suggested != nil {
		t.Fatalf("подсказки быть не должно: %+v", resp.Suggested)
	}
	if resp.HasBackup != nil {
		t.Fatalf("о резерве бэкенд без роутера не знает -- поле пустое: %v", *resp.HasBackup)
	}
	byProv := map[string]miniappAutorepairSrc{}
	for _, s := range resp.Sources {
		byProv[s.Provider] = s
	}
	am, ok := byProv["amnezia"]
	if !ok || !am.OK || len(am.Options) != 2 || am.Options[0].ID != "nl" || am.Options[0].Label != "Нидерланды" {
		t.Fatalf("подключённый кабинет обязан быть с вариантами: %+v", resp.Sources)
	}
	hm, ok := byProv["hidemyname"]
	if !ok || hm.OK || hm.Note == "" {
		t.Fatalf("неподключённый кабинет -- ok=false с объяснением: %+v", hm)
	}
	// Владелец не выдаёт с панели -- своего сервера в списке нет.
	if _, ok := byProv["awg3"]; ok {
		t.Fatalf("панель без допуска не предлагается: %+v", resp.Sources)
	}

	// Оператор с допуском видит свой сервер с парами «панель/интерфейс».
	rec = env.do(t, cabOperator, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", "")
	resp = decodeAutorepair(t, rec.Body.Bytes())
	var awg3 *miniappAutorepairSrc
	for i := range resp.Sources {
		if resp.Sources[i].Provider == "awg3" {
			awg3 = &resp.Sources[i]
		}
	}
	if awg3 == nil || !awg3.OK || len(awg3.Options) != 2 || awg3.Options[0].ID != "main/awg1" {
		t.Fatalf("свой сервер для допущенного: %+v", resp.Sources)
	}
	for _, s := range resp.Sources {
		for _, text := range []string{s.Label, s.Note} {
			if words := latinOutsideGuillemets(text); len(words) > 0 && s.Provider == "awg3" {
				t.Errorf("латиница вне ёлочек %v: %q", words, text)
			}
		}
	}
}

func TestAutorepair_SuggestFromOrigin(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg12", "Дача")
	if err := env.d.TunnelOrigins().Record(env.ownedID, "awg12", "Дача", "amnezia", "nl", time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", "")
	resp := decodeAutorepair(t, rec.Body.Bytes())
	if resp.Suggested == nil || resp.Suggested.Provider != "amnezia" || resp.Suggested.Option != "nl" ||
		resp.Suggested.Why != "так он был выпущен" {
		t.Fatalf("подсказка из происхождения: %+v", resp.Suggested)
	}
	if resp.Enabled {
		t.Fatal("подсказка режим не включает")
	}
}

func TestAutorepair_SuggestFromAwg3Name(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg13", awg3panel.TunnelName("main", "awg2"))
	rec := env.do(t, cabOperator, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg13/autorepair", "")
	resp := decodeAutorepair(t, rec.Body.Bytes())
	if resp.Suggested == nil || resp.Suggested.Provider != "awg3" || resp.Suggested.Option != "main/awg2" {
		t.Fatalf("подсказка по имени своего сервера: %+v", resp.Suggested)
	}
	if words := latinOutsideGuillemets(resp.Suggested.Why); len(words) > 0 {
		t.Fatalf("латиница вне ёлочек %v: %q", words, resp.Suggested.Why)
	}
	// Тот, кому панель не разрешена, подсказки на неё не получает.
	rec = env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg13/autorepair", "")
	if resp := decodeAutorepair(t, rec.Body.Bytes()); resp.Suggested != nil {
		t.Fatalf("подсказка на чужую панель: %+v", resp.Suggested)
	}
}

func TestAutorepair_SuggestFromAmneziaName(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg14", "amnezia_de")
	seedAutorepairTunnel(t, env, "awg15", "hidemy_a1b2c3")
	rec := env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg14/autorepair", "")
	resp := decodeAutorepair(t, rec.Body.Bytes())
	if resp.Suggested == nil || resp.Suggested.Provider != "amnezia" || resp.Suggested.Option != "de" {
		t.Fatalf("подсказка по имени amnezia_: %+v", resp.Suggested)
	}
	// Кабинет HideMy.name не подключён -- подсказывать его нельзя.
	rec = env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg15/autorepair", "")
	if resp := decodeAutorepair(t, rec.Body.Bytes()); resp.Suggested != nil {
		t.Fatalf("неподключённый кабинет в подсказке: %+v", resp.Suggested)
	}
	env.cab.accounts["hidemyname"] = VPNAccount{Provider: "hidemyname", Label: "HideMy.name", Connected: true}
	rec = env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg15/autorepair", "")
	resp = decodeAutorepair(t, rec.Body.Bytes())
	if resp.Suggested == nil || resp.Suggested.Provider != "hidemyname" || resp.Suggested.Option != "a1b2c3" {
		t.Fatalf("подсказка по имени hidemy_: %+v", resp.Suggested)
	}
}

func TestAutorepair_PutRejectsUnconnectedSource(t *testing.T) {
	env := autorepairEnv(t)
	rec := env.do(t, cabOwner, http.MethodPut, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair",
		`{"enabled":true,"provider":"hidemyname","option":"a1b2c3"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "source_not_connected") {
		t.Fatalf("код %d, хотим 409 source_not_connected: %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := env.d.TunnelRepairSettings().Get(env.ownedID, "awg12"); ok {
		t.Fatal("отказ не пишет настройку")
	}
	rec = env.do(t, cabOwner, http.MethodPut, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair",
		`{"enabled":true,"provider":"selfhosted","option":"x"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_provider") {
		t.Fatalf("код %d, хотим 400 bad_provider: %s", rec.Code, rec.Body.String())
	}
}

func TestAutorepair_PutAwg3NeedsIssuer(t *testing.T) {
	env := autorepairEnv(t)
	body := `{"enabled":true,"provider":"awg3","option":"main/awg1"}`
	rec := env.do(t, cabOwner, http.MethodPut, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", body)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "source_forbidden") {
		t.Fatalf("код %d, хотим 403 source_forbidden: %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabOperator, http.MethodPut, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("допущенный оператор: код %d: %s", rec.Code, rec.Body.String())
	}
	s, ok, err := env.d.TunnelRepairSettings().Get(env.ownedID, "awg12")
	if err != nil || !ok || !s.Enabled || s.Provider != "awg3" || s.Option != "main/awg1" || s.UpdatedBy != cabOperator {
		t.Fatalf("настройка: %+v ok=%v err=%v", s, ok, err)
	}
	resp := decodeAutorepair(t, rec.Body.Bytes())
	if !resp.Enabled || resp.Provider != "awg3" || resp.Option != "main/awg1" {
		t.Fatalf("ответ PUT -- настройка как есть: %+v", resp)
	}
}

func TestAutorepair_PutEnableClearsBlock(t *testing.T) {
	env := autorepairEnv(t)
	nick := env.nickname(t)
	kv := linkrepair.Attempts{KV: env.d.KV()}
	if err := kv.Record(nick, "tunnel_awg12", false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := kv.Allow(nick, "tunnel_awg12"); ok {
		t.Fatal("после провала автопочинка обязана стоять")
	}
	rec := env.do(t, cabOwner, http.MethodPut, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair",
		`{"enabled":true,"provider":"amnezia","option":"nl","allow_relocate":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	if ok, why := kv.Allow(nick, "tunnel_awg12"); !ok {
		t.Fatalf("включение снимает стоп (D1), а стоит: %s", why)
	}
	resp := decodeAutorepair(t, rec.Body.Bytes())
	if resp.Blocked != "" || !resp.AllowRelocate {
		t.Fatalf("ответ: %+v", resp)
	}

	// Выключение -- без проверок источника, источник остаётся на месте.
	rec = env.do(t, cabOwner, http.MethodPut, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("выключение: код %d: %s", rec.Code, rec.Body.String())
	}
	s, ok, _ := env.d.TunnelRepairSettings().Get(env.ownedID, "awg12")
	if !ok || s.Enabled || s.Provider != "amnezia" || s.Option != "nl" {
		t.Fatalf("выключение не трогает источник: %+v", s)
	}
}

func TestAutorepair_ListStates(t *testing.T) {
	env := autorepairEnv(t)
	repo := env.d.TunnelRepairSettings()
	for _, s := range []db.TunnelRepairSetting{
		{UserID: env.ownedID, TunnelID: "awg12", Enabled: true, Provider: "amnezia", Option: "nl"},
		{UserID: env.ownedID, TunnelID: "awg13", Enabled: true},
		{UserID: env.ownedID, TunnelID: "awg14", Enabled: true, Provider: "amnezia", Option: "de"},
		{UserID: env.ownedID, TunnelID: "awg15", Enabled: false, Provider: "amnezia", Option: "de"},
	} {
		if err := repo.Put(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := (linkrepair.Attempts{KV: env.d.KV()}).Record(env.nickname(t), "tunnel_awg14", false); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, cabOperator, http.MethodGet, "/v1/miniapp/routers/{id}/autorepair", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Tunnels map[string]string `json:"tunnels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"awg12": "on", "awg13": "limited", "awg14": "blocked"}
	if len(resp.Tunnels) != len(want) {
		t.Fatalf("метки: %v, хотим %v", resp.Tunnels, want)
	}
	for k, v := range want {
		if resp.Tunnels[k] != v {
			t.Fatalf("метки: %v, хотим %v", resp.Tunnels, want)
		}
	}

	// GET одного VPN-туннеля называет причину стопа.
	rec = env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg14/autorepair", "")
	if r := decodeAutorepair(t, rec.Body.Bytes()); r.Blocked == "" || !r.Enabled {
		t.Fatalf("стоп обязан называться: %+v", r)
	}
}

func TestAutorepair_ForeignRouter404(t *testing.T) {
	env := autorepairEnv(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", ""},
		{http.MethodPut, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", `{"enabled":true}`},
		{http.MethodGet, "/v1/miniapp/routers/{id}/autorepair", ""},
	} {
		rec := env.do(t, cabStranger, c.method, c.path, c.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: код %d, хотим 404", c.method, c.path, rec.Code)
		}
	}
	if _, ok, _ := env.d.TunnelRepairSettings().Get(env.ownedID, "awg12"); ok {
		t.Fatal("чужой записал настройку")
	}
}

// Тумблер на роутер удалён: им никто не пользовался, а настройка теперь живёт
// на VPN-туннеле.
func TestRepairAutoRouteRemoved(t *testing.T) {
	env := autorepairEnv(t)
	rec := env.do(t, cabOwner, http.MethodPut, "/v1/miniapp/routers/{id}/repair/auto", `{"enabled":true}`)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /repair/auto: код %d, хотим 404/405", rec.Code)
	}
}

// Включение запоминает имя VPN-туннеля: под этим id потом может оказаться
// другой туннель, и движок обязан это заметить.
func TestAutorepair_PutStoresTunnelName(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg12", "Дача")
	rec := env.do(t, cabOwner, http.MethodPut, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair",
		`{"enabled":true,"provider":"amnezia","option":"nl"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	s, ok, err := env.d.TunnelRepairSettings().Get(env.ownedID, "awg12")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if s.TunnelName != "Дача" {
		t.Fatalf("имя VPN-туннеля в настройке %q, ждали «Дача»", s.TunnelName)
	}
}

// VPN-туннель переименовали после включения: GET называет прежнее имя
// (rename_pending), список ставит «стоит» с причиной, а PUT включения
// переписывает имя -- и расхождение уходит.
func TestAutorepair_RenamePendingUntilConfirmed(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg12", "Дача")
	if err := env.d.TunnelRepairSettings().Put(db.TunnelRepairSetting{
		UserID: env.ownedID, TunnelID: "awg12", TunnelName: "Старый", Enabled: true, Provider: "amnezia", Option: "nl",
	}); err != nil {
		t.Fatal(err)
	}
	path := "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair"
	rec := env.do(t, cabOwner, http.MethodGet, path, "")
	if r := decodeAutorepair(t, rec.Body.Bytes()); r.RenamePending != "Старый" || !r.Enabled {
		t.Fatalf("расхождение имени не видно: %+v", r)
	}

	var list struct {
		Tunnels map[string]string `json:"tunnels"`
		Reasons map[string]string `json:"reasons"`
	}
	rec = env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/autorepair", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Tunnels["awg12"] != "blocked" || !strings.Contains(list.Reasons["awg12"], "подтвердите") {
		t.Fatalf("список: %+v", list)
	}
	if words := latinOutsideGuillemets(list.Reasons["awg12"]); len(words) > 0 {
		t.Fatalf("латиница в причине: %v", words)
	}

	rec = env.do(t, cabOwner, http.MethodPut, path, `{"enabled":true,"provider":"amnezia","option":"nl"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	if r := decodeAutorepair(t, rec.Body.Bytes()); r.RenamePending != "" {
		t.Fatalf("после подтверждения расхождение осталось: %+v", r)
	}
	if s, _, _ := env.d.TunnelRepairSettings().Get(env.ownedID, "awg12"); s.TunnelName != "Дача" {
		t.Fatalf("имя не переписано: %+v", s)
	}
	list.Tunnels, list.Reasons = nil, nil
	rec = env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/autorepair", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Tunnels["awg12"] != "on" || list.Reasons["awg12"] != "" {
		t.Fatalf("список после подтверждения: %+v", list)
	}
}

// «Amnezia Premium»: источник автопочинки -- только уже выпущенная страна.
// Это конфиг самого VPN-туннеля; невыпущенная страна при «выпустить заново»
// заняла бы новое место в подписке.
func TestAutorepair_AmneziaOnlyIssuedCountries(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg12", "amnezia_fi")
	path := "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair"
	resp := decodeAutorepair(t, env.do(t, cabOwner, http.MethodGet, path, "").Body.Bytes())
	for _, src := range resp.Sources {
		if src.Provider != "amnezia" {
			continue
		}
		if len(src.Options) != 2 || src.Options[0].ID != "nl" || src.Options[1].ID != "de" {
			t.Fatalf("варианты «Amnezia Premium»: %+v", src.Options)
		}
	}
	if resp.Suggested != nil {
		t.Fatalf("подсказана невыпущенная страна по имени: %+v", resp.Suggested)
	}
	if err := env.d.TunnelOrigins().Record(env.ownedID, "awg12", "amnezia_fi", "amnezia", "fi", time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	resp = decodeAutorepair(t, env.do(t, cabOwner, http.MethodGet, path, "").Body.Bytes())
	if resp.Suggested != nil && resp.Suggested.Option == "fi" {
		t.Fatalf("подсказана невыпущенная страна из происхождения: %+v", resp.Suggested)
	}

	rec := env.do(t, cabOwner, http.MethodPut, path, `{"enabled":true,"provider":"amnezia","option":"fi"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "эта страна ещё не выпущена в кабинете — выберите выпущенную") {
		t.Fatalf("PUT невыпущенной страны: код %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := env.d.TunnelRepairSettings().Get(env.ownedID, "awg12"); ok {
		t.Fatal("настройка с невыпущенной страной записана")
	}
	if rec := env.do(t, cabOwner, http.MethodPut, path, `{"enabled":true,"provider":"amnezia","option":"nl"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT выпущенной страны: код %d: %s", rec.Code, rec.Body.String())
	}

	// Ни одной выпущенной -- источник недоступен, с объяснением.
	env.cab.accounts["amnezia"] = VPNAccount{Provider: "amnezia", Label: "Amnezia Premium", Connected: true,
		Options: []VPNOption{{ID: "fi", Label: "Финляндия"}}}
	resp = decodeAutorepair(t, env.do(t, cabOwner, http.MethodGet, path, "").Body.Bytes())
	for _, src := range resp.Sources {
		if src.Provider == "amnezia" && (src.OK || len(src.Options) != 0 || src.Note == "") {
			t.Fatalf("кабинет без выпущенных стран: %+v", src)
		}
	}
}

// Смена страны «Amnezia Premium» уже израсходована -- GET это говорит, лист
// не обещает её снова.
func TestAutorepair_GetRelocateSpent(t *testing.T) {
	env := autorepairEnv(t)
	repo := env.d.TunnelRepairSettings()
	if err := repo.Put(db.TunnelRepairSetting{UserID: env.ownedID, TunnelID: "awg12", Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true}); err != nil {
		t.Fatal(err)
	}
	path := "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair"
	rec := env.do(t, cabOwner, http.MethodGet, path, "")
	if !strings.Contains(rec.Body.String(), `"relocate_spent":""`) {
		t.Fatalf("поле обязано быть и пустым: %s", rec.Body.String())
	}
	if err := repo.SpendRelocation(env.ownedID, "awg12", "de"); err != nil {
		t.Fatal(err)
	}
	if r := decodeAutorepair(t, env.do(t, cabOwner, http.MethodGet, path, "").Body.Bytes()); r.RelocateSpent != "de" {
		t.Fatalf("relocate_spent: %+v", r)
	}
}
