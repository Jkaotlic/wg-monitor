package awgmgr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAccessPolicies_LiveShape(t *testing.T) {
	body, err := os.ReadFile("testdata/live-2172/access-policies.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/routing/access-policies" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := New(srv.URL).AccessPolicies(context.Background())
	if err != nil {
		t.Fatalf("AccessPolicies: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("policies = %d, want 2", len(got))
	}
	if got[0].Name != "HydraRoute" || len(got[0].Interfaces) != 3 {
		t.Fatalf("first policy = %+v", got[0])
	}
	// Цепочка приходит с приоритетами; имена -- NDMS, а не ядерные ifaces.
	if got[0].Interfaces[0].Name != "OpkgTun11" || got[0].Interfaces[0].Order != 0 {
		t.Errorf("first interface = %+v", got[0].Interfaces[0])
	}
	if got[0].Interfaces[0].Label != "awg3-work-via-ru1" {
		t.Errorf("label = %q", got[0].Interfaces[0].Label)
	}
	if got[1].Name != "RU" || got[1].Interfaces[0].Name != "GigabitEthernet1" {
		t.Errorf("second policy = %+v", got[1])
	}
}

func TestPolicyInterfaces_LiveShape(t *testing.T) {
	body, err := os.ReadFile("testdata/live-2172/policy-interfaces.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := New(srv.URL).PolicyInterfaces(context.Background())
	if err != nil {
		t.Fatalf("PolicyInterfaces: %v", err)
	}
	up := map[string]bool{}
	for _, pi := range got {
		up[pi.Name] = pi.Up
	}
	if !up["OpkgTun11"] || up["OpkgTun10"] || up["Wireguard0"] {
		t.Errorf("up states = %+v", up)
	}
}

func TestPermitPolicyInterface_BodyShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	if err := New(srv.URL).PermitPolicyInterface(context.Background(), "HydraRoute", "Wireguard0", 2); err != nil {
		t.Fatalf("PermitPolicyInterface: %v", err)
	}
	// Запись живёт в другом неймспейсе, чем чтение -- это контракт роутера.
	if gotPath != "/api/access-policies/permit" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["name"] != "HydraRoute" || gotBody["interface"] != "Wireguard0" || gotBody["order"] != float64(2) {
		t.Errorf("body = %+v", gotBody)
	}
}

func TestDenyPolicyInterface_EncodesQuery(t *testing.T) {
	var gotMethod, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.Query().Get("name") + "|" + r.URL.Query().Get("interface")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	if err := New(srv.URL).DenyPolicyInterface(context.Background(), "Hydra Route", "Wireguard0"); err != nil {
		t.Fatalf("DenyPolicyInterface: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q", gotMethod)
	}
	if gotQuery != "Hydra Route|Wireguard0" {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestRestartTunnel_ByID(t *testing.T) {
	var gotPath, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotID = r.URL.Path, r.URL.Query().Get("id")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	if err := New(srv.URL).RestartTunnel(context.Background(), "awg11"); err != nil {
		t.Fatalf("RestartTunnel: %v", err)
	}
	if gotPath != "/api/control/restart" || gotID != "awg11" {
		t.Errorf("path=%q id=%q", gotPath, gotID)
	}
}

func TestRestartTunnel_MissingEndpointIsRecognisable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":true,"message":"эндпоинт не найден","code":"NOT_FOUND"}`))
	}))
	defer srv.Close()

	err := New(srv.URL).RestartTunnel(context.Background(), "awg11")
	if err == nil {
		t.Fatal("want error on 404")
	}
	// Старые сборки без /api/control/restart должны отличаться от настоящей
	// ошибки: только по этому признаку агент решает откатиться на ndmc.
	if !IsEndpointMissing(err) {
		t.Errorf("IsEndpointMissing(%v) = false, want true", err)
	}
}

// Список политик роутер по умолчанию отдаёт из кэша NDMS. Для проверки
// постусловия записи это неприемлемо: кэш вернёт "интерфейса нет" на успешно
// применённый permit. Параметр refresh=true и есть весь смысл метода, поэтому
// тест проверяет именно его.
func TestAccessPoliciesFresh_BypassesNDMSCache(t *testing.T) {
	body, err := os.ReadFile("testdata/live-2172/access-policies.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var gotPath, gotRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRefresh = r.URL.Query().Get("refresh")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := New(srv.URL).AccessPoliciesFresh(context.Background())
	if err != nil {
		t.Fatalf("AccessPoliciesFresh: %v", err)
	}
	if gotPath != "/api/access-policies" {
		t.Errorf("path = %q, want /api/access-policies", gotPath)
	}
	if gotRefresh != "true" {
		t.Errorf("refresh = %q, want true", gotRefresh)
	}
	// Форма ответа та же, что у кэшированного чтения -- декодер общий.
	if len(got) != 2 || got[0].Name != "HydraRoute" || len(got[0].Interfaces) != 3 {
		t.Fatalf("policies = %+v", got)
	}
	if got[0].Interfaces[0].Name != "OpkgTun11" || got[0].Interfaces[0].Order != 0 {
		t.Errorf("first interface = %+v", got[0].Interfaces[0])
	}
}
