package backend

import (
	"net/http"
	"testing"
)

// v0.61.3: кабинет Amnezia Premium отключён целиком. Каждый маршрут, который
// раньше ходил в кабинет, отвечает 410 amnezia_disabled той же фразой, что
// экран подписки, и не шлёт агенту ни одной команды. Фейковый кабинет здесь
// исправен и выдал бы конфиг: отказ должен идти от обработчика, а не от
// случайной ошибки кабинета.

func expectAmneziaDisabled(t *testing.T, what, code, msg string, status int) {
	t.Helper()
	if status != http.StatusGone || code != "amnezia_disabled" || msg != AmneziaDisabledText {
		t.Fatalf("%s: %d %s %q", what, status, code, msg)
	}
}

func TestAmneziaDisabled_HandlersRefuseWith410(t *testing.T) {
	env := newCabinetEnv(t)
	env.keys.list["amnezia"] = []CabinetSecret{{ID: "k1", Label: "старый", Mask: "••••abcd", Active: true}}

	rec := env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", `{"provider":"amnezia","option_id":"nl"}`)
	code, msg, _ := cabinetErrorBody(t, rec)
	expectAmneziaDisabled(t, "выпуск", code, msg, rec.Code)

	rec = env.do(t, cabOwner, http.MethodPost, sendConfPath, `{"provider":"amnezia","option_id":"nl"}`)
	code, msg, _ = cabinetErrorBody(t, rec)
	expectAmneziaDisabled(t, "файл в личку", code, msg, rec.Code)

	rec = env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/revoke", `{"country":"nl","confirm":"router-owned"}`)
	code, msg, _ = cabinetErrorBody(t, rec)
	expectAmneziaDisabled(t, "отзыв страны", code, msg, rec.Code)

	rec = env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":"vpn://NEW-KEY-efgh5678","label":"новый"}`)
	code, msg, _ = cabinetErrorBody(t, rec)
	expectAmneziaDisabled(t, "добавление ключа", code, msg, rec.Code)

	if len(env.cab.issued) != 0 || len(env.sink.enqueued) != 0 || len(env.docs.sent) != 0 || len(env.keys.revoked) != 0 || len(env.keys.added) != 0 {
		t.Fatalf("дошло до кабинета или агента: issued=%v cmds=%d docs=%d revoked=%v added=%v",
			env.cab.issued, len(env.sink.enqueued), len(env.docs.sent), env.keys.revoked, env.keys.added)
	}

	// Сохранённый ключ удаляется: это файл на сервере, не кабинет.
	rec = env.do(t, cabOwner, http.MethodDelete, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys/k1", "")
	if rec.Code != http.StatusNoContent || len(env.keys.list["amnezia"]) != 0 {
		t.Fatalf("удаление ключа: %d %s", rec.Code, rec.Body.String())
	}

	// Чужому -- по-прежнему 404 до любых слов о кабинете.
	rec = env.do(t, cabStranger, http.MethodPost, "/v1/miniapp/routers/{id}/vpn/issue", `{"provider":"amnezia","option_id":"nl"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("чужой роутер: %d %s", rec.Code, rec.Body.String())
	}
}

// Кабинет сам вернул ErrAmneziaDisabled (провайдер дошёл до него другим
// путём) -- та же фраза и тот же код, а не «кабинет не ответил».
func TestAmneziaDisabled_CabinetErrorMapsTo410(t *testing.T) {
	env := newCabinetEnv(t)
	env.cab.err = ErrAmneziaDisabled
	env.keys.addErr = ErrAmneziaDisabled
	env.keys.revokeErr = ErrAmneziaDisabled
	for _, tc := range []struct{ what, path, body string }{
		{"выпуск", "/v1/miniapp/routers/{id}/vpn/issue", `{"provider":"hidemyname","option_id":"nl"}`},
		{"файл в личку", sendConfPath, `{"provider":"hidemyname","option_id":"nl"}`},
		{"добавление", "/v1/miniapp/routers/{id}/cabinets/hidemy/codes", `{"access_code":"123456789012345"}`},
	} {
		rec := env.do(t, cabOwner, http.MethodPost, tc.path, tc.body)
		code, msg, _ := cabinetErrorBody(t, rec)
		expectAmneziaDisabled(t, tc.what, code, msg, rec.Code)
	}
	if len(env.sink.enqueued) != 0 {
		t.Fatalf("команда агенту: %d", len(env.sink.enqueued))
	}
}

func TestAmneziaDisabled_TextInTable(t *testing.T) {
	if miniappCabinetErrorText("amnezia_disabled") != AmneziaDisabledText {
		t.Fatalf("в таблице: %q", miniappCabinetErrorText("amnezia_disabled"))
	}
}

// Мастер замены: отказ до выпуска -- задание не заводится.
func TestAmneziaDisabled_ReplaceRefusedBeforeIssue(t *testing.T) {
	deps, ownedID, tgUser := replaceDeps(t)
	rec := postReplace(t, NewMux(deps), ownedID, tgUser,
		`{"provider":"amnezia","option_id":"nl","old_tunnel_id":"awg11","policy_name":"HydraRoute"}`)
	code, msg, _ := cabinetErrorBody(t, rec)
	expectAmneziaDisabled(t, "мастер замены", code, msg, rec.Code)
	if j, ok := deps.Replace.Store.LatestFor("router-owned"); ok {
		t.Fatalf("задание замены заведено: %+v", j)
	}
}
