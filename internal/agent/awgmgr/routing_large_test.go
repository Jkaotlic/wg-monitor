package awgmgr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 07.09.2026 на snekhaev список DNS-маршрутов перерос общий потолок get
// (1 МиБ): обрезанный JSON падал на разборе, агент считал правил 0, экран
// писал «трафик идёт напрямую», а проверка туннелей глушила их падения как
// «правил нет». Большой список -- честный ответ, и он обязан читаться.
func TestListDNSRoutes_BodyOverOneMiB(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"success":true,"data":[`)
	const lists, domains = 30, 3000
	for i := 0; i < lists; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"list_%d","name":"L%d","enabled":true,"backend":"hydraroute","hrPolicyName":"HydraRoute","domains":[`, i, i)
		for j := 0; j < domains; j++ {
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `"site-%02d-%05d.example.com"`, i, j)
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	body := b.String()
	if len(body) <= 2<<20 {
		t.Fatalf("тело %d байт не больше 2 МиБ -- тест ничего не проверяет", len(body))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := New(srv.URL).ListDNSRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != lists {
		t.Fatalf("len = %d, want %d", len(got), lists)
	}
}

// Тело больше потолка -- явная ошибка с размером, а не «unexpected end of JSON
// input», по которому причину не узнать из отчёта.
func TestListDNSRoutes_BodyOverCeilingNamesTheSize(t *testing.T) {
	body := `{"success":true,"data":[{"id":"big","domains":["` + strings.Repeat("a", routeListMaxBody) + `"]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := New(srv.URL).ListDNSRoutes(context.Background())
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("want «larger than» error, got %v", err)
	}
}
