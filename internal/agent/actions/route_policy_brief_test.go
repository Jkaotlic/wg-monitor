package actions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// policyBriefServer отдаёт живые ответы 2.17.2 с диска; missing -- пути,
// которые отвечают 404 (старая сборка), broken -- 500.
func policyBriefServer(t *testing.T, missing, broken map[string]bool) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/api/tunnels/all":               "tunnels-all.json",
		"/api/routing/access-policies":   "access-policies.json",
		"/api/routing/policy-interfaces": "policy-interfaces.json",
	}
	mux := http.NewServeMux()
	for path, name := range files {
		path, name := path, name
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			if missing[path] {
				http.NotFound(w, nil)
				return
			}
			if broken[path] {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			raw, err := os.ReadFile("../awgmgr/testdata/live-2172/" + name)
			if err != nil {
				t.Errorf("fixture %s: %v", name, err)
			}
			_, _ = w.Write(raw)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Сводка политик -- та же логика ролей и те же счётчики, что у route_status:
// один роутер не может отвечать экрану и команде по-разному.
func TestPolicyBriefsMatchRouteStatus(t *testing.T) {
	srv := policyBriefServer(t, nil, nil)
	dns := loadLiveFixture[[]awgmgr.DNSRoute](t, "dns-routes-list.json")

	got, err := PolicyBriefs(context.Background(), awgmgr.New(srv.URL), dns)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for i, p := range got {
		byName[p.Name] = i
	}
	i, ok := byName["HydraRoute"]
	if !ok {
		t.Fatalf("HydraRoute нет в сводке: %+v", got)
	}
	hr := got[i]
	if hr.ActiveTunnelID != "awg11" || !hr.ViaVPN || hr.DNS != 26 {
		t.Errorf("HydraRoute = %+v, хотим active=awg11 via_vpn dns=26", hr)
	}
	if len(hr.Links) != 3 ||
		hr.Links[0].TunnelID != "awg11" || hr.Links[0].Role != "active" ||
		hr.Links[1].TunnelID != "awg20" || hr.Links[1].Role != "unavailable" ||
		hr.Links[2].TunnelID != "awg10" || hr.Links[2].Role != "unavailable" {
		t.Errorf("звенья HydraRoute = %+v", hr.Links)
	}
	ru := got[byName["RU"]]
	if ru.ActiveTunnelID != "" || ru.ViaVPN || ru.DNS != 2 {
		t.Errorf("RU = %+v, хотим мимо VPN, dns=2", ru)
	}
}

// Сборка без политик (404) -- не ошибка и не пустая сводка: поля нет вовсе,
// бэкенд отвечает по-старому.
func TestPolicyBriefsEndpointMissingIsNotAnError(t *testing.T) {
	srv := policyBriefServer(t, map[string]bool{"/api/routing/access-policies": true}, nil)
	got, err := PolicyBriefs(context.Background(), awgmgr.New(srv.URL), nil)
	if err != nil || got != nil {
		t.Fatalf("got %+v, err %v; хотим nil, nil", got, err)
	}
}

// Любой другой отказ -- ошибка: без неё сводка без ролей читалась бы как
// «ни одного живого звена».
func TestPolicyBriefsReadFailuresAreErrors(t *testing.T) {
	for _, path := range []string{"/api/routing/access-policies", "/api/routing/policy-interfaces", "/api/tunnels/all"} {
		srv := policyBriefServer(t, nil, map[string]bool{path: true})
		if got, err := PolicyBriefs(context.Background(), awgmgr.New(srv.URL), nil); err == nil {
			t.Errorf("%s: 500 без ошибки, сводка %+v", path, got)
		}
	}
	// Интерфейсы политик 404 при прочитанных политиках -- тоже недостающие
	// данные, а не старая сборка (как в RouteStatus).
	srv := policyBriefServer(t, map[string]bool{"/api/routing/policy-interfaces": true}, nil)
	if _, err := PolicyBriefs(context.Background(), awgmgr.New(srv.URL), nil); err == nil {
		t.Error("policy-interfaces 404 при живых политиках: хотим ошибку")
	}
}
