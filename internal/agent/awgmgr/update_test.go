package awgmgr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Проверка обновления спрашивает awg-manager с force=true и понимает ответ и
// в конверте success/data, и голым объектом: конверт у /system/update/* в
// openapi не описан, и угадывать одну форму значит сломаться на другой.
func TestClient_UpdateCheck_ForceAndEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/update/check" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.URL.Query().Get("force") != "true" {
			t.Errorf("force query: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"available":true,"currentVersion":"2.19.0+r2","latestVersion":"2.19.1","checking":false}}`))
	}))
	defer srv.Close()
	got, err := New(srv.URL).UpdateCheck(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.CurrentVersion != "2.19.0+r2" || got.LatestVersion != "2.19.1" {
		t.Errorf("got %+v", got)
	}
}

func TestClient_UpdateCheck_BareObjectWithoutForce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("без force query не нужен: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"available":false,"currentVersion":"2.19.0+r2","latestVersion":"2.19.0+r2"}`))
	}))
	defer srv.Close()
	got, err := New(srv.URL).UpdateCheck(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Available || got.CurrentVersion != "2.19.0+r2" {
		t.Errorf("got %+v", got)
	}
}

func TestClient_UpdateCheck_EnvelopeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"data":null}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL).UpdateCheck(context.Background(), true); err == nil || !strings.Contains(err.Error(), "success=false") {
		t.Fatalf("ожидалась ошибка success=false, got %v", err)
	}
}

// Отказ самого awg-manager обязан доехать с HTTP-кодом: по нему действие
// отличает «отказался» от «перезапускается и не отвечает».
func TestClient_UpdateApply_PostsAndSurfacesRefusal(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/system/update/apply" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"updating"}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.UpdateApply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	status = http.StatusConflict
	if err := c.UpdateApply(context.Background()); err == nil || !strings.Contains(err.Error(), ": HTTP 409") {
		t.Fatalf("ожидался отказ с HTTP 409, got %v", err)
	}
}

func TestClient_SystemInfo_ReadsLoadedKernelModule(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":"2.19.0+r2","kernelModuleVersion":"3.2.20260930","kernelModuleLoadedVersion":"3.1.20260906"}}`))
	}))
	defer srv.Close()
	got, err := New(srv.URL).SystemInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.KernelModuleVersion != "3.2.20260930" || got.KernelModuleLoadedVersion != "3.1.20260906" {
		t.Errorf("got %+v", got)
	}
}
