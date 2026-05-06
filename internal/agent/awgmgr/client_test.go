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
