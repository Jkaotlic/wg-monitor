package backend

import (
	"net/http"
	"testing"
	"time"
)

// PROV-01: регистрация из дашборда не перевыпускает токен живого агента --
// иначе он получает 401 и замолкает. Как у мини-аппа: живой -- 409.
func TestDashboardProvisionRegister_RefusesLiveAgent(t *testing.T) {
	relay := &fakeProvisionRelay{}
	database, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	body := `{"kind":"register","nickname":"liverouter","agent_kind":"static","awgm_url":"https://awg.example"}`
	if rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body); rec.Code != http.StatusCreated {
		t.Fatalf("первая регистрация: %d %s", rec.Code, rec.Body.String())
	}
	u, _ := database.Users().GetByNickname("liverouter")
	if err := database.Users().UpdateLastSeen(u.ID); err != nil {
		t.Fatal(err)
	}
	before := tokenHashOf(t, database, "liverouter")
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("живой агент: %d %s, хотим 409", rec.Code, rec.Body.String())
	}
	if tokenHashOf(t, database, "liverouter") != before {
		t.Fatal("токен живого агента перевыпущен")
	}
}

// Идущая установка того же роутера держит замок выпуска токена: регистрация
// в эту щель переписала бы токен, который установка закоммитит.
func TestDashboardProvisionRegister_RespectsMintLock(t *testing.T) {
	relay := &fakeProvisionRelay{}
	database, store, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	release, ok := tryProvisionMintLock(store, "lockedrouter")
	if !ok {
		t.Fatal("замок не взят")
	}
	defer release()
	body := `{"kind":"register","nickname":"lockedrouter","agent_kind":"static","awgm_url":"https://awg.example"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("под замком: %d %s, хотим 409", rec.Code, rec.Body.String())
	}
	if _, err := database.Users().GetByNickname("lockedrouter"); err == nil {
		t.Fatal("токен выпущен под чужим замком")
	}
}
