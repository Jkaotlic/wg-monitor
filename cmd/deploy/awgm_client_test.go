package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAWGMClientLoginStoresSessionCookie(t *testing.T) {
	var sawCookie bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "awg_session", Value: "s1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"login":"admin"}`))
		case "/api/system/info":
			if ck, err := r.Cookie("awg_session"); err == nil && ck.Value == "s1" {
				sawCookie = true
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"goArch":"arm64","routerIP":"192.168.1.1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewAWGMClient(srv.URL, "admin", "secret")
	c.HTTP = srv.Client()
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.SystemInfo(context.Background()); err != nil {
		t.Fatalf("SystemInfo: %v", err)
	}
	if !sawCookie {
		t.Fatal("session cookie not sent")
	}
}

func TestAWGMClientTerminalBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/terminal/status" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true,"sessionActive":true}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewAWGMClient(srv.URL, "", "")
	c.HTTP = srv.Client()
	st, err := c.TerminalStatus(context.Background())
	if err != nil {
		t.Fatalf("TerminalStatus: %v", err)
	}
	if !st.SessionActive {
		t.Fatalf("expected busy terminal: %+v", st)
	}
}

func TestNormalizeAWGMURLDefaultsToHTTPS(t *testing.T) {
	if got := normalizeAWGMURL("awg.test.example/"); got != "https://awg.test.example" {
		t.Fatalf("normalizeAWGMURL without scheme = %q", got)
	}
	if got := normalizeAWGMURL("http://127.0.0.1:2222/"); got != "http://127.0.0.1:2222" {
		t.Fatalf("normalizeAWGMURL with scheme = %q", got)
	}
}
