package awgmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		var body struct {
			Backend string `json:"backend"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Backend != "nativeWG" {
			t.Fatalf("backend=%q, want nativeWG", body.Backend)
		}
		w.Write([]byte(want))
	}))
	defer srv.Close()
	c := New(srv.URL)
	tun, err := c.ImportConf(context.Background(), "[Interface]\nPrivateKey=x\n", "sg", "nativeWG")
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
	_, err := c.ImportConf(context.Background(), "", "x", "")
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

func TestClient_DiagRun_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/diagnostics/run" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method: %q", r.Method)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":{"status":"running"}}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	if err := c.DiagRun(context.Background()); err != nil {
		t.Errorf("DiagRun: %v", err)
	}
}

func TestClient_DiagRun_BubblesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":true,"message":"down"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	err := c.DiagRun(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP_503") {
		t.Errorf("expected HTTP_503 in error, got: %v", err)
	}
}

func TestClient_DiagResult_TypedNoReportOnHTTP400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":true,"message":"no report available","code":"NO_REPORT"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	_, err := c.DiagResult(context.Background())
	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	if !strings.Contains(err.Error(), "NO_REPORT") {
		t.Errorf("expected NO_REPORT in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP_400") {
		t.Errorf("expected HTTP_400 prefix, got: %v", err)
	}
}

func TestClient_UsesSessionCookieWhenCredentialsConfigured(t *testing.T) {
	var loginHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			loginHits++
			if r.Method != http.MethodPost {
				t.Errorf("login method: %s", r.Method)
			}
			http.SetCookie(w, &http.Cookie{Name: "awg_session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		case "/api/tunnels/all":
			ck, err := r.Cookie("awg_session")
			if err != nil || ck.Value != "session-1" {
				t.Fatalf("missing session cookie: %v / %#v", err, ck)
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.SetCredentials("admin", "secret")
	if _, err := c.TunnelsAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loginHits != 1 {
		t.Fatalf("login hits: got %d want 1", loginHits)
	}
	if _, err := c.TunnelsAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loginHits != 1 {
		t.Fatalf("session should be reused, login hits=%d", loginHits)
	}
}
