package actions

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Формы ответов сняты с живого роутера 2.17.2: цепочка политики приезжает
// массивом interfaces с полями name/label/order.
func policyChainJSON(first, second string) string {
	return `{"success":true,"data":[{"name":"HydraRoute","interfaces":[` +
		`{"name":"` + first + `","label":"a","order":0},` +
		`{"name":"` + second + `","label":"b","order":1}]}]}`
}

const promoteTunnelsJSON = `{"success":true,"data":{"tunnels":[` +
	`{"id":"awg11","name":"work","interfaceName":"OpkgTun11","enabled":true},` +
	`{"id":"awg10","name":"main","interfaceName":"OpkgTun10","enabled":true}]}}`

func promoteServer(t *testing.T, applied *bool, permitBody *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/access-policies/permit"):
			b, _ := io.ReadAll(r.Body)
			*permitBody = string(b)
			*applied = true
			_, _ = w.Write([]byte(`{"success":true,"data":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/api/tunnels/all"):
			_, _ = w.Write([]byte(promoteTunnelsJSON))
		case strings.Contains(r.URL.RawQuery, "refresh=true"):
			// Постусловие читается МИМО кеша NDMS.
			if *applied {
				_, _ = w.Write([]byte(policyChainJSON("OpkgTun10", "OpkgTun11")))
				return
			}
			_, _ = w.Write([]byte(policyChainJSON("OpkgTun11", "OpkgTun10")))
		default:
			_, _ = w.Write([]byte(policyChainJSON("OpkgTun11", "OpkgTun10")))
		}
	}))
}

// «Сделать главным» -- это permit с order 0 для интерфейса, УЖЕ состоящего в
// цепочке. Отдельного эндпоинта переупорядочивания у awg-manager нет, и
// родной веб-интерфейс роутера делает ровно это.
func TestRoutePolicyPromote_PermitsWithOrderZero(t *testing.T) {
	var applied bool
	var permitBody string
	srv := promoteServer(t, &applied, &permitBody)
	defer srv.Close()

	out, err := RoutePolicyPromoteJSON(context.Background(), awgmgr.New(srv.URL),
		wire.RoutePolicyPromoteRequest{PolicyName: "HydraRoute", TunnelID: "awg10"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if !strings.Contains(permitBody, `"order":0`) {
		t.Fatalf("тело permit = %q, ожидался order 0", permitBody)
	}
	if !strings.Contains(permitBody, `"interface":"OpkgTun10"`) {
		t.Fatalf("тело permit = %q, ожидался интерфейс туннеля awg10", permitBody)
	}
	if !strings.Contains(out, "OpkgTun10") {
		t.Fatalf("ответ = %q, ожидалась новая цепочка", out)
	}
}

// Этот API умеет ответить success:true и не сделать ничего -- наблюдалось на
// мосте, и из нашего клиента, и из родного UI роутера. Считать такой ответ
// успехом значит отрапортовать о переключении, которого не было.
func TestRoutePolicyPromote_SilentNoopIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/access-policies/permit"):
			_, _ = w.Write([]byte(`{"success":true,"data":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/api/tunnels/all"):
			_, _ = w.Write([]byte(promoteTunnelsJSON))
		default:
			_, _ = w.Write([]byte(policyChainJSON("OpkgTun11", "OpkgTun10")))
		}
	}))
	defer srv.Close()

	_, err := RoutePolicyPromoteJSON(context.Background(), awgmgr.New(srv.URL),
		wire.RoutePolicyPromoteRequest{PolicyName: "HydraRoute", TunnelID: "awg10"})
	if err == nil {
		t.Fatal("молчаливый no-op должен быть ошибкой, а не успехом")
	}
	if !strings.Contains(err.Error(), "did not become") {
		t.Fatalf("err = %v, ожидалось объяснение про непроверенное постусловие", err)
	}
}

// Добавить новый интерфейс в политику -- другая операция с другим радиусом
// поражения: она меняет то, на что политика может переключиться, а не только
// порядок. Тихо сделать это вместо переупорядочивания нельзя.
func TestRoutePolicyPromote_RefusesTunnelOutsideChain(t *testing.T) {
	var applied bool
	var body string
	srv := promoteServer(t, &applied, &body)
	defer srv.Close()

	_, err := RoutePolicyPromoteJSON(context.Background(), awgmgr.New(srv.URL),
		wire.RoutePolicyPromoteRequest{PolicyName: "HydraRoute", TunnelID: "awg99"})
	if err == nil || !strings.Contains(err.Error(), "not in policy") {
		t.Fatalf("err = %v, ожидался отказ добавлять новый интерфейс", err)
	}
	if applied {
		t.Fatal("permit не должен был уйти вовсе")
	}
}

func TestRoutePolicyPromote_RefusesUnknownPolicy(t *testing.T) {
	var applied bool
	var body string
	srv := promoteServer(t, &applied, &body)
	defer srv.Close()

	_, err := RoutePolicyPromoteJSON(context.Background(), awgmgr.New(srv.URL),
		wire.RoutePolicyPromoteRequest{PolicyName: "НетТакой", TunnelID: "awg10"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, ожидался отказ по неизвестной политике", err)
	}
}
