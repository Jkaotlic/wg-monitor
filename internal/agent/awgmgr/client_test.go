package awgmgr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_DiagResult_HappyPath(t *testing.T) {
	const wantPayload = `{"success":true,"data":{"summary":"all green","checks":42}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/diagnostics/result" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(wantPayload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	out, err := c.DiagResult(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "all green") {
		t.Errorf("expected summary in body, got %q", out)
	}
}

func TestClient_DiagResult_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := New(srv.URL)
	_, err := c.DiagResult(context.Background())
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err must mention status: %v", err)
	}
}

func TestClient_ImportConf_OK(t *testing.T) {
	want := `{"success":true,"data":{"id":"abc123","name":"sg","type":"awg","status":"running","enabled":false,"defaultRoute":false}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		if r.URL.Path != "/api/import/conf" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type: %s", r.Header.Get("Content-Type"))
		}
		w.Write([]byte(want))
	}))
	defer srv.Close()
	c := New(srv.URL)
	tun, err := c.ImportConf(context.Background(), "[Interface]\nPrivateKey=x\n", "sg")
	if err != nil {
		t.Fatal(err)
	}
	if tun.ID != "abc123" {
		t.Errorf("id: %q", tun.ID)
	}
}

func TestClient_ImportConf_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":true,"message":"parse conf: missing Address","code":"IMPORT_FAILED"}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	_, err := c.ImportConf(context.Background(), "", "x")
	if err == nil {
		t.Fatal("expected error on 400")
	}
}

func TestClient_DeleteTunnel_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		if r.URL.Path != "/api/tunnels/delete" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.URL.RawQuery != "id=abc123" {
			t.Errorf("query: %q", r.URL.RawQuery)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.DeleteTunnel(context.Background(), "abc123"); err != nil {
		t.Fatal(err)
	}
}

func TestClient_DeleteTunnel_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()
	c := New(srv.URL)
	err := c.DeleteTunnel(context.Background(), "bad-id")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}
